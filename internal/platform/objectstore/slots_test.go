package objectstore

import (
	"math"
	"testing"
)

func TestUploadSlots(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20

	tests := []struct {
		name  string
		limit int64 // Go's soft memory limit
		want  int
	}{
		{
			name:  "no limit",
			limit: math.MaxInt64,
			want:  16,
		},
		{
			name:  "1 GiB container (90%)",
			limit: 1024 * mib / 10 * 9,
			want:  16,
		},
		{
			name:  "2 GiB container (90%)",
			limit: 2048 * mib / 10 * 9,
			want:  39,
		},
		{
			name:  "4 GiB container (90%)",
			limit: 4096 * mib / 10 * 9,
			want:  64,
		},
		{
			name:  "a small container keeps a few",
			limit: 256 * mib,
			want:  4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := UploadSlots(tt.limit); got != tt.want {
				t.Errorf("UploadSlots(%d MiB) = %d, want %d", tt.limit/mib, got, tt.want)
			}
		})
	}
}
