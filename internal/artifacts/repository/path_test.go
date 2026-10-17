package repository

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "root",
			in:   "",
			want: "",
		},
		{
			name: "slash root",
			in:   "/",
			want: "",
		},
		{
			name: "simple",
			in:   "a/b.txt",
			want: "a/b.txt",
		},
		{
			name: "surrounding slashes",
			in:   "/a/b/",
			want: "a/b",
		},
		{
			name: "repeated slashes",
			in:   "a//b",
			want: "a/b",
		},
		{
			name: "spaces and unicode kept",
			in:   "my dir/ü.txt",
			want: "my dir/ü.txt",
		},
		{
			name:    "dot segment",
			in:      "a/./b",
			wantErr: true,
		},
		{
			name:    "dotdot segment",
			in:      "a/../b",
			wantErr: true,
		},
		{
			name:    "leading dotdot",
			in:      "../etc/passwd",
			wantErr: true,
		},
		{
			name:    "NUL",
			in:      "a\x00b",
			wantErr: true,
		},
		{
			name:    "backslash",
			in:      `a\b`,
			wantErr: true,
		},
		{
			name:    "invalid UTF-8",
			in:      "bad/a\xffb.txt",
			wantErr: true,
		},
		{
			name: "non-ASCII UTF-8",
			in:   "docs/Straße.txt",
			want: "docs/Straße.txt",
		},
		{
			name: "dots inside names",
			in:   "a/..b/c..",
			want: "a/..b/c..",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CleanPath(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidPath) {
					t.Fatalf("CleanPath(%q) = %q, %v; want ErrInvalidPath", tt.in, got, err)
				}

				return
			}

			if err != nil || got != tt.want {
				t.Errorf("CleanPath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestSplitAndJoinPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, parent, base string
	}{
		{
			name:   "root file",
			in:     "a.txt",
			parent: ".",
			base:   "a.txt",
		},
		{
			name:   "nested",
			in:     "a/b/c.txt",
			parent: "a/b",
			base:   "c.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parent, base := SplitPath(tt.in)
			if parent != tt.parent || base != tt.base {
				t.Errorf("SplitPath(%q) = %q, %q", tt.in, parent, base)
			}

			if got := JoinPath(parent, base); got != tt.in {
				t.Errorf("JoinPath = %q, want %q", got, tt.in)
			}
		})
	}
}

func TestLikePrefixEscapesWildcards(t *testing.T) {
	t.Parallel()

	if got := likePrefix(`a_%\b`); got != `a\_\%\\b/%` {
		t.Errorf("likePrefix = %q", got)
	}
}

func FuzzCleanPath(f *testing.F) {
	for _, s := range []string{
		"",
		"a/b",
		"/a//b/",
		"../x",
		"a/./b",
		"a\\b",
		"ü/..x",
		"\x00",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got, err := CleanPath(in)
		if err != nil {
			return
		}

		if strings.HasPrefix(got, "/") || strings.HasSuffix(got, "/") || strings.Contains(got, "//") {
			t.Fatalf("CleanPath(%q) = %q has stray slashes", in, got)
		}

		for s := range strings.SplitSeq(got, "/") {
			if s == "." || s == ".." {
				t.Fatalf("CleanPath(%q) = %q keeps a dot segment", in, got)
			}
		}

		if strings.ContainsAny(got, "\x00\\") {
			t.Fatalf("CleanPath(%q) = %q keeps a forbidden byte", in, got)
		}

		if again, err := CleanPath(got); err != nil || again != got {
			t.Fatalf("CleanPath is not idempotent on %q: %q, %v", got, again, err)
		}

		if got != "" {
			parent, name := SplitPath(got)
			if JoinPath(parent, name) != got {
				t.Fatalf("SplitPath/JoinPath do not round-trip %q", got)
			}
		}
	})
}
