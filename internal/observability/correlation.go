// Package observability provides structured logging, request correlation, and
// health aggregation.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
)

// CorrelationHeader is the inbound header honoured for request correlation.
// Accepting a caller-supplied value lets a trace span client and server; it is
// sanitised before use because it ends up in logs and audit records.
const CorrelationHeader = "X-Correlation-Id"

type correlationKey struct{}

// WithCorrelationID returns a context carrying a correlation ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the request's correlation ID, or "" if unset.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// NewCorrelationID generates a random identifier.
func NewCorrelationID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Correlation IDs are for tracing, not security. A degraded ID is far
		// better than failing the request.
		return "unseeded"
	}
	return hex.EncodeToString(b[:])
}

// sanitizeCorrelationID bounds a caller-supplied value to printable ASCII of
// limited length. Without this, a caller could inject newlines into structured
// logs or store arbitrary bytes in the audit trail.
func sanitizeCorrelationID(v string) string {
	const maxLen = 64
	if len(v) > maxLen {
		v = v[:maxLen]
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			// Drop anything else rather than substituting, so a hostile value
			// cannot be reconstructed from the log.
		}
	}
	return b.String()
}

// Correlate assigns each request a correlation ID, propagates it in the
// context and the response header, and tags the request logger with it.
func Correlate(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeCorrelationID(r.Header.Get(CorrelationHeader))
		if id == "" {
			id = NewCorrelationID()
		}
		ctx := WithCorrelationID(r.Context(), id)
		ctx = WithLogger(ctx, log.With("correlation_id", id))
		w.Header().Set(CorrelationHeader, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type loggerKey struct{}

// WithLogger returns a context carrying a request-scoped logger.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, log)
}

// Logger returns the request-scoped logger, falling back to the default. It
// never returns nil, so call sites need no guard.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
