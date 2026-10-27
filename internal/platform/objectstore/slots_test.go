package objectstore

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestUploadSlots(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20

	tests := []struct {
		name      string
		storeType string
		limit     int64 // Go's soft memory limit
		want      int
	}{
		{
			name:      "no limit",
			storeType: TypeS3,
			limit:     math.MaxInt64,
			want:      16,
		},
		{
			name:      "S3, 1 GiB container (90%)",
			storeType: TypeS3,
			limit:     1024 * mib / 10 * 9,
			want:      16,
		},
		{
			name:      "S3, 2 GiB container (90%)",
			storeType: TypeS3,
			limit:     2048 * mib / 10 * 9,
			want:      39,
		},
		{
			name:      "S3, 4 GiB container (90%)",
			storeType: TypeS3,
			limit:     4096 * mib / 10 * 9,
			want:      64,
		},
		{
			name:      "S3, a small container keeps a few",
			storeType: TypeS3,
			limit:     256 * mib,
			want:      4,
		},
		{
			name:      "Azure, 1 GiB container (90%): four 8 MiB blocks and one filling",
			storeType: TypeAzure,
			limit:     1024 * mib / 10 * 9,
			want:      16,
		},
		{
			name:      "GCS, 1 GiB container (90%): one 16 MiB chunk and one filling",
			storeType: TypeGCS,
			limit:     1024 * mib / 10 * 9,
			want:      20,
		},
		{
			name:      "a filesystem holds no upload buffers",
			storeType: TypeFilesystem,
			limit:     256 * mib,
			want:      16,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := UploadSlots(tt.storeType, tt.limit); got != tt.want {
				t.Errorf("UploadSlots(%s, %d MiB) = %d, want %d", tt.storeType, tt.limit/mib, got, tt.want)
			}
		})
	}
}

func TestCloudUploadsWaitForASlot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		store func(uploadGate) Store
	}{
		{
			name: "Azure",
			store: func(gate uploadGate) Store {
				return &AzureStore{
					uploads: gate,
				}
			},
		},
		{
			name: "GCS",
			store: func(gate uploadGate) Store {
				return &GCSStore{
					uploads: gate,
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gate := newUploadGate(1)
			if _, err := gate.acquire(t.Context(), "held"); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			err := tc.store(gate).Put(ctx, "k", strings.NewReader("x"), 1)
			if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "upload slot") {
				t.Errorf("Put with every slot taken = %v, want it to wait for a slot", err)
			}
		})
	}
}
