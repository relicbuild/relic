//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"crypto/sha1" //nolint:gosec // wire checksum
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestSecurityRegressions(t *testing.T) {
	t.Parallel()

	srv := server(t)
	for _, key := range []string{
		"secret-local",
		"scratch-local",
		"libs-local",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + key,
				body:   `{"rclass":"local","packageType":"generic"}`,
			},
		)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/mallory",
			body:   `{"email":"m@example.com","password":"Mallory-pass-1"}`,
		},
	); resp.StatusCode >= 300 {
		t.Fatalf("create user = %d %s", resp.StatusCode, body)
	}

	perm := `{"name":"mallory",
		"repo":{"repositories":["scratch-local","libs-local"],"include-patterns":["**"],"exclude-patterns":["secret/**"],"actions":{"users":{"mallory":["read","write"]}}},
		"build":{"repositories":["artifactory-build-info"],"include-patterns":["**"],"actions":{"users":{"mallory":["read","write","delete"]}}}}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/mallory",
			body:   perm,
		},
	); resp.StatusCode >= 300 {
		t.Fatalf("permission target = %d %s", resp.StatusCode, body)
	}

	mallory := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("mallory:Mallory-pass-1")),
	}
	as := func(c call) (reply, string) {
		c.noAuth = true
		if c.headers == nil {
			c.headers = map[string]string{}
		}

		for k, v := range mallory {
			c.headers[k] = v
		}

		return do(t, srv, c)
	}

	secret := strings.Repeat("top secret ", 100)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/secret-local/s.bin",
		body:   secret,
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs-local/secret/k.txt",
		body:   "excluded",
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs-local/rel.jar",
		body:   "release v1",
	})

	s1 := sha1.Sum([]byte(secret)) //nolint:gosec // wire checksum
	secretSHA1 := hex.EncodeToString(s1[:])

	t.Run("H2 checksum deploy of unreadable content", func(t *testing.T) {
		resp, _ := as(
			call{
				method: "PUT",
				path:   "/artifactory/scratch-local/copy.bin",
				headers: map[string]string{
					"X-Checksum-Deploy": "true",
					"X-Checksum-Sha256": sha256Hex(secret),
				},
			},
		)
		if resp.StatusCode != 404 {
			t.Errorf("checksum deploy = %d, want 404", resp.StatusCode)
		}

		as(call{
			method: "PUT",
			path:   "/artifactory/scratch-local/mine.bin",
			body:   "mine",
		})

		if resp, _ := as(
			call{
				method: "PUT",
				path:   "/artifactory/scratch-local/mine2.bin",
				headers: map[string]string{
					"X-Checksum-Deploy": "true",
					"X-Checksum-Sha256": sha256Hex("mine"),
				},
			},
		); resp.StatusCode != 201 {
			t.Errorf("checksum deploy of readable content = %d, want 201", resp.StatusCode)
		}
	})

	t.Run("H3 empty segments cannot dodge excludes", func(t *testing.T) {
		for _, p := range []string{
			"/artifactory/libs-local/secret/k.txt",
			"/artifactory/libs-local/%2Fsecret/k.txt",
			"/artifactory/libs-local//secret/k.txt",
		} {
			if resp, body := as(call{
				method: "GET",
				path:   p,
			}); resp.StatusCode != 403 && resp.StatusCode != 404 ||
				body == "excluded" {
				t.Errorf("GET %s = %d %q", p, resp.StatusCode, body)
			}
		}

		if resp, _ := as(
			call{
				method: "PUT",
				path:   "/artifactory/libs-local/%2Fsecret/new.txt",
				body:   "x",
			},
		); resp.StatusCode != 403 {
			t.Errorf("PUT through %%2F = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("H4 archive entries are checked one by one", func(t *testing.T) {
		zipOf := func(name, content string) string {
			var buf bytes.Buffer

			zw := zip.NewWriter(&buf)
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(content))
			_ = zw.Close()

			return buf.String()
		}

		explode := map[string]string{
			"X-Explode-Archive": "true",
		}
		if resp, _ := as(
			call{
				method:  "PUT",
				path:    "/artifactory/libs-local/a.zip",
				body:    zipOf("secret/evil.txt", "evil"),
				headers: explode,
			},
		); resp.StatusCode != 403 {
			t.Errorf("explode into an excluded path = %d, want 403", resp.StatusCode)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/libs-local/secret/evil.txt",
			},
		); resp.StatusCode != 404 {
			t.Errorf("excluded entry was written")
		}

		if resp, _ := as(
			call{
				method:  "PUT",
				path:    "/artifactory/libs-local/b.zip",
				body:    zipOf("rel.jar", "tampered"),
				headers: explode,
			},
		); resp.StatusCode != 403 {
			t.Errorf("explode over an existing file without delete = %d, want 403", resp.StatusCode)
		}

		if _, got := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/libs-local/rel.jar",
		}); got != "release v1" {
			t.Errorf("rel.jar = %q", got)
		}

		if resp, _ := as(
			call{
				method:  "PUT",
				path:    "/artifactory/libs-local/c.zip",
				body:    zipOf("ok/new.txt", "fine"),
				headers: explode,
			},
		); resp.StatusCode != 200 {
			t.Errorf("allowed explode = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("H1 build operations only touch permitted items", func(t *testing.T) {
		build := `{"name":"evil","number":"1","started":"2026-09-24T10:00:00.000+0000",` +
			`"modules":[{"id":"m","artifacts":[{"name":"s.bin","path":"s.bin",` +
			`"sha1":"` + secretSHA1 + `","sha256":"` + sha256Hex(secret) + `","md5":"x"}]}]}`
		if resp, body := as(call{
			method: "PUT",
			path:   "/artifactory/api/build",
			body:   build,
		}); resp.StatusCode != 204 {
			t.Fatalf("publish = %d %s", resp.StatusCode, body)
		}

		if _, body := as(
			call{
				method: "GET",
				path:   "/artifactory/api/builds/buildArtifacts/evil/1/artifactory-build-info",
			},
		); strings.Contains(body, "secret-local") {
			t.Errorf("buildArtifacts disclosed %s", body)
		}

		_, body := as(
			call{
				method: "POST",
				path:   "/artifactory/api/build/promote/evil/1",
				body:   `{"targetRepo":"scratch-local","copy":true}`,
			},
		)
		if !strings.Contains(body, "permission denied") {
			t.Errorf("promote messages = %s", body)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/scratch-local/s.bin",
			},
		); resp.StatusCode != 404 {
			t.Errorf("secret was promoted into scratch-local")
		}

		as(
			call{
				method: "POST",
				path:   "/artifactory/api/build/delete",
				body:   `{"buildName":"evil","buildNumbers":["1"],"deleteArtifacts":true}`,
			},
		)

		if _, got := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/secret-local/s.bin",
		}); got != secret {
			t.Errorf("secret was deleted through build delete")
		}
	})

	t.Run("M1 listings hide excluded paths", func(t *testing.T) {
		_, list := as(call{
			method: "GET",
			path:   "/artifactory/api/storage/libs-local/?list&deep=1&listFolders=1",
		})
		if strings.Contains(list, "secret") {
			t.Errorf("deep list shows excluded paths: %s", list)
		}

		if !strings.Contains(list, "rel.jar") {
			t.Errorf("deep list misses readable files: %s", list)
		}
	})

	t.Run("unreadable repositories and builds answer as missing ones", func(t *testing.T) {
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/security/users/eve",
			body:   `{"email":"eve@example.com","password":"Eve-pass-12345"}`,
		}); resp.StatusCode >= 300 {
			t.Fatalf("create eve = %d %s", resp.StatusCode, body)
		}

		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/build",
			body: `{"name":"hiddenbuild","number":"1","started":"2026-10-24T10:00:00.000+0000",` +
				`"modules":[]}`,
		}); resp.StatusCode >= 300 {
			t.Fatalf("publish hiddenbuild = %d %s", resp.StatusCode, body)
		}

		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/hidden-docker",
			body:   `{"rclass":"local","packageType":"docker"}`,
		}); resp.StatusCode >= 300 {
			t.Fatalf("create hidden-docker = %d %s", resp.StatusCode, body)
		}

		eve := func(path string) (int, string) {
			resp, body := do(t, srv, call{
				method: "GET",
				path:   path,
				noAuth: true,
				headers: map[string]string{
					"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("eve:Eve-pass-12345")),
				},
			})

			return resp.StatusCode, body
		}
		for hidden, missing := range map[string]string{
			"/artifactory/api/storage/secret-local/nope.txt": "/artifactory/api/storage/nope-repo/nope.txt",
			"/artifactory/secret-local/s.bin":                "/artifactory/nope-repo/s.bin",
			"/artifactory/api/build/hiddenbuild":             "/artifactory/api/build/nopebuild",
			"/v2/hidden-docker/app/manifests/latest":         "/v2/nope-docker/app/manifests/latest",
		} {
			hiddenStatus, hiddenBody := eve(hidden)
			missingStatus, missingBody := eve(missing)

			if hiddenStatus != missingStatus || hiddenStatus != http.StatusNotFound ||
				strings.Contains(hiddenBody, "secret") {
				t.Errorf("GET %s = %d %s; GET %s = %d %s; want the same 404",
					hidden, hiddenStatus, hiddenBody, missing, missingStatus, missingBody)
			}
		}

		if status, body := func() (int, string) {
			resp, body := as(call{
				method: "DELETE",
				path:   "/artifactory/libs-local/rel.jar",
			})

			return resp.StatusCode, body
		}(); status != http.StatusForbidden {
			t.Errorf("deleting in a readable repository without delete = %d %s, want 403", status, body)
		}
	})

	t.Run("M2 a scoped token cannot mint a wider one", func(t *testing.T) {
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/groups/readers",
				body:   `{"name":"readers"}`,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("create group = %d %s", resp.StatusCode, body)
		}

		_, first := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/security/token",
				body:   "username=mallory&scope=applied-permissions/groups:readers",
				headers: map[string]string{
					"Content-Type": "application/x-www-form-urlencoded",
				},
			},
		)

		var tok struct {
			AccessToken string `json:"access_token"`
			Scope       string `json:"scope"`
		}
		if err := json.Unmarshal([]byte(first), &tok); err != nil || tok.AccessToken == "" {
			t.Fatalf("scoped token = %s", first)
		}

		for _, repo := range []string{
			`{"key":"m2-gems","rclass":"local","packageType":"gems"}`,
			`{"key":"m2-conan","rclass":"local","packageType":"conan"}`,
		} {
			key := strings.Split(strings.Split(repo, `"key":"`)[1], `"`)[0]
			do(t, srv, call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + key,
				body:   repo,
			})
		}

		scoped := map[string]string{
			"Authorization": "Bearer " + tok.AccessToken,
		}
		_, gemKey := do(t, srv, call{
			method:  "GET",
			path:    "/artifactory/api/gems/m2-gems/api/v1/api_key",
			noAuth:  true,
			headers: scoped,
		})

		_, conanToken := do(t, srv, call{
			method:  "GET",
			path:    "/artifactory/api/conan/m2-conan/v2/users/authenticate",
			noAuth:  true,
			headers: scoped,
		})
		for name, minter := range map[string]string{
			"scoped token": tok.AccessToken,
			"gem api key":  strings.TrimPrefix(gemKey, "Bearer "),
			"conan token":  conanToken,
		} {
			_, second := do(t, srv, call{
				method: "POST",
				path:   "/artifactory/api/security/token",
				noAuth: true,
				headers: map[string]string{
					"Authorization": "Bearer " + minter,
					"Content-Type":  "application/x-www-form-urlencoded",
				},
				body: "scope=member-of-groups:*&expires_in=0",
			})

			var wide struct {
				Scope     string `json:"scope"`
				ExpiresIn int64  `json:"expires_in"`
			}
			if err := json.Unmarshal([]byte(second), &wide); err != nil || wide.Scope == "member-of-groups:*" ||
				wide.ExpiresIn == 0 {
				t.Errorf("%s minted %s", name, second)
			}
		}

		for _, path := range []string{
			"/artifactory/api/security/token",
			"/access/api/v1/tokens",
		} {
			body := "expires_in=9223372037"
			contentType := "application/x-www-form-urlencoded"

			if strings.HasPrefix(path, "/access") {
				body = `{"expires_in":9223372037}`
				contentType = "application/json"
			}

			resp, got := as(call{
				method: "POST",
				path:   path,
				body:   body,
				headers: map[string]string{
					"Content-Type": contentType,
				},
			})
			if resp.StatusCode != 400 {
				t.Errorf("%s with an overflowing expires_in = %d %s, want 400", path, resp.StatusCode, got)
			}
		}
	})

	t.Run("a move needs read on the source", func(t *testing.T) {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/drop-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		})
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/drop-local/hidden.txt",
			body:   "unreadable",
		})

		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/dee",
				body:   `{"email":"dee@example.com","password":"Dee-pass-1"}`,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("create user = %d %s", resp.StatusCode, body)
		}

		for name, repoPerm := range map[string]string{
			"dee-drop":    `{"repositories":["drop-local"],"include-patterns":["**"],"actions":{"users":{"dee":["delete"]}}}`,
			"dee-scratch": `{"repositories":["scratch-local"],"include-patterns":["**"],"actions":{"users":{"dee":["read","write"]}}}`,
		} {
			if resp, body := do(
				t,
				srv,
				call{
					method: "POST",
					path:   "/artifactory/api/v2/security/permissions/" + name,
					body:   `{"name":"` + name + `","repo":` + repoPerm + `}`,
				},
			); resp.StatusCode >= 300 {
				t.Fatalf("permission target = %d %s", resp.StatusCode, body)
			}
		}

		dee := map[string]string{
			"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("dee:Dee-pass-1")),
		}
		if resp, body := do(t, srv, call{
			method:  "POST",
			path:    "/artifactory/api/move/drop-local/hidden.txt?to=/scratch-local/hidden.txt",
			noAuth:  true,
			headers: dee,
		}); resp.StatusCode != 403 {
			t.Errorf("move without read = %d %s, want 403", resp.StatusCode, body)
		}

		deeArts := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("dee", "Dee-pass-1"), srv.URL))
		if _, err := deeArts.MoveItem(t.Context(), &relicv1.MoveItemRequest{
			Repository:   "drop-local",
			Path:         "hidden.txt",
			ToRepository: "scratch-local",
			ToPath:       "hidden.txt",
		}); code(err) != connect.CodeNotFound {
			t.Errorf("Relic API move without read = %v, want NotFound", err)
		}

		if resp, _ := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/drop-local/hidden.txt",
		}); resp.StatusCode != 200 {
			t.Errorf("source after a refused move = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("a user minting their own access token in another case", func(t *testing.T) {
		if resp, body := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/Dora",
				body:   `{"email":"d@example.com","password":"Dora-pass-1"}`,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("create user = %d %s", resp.StatusCode, body)
		}

		resp, body := do(t, srv, call{
			method: "POST",
			path:   "/access/api/v1/tokens",
			body:   `{"username":"dora","scope":"applied-permissions/user","expires_in":600}`,
			noAuth: true,
			headers: map[string]string{
				"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("Dora:Dora-pass-1")),
			},
		})

		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &tok) != nil || tok.AccessToken == "" {
			t.Fatalf("token = %d %s", resp.StatusCode, body)
		}

		if resp, body := do(t, srv, call{
			method: "POST",
			path:   "/access/api/v1/tokens",
			body:   `{"scope":"applied-permissions/user","expires_in":600}`,
			noAuth: true,
			headers: map[string]string{
				"Authorization": "Bearer " + tok.AccessToken,
			},
		}); resp.StatusCode != 200 {
			t.Errorf("token minted with the first token = %d %s", resp.StatusCode, body)
		}
	})
}

func TestSDKCompatibility(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/a.txt",
		body:   "alpha",
	})

	t.Run("a batch create with a taken key creates nothing", func(t *testing.T) {
		for _, batch := range []string{
			`[{"key":"batch-new","rclass":"local","packageType":"generic"},{"key":"libs","rclass":"local","packageType":"generic"}]`,
			`[{"key":"batch-new","rclass":"local","packageType":"generic"},{"key":"batch-new","rclass":"local","packageType":"generic"}]`,
		} {
			if resp, body := do(t, srv, call{
				method: "PUT",
				path:   "/artifactory/api/repositories",
				body:   batch,
			}); resp.StatusCode < 400 {
				t.Errorf("batch %s = %d %s, want an error", batch, resp.StatusCode, body)
			}

			if resp, _ := do(t, srv, call{
				method: "GET",
				path:   "/artifactory/api/repositories/batch-new",
			}); resp.StatusCode != 400 {
				t.Errorf("batch-new after a refused batch = %d, want 400 (no such repository)", resp.StatusCode)
			}
		}
	})

	t.Run("group update", func(t *testing.T) {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/groups/devs",
				body:   `{"name":"devs","description":"old"}`,
			},
		)

		if resp, b := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/security/groups/devs",
				body:   `{"name":"devs","description":"new","adminPrivileges":false}`,
			},
		); resp.StatusCode != 200 {
			t.Fatalf("POST = %d %s", resp.StatusCode, b)
		}

		if _, b := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/security/groups/devs",
		}); !strings.Contains(
			b,
			`"description": "new"`,
		) {
			t.Errorf("group = %s", b)
		}

		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/ann",
				body:   `{"email":"ann@example.com","password":"Ann-pass-12345"}`,
			},
		)
		do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/security/groups/devs",
				body:   `{"name":"devs","userNames":["ann"]}`,
			},
		)

		if _, b := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/security/groups/devs?includeUsers=true",
			},
		); !strings.Contains(b, `"description": "new"`) ||
			!strings.Contains(b, `"ann"`) {
			t.Errorf("group after adding users = %s", b)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/security/groups/devs",
				body:   `{"name":"devs","description":"changed","userNames":["nobody"]}`,
			},
		); resp.StatusCode != 404 {
			t.Errorf("update naming an unknown user = %d, want 404", resp.StatusCode)
		}

		if _, b := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/security/groups/devs",
			},
		); !strings.Contains(b, `"description": "new"`) {
			t.Errorf("group after a refused update = %s, want it unchanged", b)
		}
	})
	t.Run("replication update", func(t *testing.T) {
		body := `{"url":"http://127.0.0.1:1/artifactory/libs","username":"u","password":"p","cronExp":"0 0 12 * * ?","enabled":false,"syncDeletes":false}`
		if resp, _ := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/replications/libs",
				body:   body,
			},
		); resp.StatusCode != 404 {
			t.Errorf("update before create = %d, want 404", resp.StatusCode)
		}

		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/replications/libs",
			body:   body,
		})

		if resp, b := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/artifactory/api/replications/libs",
				body:   strings.Replace(body, `"syncDeletes":false`, `"syncDeletes":true`, 1),
			},
		); resp.StatusCode != 200 {
			t.Fatalf("update = %d %s", resp.StatusCode, b)
		}

		if _, b := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/replications/libs",
			},
		); !strings.Contains(b, `"syncDeletes": true`) ||
			strings.Contains(b, `"p"`) {
			t.Errorf("replication = %s", b)
		}
	})
	t.Run("stats", func(t *testing.T) {
		_, b := do(t, srv, call{
			method: "GET",
			path:   "/artifactory/api/storage/libs/a.txt?stats",
		})

		var st map[string]any
		if json.Unmarshal([]byte(b), &st) != nil || st["downloadCount"] != float64(0) ||
			st["remoteDownloadCount"] != float64(0) ||
			st["uri"] == nil {
			t.Errorf("stats = %s", b)
		}

		if _, ok := st["size"]; ok {
			t.Errorf("stats answered with file info: %s", b)
		}
	})
}
