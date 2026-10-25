//go:build integration

package integration

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTerraformModules(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf-local",
			body:   `{"rclass":"local","packageType":"terraform"}`,
		},
	)

	for _, tag := range []string{
		"v1.0.0",
		"v1.2.0",
		"0.9.0",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/tf-local/acme/network/aws/" + tag + ".zip",
				body:   "zip " + tag,
			},
		)
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   "/.well-known/terraform.json",
		noAuth: true,
	}); !strings.Contains(
		body,
		`"modules.v1": "/terraform/v1/modules/"`,
	) {
		t.Errorf("discovery = %s", body)
	}

	_, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf-local__acme/network/aws/versions",
		},
	)
	if !strings.Contains(body, `"version": "0.9.0"`) || !strings.Contains(body, `"version": "1.2.0"`) {
		t.Errorf("versions = %s", body)
	}

	resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf-local__acme/network/aws/1.2.0/download",
		},
	)

	get := resp.Header.Get("X-Terraform-Get")
	if resp.StatusCode != 204 || !strings.Contains(get, "/tf-local/acme/network/aws/v1.2.0.zip?archive=zip&") {
		t.Fatalf("download = %d %q", resp.StatusCode, get)
	}

	if resp, got := do(t, srv, call{
		method: "GET",
		path:   get,
		noAuth: true,
	}); resp.StatusCode != 200 ||
		got != "zip v1.2.0" {
		t.Errorf("signed archive = %d %q", resp.StatusCode, got)
	}

	tampered := strings.Replace(get, "v1.2.0.zip", "v1.0.0.zip", 1)
	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   tampered,
		noAuth: true,
	}); resp.StatusCode != 403 {
		t.Errorf("tampered link = %d, want 403", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf-local__acme/network/aws/versions",
			noAuth: true,
		},
	); resp.StatusCode != 401 {
		t.Errorf("anonymous versions = %d", resp.StatusCode)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf__mods",
			body:   `{"rclass":"local","packageType":"terraform"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/tf__mods/acme/network/aws/v2.0.0.zip",
			body:   "zip v2.0.0",
		},
	)

	if resp, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf__mods__acme/network/aws/versions",
		},
	); resp.StatusCode != 200 || !strings.Contains(body, `"version": "2.0.0"`) {
		t.Errorf("versions in a repository whose key holds __ = %d %s", resp.StatusCode, body)
	}
}

func moduleTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}

		_, _ = io.WriteString(tw, body)
	}

	_ = tw.Close()
	_ = gz.Close()

	return buf.Bytes()
}

func TestTerraformModulesThroughARemote(t *testing.T) {
	t.Parallel()

	archive := moduleTarGz(t, map[string]string{
		"terraform-null-label-0.25.0/README.md":            "label",
		"terraform-null-label-0.25.0/modules/null/main.tf": `variable "name" {}`,
	})

	var archives atomic.Int32

	var upstream *httptest.Server

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/modules/cloudposse/label/null/versions":
			_, _ = io.WriteString(w, `{"modules":[{"versions":[{"version":"0.24.1"},{"version":"0.25.0"}]}]}`)
		case "/v1/modules/cloudposse/label/null/0.25.0/download":
			w.Header().Set(
				"X-Terraform-Get",
				upstream.URL+"/archives/label.tar.gz//terraform-null-label-0.25.0/modules/null?archive=tar.gz",
			)
			w.WriteHeader(http.StatusNoContent)
		case "/archives/label.tar.gz":
			archives.Add(1)

			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	srv := server(t)
	for _, repo := range []struct {
		key, body string
	}{
		{
			key:  "tf-local",
			body: `{"rclass":"local","packageType":"terraform"}`,
		},
		{
			key:  "tf-remote",
			body: `{"rclass":"remote","packageType":"terraform","url":"` + upstream.URL + `"}`,
		},
		{
			key:  "tf",
			body: `{"rclass":"virtual","packageType":"terraform","repositories":["tf-local","tf-remote"]}`,
		},
	} {
		if resp, got := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/" + repo.key,
			body:   repo.body,
		}); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", repo.key, resp.StatusCode, got)
		}
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/tf-local/cloudposse/label/null/v0.26.0.zip",
		body:   "zip v0.26.0",
	})

	for _, ns := range []string{
		"tf__cloudposse",
		"tf-remote__cloudposse",
	} {
		resp, body := do(t, srv, call{
			method: "GET",
			path:   "/terraform/v1/modules/" + ns + "/label/null/versions",
		})
		if resp.StatusCode != 200 || !strings.Contains(body, `"version": "0.25.0"`) ||
			!strings.Contains(body, `"version": "0.24.1"`) {
			t.Errorf("%s versions = %d %s, want the upstream's", ns, resp.StatusCode, body)
		}
	}

	for range 2 {
		resp, _ := do(t, srv, call{
			method: "GET",
			path:   "/terraform/v1/modules/tf__cloudposse/label/null/0.25.0/download",
		})

		get := resp.Header.Get("X-Terraform-Get")
		if resp.StatusCode != 204 ||
			!strings.Contains(get, "/tf-remote/cloudposse/label/null/0.25.0.zip?archive=zip&") {
			t.Fatalf("download = %d %q, want a link to the cached archive", resp.StatusCode, get)
		}

		resp, got := do(t, srv, call{
			method: "GET",
			path:   get,
			noAuth: true,
		})

		zr, err := zip.NewReader(strings.NewReader(got), int64(len(got)))
		if resp.StatusCode != 200 || err != nil || len(zr.File) != 1 || zr.File[0].Name != "main.tf" {
			t.Fatalf("module archive = %d %v, want main.tf at its root", resp.StatusCode, err)
		}
	}

	if archives.Load() != 1 {
		t.Errorf("upstream archive fetched %d times, want once and then the cache", archives.Load())
	}
}
