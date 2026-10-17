package packages

import (
	"reflect"
	"testing"

	"relic/internal/platform/objectstore"
)

func TestParseRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		size   int64
		want   *objectstore.Range
		ok     bool
	}{
		{
			name:   "none",
			header: "",
			size:   10,
			ok:     true,
		},
		{
			name:   "closed",
			header: "bytes=0-4",
			size:   10,
			want: &objectstore.Range{
				Start: 0,
				End:   4,
			},
			ok: true,
		},
		{
			name:   "open end",
			header: "bytes=5-",
			size:   10,
			want: &objectstore.Range{
				Start: 5,
				End:   9,
			},
			ok: true,
		},
		{
			name:   "suffix",
			header: "bytes=-3",
			size:   10,
			want: &objectstore.Range{
				Start: 7,
				End:   9,
			},
			ok: true,
		},
		{
			name:   "end clamped",
			header: "bytes=8-100",
			size:   10,
			want: &objectstore.Range{
				Start: 8,
				End:   9,
			},
			ok: true,
		},
		{
			name:   "multi range ignored",
			header: "bytes=0-1,4-5",
			size:   10,
			ok:     true,
		},
		{
			name:   "start past end",
			header: "bytes=10-",
			size:   10,
			ok:     false,
		},
		{
			name:   "inverted",
			header: "bytes=5-2",
			size:   10,
			ok:     false,
		},
		{
			name:   "garbage",
			header: "bytes=a-b",
			size:   10,
			ok:     false,
		},
		{
			name:   "empty file",
			header: "bytes=0-",
			size:   0,
			ok:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseRange(tt.header, tt.size)
			if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseRange(%q, %d) = %+v, %v; want %+v, %v", tt.header, tt.size, got, ok, tt.want, tt.ok)
			}
		})
	}
}
