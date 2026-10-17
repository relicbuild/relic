//go:build unix

package localdisk

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Lock takes an exclusive lock on the file at path, creating it, and waits while another process holds it, even one that removes the file.
func Lock(path string) (func(), error) {
	for {
		//nolint:gosec // a path Relic derives from its configuration
		f, err := os.OpenFile(
			path,
			os.O_RDWR|os.O_CREATE,
			0o600,
		)
		if err != nil {
			return nil, fmt.Errorf("open lock file: %w", err)
		}

		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil { //nolint:gosec // a file descriptor fits an int
			_ = f.Close()

			return nil, fmt.Errorf("lock %s: %w", path, err)
		}

		unlock := func() {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:gosec // a file descriptor fits an int
			_ = f.Close()
		}

		held, heldErr := f.Stat()
		current, currentErr := os.Stat(path)

		if heldErr == nil && currentErr == nil && os.SameFile(held, current) {
			return unlock, nil
		}

		unlock()
	}
}
