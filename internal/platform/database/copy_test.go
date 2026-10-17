package database

import "testing"

func TestCopyBatchFull(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rows int
		size int
		want bool
	}{
		{
			name: "few small rows",
			rows: 10,
			size: 1 << 10,
			want: false,
		},
		{
			name: "row limit",
			rows: copyBatch,
			size: 1 << 10,
			want: true,
		},
		{
			name: "one row short of the row limit",
			rows: copyBatch - 1,
			size: 1 << 10,
			want: false,
		},
		{
			name: "few wide rows",
			rows: 3,
			size: copyBatchBytes,
			want: true,
		},
		{
			name: "one byte short of the size limit",
			rows: 3,
			size: copyBatchBytes - 1,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := copyBatchFull(tt.rows, tt.size); got != tt.want {
				t.Errorf("copyBatchFull(%d, %d) = %v; want %v", tt.rows, tt.size, got, tt.want)
			}
		})
	}
}
