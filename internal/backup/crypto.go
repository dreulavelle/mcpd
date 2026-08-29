package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrPassphrase reports that an archive did not open with the passphrase
// given. It is deliberately the same error for a wrong passphrase and for a
// tampered archive: AES-GCM cannot tell them apart, and guessing which it was
// would be inventing a distinction the cryptography does not offer.
var ErrPassphrase = errors.New(
	"backup: the archive did not open. The passphrase is wrong, or the file " +
		"has been altered or truncated")

// MinPassphrase is the shortest passphrase an archive may be sealed with.
//
// An archive holds every account, group, grant and audit entry this host has,
// so it is encrypted rather than offered as a plain tarball. The length floor
// exists because the whole file's confidentiality rests on this one string and
// nothing else -- there is no second factor and no rate limit on a file
// somebody has already copied.
const MinPassphrase = 12

// Chunking, and why the archive is not one Seal over the whole tarball.
//
// A one-shot Seal has to hold the plaintext and the ciphertext in memory at
// once, so a database of any size would need twice its own size in heap on a
// host running under a GOMEMLIMIT chosen for a control plane. Sealing in
// chunks bounds that to the chunk size regardless of how large the instance
// has grown.
const (
	chunkSize   = 1 << 20 // 1 MiB of plaintext per sealed chunk.
	maxChunk    = chunkSize + 16
	nonceSize   = 12
	saltSize    = 16
	noncePrefix = 8 // The rest of the nonce is the chunk counter.

	// Deliberately expensive. This is a human-chosen passphrase guarded by
	// nothing but its own length, protecting a file an attacker can grind on
	// offline for as long as they like. OWASP's floor for PBKDF2-HMAC-SHA256.
	iterations = 600_000
)

// deriveKey turns a passphrase into the 32 bytes AES-256 wants.
//
// PBKDF2 rather than the bare SHA-256 that internal/settings uses on its own
// key, and the difference is the input. That key is generated and has full
// entropy, so stretching it buys nothing. This one is typed by a person.
//
// The work factor is a parameter rather than the constant, because reading an
// archive has to use the count it was written with. Raising the constant must
// not strand yesterday's backups.
func deriveKey(passphrase string, salt []byte, iter int) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
	if err != nil {
		return nil, fmt.Errorf("backup: derive key: %w", err)
	}
	return key, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("backup: build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("backup: build GCM: %w", err)
	}
	return aead, nil
}

// chunkNonce builds the nonce for one chunk: a random per-archive prefix and
// the chunk's index. Never repeated under one key, which is the property GCM
// depends on absolutely.
func chunkNonce(prefix []byte, index uint32) []byte {
	nonce := make([]byte, nonceSize)
	copy(nonce, prefix[:noncePrefix])
	binary.BigEndian.PutUint32(nonce[noncePrefix:], index)
	return nonce
}

// chunkAAD binds a chunk to its archive, its position, and whether it is the
// last one.
//
// The header hash is in here so a chunk cannot be lifted into an archive that
// claims to hold something else; the final flag is here so an attacker cannot
// truncate the archive and have it open as a shorter, complete one. Neither is
// covered by the nonce alone.
func chunkAAD(headerHash []byte, index uint32, final bool) []byte {
	aad := make([]byte, 0, len(headerHash)+5)
	aad = append(aad, headerHash...)
	aad = binary.BigEndian.AppendUint32(aad, index)
	if final {
		return append(aad, 1)
	}
	return append(aad, 0)
}

// sealer encrypts a stream into length-prefixed chunks.
type sealer struct {
	w          io.Writer
	aead       cipher.AEAD
	prefix     []byte
	headerHash []byte
	index      uint32
	buf        []byte
}

func newSealer(w io.Writer, key, noncePrefixBytes, headerHash []byte) (*sealer, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &sealer{
		w:          w,
		aead:       aead,
		prefix:     noncePrefixBytes,
		headerHash: headerHash,
		buf:        make([]byte, 0, chunkSize),
	}, nil
}

// Write buffers plaintext, sealing a chunk each time one fills.
func (s *sealer) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		space := chunkSize - len(s.buf)
		take := min(space, len(p))
		s.buf = append(s.buf, p[:take]...)
		p = p[take:]
		if len(s.buf) == chunkSize {
			if err := s.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return written, nil
}

// Close seals whatever is left as the final chunk.
//
// Always writes one, even when the buffer is empty: the final flag is what
// makes truncation detectable, so an archive with no terminal chunk must not
// be a valid archive.
func (s *sealer) Close() error { return s.flush(true) }

func (s *sealer) flush(final bool) error {
	sealed := s.aead.Seal(nil,
		chunkNonce(s.prefix, s.index),
		s.buf,
		chunkAAD(s.headerHash, s.index, final))

	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
	if _, err := s.w.Write(length[:]); err != nil {
		return fmt.Errorf("backup: write chunk length: %w", err)
	}
	if _, err := s.w.Write(sealed); err != nil {
		return fmt.Errorf("backup: write chunk: %w", err)
	}
	s.buf = s.buf[:0]
	s.index++
	return nil
}

// opener decrypts the stream a sealer wrote.
type opener struct {
	r          io.Reader
	aead       cipher.AEAD
	prefix     []byte
	headerHash []byte
	index      uint32
	pending    []byte
	done       bool
}

func newOpener(r io.Reader, key, noncePrefixBytes, headerHash []byte) (*opener, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &opener{r: r, aead: aead, prefix: noncePrefixBytes, headerHash: headerHash}, nil
}

func (o *opener) Read(p []byte) (int, error) {
	for len(o.pending) == 0 {
		if o.done {
			return 0, io.EOF
		}
		if err := o.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, o.pending)
	o.pending = o.pending[n:]
	return n, nil
}

// next reads and opens one chunk.
func (o *opener) next() error {
	var length [4]byte
	if _, err := io.ReadFull(o.r, length[:]); err != nil {
		// Any short read here is a truncated archive: the sealer always writes
		// a final chunk, so the stream never ends where a length was expected.
		return ErrPassphrase
	}
	size := binary.BigEndian.Uint32(length[:])
	if size > maxChunk {
		return fmt.Errorf("backup: chunk of %d bytes is larger than this format allows", size)
	}
	sealed := make([]byte, size)
	if _, err := io.ReadFull(o.r, sealed); err != nil {
		return ErrPassphrase
	}

	// Which flag it is, is not known before opening. A chunk opens under
	// exactly one of them, so try the ordinary case and then the terminal one.
	nonce := chunkNonce(o.prefix, o.index)
	plain, err := o.aead.Open(nil, nonce, sealed, chunkAAD(o.headerHash, o.index, false))
	if err != nil {
		plain, err = o.aead.Open(nil, nonce, sealed, chunkAAD(o.headerHash, o.index, true))
		if err != nil {
			return ErrPassphrase
		}
		o.done = true
	}
	o.pending = plain
	o.index++
	return nil
}

// randomBytes is a named helper so the error message says what was being
// generated rather than surfacing a bare read failure.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("backup: read random bytes: %w", err)
	}
	return b, nil
}
