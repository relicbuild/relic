// Package localdisk holds what Relic's single-node backends need from the local disk.
package localdisk

import "errors"

// ErrFull is returned by a write that found no space left, on the local disk or the database server's.
var ErrFull = errors.New("the disk is full")
