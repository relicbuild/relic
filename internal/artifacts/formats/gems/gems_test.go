package gems

import "testing"

func TestInfoLineVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line string
		want string
		ok   bool
	}{
		{
			"1.0.0 rack:>= 2|checksum:abc",
			"1.0.0",
			true,
		},
		{
			"---",
			"",
			false,
		},
		{
			"",
			"",
			false,
		},
		{
			"   ",
			"",
			false,
		},
		{
			"\r",
			"",
			false,
		},
	}
	for _, tt := range tests {
		got, ok := infoLineVersion(tt.line)
		if got != tt.want || ok != tt.ok {
			t.Errorf("infoLineVersion(%q) = %q, %v; want %q, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}
