//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestNixBinaryCache(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nix-local",
			body:   `{"rclass":"local","packageType":"nix"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/nix",
			body:   `{"rclass":"virtual","packageType":"nix","repositories":["nix-local"],"defaultDeploymentRepo":"nix-local"}`,
		},
	)

	const hash = "0c8gfj3z8f2y1a5d5bgxb3k9xw3cdzvq"

	nar := "1abc0nar.nar.xz"
	narinfo := "StorePath: /nix/store/" + hash + "-hello-2.12\nURL: nar/" + nar + "\nCompression: xz\nFileHash: sha256:x\nFileSize: 4\nNarHash: sha256:y\nNarSize: 8\nReferences: \n"
	api := "/artifactory/api/nix/"

	calls := []struct {
		name string
		c    call
		want int
	}{
		{
			"cache info upload",
			call{
				method: "PUT",
				path:   api + "nix/nix-cache-info",
				body:   "StoreDir: /nix/store\n",
			},
			201,
		},
		{
			"nar first (through the virtual)",
			call{
				method: "PUT",
				path:   api + "nix/nar/" + nar,
				body:   "NAR!",
			},
			201,
		},
		{
			"narinfo",
			call{
				method: "PUT",
				path:   api + "nix/" + hash + ".narinfo",
				body:   narinfo,
			},
			201,
		},
		{
			"narinfo without StorePath",
			call{
				method: "PUT",
				path:   api + "nix-local/" + strings.Repeat("a", 32) + ".narinfo",
				body:   "URL: nar/x\n",
			},
			400,
		},
		{
			"missing narinfo",
			call{
				method: "HEAD",
				path:   api + "nix-local/" + strings.Repeat("b", 32) + ".narinfo",
			},
			404,
		},
	}
	for _, tc := range calls {
		if resp, body := do(t, srv, tc.c); resp.StatusCode != tc.want {
			t.Errorf("%s: %s %s = %d %s, want %d", tc.name, tc.c.method, tc.c.path, resp.StatusCode, body, tc.want)
		}
	}

	for _, repoKey := range []string{
		"nix-local",
		"nix",
	} {
		if _, body := do(t, srv, call{
			method: "GET",
			path:   api + repoKey + "/nix-cache-info",
		}); !strings.Contains(
			body,
			"StoreDir: /nix/store",
		) {
			t.Errorf("%s nix-cache-info = %q", repoKey, body)
		}

		if resp, body := do(
			t,
			srv,
			call{
				method: "GET",
				path:   api + repoKey + "/" + hash + ".narinfo",
			},
		); resp.StatusCode != 200 ||
			body != narinfo {
			t.Errorf("%s narinfo = %d %q", repoKey, resp.StatusCode, body)
		}

		if resp, body := do(t, srv, call{
			method: "GET",
			path:   api + repoKey + "/nar/" + nar,
		}); resp.StatusCode != 200 ||
			body != "NAR!" {
			t.Errorf("%s nar = %d %q", repoKey, resp.StatusCode, body)
		}
	}

	for _, p := range []string{
		"binary-cache/" + hash + "/" + hash + ".narinfo",
		"binary-cache/" + hash + "/" + nar,
	} {
		if resp, _ := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/nix-local/" + p,
		}); resp.StatusCode != 200 {
			t.Errorf("%s = %d", p, resp.StatusCode)
		}
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/nix-local/nar/" + nar,
	}); resp.StatusCode != 404 {
		t.Errorf("staged NAR still at nar/ = %d", resp.StatusCode)
	}

	for _, repo := range []string{
		`{"key":"nix-generic","rclass":"local","packageType":"generic"}`,
		`{"key":"nix-misdirected","rclass":"virtual","packageType":"nix","repositories":["nix-local","nix-generic"],"defaultDeploymentRepo":"nix-generic"}`,
	} {
		key := strings.Split(strings.Split(repo, `"key":"`)[1], `"`)[0]
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/" + key,
			body:   repo,
		}); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", key, resp.StatusCode, body)
		}
	}

	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   api + "nix-misdirected/nix-cache-info",
		body:   "StoreDir: /nix/store\n",
	}); resp.StatusCode != 400 {
		t.Errorf("nix copy into a generic deployment repository = %d %s, want 400", resp.StatusCode, body)
	}
}
