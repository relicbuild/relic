// Package scripttest writes executable scripts for tests that run them.
package scripttest

import (
	"os"
	"syscall"
	"testing"
)

// Write creates the executable script path with body. It holds the fork
// lock while the file is open for writing: a process forked meanwhile by a
// parallel test would inherit the descriptor and make running the script
// fail with "text file busy".
func Write(t testing.TB, path, body string) {
	t.Helper()

	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()

	//nolint:gosec // an executable test script
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
