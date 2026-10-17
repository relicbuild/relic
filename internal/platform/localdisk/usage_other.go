//go:build !unix

package localdisk

import "errors"

// Usage is not measured outside Unix.
func Usage(string) (free, size uint64, err error) {
	return 0, 0, errors.New("disk usage is not measured on this platform")
}
