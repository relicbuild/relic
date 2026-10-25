package main

import (
	"path/filepath"
	"testing"

	"relic/internal/platform/scripttest"
)

func TestSyftPURLsSaysWhySyftFailed(t *testing.T) {
	t.Parallel()

	failing := "#!/bin/sh\nfor a; do [ \"$a\" = -q ] && exit 1; done\n" +
		"echo 'ERROR could not determine source: snap: no snap found' >&2\nexit 1\n"
	missing := filepath.Join(t.TempDir(), "missing")

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "a missing path",
			target: missing,
			want:   "no such file or directory",
		},
		{
			name:   "a missing directory",
			target: "dir:" + missing,
			want:   "no such file or directory",
		},
		{
			name:   "a missing relative path",
			target: "./no-such-target",
			want:   "no such file or directory",
		},
		{
			name:   "an image Syft cannot pull",
			target: "team/app:1.4.0",
			want:   "syft team/app:1.4.0: exit status 1: ERROR could not determine source: snap: no snap found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			syft := filepath.Join(t.TempDir(), "syft")
			scripttest.Write(t, syft, failing)

			_, err := syftPURLs(t.Context(), syft, tt.target)
			if err == nil || err.Error() != tt.want {
				t.Errorf("syftPURLs(%q) = %v, want %q", tt.target, err, tt.want)
			}
		})
	}
}
