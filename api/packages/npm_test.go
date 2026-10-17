package packages

import "testing"

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
