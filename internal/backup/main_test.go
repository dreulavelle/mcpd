package backup

import (
	"os"
	"testing"
)

// The archive tests run at a work factor a test can afford. 600,000 PBKDF2
// rounds under the race detector cost seconds per key, and nearly every test
// here writes an archive and reads it back. What is under test is the format
// and the checks around it, and an archive records the count it was written
// with, so nothing they prove depends on the count being high.
func TestMain(m *testing.M) {
	iterations = 1_000
	os.Exit(m.Run())
}

// The production work factor, held where lowering it for the tests cannot
// hide a change to it.
func TestWorkFactorIsOWASPsFloor(t *testing.T) {
	if defaultIterations < 600_000 {
		t.Fatalf("new archives are written with %d PBKDF2 rounds; OWASP's floor for "+
			"PBKDF2-HMAC-SHA256 is 600,000", defaultIterations)
	}
}
