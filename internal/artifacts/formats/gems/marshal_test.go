package gems

import (
	"bytes"
	"testing"
)

func TestMarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want string
	}{
		{
			"nil",
			nil,
			"\x04\x080",
		},
		{
			"true",
			true,
			"\x04\x08T",
		},
		{
			"small int",
			1,
			"\x04\x08i\x06",
		},
		{
			"zero",
			0,
			"\x04\x08i\x00",
		},
		{
			"122, the largest one-byte int",
			122,
			"\x04\x08i\x7f",
		},
		{
			"123, the smallest multi-byte int",
			123,
			"\x04\x08i\x01\x7b",
		},
		{
			"-123, the smallest one-byte int",
			-123,
			"\x04\x08i\x80",
		},
		{
			"-124, the largest negative multi-byte int",
			-124,
			"\x04\x08i\xff\x84",
		},
		{
			"300",
			300,
			"\x04\x08i\x02,\x01",
		},
		{
			"-1",
			-1,
			"\x04\x08i\xfa",
		},
		{
			"-300",
			-300,
			"\x04\x08i\xfe\xd4\xfe",
		},
		{
			"utf-8 string",
			"a",
			"\x04\x08I\"\x06a\x06:\x06ET",
		},
		{
			"symbol",
			Symbol("a"),
			"\x04\x08:\x06a",
		},
		{
			"array",
			[]any{
				nil,
				true,
			},
			"\x04\x08[\x070T",
		},
		{
			"symbol link",
			[]any{
				Symbol("a"),
				Symbol("a"),
			},
			"\x04\x08[\x07:\x06a;\x00",
		},
		{
			"gem version",
			UserMarshal{
				Class: "Gem::Version",
				Data: []any{
					"1.0",
				},
			},
			"\x04\x08U:\x11Gem::Version[\x06I\"\x081.0\x06:\x06ET",
		},
		{
			"hash",
			Hash{
				{
					Symbol("name"),
					"x",
				},
			},
			"\x04\x08{\x06:\x09nameI\"\x06x\x06:\x06ET",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Marshal(tt.in); !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("Marshal = %q, want %q", got, tt.want)
			}
		})
	}
}
