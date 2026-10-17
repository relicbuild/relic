package nuget

import "testing"

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want int
	}{
		{
			"equal",
			"1.2.3",
			"1.2.3",
			0,
		},
		{
			"missing parts are zero",
			"1.2",
			"1.2.0.0",
			0,
		},
		{
			"the fourth part counts",
			"1.0.0.1",
			"1.0.0.2",
			-1,
		},
		{
			"numbers, not text",
			"1.10.0",
			"1.9.0",
			1,
		},
		{
			"a release follows its prerelease",
			"1.0.0",
			"1.0.0-beta",
			1,
		},
		{
			"prerelease labels ignore case",
			"1.0.0-Beta",
			"1.0.0-beta",
			0,
		},
		{
			"prerelease labels in order",
			"1.0.0-alpha",
			"1.0.0-beta.2",
			-1,
		},
		{
			"build metadata is ignored",
			"1.0.0+build.5",
			"1.0.0",
			0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := compareVersions(tt.a, tt.b); got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}

			if got := compareVersions(tt.b, tt.a); got != -tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.b, tt.a, got, -tt.want)
			}
		})
	}
}
