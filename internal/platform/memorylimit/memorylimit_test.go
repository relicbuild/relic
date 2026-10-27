package memorylimit

import (
	"math"
	"testing"
)

func TestShare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limit   int64
		ceiling int64
		divisor int64
		want    int64
	}{
		{
			name:    "no limit keeps the ceiling",
			limit:   math.MaxInt64,
			ceiling: 256 << 20,
			divisor: 16,
			want:    256 << 20,
		},
		{
			name:    "a small limit takes its share",
			limit:   230 << 20,
			ceiling: 256 << 20,
			divisor: 16,
			want:    (230 << 20) / 16,
		},
		{
			name:    "a large limit stops at the ceiling",
			limit:   16 << 30,
			ceiling: 256 << 20,
			divisor: 16,
			want:    256 << 20,
		},
		{
			name:    "a tiny limit keeps 1 MiB",
			limit:   4 << 20,
			ceiling: 256 << 20,
			divisor: 16,
			want:    1 << 20,
		},
		{
			name:    "a ceiling under 1 MiB is kept",
			limit:   4 << 20,
			ceiling: 512 << 10,
			divisor: 16,
			want:    512 << 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := share(tt.limit, tt.ceiling, tt.divisor); got != tt.want {
				t.Errorf("share(%d, %d, %d) = %d, want %d", tt.limit, tt.ceiling, tt.divisor, got, tt.want)
			}
		})
	}
}
