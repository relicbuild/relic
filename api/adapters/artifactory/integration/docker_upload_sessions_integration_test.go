//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

func TestDockerUploadSessionsTheRegistryNeverStartedAreUnknown(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/docker-local",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
	)

	unknown := func(name, method, path string) {
		t.Helper()

		resp, body := do(t, srv, call{
			method: method,
			path:   path,
			body:   "chunk",
		})
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "BLOB_UPLOAD_UNKNOWN") {
			t.Errorf("%s: %s = %d %s, want 404 BLOB_UPLOAD_UNKNOWN", name, method, resp.StatusCode, body)
		}
	}

	never := "/v2/docker-local/app/blobs/uploads/9b2f3a4e-1111-4222-8333-944455556666"
	for _, method := range []string{
		"GET",
		"PATCH",
		"DELETE",
	} {
		unknown("a session never started", method, never)
	}

	unknown("a session never started", "PUT", never+"?digest="+digestOf("chunk"))

	resp, _ := do(t, srv, call{
		method: "POST",
		path:   "/v2/docker-local/app/blobs/uploads/",
	})
	started := resp.Header.Get("Location")

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   started,
	}); resp.StatusCode != http.StatusNoContent || resp.Header.Get("Range") != "0-0" {
		t.Errorf("status of a started session = %d, Range %q; want 204, 0-0", resp.StatusCode, resp.Header.Get("Range"))
	}

	if resp, _ := do(t, srv, call{
		method: "DELETE",
		path:   started,
	}); resp.StatusCode != http.StatusNoContent {
		t.Errorf("cancel a started session = %d, want 204", resp.StatusCode)
	}

	unknown("a cancelled session", "GET", started)

	resp, _ = do(t, srv, call{
		method: "POST",
		path:   "/v2/docker-local/app/blobs/uploads/",
	})
	monolithic := resp.Header.Get("Location")

	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   monolithic + "&digest=" + digestOf("whole blob"),
		body:   "whole blob",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("finish a session without chunks = %d %s", resp.StatusCode, body)
	}

	unknown("a finished session", "GET", monolithic)

	pushBlob(t, srv, "docker-local/app", "a chunked blob")
}
