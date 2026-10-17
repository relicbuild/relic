//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"relic/internal/platform/telemetry"
)

type scimClient struct {
	t     *testing.T
	srv   *httptest.Server
	token string
}

func (c scimClient) do(method, path, body string) (int, map[string]any) {
	c.t.Helper()

	req, err := http.NewRequestWithContext(c.t.Context(), method, c.srv.URL+"/scim/v2"+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	req.Header.Set("Content-Type", "application/scim+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}

	var out map[string]any

	_ = json.Unmarshal(raw, &out)

	return resp.StatusCode, out
}

func accessToken(t *testing.T, srv *httptest.Server, user, password, forUser string) string {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"username":   forUser,
		"scope":      "applied-permissions/user",
		"expires_in": 3600,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		srv.URL+"/access/api/v1/tokens",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}

	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		t.Fatalf("token for %s = %d %v", forUser, resp.StatusCode, err)
	}

	return out.AccessToken
}

func filter(attr, value string) string {
	return "?filter=" + url.QueryEscape(attr+` eq "`+value+`"`)
}

func TestSCIM(t *testing.T) {
	t.Parallel()
	srv := server(t)
	admin := scimClient{
		t:     t,
		srv:   srv,
		token: accessToken(t, srv, "admin", "password", "admin"),
	}

	if status, unauthorized := (scimClient{
		t:   t,
		srv: srv,
	}).do(http.MethodGet, "/Users", ""); status != http.StatusUnauthorized ||
		unauthorized["status"] != "401" {
		t.Errorf("without a token: %d %v", status, unauthorized)
	}

	if family := telemetry.Family("/scim/v2/Users"); family != "access" {
		t.Errorf("SCIM requests count toward %q", family)
	}

	if status, cfg := admin.do(http.MethodGet, "/ServiceProviderConfig", ""); status != http.StatusOK ||
		cfg["patch"].(map[string]any)["supported"] != true {
		t.Errorf("service provider config = %d %v", status, cfg)
	}

	if status, found := admin.do(http.MethodGet, "/Users"+filter("userName", "gina"), ""); status != http.StatusOK ||
		found["totalResults"] != 0.0 {
		t.Fatalf("search before creating = %d %v", status, found)
	}

	status, gina := admin.do(http.MethodPost, "/Users", `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName": "gina",
		"name": {"givenName": "Gina", "familyName": "Lee"},
		"emails": [{"primary": true, "value": "gina@example.com", "type": "work"}],
		"active": true,
		"password": "ignored-Pw-1"
	}`)
	if status != http.StatusCreated || gina["id"] == nil || gina["active"] != true {
		t.Fatalf("create gina = %d %v", status, gina)
	}

	id := gina["id"].(string)

	for _, name := range []string{
		"gina",
		"admin",
	} {
		if status, conflict := admin.do(
			http.MethodPost,
			"/Users",
			`{"userName": "`+name+`"}`,
		); status != http.StatusConflict ||
			conflict["scimType"] != "uniqueness" {
			t.Errorf("create %s again = %d %v", name, status, conflict)
		}
	}

	if status, henry := admin.do(
		http.MethodPost,
		"/Users",
		`{"userName": "henry", "active": false}`,
	); status != http.StatusCreated ||
		henry["active"] != false {
		t.Errorf("create an inactive user = %d %v", status, henry)
	}

	if status, found := admin.do(http.MethodGet, "/Users"+filter("userName", "GINA"), ""); status != http.StatusOK ||
		found["totalResults"] != 1.0 {
		t.Errorf("search = %d %v", status, found)
	}

	if status, found := admin.do(http.MethodGet, "/Users"+filter("userName", "admin"), ""); status != http.StatusOK ||
		found["totalResults"] != 0.0 {
		t.Errorf("SCIM sees the local admin: %d %v", status, found)
	}

	if status, far := admin.do(http.MethodGet, "/Users?startIndex=2147483649", ""); status != http.StatusOK ||
		len(far["Resources"].([]any)) != 0 {
		t.Errorf("a start index past int32 = %d %v", status, far)
	}

	if status, bad := admin.do(http.MethodGet, "/Users"+filter("title", "x"), ""); status != http.StatusBadRequest ||
		bad["scimType"] != "invalidFilter" {
		t.Errorf("unsupported filter = %d %v", status, bad)
	}

	status, put := admin.do(http.MethodPut, "/Users/"+id, `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName": "Gina",
		"emails": [{"value": "gina.lee@example.com", "type": "work"}],
		"active": true
	}`)
	if status != http.StatusOK || !strings.Contains(asJSON(t, put), "gina.lee@example.com") {
		t.Errorf("replace = %d %v", status, put)
	}

	if status, renamed := admin.do(
		http.MethodPut,
		"/Users/"+id,
		`{"userName": "gina2"}`,
	); status != http.StatusBadRequest ||
		renamed["scimType"] != "mutability" {
		t.Errorf("rename = %d %v", status, renamed)
	}

	ginaToken := accessToken(t, srv, "admin", "password", "gina")
	gina2 := scimClient{
		t:     t,
		srv:   srv,
		token: ginaToken,
	}

	if status, _ := gina2.do(http.MethodGet, "/Users", ""); status != http.StatusForbidden {
		t.Errorf("a non-administrator's token = %d", status)
	}

	status, patched := admin.do(http.MethodPatch, "/Users/"+id, `{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [
			{"op": "Replace", "path": " active ", "value": "False"},
			{"op": "Add", "path": "emails[type eq \"work\"].value", "value": "g@example.com"},
			{"op": "Replace", "path": "name.givenName", "value": "G"}
		]
	}`)
	if status != http.StatusOK || patched["active"] != false || !strings.Contains(asJSON(t, patched), "g@example.com") {
		t.Fatalf("Entra ID patch = %d %v", status, patched)
	}

	if status, _ := gina2.do(http.MethodGet, "/ServiceProviderConfig", ""); status != http.StatusUnauthorized {
		t.Errorf("a disabled user's token = %d, want 401", status)
	}

	if status, okta := admin.do(http.MethodPatch, "/Users/"+id, `{
		"schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations": [{"op": "replace", "value": {"active": true}}]
	}`); status != http.StatusOK || okta["active"] != true {
		t.Errorf("Okta patch = %d %v", status, okta)
	}

	if status, kept := admin.do(http.MethodPatch, "/Users/"+id, `{
		"Operations": [
			{"op": "remove", "path": "emailsFoo"},
			{"op": "add", "path": "emails[type eq \"home\"].value", "value": "home@example.com"},
			{"op": "remove", "path": "emails[type eq \"home\"]"}
		]
	}`); status != http.StatusOK || !strings.Contains(asJSON(t, kept), "g@example.com") ||
		strings.Contains(asJSON(t, kept), "home@example.com") {
		t.Errorf("changes to emails Relic does not keep = %d %v", status, kept)
	}

	_, hal := admin.do(http.MethodPost, "/Users", `{"userName": "hal", "active": true}`)
	halID := hal["id"].(string)

	status, group := admin.do(http.MethodPost, "/Groups", `{
		"schemas": ["urn:ietf:params:scim:schemas:core:2.0:Group"],
		"displayName": "scim-devs",
		"members": [{"value": "`+id+`"}]
	}`)
	if status != http.StatusCreated || group["id"] != "scim-devs" || len(group["members"].([]any)) != 1 {
		t.Fatalf("create group = %d %v", status, group)
	}

	if status, found := admin.do(
		http.MethodGet,
		"/Groups"+filter("displayName", " scim-devs "),
		"",
	); status != http.StatusOK ||
		found["totalResults"] != 1.0 {
		t.Errorf("group search = %d %v", status, found)
	}

	if status, orphan := admin.do(http.MethodPost, "/Groups", `{
		"displayName": "scim-orphan",
		"members": [{"value": "00000000-0000-0000-0000-000000000000"}]
	}`); status != http.StatusBadRequest || orphan["scimType"] != "invalidValue" {
		t.Errorf("create a group with an unknown member = %d %v", status, orphan)
	}

	if status, _ := admin.do(http.MethodGet, "/Groups/scim-orphan", ""); status != http.StatusNotFound {
		t.Errorf("a refused group create left the group: %d", status)
	}

	status, added := admin.do(http.MethodPatch, "/Groups/scim-devs", `{
		"Operations": [
			{"op": "Replace", "path": "externalId", "value": "e-1"},
			{"op": "Add", "value": {"urn:ietf:params:scim:schemas:extension:example:2.0:Group:code": "x"}},
			{"op": "Add", "path": "members", "value": [{"value": "`+halID+`"}]}
		]
	}`)
	if status != http.StatusOK || len(added["members"].([]any)) != 2 {
		t.Errorf("add member = %d %v", status, added)
	}

	status, removed := admin.do(http.MethodPatch, "/Groups/scim-devs", `{
		"Operations": [{"op": "Remove", "path": "members[value eq \"`+id+`\"]"}]
	}`)
	if status != http.StatusOK || len(removed["members"].([]any)) != 1 {
		t.Errorf("remove member = %d %v", status, removed)
	}

	status, ordered := admin.do(http.MethodPatch, "/Groups/scim-devs", `{
		"Operations": [
			{"op": "Replace", "path": "members", "value": [{"value": "`+id+`"}, {"value": "`+halID+`"}]},
			{"op": "Remove", "path": "members[value eq \"`+id+`\"]"}
		]
	}`)
	if members, _ := ordered["members"].([]any); status != http.StatusOK || len(members) != 1 ||
		members[0].(map[string]any)["value"] != halID {
		t.Errorf("replace then remove = %d %v", status, ordered)
	}

	if status, replaced := admin.do(http.MethodPut, "/Groups/scim-devs", `{
		"displayName": "SCIM-Devs",
		"members": [{"value": "`+halID+`"}]
	}`); status != http.StatusOK || len(replaced["members"].([]any)) != 1 {
		t.Errorf("replace with the name in other case = %d %v", status, replaced)
	}

	if status, quoted := admin.do(
		http.MethodPost,
		"/Groups",
		`{"displayName": "scim \"q\""}`,
	); status != http.StatusCreated {
		t.Errorf("create a group with a quote = %d %v", status, quoted)
	}

	if status, found := admin.do(
		http.MethodGet,
		"/Groups"+filter("displayName", `scim \"\u0071\"`),
		"",
	); status != http.StatusOK ||
		found["totalResults"] != 1.0 {
		t.Errorf("filter with JSON escapes = %d %v", status, found)
	}

	status, spaced := admin.do(http.MethodPost, "/Groups", `{"displayName": "scim ops"}`)

	spacedMeta, _ := spaced["meta"].(map[string]any)
	if location, _ := spacedMeta["location"].(string); status != http.StatusCreated ||
		!strings.HasSuffix(location, "/Groups/scim%20ops") {
		t.Errorf("group with a space = %d %v", status, spaced)
	}

	if status, _ := admin.do(http.MethodDelete, "/Groups/scim%20ops", ""); status != http.StatusNoContent {
		t.Errorf("delete group with a space = %d", status)
	}

	if status, renamed := admin.do(http.MethodPatch, "/Groups/scim-devs", `{
		"Operations": [{"op": "Replace", "path": "displayName", "value": "other"}]
	}`); status != http.StatusBadRequest || renamed["scimType"] != "mutability" {
		t.Errorf("rename group = %d %v", status, renamed)
	}

	if status, _ := admin.do(http.MethodDelete, "/Groups/scim-devs", ""); status != http.StatusNoContent {
		t.Errorf("delete group = %d", status)
	}

	if status, _ := admin.do(http.MethodGet, "/Groups/scim-devs", ""); status != http.StatusNotFound {
		t.Errorf("deleted group = %d", status)
	}

	if status, _ := admin.do(http.MethodDelete, "/Users/"+id, ""); status != http.StatusNoContent {
		t.Errorf("delete user = %d", status)
	}

	if status, deleted := admin.do(http.MethodGet, "/Users/"+id, ""); status != http.StatusNotFound ||
		strings.Contains(asJSON(t, deleted), "identity provider") {
		t.Errorf("deleted user = %d %v", status, deleted)
	}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()

	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}
