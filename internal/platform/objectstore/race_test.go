//go:build race

package objectstore_test

// The race detector's bookkeeping adds heap a plain build does not use.
const maxUploadHeapMiB = 56
