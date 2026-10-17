//go:build integration

package integration

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPackageMountHeaders(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/mvn-local",
			body:   `{"rclass":"local","packageType":"maven"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/mvn-local/org/app/1.0/app-1.0.jar",
			body:   "jar bytes",
		},
	)

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "at the adapter's path",
			path: "/artifactory/mvn-local/org/app/1.0/app-1.0.jar",
			want: "app-1.0.jar",
		},
		{
			name: "at Relic's mount",
			path: "/maven/mvn-local/org/app/1.0/app-1.0.jar",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, body := do(t, srv, call{
				method: "GET",
				path:   tt.path,
			})
			if resp.StatusCode != 200 || body != "jar bytes" || resp.Header.Get("X-Artifactory-Filename") != tt.want {
				t.Errorf("GET %s = %d %q, X-Artifactory-Filename %q; want %q",
					tt.path, resp.StatusCode, body, resp.Header.Get("X-Artifactory-Filename"), tt.want)
			}
		})
	}
}

type deployBeforeReading struct {
	deploy func()
	body   io.Reader
	once   sync.Once
}

func (d *deployBeforeReading) Read(p []byte) (int, error) {
	d.once.Do(d.deploy)

	return d.body.Read(p)
}

func TestPackageMountOverwriteNeedsDeleteAtTheWrite(t *testing.T) {
	t.Parallel()

	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/mvn-local",
			body:   `{"rclass":"local","packageType":"maven"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/deployer",
			body:   `{"email":"x@example.com","password":"Pass-word-1234"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/deployer",
			body: `{"name":"deployer","repo":{"repositories":["mvn-local"],"include-patterns":["**"],` +
				`"actions":{"users":{"deployer":["read","write"]}}}}`,
		},
	)

	const jar = "/maven/mvn-local/org/app/1.0/app-1.0.jar"

	body := &deployBeforeReading{
		deploy: func() {
			do(t, srv, call{
				method: "PUT",
				path:   jar,
				body:   "the admin's jar",
			})
		},
		body: strings.NewReader("the deployer's jar"),
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+jar, body)
	if err != nil {
		t.Fatal(err)
	}

	req.SetBasicAuth("deployer", "Pass-word-1234")
	req.Header.Set("Expect", "100-continue")

	resp, err := (&http.Client{
		Transport: &http.Transport{
			ExpectContinueTimeout: time.Minute,
		},
	}).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("overwrite of a file created while the upload ran = %d, want 403", resp.StatusCode)
	}

	if _, got := do(t, srv, call{
		method: "GET",
		path:   jar,
	}); got != "the admin's jar" {
		t.Errorf("the file holds %q, want the admin's jar", got)
	}
}
