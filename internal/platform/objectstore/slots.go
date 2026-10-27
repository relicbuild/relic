package objectstore

import (
	"cmp"
	"context"
	"fmt"
	"math"
)

const (
	s3UploadMemory              int64 = 40 << 20
	azureUploadMemory                 = azureBlockSize * (azureConcurrency + 1)
	gcsUploadMemory                   = gcsChunkSize * 2
	reservedMemory              int64 = 256 << 20
	minUploadSlots                    = 4
	maxUploadSlots                    = 64
	defaultMaxConcurrentUploads       = 16
)

// UploadSlots is how many uploads to a store of storeType may be in flight
// under Go's soft memory limit, given the buffers each upload holds.
func UploadSlots(storeType string, memoryLimit int64) int {
	perUpload := uploadMemory(storeType)
	if perUpload == 0 || memoryLimit <= 0 || memoryLimit == math.MaxInt64 {
		return defaultMaxConcurrentUploads
	}

	return int(min(max((memoryLimit-reservedMemory)/perUpload, minUploadSlots), maxUploadSlots))
}

func uploadMemory(storeType string) int64 {
	switch storeType {
	case TypeS3:
		return s3UploadMemory
	case TypeAzure:
		return azureUploadMemory
	case TypeGCS:
		return gcsUploadMemory
	default:
		return 0
	}
}

type uploadGate chan struct{}

func newUploadGate(slots int) uploadGate {
	return make(uploadGate, cmp.Or(slots, defaultMaxConcurrentUploads))
}

func (g uploadGate) acquire(ctx context.Context, key string) (func(), error) {
	select {
	case g <- struct{}{}:
		return func() {
			<-g
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("put %s: waiting for an upload slot: %w", key, ctx.Err())
	}
}
