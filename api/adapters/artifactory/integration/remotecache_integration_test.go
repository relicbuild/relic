//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRemoteCacheView(t *testing.T) {
	t.Parallel()

	var upstreamHits atomic.Int64

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)

		if r.URL.Path == "/files/a/b.txt" {
			_, _ = io.WriteString(w, "cached content")
			return
		}

		if r.URL.Path == "/files/a/failing.txt" {
			http.Error(w, "upstream down", http.StatusInternalServerError)
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	srv := server(t)

	for _, body := range []string{
		`{"key":"gen-remote","rclass":"remote","packageType":"generic","url":"` + upstream.URL + `/files"}`,
		`{"key":"gen-local","rclass":"local","packageType":"generic"}`,
	} {
		key := strings.Split(strings.Split(body, `"key":"`)[1], `"`)[0]
		if resp, b := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + key,
				body:   body,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", key, resp.StatusCode, b)
		}
	}

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/gen-remote/a/b.txt",
	}); resp.StatusCode != 200 ||
		body != "cached content" {
		t.Fatalf("warm through the remote = %d %q", resp.StatusCode, body)
	}

	hits := upstreamHits.Load()

	type info struct {
		Repo      string `json:"repo"`
		Path      string `json:"path"`
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
	}

	storage := func(t *testing.T, path string) info {
		t.Helper()

		resp, body := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/storage/" + path,
		})
		if resp.StatusCode != 200 {
			t.Fatalf("storage %s = %d %s", path, resp.StatusCode, body)
		}

		var i info
		if err := json.Unmarshal([]byte(body), &i); err != nil {
			t.Fatal(err)
		}

		return i
	}

	t.Run("storage API through the cache key", func(t *testing.T) {
		i := storage(t, "gen-remote-cache/a/b.txt")
		if i.Repo != "gen-remote-cache" || i.Path != "/a/b.txt" || len(i.Checksums.SHA256) != 64 {
			t.Errorf("file info = %+v", i)
		}

		if root := storage(t, "gen-remote-cache"); root.Repo != "gen-remote-cache" {
			t.Errorf("root info repo = %q", root.Repo)
		}
	})
	t.Run("storage API through the remote key names the cache", func(t *testing.T) {
		if i := storage(t, "gen-remote/a/b.txt"); i.Repo != "gen-remote-cache" {
			t.Errorf("repo = %q, want gen-remote-cache", i.Repo)
		}
	})
	t.Run("downloads serve cached items only", func(t *testing.T) {
		if resp, body := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/gen-remote-cache/a/b.txt",
			},
		); resp.StatusCode != 200 ||
			body != "cached content" {
			t.Errorf("cached download = %d %q", resp.StatusCode, body)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "HEAD",
				path:   "/artifactory/gen-remote-cache/a/b.txt",
			},
		); resp.StatusCode != 200 {
			t.Errorf("cached HEAD = %d", resp.StatusCode)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/gen-remote-cache/a/missing.txt",
			},
		); resp.StatusCode != 404 {
			t.Errorf("uncached download = %d, want 404", resp.StatusCode)
		}

		if got := upstreamHits.Load(); got != hits {
			t.Errorf("the cache view went upstream %d times", got-hits)
		}
	})
	t.Run("AQL names remote items by the cache key", func(t *testing.T) {
		for _, repoKey := range []string{
			"gen-remote-cache",
			"gen-remote",
		} {
			resp, body := do(t, srv, call{
				method: "POST",
				path:   "/artifactory/api/search/aql",
				body:   `items.find({"repo":"` + repoKey + `","name":"b.txt"}).include("repo","path","name")`,
			})
			if resp.StatusCode != 200 || !strings.Contains(body, `"repo":"gen-remote-cache"`) ||
				strings.Contains(body, `"repo":"gen-remote"`) {
				t.Errorf("AQL on %s = %d %s", repoKey, resp.StatusCode, body)
			}
		}
	})
	t.Run("copy from the cache", func(t *testing.T) {
		resp, body := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/copy/gen-remote-cache/a/b.txt?to=gen-local%2Fx%2Fb.txt",
			},
		)
		if resp.StatusCode != 200 {
			t.Fatalf("copy = %d %s", resp.StatusCode, body)
		}

		if src, dst := storage(t, "gen-remote-cache/a/b.txt"), storage(
			t,
			"gen-local/x/b.txt",
		); src.Checksums.SHA256 != dst.Checksums.SHA256 {
			t.Errorf("copied sha256 %s != %s", dst.Checksums.SHA256, src.Checksums.SHA256)
		}
	})
	t.Run("read on the remote covers its cache", func(t *testing.T) {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/reader",
				body:   `{"email":"r@example.com","password":"Reader-pass-1"}`,
			},
		)
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/other",
				body:   `{"email":"o@example.com","password":"Other-pass-1"}`,
			},
		)

		perm := `{"name":"reader","repo":{"repositories":["gen-remote"],"include-patterns":["**"],"actions":{"users":{"reader":["read"]}}}}`
		if resp, body := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/v2/security/permissions/reader",
				body:   perm,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("permission target = %d %s", resp.StatusCode, body)
		}

		as := func(user, pass string) map[string]string {
			return map[string]string{
				"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
			}
		}
		if resp, body := do(
			t,
			srv,
			call{
				method:  "GET",
				path:    "/artifactory/api/storage/gen-remote-cache/a/b.txt",
				noAuth:  true,
				headers: as("reader", "Reader-pass-1"),
			},
		); resp.StatusCode != 200 {
			t.Errorf("reader = %d %s", resp.StatusCode, body)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method:  "GET",
				path:    "/artifactory/gen-remote-cache/a/b.txt",
				noAuth:  true,
				headers: as("other", "Other-pass-1"),
			},
		); resp.StatusCode != 403 &&
			resp.StatusCode != 404 {
			t.Errorf("user without read = %d", resp.StatusCode)
		}
	})
	t.Run("properties and file lists name the cache", func(t *testing.T) {
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/storage/gen-remote-cache/a/b.txt?properties=team=core",
		}); resp.StatusCode != 204 {
			t.Fatalf("set properties = %d %s", resp.StatusCode, body)
		}

		for path, want := range map[string]string{
			"/artifactory/api/storage/gen-remote-cache/a/b.txt?properties": "/api/storage/gen-remote-cache/a/b.txt",
			"/artifactory/api/storage/gen-remote-cache/a?list":             "/api/storage/gen-remote-cache/a",
		} {
			resp, body := do(t, srv, call{
				method: "GET",
				path:   path,
			})

			var got struct {
				URI string `json:"uri"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil || resp.StatusCode != 200 ||
				!strings.HasSuffix(got.URI, want) {
				t.Errorf("%s = %d uri %q, want it to end with %s", path, resp.StatusCode, got.URI, want)
			}
		}
	})
	t.Run("delete from the cache", func(t *testing.T) {
		if resp, body := do(
			t,
			srv,
			call{
				method: "DELETE",
				path:   "/artifactory/gen-remote-cache/a/b.txt",
			},
		); resp.StatusCode != 204 {
			t.Fatalf("delete = %d %s", resp.StatusCode, body)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/storage/gen-remote-cache/a/b.txt",
			},
		); resp.StatusCode != 404 {
			t.Errorf("after delete = %d", resp.StatusCode)
		}
	})
	t.Run("a checksum of a file the upstream fails on is not a missing file", func(t *testing.T) {
		if resp, body := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/gen-remote/a/failing.txt.sha1",
			},
		); resp.StatusCode != http.StatusBadGateway {
			t.Errorf("checksum of a failing upstream file = %d %s, want 502", resp.StatusCode, body)
		}
	})
}
