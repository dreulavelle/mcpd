package app

import (
	"os"
	"testing"

	"github.com/spoked/mcpd/internal/auth/users"
)

// Accounts made in these tests hash a password at the real bcrypt cost
// otherwise, which is seconds each under the race detector. The cost is what
// a leaked database faces, and nothing here is about it.
func TestMain(m *testing.M) {
	users.LowerHashCostForTests()
	os.Exit(m.Run())
}
