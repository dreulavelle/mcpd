package cnmaestro

import (
	"errors"
	"fmt"
	"net/http"
)

// stdErrorsAs wraps errors.As so other files in this package can use it
// without importing errors for a single call.
func stdErrorsAs(err error, target any) bool { return errors.As(err, target) }

// APIError is a non-success response from cnMaestro.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	// Message is the upstream's own error text, already bounded. It is never
	// the raw body: an error page can be a kilobyte of HTML.
	Message string
	// RetryAfter is populated from the header when present. cnMaestro
	// documents 429 but not whether it sends this, so it is often zero.
	RetryAfter int
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("cnmaestro: %s %s returned %d", e.Method, e.Path, e.StatusCode)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Retryable reports whether repeating the request could plausibly succeed.
//
// This governs read retries only. A write is never retried on this basis: a
// 5xx or a timeout on a mutation means the outcome is unknown, which the
// executor records as indeterminate rather than retrying.
func (e *APIError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// NotFound reports whether the resource does not exist.
func (e *APIError) NotFound() bool { return e.StatusCode == http.StatusNotFound }

// Sentinel errors for conditions callers branch on.
var (
	// ErrNotConfigured reports that the plugin has no usable credentials.
	ErrNotConfigured = errors.New("cnmaestro: plugin is not configured")
	// ErrDeviceNotFound reports an unknown device.
	ErrDeviceNotFound = errors.New("cnmaestro: device not found")
	// ErrUnsupportedDevice reports an operation the device type cannot
	// perform, such as a radio override on a switch.
	ErrUnsupportedDevice = errors.New("cnmaestro: operation is not supported on this device type")
)
