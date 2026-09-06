package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A directory on this host.
//
// It looks like the least useful of the four and is probably the most used: an
// SMB or NFS share the operating system already mounts is a directory, and so
// is the folder an rclone or restic job on the same machine watches. mcpd
// writing a file and something else moving it is a better division than mcpd
// learning every protocol.

type localTransport struct {
	dir string
}

func openLocal(d Destination, _ TransportOptions) (Transport, error) {
	if strings.TrimSpace(d.Settings.Path) == "" {
		return nil, errors.New("backup: this destination has no directory")
	}
	return &localTransport{dir: d.Settings.Path}, nil
}

// resolveLocalPath settles a directory and refuses the ones that would eat
// their own tail.
//
// mcpd's data directory is refused, along with anything inside it and anything
// containing it. A backup written into the directory being backed up is in the
// next backup, which doubles in size every run until the volume is full -- and
// a destination pointed at the data directory itself would have retention
// deleting files beside the live database.
//
// The directory has to exist. Creating one is not this host's decision to make:
// a typo in an operator's path would otherwise silently produce an empty
// directory in a place nobody meant, and the backups would appear to be working.
func resolveLocalPath(path, storageDir string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("backup destination: give the directory to write into")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("backup destination: %s is not a path this host can resolve", path)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf(
			"backup destination: there is no directory at %s. Create it, and "+
				"make sure mcpd can write to it", abs)
	}
	// After the symlinks, because a link into the data directory is the way
	// this check is passed by accident.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if storageDir != "" {
		data, err := filepath.Abs(storageDir)
		if err == nil {
			if resolved, err := filepath.EvalSymlinks(data); err == nil {
				data = resolved
			}
			if abs == data || within(abs, data) || within(data, abs) {
				return "", fmt.Errorf(
					"backup destination: %s is mcpd's own data directory, or holds "+
						"it. A backup written there is in the next backup, and it "+
						"is lost with the disk it is meant to survive. Choose a "+
						"directory outside %s", abs, data)
			}
		}
	}
	return abs, nil
}

// within reports whether child is inside parent.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (l *localTransport) Put(ctx context.Context, name string, r io.Reader, _ int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Written under a temporary name and renamed, so a run interrupted halfway
	// never leaves a short file wearing an archive's name -- which the next
	// run's retention would count, and a person would restore from.
	tmp, err := os.CreateTemp(l.dir, ".mcpd-upload-")
	if err != nil {
		return fmt.Errorf("write to %s: %w", l.dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("write to %s: %w", l.dir, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush to %s: %w", l.dir, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("set permissions on %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(l.dir, name)); err != nil {
		return fmt.Errorf("rename into %s: %w", l.dir, err)
	}
	return nil
}

func (l *localTransport) List(ctx context.Context) ([]Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", l.dir, err)
	}
	var out []Object
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ours := TimeFromName(e.Name()); !ours {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Object{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return out, nil
}

func (l *localTransport) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only ever a name this package wrote. A caller passing a path would
	// otherwise be able to remove something outside the directory.
	if _, ours := TimeFromName(name); !ours {
		return fmt.Errorf("backup: %q is not an archive mcpd wrote", name)
	}
	if err := os.Remove(filepath.Join(l.dir, name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

func (l *localTransport) Check(ctx context.Context) error {
	if _, err := l.List(ctx); err != nil {
		return err
	}
	// Listing proves the directory is readable, which is not the question. A
	// share mounted read-only lists perfectly and fails at four in the morning.
	probe, err := os.CreateTemp(l.dir, ".mcpd-check-")
	if err != nil {
		return fmt.Errorf("write to %s: %w", l.dir, err)
	}
	name := probe.Name()
	probe.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

func (l *localTransport) Close() error { return nil }
