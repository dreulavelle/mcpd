package servertls

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Holder is the certificate a listener presents, replaceable while it runs.
//
// The leaf lives for a year and was only ever reissued at startup, which is
// right for a deployment that restarts more often than that and wrong for one
// that does not: a host nobody touched for thirteen months would begin
// serving an expired certificate, and every browser would say so. A listener
// built from a Holder asks it for the certificate on each handshake, so a
// renewal reaches connections made after it without a restart.
type Holder struct {
	current atomic.Pointer[Materials]
}

// NewHolder starts with m.
func NewHolder(m *Materials) *Holder {
	h := &Holder{}
	h.current.Store(m)
	return h
}

// Materials is what the listener presents now.
func (h *Holder) Materials() *Materials { return h.current.Load() }

// Store replaces what the listener presents from the next handshake on.
func (h *Holder) Store(m *Materials) { h.current.Store(m) }

// TLSConfig returns a listener configuration that reads the certificate from
// the holder on every handshake. See Materials.TLSConfig for the floor.
func (h *Holder) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return &h.current.Load().Certificate, nil
		},
		MinVersion: tls.VersionTLS12,
	}
}

// sniffTimeout bounds how long a connection may sit without sending a byte
// before it is dropped. Each connection is read in its own goroutine, so a
// slow one holds up nobody else; this only stops them accumulating.
const sniffTimeout = 10 * time.Second

// tlsHandshakeRecord is the first byte of every TLS connection: the record
// type of the ClientHello. No HTTP method begins with it.
const tlsHandshakeRecord = 0x16

// Sniff answers https and plain http on the same listener.
//
// Turning https on for the dashboard changes nothing about where it is
// reached, so every bookmark and every link somebody pasted into a ticket is
// still http://. Serving https alone on that port would answer each of them
// with a connection reset, which reads as mcpd being down. So a connection
// that opens with a TLS handshake is served over TLS, and one that opens with
// anything else is handed through as it is, for RedirectToHTTPS to send on.
//
// Connections come out of Accept as *tls.Conn or as plain ones, which is what
// net/http looks at to decide whether to run a handshake and fill in r.TLS.
func Sniff(l net.Listener, config *tls.Config) net.Listener {
	s := &sniffer{
		Listener: l,
		config:   config,
		conns:    make(chan net.Conn),
		errs:     make(chan error),
		done:     make(chan struct{}),
	}
	go s.accept()
	return s
}

type sniffer struct {
	net.Listener
	config *tls.Config
	conns  chan net.Conn
	errs   chan error
	done   chan struct{}
	once   sync.Once
}

func (s *sniffer) accept() {
	for {
		c, err := s.Listener.Accept()
		if err != nil {
			// Handed to whoever is serving, which decides whether it is worth
			// retrying -- net/http backs off on a temporary one and returns on
			// anything else, and that decision is its to make, not ours.
			select {
			case s.errs <- err:
			case <-s.done:
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go s.classify(c)
	}
}

func (s *sniffer) classify(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(sniffTimeout))
	r := bufio.NewReader(c)
	first, err := r.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	var out net.Conn = &peeked{Conn: c, r: r}
	if first[0] == tlsHandshakeRecord {
		out = tls.Server(out, s.config)
	}
	select {
	case s.conns <- out:
	case <-s.done:
		_ = out.Close()
	}
}

func (s *sniffer) Accept() (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case err := <-s.errs:
		return nil, err
	case <-s.done:
		return nil, net.ErrClosed
	}
}

func (s *sniffer) Close() error {
	s.once.Do(func() { close(s.done) })
	return s.Listener.Close()
}

// peeked is a connection whose first bytes have already been read into r.
type peeked struct {
	net.Conn
	r *bufio.Reader
}

func (p *peeked) Read(b []byte) (int, error) { return p.r.Read(b) }

// RedirectToHTTPS sends a plain-http request to the same address over https,
// and serves one that arrived over TLS.
//
// Temporary (307), not permanent. A browser remembers a permanent redirect
// and follows it without asking again, so turning https off later would leave
// every browser that had visited sending itself to a port that no longer
// speaks TLS, with nothing on the server able to tell it otherwise. For the
// same reason there is no Strict-Transport-Security header: it is the same
// promise, made harder to take back. 307 also keeps the method and the body,
// so an API call made to the old address arrives intact rather than as a GET.
func RedirectToHTTPS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.Host == "" {
			http.Error(w, "This address is served over https.", http.StatusBadRequest)
			return
		}
		// The same host and port the request was addressed to: https and http
		// share this port, so nothing about the address changes but the scheme.
		http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	})
}
