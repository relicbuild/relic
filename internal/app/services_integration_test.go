//go:build integration

package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // build info identifies files by SHA-1
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type rest struct {
	t    *testing.T
	srv  func(call) (reply, string)
	user string
}

func (r rest) call(method, path, body string, want int) string {
	r.t.Helper()

	c := call{
		method: method,
		path:   path,
		body:   body,
	}
	if body != "" {
		c.headers = map[string]string{
			"Content-Type": "application/json",
		}
	}

	if r.user != "" {
		if c.headers == nil {
			c.headers = map[string]string{}
		}

		c.headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(r.user))
	}

	resp, got := r.srv(c)
	if resp.StatusCode != want {
		r.t.Fatalf("%s %s as %q = %d %s, want %d", method, path, cmpUser(r.user), resp.StatusCode, got, want)
	}

	return got
}

func cmpUser(u string) string {
	if u == "" {
		return "admin"
	}

	name, _, _ := strings.Cut(u, ":")

	return name
}

func TestRelicAPIServices(t *testing.T) {
	t.Parallel()
	srv := server(t)
	send := func(c call) (reply, string) {
		return do(t, srv, c)
	}
	admin := rest{
		t:   t,
		srv: send,
	}
	bob := rest{
		t:    t,
		srv:  send,
		user: "bob:Bob-pass-1",
	}

	admin.call(
		http.MethodPut,
		"/api/v1/users/bob",
		`{"name":"bob","email":"bob@example.com","password":"Bob-pass-1"}`,
		http.StatusOK,
	)

	t.Run("tokens", func(t *testing.T) {
		admin.t, bob.t = t, t
		admin.call(http.MethodPost, "/api/v1/tokens", `{"description":"admin-ci"}`, http.StatusOK)
		bob.call(http.MethodPost, "/api/v1/tokens", `{"description":"bob-ci"}`, http.StatusOK)

		var list struct {
			Tokens []struct {
				ID          string `json:"id"`
				Username    string `json:"username"`
				Description string `json:"description"`
				Revoked     bool   `json:"revoked"`
			} `json:"tokens"`
		}

		mine := bob.call(http.MethodGet, "/api/v1/tokens", "", http.StatusOK)
		if !strings.Contains(mine, "bob-ci") || strings.Contains(mine, "admin-ci") {
			t.Fatalf("bob's tokens = %s", mine)
		}

		all := admin.call(http.MethodGet, "/api/v1/tokens", "", http.StatusOK)
		if err := json.Unmarshal([]byte(all), &list); err != nil {
			t.Fatal(err)
		}

		ids := map[string]string{}
		for _, tok := range list.Tokens {
			ids[tok.Description] = tok.ID
		}

		if ids["admin-ci"] == "" || ids["bob-ci"] == "" {
			t.Fatalf("admin's list = %s", all)
		}

		bob.call(http.MethodDelete, "/api/v1/tokens/"+ids["admin-ci"], "", http.StatusNotFound)
		bob.call(http.MethodDelete, "/api/v1/tokens/"+ids["bob-ci"], "", http.StatusOK)

		if got := bob.call(http.MethodGet, "/api/v1/tokens", "", http.StatusOK); strings.Contains(got, "bob-ci") {
			t.Errorf("a revoked token is listed: %s", got)
		}

		if got := bob.call(http.MethodGet, "/api/v1/tokens?includeRevoked=true", "", http.StatusOK); !strings.Contains(
			got,
			`"revoked":true`,
		) {
			t.Errorf("revoked tokens = %s", got)
		}
	})

	t.Run("key pairs", func(t *testing.T) {
		admin.t, bob.t = t, t
		admin.call(http.MethodGet, "/api/v1/key-pairs", "", http.StatusOK)
		bob.call(http.MethodGet, "/api/v1/key-pairs", "", http.StatusForbidden)
		bob.call(http.MethodGet, "/api/v1/key-pairs/nope/public-key", "", http.StatusNotFound)

		rotating := `{"name":"deb-rotating","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_DEBIAN",` +
			`"signingKey":"deb-old"%s}`
		admin.call(http.MethodPost, "/api/v1/repositories", fmt.Sprintf(rotating, `,"secondarySigningKey":"deb-new"`),
			http.StatusOK)

		var keys struct {
			KeyPair            string `json:"keyPair"`
			PublicKey          string `json:"publicKey"`
			SecondaryKeyPair   string `json:"secondaryKeyPair"`
			SecondaryPublicKey string `json:"secondaryPublicKey"`
		}
		if err := json.Unmarshal([]byte(admin.call(
			http.MethodGet,
			"/api/v1/repositories/deb-rotating/signing-key",
			"",
			http.StatusOK,
		)), &keys); err != nil || keys.KeyPair != "deb-old" || keys.SecondaryKeyPair != "deb-new" ||
			keys.PublicKey == "" || keys.SecondaryPublicKey == "" || keys.PublicKey == keys.SecondaryPublicKey {
			t.Errorf("the signing keys of a repository with a secondary key = %+v, %v", keys, err)
		}

		bob.call(http.MethodDelete, "/api/v1/key-pairs/deb-new", "", http.StatusForbidden)
		admin.call(http.MethodDelete, "/api/v1/key-pairs/deb-new", "", http.StatusBadRequest)
		admin.call(http.MethodDelete, "/api/v1/key-pairs/release-bundles", "", http.StatusBadRequest)
		admin.call(http.MethodPut, "/api/v1/repositories/deb-rotating", fmt.Sprintf(rotating, ""), http.StatusOK)
		admin.call(http.MethodDelete, "/api/v1/key-pairs/deb-new", "", http.StatusOK)
		admin.call(http.MethodGet, "/api/v1/key-pairs/deb-new/public-key", "", http.StatusNotFound)
		admin.call(http.MethodDelete, "/api/v1/key-pairs/deb-new", "", http.StatusNotFound)
	})

	t.Run("trusted keys and evidence", func(t *testing.T) {
		admin.t, bob.t = t, t
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
		pubPEM := string(pem.EncodeToMemory(&pem.Block{
			Type:  "PUBLIC KEY",
			Bytes: der,
		}))
		body, _ := json.Marshal(map[string]string{
			"alias":     "rest-key",
			"publicKey": pubPEM,
		})

		var added struct {
			Key struct {
				Kid string `json:"kid"`
			} `json:"key"`
		}
		if err := json.Unmarshal(
			[]byte(admin.call(http.MethodPost, "/api/v1/trusted-keys", string(body), http.StatusOK)),
			&added,
		); err != nil ||
			added.Key.Kid == "" {
			t.Fatalf("added key = %+v, %v", added, err)
		}

		admin.call(http.MethodPost, "/api/v1/trusted-keys", string(body), http.StatusConflict)

		if got := admin.call(http.MethodGet, "/api/v1/trusted-keys", "", http.StatusOK); !strings.Contains(
			got,
			"rest-key",
		) {
			t.Errorf("trusted keys = %s", got)
		}

		bob.call(http.MethodGet, "/api/v1/trusted-keys", "", http.StatusForbidden)

		admin.call(
			http.MethodPost,
			"/api/v1/repositories",
			`{"name":"ev-local","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC"}`,
			http.StatusOK,
		)
		send(call{
			method: http.MethodPut,
			path:   "/api/v1/files/ev-local/dist/app.bin",
			body:   "binary",
		})

		env, _ := json.Marshal(
			map[string]string{
				"envelope": envelope(t, key, "rest-key", sha256Hex("binary")),
				"provider": "ci",
			},
		)
		if got := admin.call(
			http.MethodPost,
			"/api/v1/repositories/ev-local/evidence/dist/app.bin",
			string(env),
			http.StatusOK,
		); !strings.Contains(got, `"verified":true`) {
			t.Errorf("created evidence = %s", got)
		}

		if got := admin.call(
			http.MethodGet,
			"/api/v1/repositories/ev-local/evidence/dist/app.bin",
			"",
			http.StatusOK,
		); !strings.Contains(got, `"predicateSlug"`) {
			t.Errorf("evidence = %s", got)
		}

		bob.call(http.MethodGet, "/api/v1/repositories/ev-local/evidence/dist/app.bin", "", http.StatusNotFound)
		bob.call(
			http.MethodPost,
			"/api/v1/repositories/ev-local/evidence/dist/app.bin",
			string(env),
			http.StatusNotFound,
		)
		admin.call(http.MethodDelete, "/api/v1/trusted-keys/"+added.Key.Kid, "", http.StatusOK)
	})

	t.Run("build management", func(t *testing.T) {
		admin.t, bob.t = t, t
		admin.call(
			http.MethodPost,
			"/api/v1/repositories",
			`{"name":"bld-local","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC"}`,
			http.StatusOK,
		)
		admin.call(
			http.MethodPost,
			"/api/v1/repositories",
			`{"name":"bld-release","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC"}`,
			http.StatusOK,
		)
		send(call{
			method: http.MethodPut,
			path:   "/api/v1/files/bld-local/app.jar",
			body:   "app",
		})

		sum := sha1.Sum([]byte("app")) //nolint:gosec // build info's checksum
		for _, n := range []string{
			"1",
			"2",
			"3",
		} {
			run := `{"name":"svc","number":"` + n + `","started":"2026-09-2` + n + `T10:00:00Z",` +
				`"modules":[{"id":"svc:app",` +
				`"artifacts":[{"name":"app.jar","type":"jar","sha1":"` + hex.EncodeToString(sum[:]) + `"}]}]}`
			admin.call(http.MethodPost, "/api/v1/builds/svc/runs", run, http.StatusOK)
		}

		bob.call(http.MethodPost, "/api/v1/builds/svc/runs/3:promote", `{"status":"released"}`, http.StatusNotFound)

		got := admin.call(
			http.MethodPost,
			"/api/v1/builds/svc/runs/3:promote",
			`{"status":"released","targetRepository":"bld-release","copy":true}`,
			http.StatusOK,
		)
		if strings.Contains(got, `"level":"error"`) {
			t.Errorf("promotion = %s", got)
		}

		admin.call(http.MethodGet, "/api/v1/repositories/bld-release/items/app.jar", "", http.StatusOK)
		admin.call(http.MethodDelete, "/api/v1/builds/svc/runs?numbers=1", "", http.StatusOK)
		admin.call(http.MethodPost, "/api/v1/builds/svc:applyRetention", `{"maxRuns":1}`, http.StatusOK)

		var runs struct {
			Runs []struct {
				Number string `json:"number"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(
			[]byte(admin.call(http.MethodGet, "/api/v1/builds/svc/runs", "", http.StatusOK)),
			&runs,
		); err != nil {
			t.Fatal(err)
		}

		if len(runs.Runs) != 1 || runs.Runs[0].Number != "3" {
			t.Errorf("runs after delete and discard = %+v, want only 3", runs.Runs)
		}

		admin.call(http.MethodDelete, "/api/v1/builds/svc/runs", "", http.StatusBadRequest)
	})

	t.Run("releases", func(t *testing.T) {
		admin.t, bob.t = t, t
		admin.call(
			http.MethodPost,
			"/api/v1/repositories",
			`{"name":"rel-src","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC"}`,
			http.StatusOK,
		)
		send(call{
			method: http.MethodPut,
			path:   "/api/v1/files/rel-src/a.txt",
			body:   "a",
		})
		send(call{
			method: http.MethodPut,
			path:   "/api/v1/files/rel-src/b.txt",
			body:   "b",
		})

		created := admin.call(http.MethodPost, "/api/v1/releases/shop/versions",
			`{"name":"shop","version":"1.0","sources":[{"files":[{"path":"rel-src/a.txt"}]}]}`, http.StatusOK)
		if !strings.Contains(created, "RELEASE_STATUS_COMPLETED") || !strings.Contains(created, `"path":"a.txt"`) {
			t.Errorf("created release = %s", created)
		}

		admin.call(
			http.MethodPost,
			"/api/v1/releases/shop/versions",
			`{"name":"shop","version":"1.0","sources":[{"files":[{"path":"rel-src/a.txt"}]}]}`,
			http.StatusConflict,
		)
		admin.call(
			http.MethodPost,
			"/api/v1/releases/shop/versions",
			`{"name":"shop","version":"bad","sources":[{"query":{},"files":[{"path":"rel-src/a.txt"}]}]}`,
			http.StatusBadRequest,
		)

		admin.call(
			http.MethodPost,
			"/api/v1/releases/shop/versions",
			`{"name":"shop","version":"2.0","draft":true,"sources":[{"files":[{"path":"rel-src/a.txt"}]}]}`,
			http.StatusOK,
		)
		admin.call(
			http.MethodPatch,
			"/api/v1/releases/shop/versions/2.0",
			`{"name":"shop","version":"2.0","sources":[{"files":[{"path":"rel-src/b.txt"}]}]}`,
			http.StatusOK,
		)

		if got := admin.call(
			http.MethodPost,
			"/api/v1/releases/shop/versions/2.0:finalize",
			`{}`,
			http.StatusOK,
		); !strings.Contains(got, "RELEASE_STATUS_COMPLETED") ||
			!strings.Contains(got, "b.txt") {
			t.Errorf("finalized = %s", got)
		}

		admin.call(http.MethodPut, "/api/v1/releases/shop/versions/1.0/tag", `{"tag":"stable"}`, http.StatusOK)

		if got := admin.call(
			http.MethodGet,
			"/api/v1/releases/shop/versions/1.0",
			"",
			http.StatusOK,
		); !strings.Contains(got, `"tag":"stable"`) {
			t.Errorf("tagged = %s", got)
		}

		if got := admin.call(
			http.MethodGet,
			"/api/v1/releases/shop/versions",
			"",
			http.StatusOK,
		); !strings.Contains(got, `"2.0"`) ||
			!strings.Contains(got, `"1.0"`) {
			t.Errorf("versions = %s", got)
		}

		if got := admin.call(http.MethodGet, "/api/v1/releases", "", http.StatusOK); !strings.Contains(got, `"shop"`) {
			t.Errorf("releases = %s", got)
		}

		if got := bob.call(http.MethodGet, "/api/v1/releases", "", http.StatusOK); strings.Contains(got, `"shop"`) {
			t.Errorf("bob sees a release he may not read: %s", got)
		}

		bob.call(http.MethodGet, "/api/v1/releases/shop/versions/1.0", "", http.StatusNotFound)
		admin.call(http.MethodGet, "/api/v1/releases/shop/versions/1.0/promotions", "", http.StatusOK)

		var exported struct {
			Archive struct {
				Repository string `json:"repository"`
				Path       string `json:"path"`
			} `json:"archive"`
		}
		if err := json.Unmarshal(
			[]byte(admin.call(http.MethodPost, "/api/v1/releases/shop/versions/1.0:export", `{}`, http.StatusOK)),
			&exported,
		); err != nil ||
			exported.Archive.Path == "" {
			t.Fatalf("export = %+v, %v", exported, err)
		}

		resp, archive := send(
			call{
				method: http.MethodGet,
				path:   "/api/v1/files/" + exported.Archive.Repository + "/" + exported.Archive.Path,
			},
		)
		if resp.StatusCode != http.StatusOK || archive == "" {
			t.Fatalf("download archive = %d", resp.StatusCode)
		}

		admin.call(http.MethodDelete, "/api/v1/releases/shop/versions/1.0", "", http.StatusOK)
		admin.call(http.MethodGet, "/api/v1/releases/shop/versions/1.0", "", http.StatusNotFound)

		if resp, body := send(
			call{
				method: http.MethodPost,
				path:   "/api/v1/releases/import",
				body:   archive,
				noAuth: true,
			},
		); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous import = %d %s", resp.StatusCode, body)
		}

		if resp, body := send(
			call{
				method: http.MethodPost,
				path:   "/api/v1/releases/import",
				body:   archive,
			},
		); resp.StatusCode != http.StatusCreated ||
			!strings.Contains(body, `"shop"`) {
			t.Fatalf("import = %d %s", resp.StatusCode, body)
		}

		admin.call(http.MethodGet, "/api/v1/releases/shop/versions/1.0", "", http.StatusOK)
	})
}
