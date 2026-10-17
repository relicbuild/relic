//go:build unix

package localdisk

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Usage returns the free and total bytes of the filesystem dir is on.
func Usage(dir string) (free, size uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", dir, err)
	}

	bsize := uint64(st.Bsize) //nolint:gosec,unconvert // a block size is positive; its type varies by platform

	return st.Bavail * bsize, st.Blocks * bsize, nil
}
