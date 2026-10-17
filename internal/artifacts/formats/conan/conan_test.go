package conan

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC).UnixNano()
	for _, in := range []string{
		"2026-03-04T05:06:07.123456Z",
		"2026-03-04T05:06:07.123456+00:00",
		"2026-03-04T05:06:07.123456+0000",
		"2026-03-04T06:06:07.123456+0100",
	} {
		if got := parseTime(in); got != want {
			t.Errorf("parseTime(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestReadCapped(t *testing.T) {
	t.Parallel()

	if got, err := readCapped(strings.NewReader(strings.Repeat("x", maxFile))); err != nil || len(got) != maxFile {
		t.Errorf("at the limit = %d bytes, %v", len(got), err)
	}

	if _, err := readCapped(strings.NewReader(strings.Repeat("x", maxFile+1))); !errors.Is(err, errTooLarge) {
		t.Errorf("above the limit = %v", err)
	}
}

func TestFnmatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern, ref string
		ignoreCase   bool
		want         bool
	}{
		{
			"hel*",
			"hello/1.0@_/_",
			true,
			true,
		},
		{
			"hello/*",
			"hello/1.0@_/_",
			true,
			true,
		},
		{
			"hello/1.?",
			"hello/1.0@_/_",
			true,
			true,
		},
		{
			"Hello*",
			"hello/1.0@_/_",
			true,
			true,
		},
		{
			"Hello*",
			"hello/1.0@_/_",
			false,
			false,
		},
		{
			"zlib*",
			"hello/1.0@_/_",
			true,
			false,
		},
		{
			"hello/1.0@user/*",
			"hello/1.0@_/_",
			true,
			false,
		},
		{
			"a.b*",
			"axb/1@_/_",
			true,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.ref, func(t *testing.T) {
			t.Parallel()

			re, err := fnmatch(tt.pattern, tt.ignoreCase)
			if err != nil {
				t.Fatal(err)
			}

			if got := re.MatchString(tt.ref); got != tt.want {
				t.Errorf("fnmatch(%q) on %q = %v, want %v", tt.pattern, tt.ref, got, tt.want)
			}
		})
	}
}
