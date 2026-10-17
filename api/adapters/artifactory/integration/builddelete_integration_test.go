//go:build integration

package integration

import (
	"net/http"
	"testing"
)

func TestDeleteBuildsByQuery(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for _, number := range []string{
		"1",
		"2",
		"3",
	} {
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/build",
			body:   `{"version":"1.0.1","name":"classic","number":"` + number + `","started":"2026-09-23T10:00:00.000+0000"}`,
		}); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("publish %s = %d %s", number, resp.StatusCode, body)
		}
	}

	steps := []struct {
		name       string
		call       call
		wantStatus int
	}{
		{
			name: "delete two runs",
			call: call{
				method: "DELETE",
				path:   "/artifactory/api/build/classic?buildNumbers=1,2&artifacts=0",
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "a deleted run is gone",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/classic/1",
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "the other run stays",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/classic/3",
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "neither run numbers nor deleteAll",
			call: call{
				method: "DELETE",
				path:   "/artifactory/api/build/classic",
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "delete every run",
			call: call{
				method: "DELETE",
				path:   "/artifactory/api/build/classic?deleteAll=1",
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "the build is gone",
			call: call{
				method: "GET",
				path:   "/artifactory/api/build/classic",
			},
			wantStatus: http.StatusNotFound,
		},
	}
	for _, step := range steps {
		if resp, body := do(t, srv, step.call); resp.StatusCode != step.wantStatus {
			t.Errorf("%s: %s %s = %d %s, want %d",
				step.name, step.call.method, step.call.path, resp.StatusCode, body, step.wantStatus)
		}
	}
}
