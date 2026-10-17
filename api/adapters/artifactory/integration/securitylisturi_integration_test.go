//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSecurityListingsLinkNamesWithSlashes(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for _, c := range []call{
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/slash-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/security/groups/team%2Fslash",
			body:   `{"description":"a group with a slash"}`,
		},
		{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/target%2Fslash",
			body: `{"repo":{"repositories":["slash-local"],` +
				`"actions":{"groups":{"team/slash":["read"]}}}}`,
		},
	} {
		if resp, body := do(t, srv, c); resp.StatusCode/100 != 2 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	tests := []struct {
		name    string
		listing string
		entry   string
	}{
		{
			name:    "groups",
			listing: "/artifactory/api/security/groups",
			entry:   "team/slash",
		},
		{
			name:    "permission targets",
			listing: "/artifactory/api/v2/security/permissions",
			entry:   "target/slash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, body := do(t, srv, call{
				method: "GET",
				path:   tt.listing,
			})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d %s", tt.listing, resp.StatusCode, body)
			}

			var entries []struct {
				Name string `json:"name"`
				URI  string `json:"uri"`
			}
			if err := json.Unmarshal([]byte(body), &entries); err != nil {
				t.Fatal(err)
			}

			uri := ""

			for _, e := range entries {
				if e.Name == tt.entry {
					uri = e.URI
				}
			}

			if uri == "" {
				t.Fatalf("%s lists no %q: %s", tt.listing, tt.entry, body)
			}

			if resp, body := do(t, srv, call{
				method: "GET",
				path:   strings.TrimPrefix(uri, srv.URL),
			}); resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s = %d %s, want 200", uri, resp.StatusCode, body)
			}
		})
	}
}
