package maven

import "testing"

func TestCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want int
	}{
		{
			"1.0",
			"1.0.0",
			0,
		},
		{
			"1",
			"1.0",
			0,
		},
		{
			"1.0",
			"1.1",
			-1,
		},
		{
			"1.10",
			"1.9",
			1,
		},
		{
			"1.0.99999999999999999999",
			"1.0.1",
			1,
		},
		{
			"1.0.100000000000000000000",
			"1.0.99999999999999999999",
			1,
		},
		{
			"1.007",
			"1.7",
			0,
		},
		{
			"1.0-alpha-1",
			"1.0-beta-1",
			-1,
		},
		{
			"1.0-beta",
			"1.0-rc1",
			-1,
		},
		{
			"1.0-rc1",
			"1.0-SNAPSHOT",
			-1,
		},
		{
			"1.0-SNAPSHOT",
			"1.0",
			-1,
		},
		{
			"1.0",
			"1.0-sp1",
			-1,
		},
		{
			"2.0.0-M1",
			"2.0.0-RC1",
			-1,
		},
		{
			"1.0.1",
			"1.0-SNAPSHOT",
			1,
		},
		{
			"1.0-foo",
			"1.0-bar",
			1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			t.Parallel()

			if got := Compare(tt.a, tt.b); got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}

			if got := Compare(tt.b, tt.a); got != -tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d (antisymmetry)", tt.b, tt.a, got, -tt.want)
			}
		})
	}
}
