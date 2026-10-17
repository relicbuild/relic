package ldap

import (
	"errors"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
)

func TestSearchResult(t *testing.T) {
	t.Parallel()

	sizeLimited := goldap.NewError(goldap.LDAPResultSizeLimitExceeded, errors.New("size limit exceeded"))

	tests := []struct {
		name      string
		sizeLimit int
		err       error
		want      string
	}{
		{
			name:      "found",
			sizeLimit: 2,
			want:      "success",
		},
		{
			name:      "user search over its own limit",
			sizeLimit: 2,
			err:       sizeLimited,
			want:      "failure",
		},
		{
			name:      "group search over the server's limit",
			sizeLimit: 0,
			err:       sizeLimited,
			want:      "error",
		},
		{
			name:      "no such base",
			sizeLimit: 0,
			err:       goldap.NewError(goldap.LDAPResultNoSuchObject, errors.New("no such object")),
			want:      "failure",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := &goldap.SearchRequest{
				SizeLimit: tt.sizeLimit,
			}
			if got := searchResult(req, tt.err); got != tt.want {
				t.Errorf("searchResult = %q, want %q", got, tt.want)
			}
		})
	}
}
