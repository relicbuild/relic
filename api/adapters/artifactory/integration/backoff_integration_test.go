//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestSignInBackoff(t *testing.T) {
	t.Parallel()
	srv := server(t)
	_, token := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/access/api/v1/tokens",
			body:   `{"scope":"applied-permissions/user"}`,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		},
	)

	var bearer struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(token), &bearer); err != nil || bearer.AccessToken == "" {
		t.Fatalf("token = %s", token)
	}

	bad := func() int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/artifactory/api/repositories", nil)
		req.SetBasicAuth("admin", "wrong")

		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		return resp.StatusCode
	}
	for i := range 20 {
		if got := bad(); got != http.StatusUnauthorized {
			t.Fatalf("wrong password %d = %d, want 401", i+1, got)
		}
	}

	if got := bad(); got != http.StatusUnauthorized {
		t.Fatalf("21st wrong password = %d, want 401 (checked, then throttled)", got)
	}

	resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/api/repositories",
	})
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf(
			"right password while throttled = %d (Retry-After %q), want 429",
			resp.StatusCode,
			resp.Header.Get("Retry-After"),
		)
	}

	login := &relicv1.LoginRequest{
		Username: "admin",
		Password: "password",
	}
	if _, err := relicv1connect.NewSessionServiceClient(apiClient(relicHTTP("", ""), srv.URL)).
		Login(withRequestHeader(t.Context(), "X-Relic-UI", "1"), login); code(err) != connect.CodeResourceExhausted {
		t.Errorf("UI sign-in while throttled: %v, want resource exhausted", err)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/repositories",
			noAuth: true,
			headers: map[string]string{
				"Authorization": "Bearer " + bearer.AccessToken,
			},
		},
	); resp.StatusCode != http.StatusOK {
		t.Errorf("token while throttled = %d, want 200", resp.StatusCode)
	}
}
