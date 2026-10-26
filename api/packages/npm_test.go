package packages

import (
	"testing"

	"relic/internal/artifacts/formats/npm"
)

func TestNpmPackage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rest, name, tail string
	}{
		{
			"lodash",
			"lodash",
			"",
		},
		{
			"lodash/-/lodash-4.17.21.tgz",
			"lodash",
			"-/lodash-4.17.21.tgz",
		},
		{
			"@scope%2fpkg",
			"@scope/pkg",
			"",
		},
		{
			"@scope%2Fpkg",
			"@scope/pkg",
			"",
		},
		{
			"@scope/pkg/-/pkg-1.0.0.tgz",
			"@scope/pkg",
			"-/pkg-1.0.0.tgz",
		},
		{
			"@scope/pkg",
			"@scope/pkg",
			"",
		},
		{
			"-/package/x/dist-tags",
			"-",
			"package/x/dist-tags",
		},
	}
	for _, tt := range tests {
		t.Run(tt.rest, func(t *testing.T) {
			t.Parallel()

			name, tail, err := npmPackage(tt.rest)
			if err != nil || name != tt.name || tail != tt.tail {
				t.Errorf("npmPackage(%q) = %q, %q, %v", tt.rest, name, tail, err)
			}
		})
	}
}

func TestNpmPackageRejectsBadEscapes(t *testing.T) {
	t.Parallel()

	for _, rest := range []string{
		"bad%zz",
		"@scope%zz",
		"@scope/pkg%/-/pkg-1.0.0.tgz",
	} {
		if name, tail, err := npmPackage(rest); err == nil {
			t.Errorf("npmPackage(%q) = %q, %q, want an error", rest, name, tail)
		}
	}
}

func TestNpmDocumentForm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		accept []string
		want   npm.Form
	}{
		{
			name: "npm's own header",
			accept: []string{
				"application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*",
			},
			want: npm.FormAbbreviated,
		},
		{
			name: "the abbreviated form alone",
			accept: []string{
				"application/vnd.npm.install-v1+json",
			},
			want: npm.FormAbbreviated,
		},
		{
			name: "JSON preferred",
			accept: []string{
				"application/vnd.npm.install-v1+json;q=0.5",
				"application/json",
			},
			want: npm.FormFull,
		},
		{
			name: "the abbreviated form refused",
			accept: []string{
				"application/vnd.npm.install-v1+json;q=0, */*",
			},
			want: npm.FormFull,
		},
		{
			name: "anything",
			accept: []string{
				"*/*",
			},
			want: npm.FormFull,
		},
		{
			name: "no header",
			want: npm.FormFull,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := npmDocumentForm(tt.accept); got != tt.want {
				t.Errorf("npmDocumentForm(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}
