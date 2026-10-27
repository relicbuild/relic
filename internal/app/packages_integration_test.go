//go:build integration

package app

import (
	"crypto/sha1" //nolint:gosec // Maven's checksum file
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestNativePackageEndpoints(t *testing.T) {
	t.Parallel()

	srv := server(t)
	for _, r := range []struct {
		name, format string
	}{
		{
			name:   "mvn-local",
			format: "MAVEN",
		},
		{
			name:   "npm-local",
			format: "NPM",
		},
		{
			name:   "pypi-local",
			format: "PYPI",
		},
		{
			name:   "nuget-local",
			format: "NUGET",
		},
		{
			name:   "cargo-local",
			format: "CARGO",
		},
		{
			name:   "helm-local",
			format: "HELM",
		},
		{
			name:   "gems-local",
			format: "RUBYGEMS",
		},
		{
			name:   "conan-local",
			format: "CONAN",
		},
		{
			name:   "nix-local",
			format: "NIX",
		},
		{
			name:   "go-local",
			format: "GOMOD",
		},
		{
			name:   "deb-local",
			format: "DEBIAN",
		},
	} {
		createRepository(t, srv, r.name, "LOCAL", r.format, "")
	}

	jar := "jar bytes"
	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/maven/mvn-local/org/app/1.0/app-1.0.jar",
		body:   jar,
	}); resp.StatusCode != http.StatusCreated || !strings.Contains(body, `"path":"org/app/1.0/app-1.0.jar"`) {
		t.Fatalf("maven deploy = %d %s", resp.StatusCode, body)
	}

	sum := sha1.Sum([]byte(jar)) //nolint:gosec // Maven's checksum file
	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/maven/mvn-local/org/app/1.0/app-1.0.jar.sha1",
		body:   hexSum(sum[:]),
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("maven checksum = %d %s", resp.StatusCode, body)
	}

	resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/maven/mvn-local/org/app/1.0/app-1.0.jar",
	})
	if resp.StatusCode != http.StatusOK || body != jar {
		t.Fatalf("maven download = %d %q", resp.StatusCode, body)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/maven/mvn-local/org/app/maven-metadata.xml",
	}); resp.StatusCode != http.StatusOK || !strings.Contains(body, "<version>1.0</version>") {
		t.Fatalf("maven metadata = %d %s", resp.StatusCode, body)
	}

	tgz := npmTarball(t, "native-pkg", "1.0.0")
	integrity := sha512.Sum512(tgz)

	doc, _ := json.Marshal(map[string]any{
		"name": "native-pkg",
		"dist-tags": map[string]string{
			"latest": "1.0.0",
		},
		"versions": map[string]any{
			"1.0.0": map[string]any{
				"name":    "native-pkg",
				"version": "1.0.0",
				"dist": map[string]string{
					"tarball":   "http://ignored/native-pkg/-/native-pkg-1.0.0.tgz",
					"integrity": "sha512-" + base64.StdEncoding.EncodeToString(integrity[:]),
				},
			},
		},
		"_attachments": map[string]any{
			"native-pkg-1.0.0.tgz": map[string]any{
				"data":   base64.StdEncoding.EncodeToString(tgz),
				"length": len(tgz),
			},
		},
	})
	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/npm/npm-local/native-pkg",
		body:   string(doc),
		headers: map[string]string{
			"Content-Type": "application/json",
		},
	}); resp.StatusCode/100 != 2 {
		t.Fatalf("npm publish = %d %s", resp.StatusCode, body)
	}

	tarball := srv.URL + "/npm/npm-local/native-pkg/-/native-pkg-1.0.0.tgz"
	if resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/npm/npm-local/native-pkg",
	}); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"tarball":"`+tarball+`"`) {
		t.Fatalf("packument = %d %s; want tarball %s", resp.StatusCode, body, tarball)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/npm/npm-local/native-pkg",
		headers: map[string]string{
			"Accept": "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*",
		},
	}); resp.StatusCode != http.StatusOK ||
		resp.Header.Get("Content-Type") != "application/vnd.npm.install-v1+json" ||
		resp.Header.Get("Vary") != "Accept" || strings.Contains(body, `"time"`) {
		t.Fatalf("abbreviated packument = %d %v %s", resp.StatusCode, resp.Header, body)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   strings.TrimPrefix(tarball, srv.URL),
	}); resp.StatusCode != http.StatusOK || body != string(tgz) {
		t.Fatalf("tarball = %d, %d bytes; want the published %d bytes", resp.StatusCode, len(body), len(tgz))
	}

	tests := []struct {
		name   string
		call   call
		status int
		want   string
	}{
		{
			name: "PyPI simple index",
			call: call{
				method: http.MethodGet,
				path:   "/pypi/pypi-local/simple/",
			},
			status: http.StatusOK,
		},
		{
			name: "NuGet service index links under the mount",
			call: call{
				method: http.MethodGet,
				path:   "/nuget/nuget-local/index.json",
			},
			status: http.StatusOK,
			want:   `"` + srv.URL + "/nuget/",
		},
		{
			name: "Cargo registry configuration links under the mount",
			call: call{
				method: http.MethodGet,
				path:   "/cargo/cargo-local/index/config.json",
			},
			status: http.StatusOK,
			want:   `"dl":"` + srv.URL + "/cargo/cargo-local/v1/crates",
		},
		{
			name: "Helm index",
			call: call{
				method: http.MethodGet,
				path:   "/helm/helm-local/index.yaml",
			},
			status: http.StatusOK,
			want:   "apiVersion",
		},
		{
			name: "RubyGems compact index",
			call: call{
				method: http.MethodGet,
				path:   "/gems/gems-local/versions",
			},
			status: http.StatusOK,
		},
		{
			name: "Conan ping, anonymous",
			call: call{
				method: http.MethodGet,
				path:   "/conan/conan-local/v1/ping",
				noAuth: true,
			},
			status: http.StatusOK,
		},
		{
			name: "Nix cache info",
			call: call{
				method: http.MethodGet,
				path:   "/nix/nix-local/nix-cache-info",
			},
			status: http.StatusOK,
			want:   "StoreDir",
		},
		{
			name: "Go module list of an unknown module",
			call: call{
				method: http.MethodGet,
				path:   "/go/go-local/example.com/none/@v/list",
			},
			status: http.StatusNotFound,
		},
		{
			name: "Terraform discovery points at the native module registry",
			call: call{
				method: http.MethodGet,
				path:   "/.well-known/terraform.json",
				noAuth: true,
			},
			status: http.StatusOK,
			want:   `"modules.v1":"/terraform/v1/modules/"`,
		},
		{
			name: "Hugging Face answers in the Hub's error shape",
			call: call{
				method: http.MethodGet,
				path:   "/huggingface/missing/api/models/org/model/revision/main",
			},
			status: http.StatusNotFound,
			want:   `"error":"repository not found"`,
		},
		{
			name: "Errors in Relic's envelope",
			call: call{
				method: http.MethodGet,
				path:   "/maven/missing-repo/org/app/1.0/app-1.0.jar",
			},
			status: http.StatusNotFound,
			want:   `"code": "not_found"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, body := do(t, srv, tt.call)
			if resp.StatusCode != tt.status || !strings.Contains(body, tt.want) {
				t.Errorf(
					"%s %s = %d %s; want %d with %q",
					tt.call.method,
					tt.call.path,
					resp.StatusCode,
					body,
					tt.status,
					tt.want,
				)
			}
		})
	}
}
