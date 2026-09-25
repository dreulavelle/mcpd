package users

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The hash cost is what a leaked database faces, not what any test asserts,
// and under the race detector a cost-12 hash takes seconds. This package makes
// a few hundred of them, and the run met CI's ten-minute limit the day the
// invitation tests landed.
func TestMain(m *testing.M) {
	LowerHashCostForTests()
	os.Exit(m.Run())
}

// The production cost, held where lowering it for the tests cannot hide a
// change to it.
func TestHashCostIsAboveTheLibraryDefault(t *testing.T) {
	if defaultBcryptCost <= bcrypt.DefaultCost {
		t.Fatalf("passwords are hashed at bcrypt cost %d; it is meant to be above "+
			"the library default of %d", defaultBcryptCost, bcrypt.DefaultCost)
	}
}
