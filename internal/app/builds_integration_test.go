//go:build integration

package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestPublishBuildRun(t *testing.T) {
	t.Parallel()
	srv := server(t)
	json := map[string]string{
		"Content-Type": "application/json",
	}

	run := `{"run":{"name":"app","number":"7","started":"2026-09-25T10:00:00Z","agentName":"ci","agentVersion":"1.0",
		"vcs":[{"url":"https://git.example/app","revision":"abc","branch":"main"}],
		"modules":[{"id":"app:7","type":"generic","artifacts":[{"name":"app.bin","path":"dist/app.bin","sha1":"s1","sha256":"s256","md5":"m5"}]}],
		"properties":{"ci":"true"}}}`
	if resp, b := do(
		t,
		srv,
		call{
			method:  http.MethodPost,
			path:    "/api/v1/builds/app/runs",
			body:    run,
			headers: json,
		},
	); resp.StatusCode != http.StatusOK {
		t.Fatalf("publish = %d %s", resp.StatusCode, b)
	}

	resp, b := do(t, srv, call{
		method: http.MethodGet,
		path:   "/api/v1/builds/app/runs/7",
	})
	if resp.StatusCode != http.StatusOK || !strings.Contains(b, `"agent":"ci 1.0"`) ||
		!strings.Contains(b, `"md5":"m5"`) ||
		!strings.Contains(b, `"revision":"abc"`) {
		t.Errorf("run = %d %s", resp.StatusCode, b)
	}

	if resp, b := do(
		t,
		srv,
		call{
			method:  http.MethodPost,
			path:    "/api/v1/builds/app/runs",
			body:    `{"run":{"name":"app","number":"8"}}`,
			headers: json,
		},
	); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("run without a start = %d %s", resp.StatusCode, b)
	}

	if resp, b := do(
		t,
		srv,
		call{
			method:  http.MethodPut,
			path:    "/api/v1/users/bob",
			body:    `{"name":"bob","email":"b@example.com","password":"Bob-pass-12345"}`,
			headers: json,
		},
	); resp.StatusCode != http.StatusOK {
		t.Fatalf("create bob = %d %s", resp.StatusCode, b)
	}

	req := call{
		method: http.MethodPost,
		path:   "/api/v1/builds/other/runs",
		body:   strings.ReplaceAll(run, `"name":"app"`, `"name":"other"`),
		noAuth: true,
		headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Basic Ym9iOkJvYi1wYXNzLTEyMzQ1",
		},
	}
	if resp, b := do(t, srv, req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("publish without permission = %d %s", resp.StatusCode, b)
	}
}
