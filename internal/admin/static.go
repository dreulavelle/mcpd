package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The built dashboard is embedded so that mcpd remains a single binary with no
// asset directory to deploy, keep in sync, or accidentally serve from the
// wrong path.
//
//go:embed all:dist
var distFS embed.FS

// staticHandler serves the single-page application.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Only reachable if the embed directive and the build output disagree,
		// which is a build-time mistake rather than a runtime condition.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.writeError(w, r, http.StatusInternalServerError, "dashboard assets are unavailable")
		})
	}
	files := http.FS(sub)
	server := http.FileServer(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)

		// An unknown path is a client-side route, so index.html is served and
		// the app resolves it. Without this, a reload on any route but "/"
		// returns 404.
		if clean != "/" {
			if f, err := sub.Open(strings.TrimPrefix(clean, "/")); err == nil {
				f.Close()
				setAssetCaching(w, clean)
				server.ServeHTTP(w, r)
				return
			}
			// An unmatched /api path is a genuine 404, not a route: serving
			// HTML there would make a broken API call look like a page.
			if strings.HasPrefix(clean, "/api/") {
				s.writeError(w, r, http.StatusNotFound, "unknown endpoint")
				return
			}
		}

		index, err := sub.Open("index.html")
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "dashboard assets are unavailable")
			return
		}
		defer index.Close()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// index.html is never cached: it names the hashed asset bundles, so a
		// cached copy would keep pointing at bundles that no longer exist
		// after an upgrade.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", modTime(index), index.(readSeeker))
	})
}

type readSeeker interface {
	Read([]byte) (int, error)
	Seek(int64, int) (int64, error)
}

func modTime(f fs.File) (t time.Time) {
	if info, err := f.Stat(); err == nil {
		return info.ModTime()
	}
	return t
}

// setAssetCaching caches fingerprinted bundles aggressively and everything
// else conservatively.
//
// Vite emits content-hashed filenames, so a bundle's URL changes whenever its
// contents do. That makes an immutable, year-long cache safe: a stale copy can
// never be served for new content because the name would differ.
func setAssetCaching(w http.ResponseWriter, p string) {
	if strings.HasPrefix(p, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
}
