//go:build integration

package integration

import "testing"

func TestCurationAuditPassThrough(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/ca-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/ca-local/a/b.txt",
		body:   "content",
	})

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/curation/audit/ca-local/a/b.txt",
		},
	); resp.StatusCode != 200 ||
		got != "content" {
		t.Errorf("pass-through GET = %d %q", resp.StatusCode, got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/curation/audit/ca-local/a/b.txt",
			noAuth: true,
		},
	); resp.StatusCode != 401 {
		t.Errorf("anonymous pass-through = %d, want 401", resp.StatusCode)
	}
}
