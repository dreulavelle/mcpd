package users

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The hash cost is what a leaked database faces, not what any test asserts,
// and under the race detector a cost-12 hash takes seconds. This package makes
// a few hundred of them, and the run met CI's ten-minute limit the day the
// invitation tests landed. So the tests hash at the minimum -- including the
// decoy Authenticate compares against, which was made at package init with
// the real cost and would otherwise keep every wrong-password test slow.
func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	dummyHash = mustHash("a-password-no-account-has")
	os.Exit(m.Run())
}
