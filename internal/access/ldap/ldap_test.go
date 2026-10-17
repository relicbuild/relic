package ldap

import (
	"slices"
	"testing"
)

func TestUnitNames(t *testing.T) {
	t.Parallel()

	const dn = "uid=bob,l=berlin,ou=people,dc=example,dc=com"

	tests := []struct {
		name      string
		attribute string
		want      []string
	}{
		{
			name:      "organizational units",
			attribute: "ou",
			want: []string{
				"people",
			},
		},
		{
			name:      "another unit attribute",
			attribute: "l",
			want: []string{
				"berlin",
			},
		},
		{
			name: "no attribute set",
			want: []string{
				"people",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := unitNames(dn, "dc=example,dc=com", tt.attribute); !slices.Equal(got, tt.want) {
				t.Errorf("unitNames(%q, %q) = %v, want %v", dn, tt.attribute, got, tt.want)
			}
		})
	}
}
