package docker_test

import (
	"strings"
	"testing"

	"relic/internal/artifacts/formats/docker"
)

func TestValidTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  string
		want bool
	}{
		{
			name: "plain tag",
			tag:  "latest",
			want: true,
		},
		{
			name: "leading underscore",
			tag:  "_build",
			want: true,
		},
		{
			name: "dots and dashes",
			tag:  "1.0-rc.1",
			want: true,
		},
		{
			name: "longest tag",
			tag:  strings.Repeat("a", 128),
			want: true,
		},
		{
			name: "too long",
			tag:  strings.Repeat("a", 129),
		},
		{
			name: "leading dot",
			tag:  ".hidden",
		},
		{
			name: "the folder of unreferenced uploads",
			tag:  "_uploads",
		},
		{
			name: "the folder of a manifest pushed by digest",
			tag:  "sha256__" + strings.Repeat("a", 64),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := docker.ValidTag(tt.tag); got != tt.want {
				t.Errorf("ValidTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}
