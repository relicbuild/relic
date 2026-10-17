//go:build integration

package integration

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func createTokenUser(t *testing.T, srv *httptest.Server, name, password string, groups ...string) {
	t.Helper()

	for _, g := range groups {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/security/groups/" + g,
			body:   `{}`,
		})
	}

	groupList, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}

	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/security/users/" + name,
		body:   `{"email":"` + name + `@example.com","password":"` + password + `","groups":` + string(groupList) + `}`,
	}); resp.StatusCode >= 300 {
		t.Fatalf("create user %s = %d %s", name, resp.StatusCode, body)
	}
}

func adminToken(t *testing.T, srv *httptest.Server, form string) string {
	t.Helper()

	resp, body := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/security/token",
		body:   form,
		headers: map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		},
	})

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &tok); err != nil || resp.StatusCode != 200 || tok.AccessToken == "" {
		t.Fatalf("token %s = %d %s", form, resp.StatusCode, body)
	}

	return tok.AccessToken
}

func userTokens(t *testing.T, srv *httptest.Server, username, description string) []*relicv1.AccessToken {
	t.Helper()

	security := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	list, err := security.ListTokens(t.Context(), connect.NewRequest(&relicv1.ListTokensRequest{
		Username: username,
	}))
	if err != nil {
		t.Fatal(err)
	}

	return slices.DeleteFunc(list.Msg.GetTokens(), func(tok *relicv1.AccessToken) bool {
		return tok.GetDescription() != description
	})
}

func TestNpmLoginStaysWithinTheCredential(t *testing.T) {
	t.Parallel()

	srv := server(t)
	createTokenUser(t, srv, "dana", "Dana-pass-1", "builders", "readers")
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/login-npm",
		body:   `{"rclass":"local","packageType":"npm"}`,
	})

	scoped := adminToken(t, srv, "username=dana&scope=applied-permissions/groups:readers&expires_in=3600")

	for _, password := range []string{
		"Dana-pass-1",
		scoped,
	} {
		resp, body := do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/npm/login-npm/-/user/org.couchdb.user:dana",
			body:   `{"name":"dana","password":"` + password + `"}`,
			noAuth: true,
		})
		if resp.StatusCode != 201 {
			t.Fatalf("npm login = %d %s", resp.StatusCode, body)
		}
	}

	logins := userTokens(t, srv, "dana", "npm login")
	if len(logins) != 2 {
		t.Fatalf("npm login tokens = %v", logins)
	}

	for _, tok := range logins {
		if tok.GetExpires() == nil {
			t.Errorf("a user's npm login token never expires: %v", tok)
		}
	}

	if scope := logins[0].GetScope(); scope.GetKind() != relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_GROUPS ||
		!slices.Equal(scope.GetGroups(), []string{
			"readers",
		}) {
		t.Errorf("npm login with a token scoped to readers = %v, want the readers scope", scope)
	}

	if scope := logins[1].GetScope(); scope.GetKind() != relicv1.TokenScopeKind_TOKEN_SCOPE_KIND_USER {
		t.Errorf("npm login with a password = %v, want the user scope", scope)
	}
}
