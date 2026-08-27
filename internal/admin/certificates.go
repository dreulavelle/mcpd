package admin

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/trust"
)

// Certificates is the slice of the trust store the dashboard needs.
//
// There is no update: a certificate is the bytes it is, and "editing" one
// would mean trusting different bytes under a name somebody already recognises.
// Adding and removing are the only two things that are honest.
type Certificates interface {
	List(ctx context.Context) ([]*trust.Certificate, error)
	Add(ctx context.Context, actor, name string, raw []byte) (*trust.Certificate, error)
	Delete(ctx context.Context, actor, id string) error
}

// certificateView is what the dashboard renders.
//
// The PEM is included. It is public material -- the server presents it to
// every client that connects -- and having it on the page is what lets an
// operator check that the thing mcpd is trusting is the thing they meant,
// against an appliance they can also read it from.
type certificateView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PEM         string `json:"pem"`
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	Fingerprint string `json:"fingerprint"`
	NotBefore   string `json:"not_before"`
	NotAfter    string `json:"not_after"`
	// Status is "valid", "expiring", "expired" or "not_yet_valid". Computed
	// here so the page and the log agree on when a certificate stops counting,
	// rather than each holding its own idea of soon.
	Status string `json:"status"`
	// SelfSigned is worth saying plainly: an appliance certificate that is its
	// own issuer is the ordinary case for this feature, not a warning sign.
	SelfSigned bool `json:"self_signed"`
	IsCA       bool `json:"is_ca"`
	// Anchors reports whether this certificate can be the root of a chain at
	// all. False means trusting it cannot fix anything -- it says outright
	// that it is not an authority -- and the page says so rather than leaving
	// somebody to wonder why the handshake still fails.
	Anchors bool   `json:"anchors"`
	AddedBy string `json:"added_by"`
	AddedAt string `json:"added_at"`
}

// expiringSoon is how far ahead the page warns.
//
// Thirty days, because the thing being warned about is a certificate somebody
// has to go and get reissued by whoever runs the authority, and that is a
// request with a queue in front of it rather than a button.
const expiringSoon = 30 * 24 * time.Hour

func viewOfCertificate(c *trust.Certificate, now time.Time) certificateView {
	return certificateView{
		ID:          c.ID,
		Name:        c.Name,
		PEM:         c.PEM,
		Subject:     c.Subject,
		Issuer:      c.Issuer,
		Fingerprint: c.Fingerprint,
		NotBefore:   c.NotBefore.Format(time.RFC3339),
		NotAfter:    c.NotAfter.Format(time.RFC3339),
		Status:      statusOfCertificate(c, now),
		SelfSigned:  c.Subject == c.Issuer,
		IsCA:        c.IsCA,
		Anchors:     c.CanAnchor(),
		AddedBy:     c.AddedBy,
		AddedAt:     c.AddedAt.Format(time.RFC3339),
	}
}

func statusOfCertificate(c *trust.Certificate, now time.Time) string {
	switch {
	case c.Expired(now):
		return "expired"
	case c.NotYetValid(now):
		return "not_yet_valid"
	case c.NotAfter.Sub(now) < expiringSoon:
		return "expiring"
	default:
		return "valid"
	}
}

func (s *Server) handleListCertificates(w http.ResponseWriter, r *http.Request) {
	if s.opts.Certificates == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "certificates are not configured")
		return
	}
	list, err := s.opts.Certificates.List(r.Context())
	if err != nil {
		s.opts.Log.ErrorContext(r.Context(), "could not list certificates", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read the certificates")
		return
	}
	now := time.Now()
	out := make([]certificateView, len(list))
	for i, c := range list {
		out[i] = viewOfCertificate(c, now)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"certificates": out, "count": len(out),
	})
}

// addCertificateRequest carries the certificate in one of two fields.
//
// Two rather than one that is sometimes base64: a certificate arrives either
// as text somebody pasted or as a file the browser read, and a Windows CA
// exports a binary DER file under a .crt extension without saying so. Which
// one this is is a fact the sender knows, so it says which rather than leaving
// the server to guess from the bytes and be wrong about a file that happens to
// decode.
type addCertificateRequest struct {
	Name string `json:"name"`
	// PEM is text as pasted, headers and all.
	PEM string `json:"pem"`
	// Base64 is a binary file, base64-encoded for the trip.
	Base64 string `json:"base64"`
}

func (s *Server) handleAddCertificate(w http.ResponseWriter, r *http.Request) {
	if s.opts.Certificates == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "certificates are not configured")
		return
	}
	var req addCertificateRequest
	if !s.decode(w, r, &req) {
		return
	}

	var raw []byte
	switch {
	case req.PEM != "" && req.Base64 != "":
		s.writeError(w, r, http.StatusBadRequest,
			"send the certificate as text or as a file, not both")
		return
	case req.Base64 != "":
		decoded, err := base64.StdEncoding.DecodeString(req.Base64)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "that file could not be read")
			return
		}
		raw = decoded
	case req.PEM != "":
		raw = []byte(req.PEM)
	default:
		s.writeError(w, r, http.StatusBadRequest, "there is no certificate here")
		return
	}

	actor := auth.FromContext(r.Context()).ID
	cert, err := s.opts.Certificates.Add(r.Context(), actor, req.Name, raw)
	switch {
	case errors.Is(err, trust.ErrDuplicateName):
		s.writeError(w, r, http.StatusConflict,
			"a certificate with that name is already here")
		return
	case errors.Is(err, trust.ErrDuplicateCertificate):
		// The name it is already under is in the error, and it is the whole
		// use of the message: the answer is "you already have it", not "no".
		s.writeError(w, r, http.StatusConflict, err.Error())
		return
	case err != nil:
		// Every remaining refusal describes the certificate that was sent and
		// each one says what to do about it, so the text goes through rather
		// than being flattened into "invalid certificate".
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	s.opts.Log.InfoContext(r.Context(), "certificate added",
		"certificate", cert.ID, "name", cert.Name, "subject", cert.Subject,
		"fingerprint", cert.Fingerprint,
		"expires_at", cert.NotAfter.Format(time.RFC3339), "by", actor)
	s.trustChanged(r.Context())
	s.writeJSON(w, r, http.StatusCreated, viewOfCertificate(cert, time.Now()))
}

func (s *Server) handleDeleteCertificate(w http.ResponseWriter, r *http.Request) {
	if s.opts.Certificates == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "certificates are not configured")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	err := s.opts.Certificates.Delete(r.Context(), actor, r.PathValue("id"))
	switch {
	case errors.Is(err, trust.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such certificate")
		return
	case err != nil:
		s.opts.Log.ErrorContext(r.Context(), "could not remove a certificate", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not remove it")
		return
	}
	s.opts.Log.InfoContext(r.Context(), "certificate removed",
		"certificate", r.PathValue("id"), "by", actor)
	s.trustChanged(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// trustChanged tells the host to rebuild what it trusts.
//
// Without it a certificate would be stored and believed by nothing until the
// next restart, which is exactly the confusion this feature exists to remove:
// the operator has done the thing the page asked for and the handshake still
// fails.
func (s *Server) trustChanged(ctx context.Context) {
	if s.opts.TrustChanged == nil {
		// Not an error the operator can act on, but it is a host wired without
		// the hook -- the certificate is stored and will be picked up when
		// this process next starts.
		s.opts.Log.WarnContext(ctx, "no way to apply a trust change without a restart")
		return
	}
	s.opts.TrustChanged(ctx)
}
