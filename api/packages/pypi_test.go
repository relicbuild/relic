package packages

import "testing"

func TestPreferredLatest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		candidate, current string
		want               bool
	}{
		{
			name:      "final over its release candidate",
			candidate: "1.0",
			current:   "1.0rc1",
			want:      true,
		},
		{
			name:      "release candidate never replaces a final",
			candidate: "1.0rc1",
			current:   "1.0",
			want:      false,
		},
		{
			name:      "older final over a newer pre-release",
			candidate: "0.9",
			current:   "1.0a1",
			want:      true,
		},
		{
			name:      "numeric release order",
			candidate: "1.10.0",
			current:   "1.9.0",
			want:      true,
		},
		{
			name:      "trailing zeros are equal",
			candidate: "1.0.0",
			current:   "1.0",
			want:      false,
		},
		{
			name:      "post release after its release",
			candidate: "1.0.post1",
			current:   "1.0",
			want:      true,
		},
		{
			name:      "epoch outranks the release",
			candidate: "1!0.1",
			current:   "2.0",
			want:      true,
		},
		{
			name:      "beta after alpha",
			candidate: "1.0b1",
			current:   "1.0a2",
			want:      true,
		},
		{
			name:      "alpha after a dev release",
			candidate: "1.0a1",
			current:   "1.0.dev3",
			want:      true,
		},
		{
			name:      "a valid version over an invalid one",
			candidate: "0.1",
			current:   "not-a-version",
			want:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := preferredLatest(tt.candidate, tt.current); got != tt.want {
				t.Errorf("preferredLatest(%q, %q) = %v, want %v", tt.candidate, tt.current, got, tt.want)
			}
		})
	}
}
