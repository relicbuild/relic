//go:build race

package relicapi

// The race detector makes sync.Pool drop a share of the buffers put back.
const maxDownloadAllocation = 128 << 10
