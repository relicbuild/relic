//go:build integration

package integration

import (
	"crypto/sha1" //nolint:gosec // Conan sends SHA-1 checksums
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // Conan's checksum
	return hex.EncodeToString(sum[:])
}

func TestConanRepository(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/conan-local",
			body:   `{"rclass":"local","packageType":"conan"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/conan",
			body:   `{"rclass":"virtual","packageType":"conan","repositories":["conan-local"],"defaultDeploymentRepo":"conan-local"}`,
		},
	)

	const (
		R   = "/artifactory/api/conan/conan"
		ref = R + "/v2/conans/hello/1.0/_/_"
	)

	resp, _ := do(t, srv, call{
		method: "GET",
		path:   R + "/v1/ping",
		noAuth: true,
	})
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("X-Conan-Server-Capabilities"), "revisions") {
		t.Fatalf("ping = %d %v", resp.StatusCode, resp.Header)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   ref + "/latest",
		noAuth: true,
	}); resp.StatusCode != 401 {
		t.Errorf("anonymous latest = %d, want 401", resp.StatusCode)
	}

	resp, token := do(t, srv, call{
		method: "GET",
		path:   R + "/v2/users/authenticate",
	})
	if resp.StatusCode != 200 || token == "" {
		t.Fatalf("authenticate = %d", resp.StatusCode)
	}

	bearer := map[string]string{
		"Authorization": "Bearer " + token,
	}
	if _, who := do(
		t,
		srv,
		call{
			method:  "GET",
			path:    R + "/v2/users/check_credentials",
			headers: bearer,
			noAuth:  true,
		},
	); who != "admin" {
		t.Errorf("check_credentials = %q", who)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   ref + "/latest",
	}); resp.StatusCode != 404 {
		t.Errorf("latest before upload = %d, want 404", resp.StatusCode)
	}

	recipe := map[string]string{
		"conan_export.tgz":  "export",
		"conanfile.py":      "from conan import ConanFile",
		"conanmanifest.txt": "manifest r1",
	}
	put := func(url, body string) int {
		resp, _ := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   url,
				body:   body,
				headers: map[string]string{
					"X-Checksum-Sha1": sha1Hex(body),
				},
			},
		)

		return resp.StatusCode
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   ref + "/revisions/r1/files/conan_export.tgz",
			headers: map[string]string{
				"X-Checksum-Deploy": "true",
				"X-Checksum-Sha1":   sha1Hex("export"),
			},
		},
	); resp.StatusCode != 404 {
		t.Errorf("checksum deploy of unknown content = %d, want 404", resp.StatusCode)
	}

	for _, f := range []string{
		"conan_export.tgz",
		"conanfile.py",
	} {
		if code := put(ref+"/revisions/r1/files/"+f, recipe[f]); code != 201 {
			t.Fatalf("upload %s = %d", f, code)
		}
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   ref + "/revisions",
	}); resp.StatusCode != 404 {
		t.Errorf("revisions before manifest = %d, want 404", resp.StatusCode)
	}

	if code := put(ref+"/revisions/r1/files/conanmanifest.txt", recipe["conanmanifest.txt"]); code != 201 {
		t.Fatalf("upload manifest = %d", code)
	}

	resp, latest := do(t, srv, call{
		method: "GET",
		path:   ref + "/latest",
	})
	if resp.StatusCode != 200 || !strings.Contains(latest, `"revision":"r1"`) ||
		resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("latest = %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), latest)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   ref + "/revisions/r2/files/conan_export.tgz",
			headers: map[string]string{
				"X-Checksum-Deploy": "true",
				"X-Checksum-Sha1":   sha1Hex("export"),
			},
		},
	); resp.StatusCode != 201 {
		t.Errorf("checksum deploy of known content = %d, want 201", resp.StatusCode)
	}

	put(ref+"/revisions/r2/files/conanfile.py", "v2")
	put(ref+"/revisions/r2/files/conanmanifest.txt", "manifest r2")

	if _, latest := do(t, srv, call{
		method: "GET",
		path:   ref + "/latest",
	}); !strings.Contains(
		latest,
		`"revision":"r2"`,
	) {
		t.Errorf("latest after r2 = %s", latest)
	}

	resp, revs := do(t, srv, call{
		method: "GET",
		path:   ref + "/revisions",
	})

	var listed struct {
		Revisions []struct {
			Revision string `json:"revision"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(revs), &listed); resp.StatusCode != 200 || err != nil ||
		len(listed.Revisions) != 2 || listed.Revisions[0].Revision != "r2" || listed.Revisions[1].Revision != "r1" {
		t.Errorf("revisions = %d %s, want r2 then r1", resp.StatusCode, revs)
	}

	if _, files := do(t, srv, call{
		method: "GET",
		path:   ref + "/revisions/r1/files",
	}); !strings.Contains(
		files,
		`"conanfile.py":{}`,
	) {
		t.Errorf("files = %s", files)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   ref + "/revisions/r1/files/conanfile.py",
		},
	); resp.StatusCode != 200 ||
		got != recipe["conanfile.py"] {
		t.Errorf("download = %d %q", resp.StatusCode, got)
	}

	pkg := ref + "/revisions/r1/packages/pid1/revisions/p1/files/"
	put(pkg+"conan_package.tgz", "binary")
	put(pkg+"conaninfo.txt", "[settings]\nos=Linux\n")
	put(pkg+"conanmanifest.txt", "pkg manifest")

	if _, l := do(t, srv, call{
		method: "GET",
		path:   ref + "/revisions/r1/packages/pid1/latest",
	}); !strings.Contains(
		l,
		`"revision":"p1"`,
	) {
		t.Errorf("package latest = %s", l)
	}

	if _, s := do(t, srv, call{
		method: "GET",
		path:   ref + "/revisions/r1/search",
	}); !strings.Contains(s, `"pid1"`) ||
		!strings.Contains(s, "os=Linux") {
		t.Errorf("packages search = %s", s)
	}

	if _, s := do(t, srv, call{
		method: "GET",
		path:   ref + "/revisions/r2/search",
	}); strings.TrimSpace(s) != "{}" {
		t.Errorf("packages of r2 = %s", s)
	}

	if _, found := do(t, srv, call{
		method: "GET",
		path:   R + "/v2/conans/search?q=hel*",
	}); !strings.Contains(
		found,
		`"hello/1.0@_/_"`,
	) {
		t.Errorf("search = %s", found)
	}

	if _, found := do(t, srv, call{
		method: "GET",
		path:   R + "/v2/conans/search?q=zlib*",
	}); !strings.Contains(
		found,
		`"results":[]`,
	) {
		t.Errorf("empty search = %s", found)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/conan-local/_/hello/1.0/_/r1/export/conanfile.py",
		},
	); resp.StatusCode != 200 {
		t.Errorf("layout = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "DELETE",
			path:   ref + "/revisions/r1/packages/pid1/revisions/p1",
		},
	); resp.StatusCode != 200 {
		t.Errorf("delete package revision = %d", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "DELETE",
		path:   ref + "/revisions/r2",
	}); resp.StatusCode != 200 {
		t.Errorf("delete recipe revision = %d", resp.StatusCode)
	}

	if _, latest := do(t, srv, call{
		method: "GET",
		path:   ref + "/latest",
	}); !strings.Contains(
		latest,
		`"revision":"r1"`,
	) {
		t.Errorf("latest after delete = %s", latest)
	}

	if resp, _ := do(t, srv, call{
		method: "DELETE",
		path:   ref + "/revisions/r9",
	}); resp.StatusCode != 404 {
		t.Errorf("delete missing revision = %d, want 404", resp.StatusCode)
	}

	queries := make(chan url.Values, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case queries <- r.URL.Query():
		default:
		}

		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(upstream.Close)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/conan-remote",
			body:   `{"rclass":"remote","packageType":"conan","url":"` + upstream.URL + `"}`,
		},
	)

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/conan/conan-remote/v2/conans/search?q=" + url.QueryEscape("a&ignorecase=False #b"),
	}); resp.StatusCode != 200 {
		t.Fatalf("remote search = %d %s", resp.StatusCode, body)
	}

	select {
	case q := <-queries:
		if len(q) != 1 || q.Get("q") != "a&ignorecase=False #b" {
			t.Errorf("upstream search query = %v", q)
		}
	default:
		t.Error("the remote search never reached the upstream")
	}
}
