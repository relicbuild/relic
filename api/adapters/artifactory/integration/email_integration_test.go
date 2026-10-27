//go:build integration

package integration

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestInvalidEmailsAreRefused(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	sec := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := sec.PutUser(ctx, &relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "weak",
			Email:    "not-an-email",
			Password: "Weak-pass-12345",
		},
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("PutUser with an invalid email: %v, want invalid_argument", err)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/security/users/weak",
		body:   `{"name":"weak","email":"not-an-email","password":"Weak-pass-12345"}`,
	}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("the JFrog user route with an invalid email = %d %s, want 400", resp.StatusCode, body)
	}

	admin := scimClient{
		t:     t,
		srv:   srv,
		token: accessToken(t, srv, "admin", "password", "admin"),
	}

	if status, refused := admin.do(http.MethodPost, "/Users", `{
		"userName": "gina",
		"emails": [{"primary": true, "value": "not-an-email"}]
	}`); status != http.StatusBadRequest || refused["scimType"] != "invalidValue" {
		t.Errorf("SCIM create with an invalid email = %d %v, want 400 invalidValue", status, refused)
	}

	status, gina := admin.do(http.MethodPost, "/Users", `{
		"userName": "gina",
		"emails": [{"primary": true, "value": "gina@example.com"}]
	}`)
	if status != http.StatusCreated {
		t.Fatalf("SCIM create = %d %v", status, gina)
	}

	id, _ := gina["id"].(string)

	if status, refused := admin.do(http.MethodPut, "/Users/"+id, `{
		"userName": "gina",
		"emails": [{"primary": true, "value": "gina at example.com"}]
	}`); status != http.StatusBadRequest || refused["scimType"] != "invalidValue" {
		t.Errorf("SCIM replace with an invalid email = %d %v, want 400 invalidValue", status, refused)
	}

	if status, refused := admin.do(http.MethodPatch, "/Users/"+id, `{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [{"op": "replace", "path": "emails", "value": [{"primary": true, "value": "gina@"}]}]
	}`); status != http.StatusBadRequest || refused["scimType"] != "invalidValue" {
		t.Errorf("SCIM patch with an invalid email = %d %v, want 400 invalidValue", status, refused)
	}
}
