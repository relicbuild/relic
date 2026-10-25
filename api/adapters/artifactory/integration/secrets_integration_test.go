//go:build integration

package integration

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relic/api/adapters/artifactory/access"

	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/sealing"
)

func TestSecretsAtRest(t *testing.T) {
	t.Parallel()

	const upstreamPassword = "upstream-s3cret"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pw, _ := r.BasicAuth(); pw != upstreamPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		_, _ = w.Write([]byte("from upstream"))
	}))
	t.Cleanup(upstream.Close)

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)
	blobs := objectstoretest.NewMemory()

	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	if _, err := auth.NewTokens(t.Context(), db, nil, access.Format{}); err != nil {
		t.Fatal(err)
	}

	if err := db.CreateRepository(t.Context(), database.CreateRepositoryParams{
		Key:    "old",
		Class:  "remote",
		Format: "generic",
		Config: []byte(
			`{"remote":{"url":"` + upstream.URL + `","username":"u","password":"` + upstreamPassword + `"}}`,
		),
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertReplication(
		t.Context(),
		database.UpsertReplicationParams{
			Repository: "old",
			Config:     []byte(`{"url":"http://x/artifactory/y","password":"repl-s3cret"}`),
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertLdapServer(
		t.Context(),
		database.UpsertLdapServerParams{
			Name: "corp",
			Config: []byte(`{"name":"corp","enabled":true,"url":"ldap://ldap.invalid/dc=x",
		"search":{"filter":"uid={0}","managerDn":"cn=admin","managerPassword":"ldap-s3cret"}}`),
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertIdentityProvider(
		t.Context(),
		database.UpsertIdentityProviderParams{
			Name: "corp",
			Config: []byte(
				`{"enabled":true,"oidc":{"issuer":"https://login.invalid","clientId":"relic","clientSecret":"oidc-s3cret"}}`,
			),
			Now: time.Now(),
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertWebhook(
		t.Context(),
		database.UpsertWebhookParams{
			Name: "ci",
			Config: []byte(
				`{"url":"https://hooks.example.com/relic","events":["*"],"secret":"webhook-s3cret","format":"json","enabled":true}`,
			),
			Now: time.Now(),
		},
	); err != nil {
		t.Fatal(err)
	}

	box, err := sealing.New(bytes.Repeat([]byte{
		7,
	}, 32))
	if err != nil {
		t.Fatal(err)
	}

	if err := app.SealStoredSecrets(t.Context(), db, blobs, box, log); err != nil {
		t.Fatal(err)
	}

	if db.SQLite() {
		rows, err := db.Query(t.Context(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
		if err != nil {
			t.Fatal(err)
		}

		var file string
		if !rows.Next() || rows.Scan(&file) != nil {
			t.Fatal("no database file")
		}

		rows.Close()

		for _, path := range []string{
			file,
			file + "-wal",
		} {
			content, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}

			for _, secret := range []string{
				upstreamPassword,
				"repl-s3cret",
				"ldap-s3cret",
				"oidc-s3cret",
				"webhook-s3cret",
			} {
				if bytes.Contains(content, []byte(secret)) {
					t.Errorf("%s still holds %s after sealing", filepath.Base(path), secret)
				}
			}
		}
	}

	tokens, err := auth.NewTokens(t.Context(), db, box, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(
		app.NewHandler(db, blobs, tokens, nil, app.Options{
			AllowPrivateUpstreams: true,
			Secrets:               box,
		}, log),
	)
	t.Cleanup(srv.Close)

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/sealed-remote",
			body:   `{"rclass":"remote","packageType":"generic","url":"` + upstream.URL + `","username":"u","password":"` + upstreamPassword + `"}`,
		},
	)

	if resp, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/sealed-remote/file.txt",
		},
	); resp.StatusCode != 200 ||
		body != "from upstream" {
		t.Errorf("remote with sealed password = %d %q", resp.StatusCode, body)
	}

	do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/repositories/sealed-remote",
			body:   `{"rclass":"remote","description":"d","password":""}`,
		},
	)

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/sealed-remote/other.txt",
		},
	); resp.StatusCode != 200 {
		t.Errorf("remote after update = %d", resp.StatusCode)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/old/file.txt",
	}); resp.StatusCode != 200 {
		t.Errorf("remote sealed at startup = %d", resp.StatusCode)
	}

	if _, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/repositories/sealed-remote",
		},
	); strings.Contains(body, "{relic-sealed}") ||
		strings.Contains(body, upstreamPassword) {
		t.Errorf("repository config leaks the password: %s", body)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/replications/sealed-remote",
			body:   `{"url":"http://x/artifactory/y","username":"u","password":"new-repl-s3cret","cronExp":"0 0 * * * ?","enabled":true}`,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/apk-local",
			body:   `{"rclass":"local","packageType":"alpine"}`,
		},
	)

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/security/keypair/public/repositories/apk-local",
		},
	); resp.StatusCode != 200 {
		t.Errorf("public key = %d", resp.StatusCode)
	}

	_, sealedPassword := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/security/encryptedPassword",
	})
	if sealedPassword == "password" || !sealing.SealedString(sealedPassword) {
		t.Errorf("encrypted password = %q, want the password sealed", sealedPassword)
	}

	for _, tt := range []struct {
		name     string
		password string
		want     int
	}{
		{
			name:     "sealed password",
			password: sealedPassword,
			want:     http.StatusOK,
		},
		{
			name:     "tampered sealed password",
			password: sealedPassword[:len(sealedPassword)/2] + "~" + sealedPassword[len(sealedPassword)/2+1:],
			want:     http.StatusUnauthorized,
		},
	} {
		resp, body := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/artifactory/api/security/encryptedPassword",
				headers: map[string]string{
					"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+tt.password)),
				},
			},
		)
		if resp.StatusCode != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.name, resp.StatusCode, tt.want)
		}

		if tt.want == http.StatusOK && body != sealedPassword {
			t.Errorf("%s: encrypted password of a sealed password = %q, want it unchanged", tt.name, body)
		}
	}

	var stored bytes.Buffer

	asBytes := "::text::bytea"
	if db.SQLite() {
		asBytes = ""
	}

	for _, q := range []string{
		"SELECT private_key FROM signing_keys",
		"SELECT private_key FROM key_pairs",
		"SELECT config" + asBytes + " FROM repositories WHERE key IN ('old', 'sealed-remote')",
		"SELECT config" + asBytes + " FROM replications",
		"SELECT config" + asBytes + " FROM ldap_servers",
		"SELECT config" + asBytes + " FROM identity_providers",
		"SELECT config" + asBytes + " FROM webhooks",
	} {
		rows, err := db.Query(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}

		n := 0

		for rows.Next() {
			var b []byte
			if err := rows.Scan(&b); err != nil {
				t.Fatal(err)
			}

			if !sealing.Sealed(b) && !sealing.ContainsSealedString(b) {
				t.Errorf("%s: a row is not sealed", q)
			}

			stored.Write(b)

			n++
		}

		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}

		rows.Close()

		if n == 0 {
			t.Errorf("%s: no rows", q)
		}
	}

	for _, secret := range []string{
		upstreamPassword,
		"repl-s3cret",
		"new-repl-s3cret",
		"ldap-s3cret",
		"oidc-s3cret",
		"webhook-s3cret",
	} {
		if bytes.Contains(stored.Bytes(), []byte(secret)) {
			t.Errorf("%q is stored in plaintext", secret)
		}
	}

	restarted, err := auth.NewTokens(t.Context(), db, box, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := tokens.Issue(t.Context(), auth.IssueParams{
		Username: "admin",
		Scope:    auth.UserScope,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := restarted.Verify(t.Context(), tok.AccessToken); err != nil {
		t.Errorf("verify after restart: %v", err)
	}

	if _, err := auth.NewTokens(t.Context(), db, nil, access.Format{}); err == nil {
		t.Error("NewTokens without the key loaded sealed keys")
	}
}
