//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoModuleProxy(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/!upper/@v/list":
			_, _ = io.WriteString(w, "v0.1.0\nv0.2.0\n")
		case "/example.com/!upper/@v/v0.2.0.info":
			_, _ = io.WriteString(w, `{"Version":"v0.2.0","Time":"2026-01-02T03:04:05Z"}`)
		case "/example.com/!upper/@v/v0.2.0.mod":
			_, _ = io.WriteString(w, "module example.com/Upper\n")
		default:
			http.Error(w, "gone", http.StatusGone)
		}
	}))
	t.Cleanup(upstream.Close)
	srv, db := serverDB(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/go-local",
			body:   `{"rclass":"local","packageType":"go"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/go-remote",
			body:   `{"rclass":"remote","packageType":"go","url":"` + upstream.URL + `"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/go",
			body:   `{"rclass":"virtual","packageType":"go","repositories":["go-local","go-remote"]}`,
		},
	)

	for _, v := range []string{
		"v1.0.0",
		"v1.1.0-rc.1",
		"v0.9.0",
	} {
		for ext, body := range map[string]string{
			".zip": "zip " + v,
			".mod": "module example.com/lib\n",
		} {
			if resp, _ := do(
				t,
				srv,
				call{
					method: "PUT",
					path:   "/artifactory/go-local/example.com/lib/@v/" + v + ext,
					body:   body,
				},
			); resp.StatusCode != 201 {
				t.Fatalf("deploy %s%s = %d", v, ext, resp.StatusCode)
			}
		}
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/go/go-local/example.com/Mixed/@v/v1.0.0.zip;go.version=v1.0.0",
			body:   "mixed zip",
		},
	)

	tests := []struct {
		name, path string
		status     int
		want       string
	}{
		{
			"local list",
			"/artifactory/api/go/go-local/example.com/lib/@v/list",
			200,
			"v0.9.0\nv1.0.0\nv1.1.0-rc.1\n",
		},
		{
			"latest prefers releases",
			"/artifactory/api/go/go/example.com/lib/@latest",
			200,
			`"Version":"v1.0.0"`,
		},
		{
			"synthesized info",
			"/artifactory/api/go/go-local/example.com/lib/@v/v0.9.0.info",
			200,
			`"Version":"v0.9.0"`,
		},
		{
			"mod",
			"/artifactory/api/go/go/example.com/lib/@v/v1.0.0.mod",
			200,
			"module example.com/lib\n",
		},
		{
			"zip",
			"/artifactory/api/go/go/example.com/lib/@v/v1.0.0.zip",
			200,
			"zip v1.0.0",
		},
		{
			"case-encoded request finds the raw path",
			"/artifactory/api/go/go/example.com/!mixed/@v/v1.0.0.zip",
			200,
			"mixed zip",
		},
		{
			"latest.info query",
			"/artifactory/api/go/go/example.com/lib/@v/latest.info",
			200,
			`"Version":"v1.0.0"`,
		},
		{
			"unknown version",
			"/artifactory/api/go/go/example.com/lib/@v/v9.9.9.zip",
			404,
			"",
		},
		{
			"remote list through virtual",
			"/artifactory/api/go/go/example.com/!upper/@v/list",
			200,
			"v0.1.0\nv0.2.0\n",
		},
		{
			"remote info",
			"/artifactory/api/go/go/example.com/!upper/@v/v0.2.0.info",
			200,
			`"Time":"2026-01-02T03:04:05Z"`,
		},
		{
			"remote mod",
			"/artifactory/api/go/go/example.com/!upper/@v/v0.2.0.mod",
			200,
			"module example.com/Upper\n",
		},
		{
			"upstream 410 is not found",
			"/artifactory/api/go/go/example.com/missing/@v/list",
			404,
			"",
		},
		{
			"invalid module path",
			"/artifactory/api/go/go/Example.com/lib/@v/list",
			404,
			"",
		},
		{
			"sumdb proxied through the remote",
			"/artifactory/api/go/go/sumdb/sum.golang.org/supported",
			200,
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := do(t, srv, call{
				method: "GET",
				path:   tt.path,
			})
			if resp.StatusCode != tt.status || !strings.Contains(body, tt.want) {
				t.Errorf("GET %s = %d %q, want %d containing %q", tt.path, resp.StatusCode, body, tt.status, tt.want)
			}
		})
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/storage/go-remote/example.com/!upper/@v/v0.2.0.mod",
		},
	); resp.StatusCode != 200 {
		t.Errorf("remote cache = %d", resp.StatusCode)
	}

	rows, err := db.Query(t.Context(), "ALTER TABLE repositories RENAME TO repositories_unavailable")
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()

	for _, path := range []string{
		"/artifactory/api/go/go-local/example.com/lib/@v/list",
		"/artifactory/api/go/go-local/sumdb/sum.golang.org/supported",
	} {
		if resp, body := do(t, srv, call{
			method: "GET",
			path:   path,
		}); resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("GET %s with the repositories unreadable = %d %q, want 500", path, resp.StatusCode, body)
		}
	}
}

func TestGoSumDBEndpoint(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/go-local",
			body:   `{"rclass":"local","packageType":"go"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/go-remote",
			body:   `{"rclass":"remote","packageType":"go","url":"https://proxy.golang.org","offline":true}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/go",
			body:   `{"rclass":"virtual","packageType":"go","repositories":["go-local","go-remote"]}`,
		},
	)

	for _, tt := range []struct {
		path string
		want int
	}{
		{
			"/artifactory/api/go/go/sumdb/sum.golang.org/supported",
			200,
		},
		{
			"/artifactory/api/go/go-remote/sumdb/sum.golang.org/supported",
			200,
		},
		{
			"/artifactory/api/go/go-local/sumdb/sum.golang.org/supported",
			404,
		},
		{
			"/artifactory/api/go/go/sumdb/evil.example.com/supported",
			404,
		},
		{
			"/artifactory/api/go/go/sumdb/sum.golang.org/admin",
			404,
		},
		{
			"/artifactory/api/go/go/sumdb/sum.golang.org/latest",
			404,
		},
	} {
		if resp, _ := do(t, srv, call{
			method: "GET",
			path:   tt.path,
		}); resp.StatusCode != tt.want {
			t.Errorf("GET %s = %d, want %d", tt.path, resp.StatusCode, tt.want)
		}
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/go/go/sumdb/sum.golang.org/supported",
			noAuth: true,
		},
	); resp.StatusCode != 401 {
		t.Errorf("anonymous supported = %d, want 401", resp.StatusCode)
	}
}

func TestGoModuleUploadedAtItsCaseEncodedPath(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/go-local",
			body:   `{"rclass":"local","packageType":"go"}`,
		},
	)

	for _, prefix := range []string{
		"/go/go-local",
		"/artifactory/api/go/go-local",
	} {
		t.Run(prefix, func(t *testing.T) {
			module := prefix + "/example.com/!raw!mod" + strings.ReplaceAll(prefix, "/", "-")
			for ext, body := range map[string]string{
				".mod": "module example.com/RawMod\n",
				".zip": "zip",
			} {
				if resp, reply := do(t, srv, call{
					method: "PUT",
					path:   module + "/@v/v1.0.0" + ext,
					body:   body,
				}); resp.StatusCode != http.StatusCreated {
					t.Fatalf("upload %s = %d %s", ext, resp.StatusCode, reply)
				}
			}

			if resp, body := do(t, srv, call{
				method: "GET",
				path:   module + "/@v/list",
			}); resp.StatusCode != http.StatusOK || body != "v1.0.0\n" {
				t.Errorf("list = %d %q, want v1.0.0", resp.StatusCode, body)
			}

			if resp, body := do(t, srv, call{
				method: "GET",
				path:   module + "/@v/v1.0.0.mod",
			}); resp.StatusCode != http.StatusOK || body != "module example.com/RawMod\n" {
				t.Errorf("go.mod = %d %q", resp.StatusCode, body)
			}
		})
	}
}
