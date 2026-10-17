//go:build !unix

package localdisk

// Lock does not lock outside Unix.
func Lock(string) (func(), error) {
	return func() {}, nil
}
