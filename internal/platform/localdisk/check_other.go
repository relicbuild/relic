//go:build !linux

package localdisk

// Check cannot tell a network filesystem outside Linux.
func Check(string) error {
	return nil
}
