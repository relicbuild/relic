package gomod

import "testing"

func TestSumDBPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, path string
		ok         bool
	}{
		{
			"latest",
			"latest",
			true,
		},
		{
			"lookup",
			"lookup/github.com/google/uuid@v1.6.0",
			true,
		},
		{
			"lookup with case encoding",
			"lookup/github.com/!azure/sdk@v0.1.0+incompatible",
			true,
		},
		{
			"hash tile",
			"tile/8/0/x123/456",
			true,
		},
		{
			"partial tile",
			"tile/8/1/789.p/10",
			true,
		},
		{
			"data tile",
			"tile/8/data/x001/234",
			true,
		},
		{
			"traversal",
			"lookup/../../etc@v1",
			false,
		},
		{
			"unknown endpoint",
			"admin",
			false,
		},
		{
			"lookup without version",
			"lookup/github.com/google/uuid",
			false,
		},
		{
			"bad tile",
			"tile/8/0/12",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := validSumDBPath(tt.path); got != tt.ok {
				t.Errorf("%q accepted = %v, want %v", tt.path, got, tt.ok)
			}
		})
	}
}
