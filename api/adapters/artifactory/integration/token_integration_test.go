//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

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

	security := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	list, err := security.ListTokens(t.Context(), &relicv1.ListTokensRequest{
		Username: username,
	})
	if err != nil {
		t.Fatal(err)
	}

	return slices.DeleteFunc(list.GetTokens(), func(tok *relicv1.AccessToken) bool {
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

func basicHeaders(credentials string) map[string]string {
	return map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials)),
		"Content-Type":  "application/json",
	}
}

func accessTokenFor(t *testing.T, srv *httptest.Server, credentials, request string) (int, string) {
	t.Helper()

	resp, body := do(t, srv, call{
		method:  "POST",
		path:    "/access/api/v1/tokens",
		body:    request,
		noAuth:  true,
		headers: basicHeaders(credentials),
	})

	var tok struct {
		AccessToken string `json:"access_token"`
	}

	_ = json.Unmarshal([]byte(body), &tok)

	return resp.StatusCode, tok.AccessToken
}

func signsInAs(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()

	me, err := relicv1connect.NewSessionServiceClient(apiClient(http.DefaultClient, srv.URL)).GetCurrentUser(
		withRequestHeader(t.Context(), "Authorization", "Bearer "+token),
		&relicv1.GetCurrentUserRequest{},
	)
	if err != nil {
		return ""
	}

	return me.GetPrincipal().GetUsername()
}

func TestAccessTokensKeepTheUsernamesCase(t *testing.T) {
	t.Parallel()

	srv := server(t)
	createTokenUser(t, srv, "Carol", "Carol-pass-1")

	for _, tt := range []struct {
		credentials string
		username    string
	}{
		{
			credentials: "admin:password",
			username:    "Carol",
		},
		{
			credentials: "Carol:Carol-pass-1",
			username:    "carol",
		},
		{
			credentials: "Carol:Carol-pass-1",
		},
	} {
		status, token := accessTokenFor(t, srv, tt.credentials, `{"username":"`+tt.username+`","expires_in":300}`)
		if got := signsInAs(t, srv, token); status != 200 || got != "Carol" {
			t.Errorf(
				"a token %s asked for %q = %d, signs in as %q, want Carol",
				tt.credentials,
				tt.username,
				status,
				got,
			)
		}
	}
}

func TestAccessTokensRefuseUnsupportedScopes(t *testing.T) {
	t.Parallel()

	srv := server(t)
	createTokenUser(t, srv, "erin", "Erin-pass-1")

	tests := []struct {
		name        string
		credentials string
		scope       string
		status      int
	}{
		{
			name:        "an administrator asking for a metrics scope",
			credentials: "admin:password",
			scope:       "system:metrics:r",
			status:      400,
		},
		{
			name:        "an administrator asking for a project role",
			credentials: "admin:password",
			scope:       "applied-permissions/roles:web:Viewer",
			status:      400,
		},
		{
			name:        "a user asking for a metrics scope",
			credentials: "erin:Erin-pass-1",
			scope:       "system:metrics:r",
			status:      400,
		},
		{
			name:        "an administrator asking for their own permissions",
			credentials: "admin:password",
			scope:       "applied-permissions/user",
			status:      200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, body := do(t, srv, call{
				method:  "POST",
				path:    "/access/api/v1/tokens",
				body:    `{"scope":"` + tt.scope + `","expires_in":300}`,
				noAuth:  true,
				headers: basicHeaders(tt.credentials),
			})
			if resp.StatusCode != tt.status {
				t.Errorf("scope %q = %d %s, want %d", tt.scope, resp.StatusCode, body, tt.status)
			}
		})
	}
}

func TestTokensForUnknownUsersAreRefused(t *testing.T) {
	t.Parallel()

	srv := server(t)

	status, _ := accessTokenFor(t, srv, "admin:password", `{"username":"ghost","expires_in":300}`)
	if status != 404 {
		t.Errorf("access token for an unknown user = %d, want 404", status)
	}

	resp, body := do(t, srv, call{
		method: "POST",
		path:   "/artifactory/api/security/token",
		body:   "username=ghost&expires_in=300",
		headers: map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		},
	})
	if resp.StatusCode != 404 {
		t.Errorf("legacy token for an unknown user = %d %s, want 404", resp.StatusCode, body)
	}

	createTokenUser(t, srv, "ghost", "Ghost-pass-1")

	if tokens := userTokens(t, srv, "ghost", ""); len(tokens) != 0 {
		t.Errorf("tokens issued before the user existed: %v", tokens)
	}
}

func TestAccessTokensIncludeAReferenceTokenOnRequest(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for _, tt := range []struct {
		name    string
		request string
		want    bool
	}{
		{
			name:    "asked for",
			request: `{"expires_in":300,"include_reference_token":true}`,
			want:    true,
		},
		{
			name:    "not asked for",
			request: `{"expires_in":300}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, body := do(t, srv, call{
				method:  "POST",
				path:    "/access/api/v1/tokens",
				body:    tt.request,
				noAuth:  true,
				headers: basicHeaders("admin:password"),
			})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("create token = %d %s", resp.StatusCode, body)
			}

			var tok struct {
				ReferenceToken string `json:"reference_token"`
			}
			if err := json.Unmarshal([]byte(body), &tok); err != nil {
				t.Fatal(err)
			}

			if (tok.ReferenceToken != "") != tt.want {
				t.Fatalf("reference_token present = %t, want %t", tok.ReferenceToken != "", tt.want)
			}

			if tt.want && signsInAs(t, srv, tok.ReferenceToken) != "admin" {
				t.Error("the reference token does not sign in as admin")
			}
		})
	}
}

func TestAccessTokensNameAnExistingProject(t *testing.T) {
	t.Parallel()

	srv := server(t)

	status, _ := accessTokenFor(t, srv, "admin:password", `{"expires_in":300,"project_key":"nosuchproject"}`)
	if status != http.StatusNotFound {
		t.Errorf("token for a missing project = %d, want 404", status)
	}

	status, token := accessTokenFor(t, srv, "admin:password", `{"expires_in":300}`)
	if status != http.StatusOK || token == "" {
		t.Errorf("token without a project = %d, want 200", status)
	}
}
