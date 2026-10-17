//go:build integration

package integration

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

type restrictedUser struct {
	name, password string
	actions        string
}

func guardedRepository(t *testing.T, users ...restrictedUser) *httptest.Server {
	t.Helper()

	srv := server(t)
	for _, key := range []string{
		"guarded-local",
		"other-local",
	} {
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/" + key,
			body:   `{"rclass":"local","packageType":"generic"}`,
		}); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", key, resp.StatusCode, body)
		}
	}

	for path, body := range map[string]string{
		"guarded-local/src/public.txt":   "public",
		"guarded-local/src/secret/s.txt": "secret",
		"guarded-local/dst/open.txt":     "hello",
		"other-local/empty.txt":          "replacement",
	} {
		if resp, reply := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/" + path,
			body:   body,
		}); resp.StatusCode != 201 {
			t.Fatalf("upload %s = %d %s", path, resp.StatusCode, reply)
		}
	}

	for _, u := range users {
		if resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/security/users/" + u.name,
			body:   `{"email":"` + u.name + `@example.com","password":"` + u.password + `"}`,
		}); resp.StatusCode >= 300 {
			t.Fatalf("create user %s = %d %s", u.name, resp.StatusCode, body)
		}

		perm := `{"name":"` + u.name + `",
			"repo":{"repositories":["guarded-local"],"include-patterns":["**"],"exclude-patterns":["src/secret/**"],
			"actions":{"users":{"` + u.name + `":` + u.actions + `}}}}`
		if resp, body := do(t, srv, call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/" + u.name,
			body:   perm,
		}); resp.StatusCode >= 300 {
			t.Fatalf("permission %s = %d %s", u.name, resp.StatusCode, body)
		}

		other := `{"name":"` + u.name + `-other",
			"repo":{"repositories":["other-local"],"include-patterns":["**"],
			"actions":{"users":{"` + u.name + `":["read"]}}}}`
		if resp, body := do(t, srv, call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/" + u.name + "-other",
			body:   other,
		}); resp.StatusCode >= 300 {
			t.Fatalf("permission %s-other = %d %s", u.name, resp.StatusCode, body)
		}
	}

	return srv
}

func basicAuthHeader(user, password string) map[string]string {
	return map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)),
	}
}

func adminContent(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()

	resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/" + path,
	})

	return resp.StatusCode, body
}

func TestCopyAndMoveCheckEveryFile(t *testing.T) {
	t.Parallel()

	copier := restrictedUser{
		name:     "copier",
		password: "Copier-pass-1",
		actions:  `["read","write"]`,
	}
	mover := restrictedUser{
		name:     "mover",
		password: "Mover-pass-1",
		actions:  `["read","write","delete"]`,
	}
	srv := guardedRepository(t, copier, mover)

	rest := func(u restrictedUser, path string) (int, string) {
		resp, body := do(t, srv, call{
			method:  "POST",
			path:    path,
			noAuth:  true,
			headers: basicAuthHeader(u.name, u.password),
		})

		return resp.StatusCode, body
	}
	artifacts := func(u restrictedUser) relicv1connect.ArtifactServiceClient {
		return relicv1connect.NewArtifactServiceClient(relicHTTP(u.name, u.password), srv.URL+"/api")
	}

	t.Run("copying a folder with an unreadable file is refused", func(t *testing.T) {
		t.Parallel()

		if status, body := rest(
			copier,
			"/artifactory/api/copy/guarded-local/src?to=/guarded-local/rest-copy",
		); status != 403 {
			t.Errorf("REST copy = %d %s, want 403", status, body)
		}

		_, err := artifacts(copier).CopyItem(t.Context(), connect.NewRequest(&relicv1.CopyItemRequest{
			Repository:   "guarded-local",
			Path:         "src",
			ToRepository: "guarded-local",
			ToPath:       "api-copy",
		}))
		if code(err) != connect.CodePermissionDenied {
			t.Errorf("Relic API copy = %v, want PermissionDenied", err)
		}

		for _, path := range []string{
			"guarded-local/rest-copy/secret/s.txt",
			"guarded-local/api-copy/secret/s.txt",
		} {
			if status, _ := adminContent(t, srv, path); status != 404 {
				t.Errorf("%s after a refused copy = %d, want 404", path, status)
			}
		}
	})

	t.Run("moving a folder with an undeletable file is refused", func(t *testing.T) {
		t.Parallel()

		if status, body := rest(
			mover,
			"/artifactory/api/move/guarded-local/src?to=/guarded-local/rest-moved",
		); status != 403 {
			t.Errorf("REST move = %d %s, want 403", status, body)
		}

		_, err := artifacts(mover).MoveItem(t.Context(), connect.NewRequest(&relicv1.MoveItemRequest{
			Repository:   "guarded-local",
			Path:         "src",
			ToRepository: "guarded-local",
			ToPath:       "api-moved",
		}))
		if code(err) != connect.CodePermissionDenied {
			t.Errorf("Relic API move = %v, want PermissionDenied", err)
		}

		if status, body := adminContent(t, srv, "guarded-local/src/secret/s.txt"); status != 200 || body != "secret" {
			t.Errorf("protected file after refused moves = %d %q, want 200 secret", status, body)
		}
	})

	t.Run("copying over a file needs delete permission on it", func(t *testing.T) {
		t.Parallel()

		if status, body := rest(
			copier,
			"/artifactory/api/copy/other-local/empty.txt?to=/guarded-local/dst/open.txt",
		); status != 403 {
			t.Errorf("REST copy over a file = %d %s, want 403", status, body)
		}

		_, err := artifacts(copier).CopyItem(t.Context(), connect.NewRequest(&relicv1.CopyItemRequest{
			Repository:   "other-local",
			Path:         "empty.txt",
			ToRepository: "guarded-local",
			ToPath:       "dst/open.txt",
		}))
		if code(err) != connect.CodePermissionDenied {
			t.Errorf("Relic API copy over a file = %v, want PermissionDenied", err)
		}

		if status, body := adminContent(t, srv, "guarded-local/dst/open.txt"); status != 200 || body != "hello" {
			t.Errorf("file after refused overwrites = %d %q, want 200 hello", status, body)
		}
	})

	t.Run("permitted copies still work", func(t *testing.T) {
		t.Parallel()

		if status, body := rest(
			copier,
			"/artifactory/api/copy/other-local/empty.txt?to=/guarded-local/new/empty.txt",
		); status != 200 {
			t.Errorf("REST copy to a new file = %d %s, want 200", status, body)
		}

		if _, err := artifacts(copier).CopyItem(t.Context(), connect.NewRequest(&relicv1.CopyItemRequest{
			Repository:   "guarded-local",
			Path:         "dst",
			ToRepository: "guarded-local",
			ToPath:       "dst-copy",
		})); err != nil {
			t.Errorf("Relic API copy of a readable folder = %v", err)
		}

		if _, err := artifacts(mover).CopyItem(t.Context(), connect.NewRequest(&relicv1.CopyItemRequest{
			Repository:   "other-local",
			Path:         "empty.txt",
			ToRepository: "guarded-local",
			ToPath:       "dst-copy/open.txt",
		})); err != nil {
			t.Errorf("Relic API copy over a deletable file = %v", err)
		}
	})
}
