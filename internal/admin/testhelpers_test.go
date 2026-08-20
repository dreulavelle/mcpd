package admin

import (
	"context"
	"net/http"

	"github.com/spoked/mcpd/internal/auth"
)

// rejectingVerifier refuses every credential, which is what an unauthenticated
// request should encounter.
type rejectingVerifier struct{}

func (rejectingVerifier) Scheme() string { return "test" }

func (rejectingVerifier) Verify(context.Context, string, *http.Request) (*auth.Principal, error) {
	return nil, auth.ErrUnauthenticated
}
