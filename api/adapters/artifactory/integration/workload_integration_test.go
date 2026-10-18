//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/auth"
	"relic/internal/access/sso/ssotest"
)

func TestWorkloadTokenExchange(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	issuer := ssotest.NewOIDC(t, "unused", "unused")

	security := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	//nolint:gosec // G101: a test user's password
	if _, err := security.PutUser(ctx, &relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "ci-bot",
			Email:    "ci@example.com",
			Password: "Ci-bot-pw-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	identities := relicv1connect.NewWorkloadIdentityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := identities.PutWorkloadIdentity(ctx, &relicv1.PutWorkloadIdentityRequest{
		WorkloadIdentity: &relicv1.WorkloadIdentity{
			Name:     "github",
			Enabled:  true,
			Issuer:   issuer.URL,
			Audience: "relic",
			Rules: []*relicv1.WorkloadRule{
				{
					Claims: map[string]string{
						"repository": "acme/*",
					},
					User: "ci-bot",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	exchange := func(repo string) (int, map[string]any) {
		//nolint:gosec // G101: token-exchange field names, not credentials
		body, err := json.Marshal(map[string]string{
			"grant_type":         "urn:ietf:params:oauth:grant-type:token-exchange",
			"subject_token_type": "urn:ietf:params:oauth:token-type:id_token",
			"subject_token": issuer.Mint(t, map[string]any{
				"sub":        "repo:" + repo,
				"aud":        "relic",
				"repository": repo,
			}),
			"provider_name": "github",
		})
		if err != nil {
			t.Fatal(err)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			srv.URL+"/access/api/v1/oidc/token",
			bytes.NewReader(body),
		)
		if err != nil {
			t.Fatal(err)
		}

		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		var out map[string]any

		_ = json.NewDecoder(resp.Body).Decode(&out)

		return resp.StatusCode, out
	}

	status, granted := exchange("acme/app")
	if status != http.StatusOK || granted["username"] != "ci-bot" || granted["token_type"] != "Bearer" {
		t.Fatalf("exchange = %d %v", status, granted)
	}

	session := relicv1connect.NewSessionServiceClient(apiClient(http.DefaultClient, srv.URL))

	me, err := session.GetCurrentUser(
		withRequestHeader(ctx, "Authorization", "Bearer "+granted["access_token"].(string)),
		&relicv1.GetCurrentUserRequest{},
	)
	if err != nil || me.GetPrincipal().GetUsername() != "ci-bot" {
		t.Fatalf("the exchanged token signs in as %v, %v", me, err)
	}

	if status, refused := exchange("evil/app"); status != http.StatusUnauthorized ||
		!strings.Contains(fmt.Sprint(refused["errors"]), "the token exchange was refused") ||
		strings.Contains(fmt.Sprint(refused["errors"]), "github") {
		t.Errorf("an unmatched token = %d %v", status, refused)
	}

	native, err := session.ExchangeWorkloadToken(ctx, &relicv1.ExchangeWorkloadTokenRequest{
		Identity: "github",
		Token: issuer.Mint(t, map[string]any{
			"sub":        "repo:acme/lib",
			"aud":        "relic",
			"repository": "acme/lib",
		}),
	})
	if err != nil || native.GetUsername() != "ci-bot" || native.GetExpiresInSeconds() != 3600 {
		t.Fatalf("Relic API exchange = %v, %v", native, err)
	}

	workloadToken := native.GetAccessToken()

	minted, err := relicv1connect.NewSecurityServiceClient(apiClient(
		relicHTTP("ci-bot", workloadToken),
		srv.URL,
	)).CreateToken(ctx, &relicv1.CreateTokenRequest{
		Description: "outlives the job",
		ExpiresDays: 30,
	})
	if err != nil || minted.GetExpiresIn() <= 0 || minted.GetExpiresIn() > 3600 {
		t.Errorf("a token minted with a workload token = %v, %v; want it to expire within the hour", minted, err)
	}

	_, accessToken := do(t, srv, call{
		method: http.MethodPost,
		path:   "/access/api/v1/tokens",
		body:   `{"scope":"applied-permissions/user","expires_in":31536000,"refreshable":true}`,
		headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + workloadToken,
		},
		noAuth: true,
	})

	var accessMinted struct {
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal([]byte(accessToken), &accessMinted); err != nil ||
		accessMinted.ExpiresIn <= 0 || accessMinted.ExpiresIn > 3600 || accessMinted.RefreshToken != "" {
		t.Errorf("an Access API token minted with a workload token = %s", accessToken)
	}

	const cliSession = "6f1c2d3e-4a5b-4c7d-8e9f-0a1b2c3d4e5f"
	if _, err := session.StartWebLogin(ctx, &relicv1.StartWebLoginRequest{
		Session: cliSession,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := relicv1connect.NewSessionServiceClient(apiClient(
		relicHTTP("ci-bot", workloadToken),
		srv.URL,
	)).ApproveLogin(ctx, &relicv1.ApproveLoginRequest{
		Session: cliSession,
		Code:    cliSession[len(cliSession)-4:],
	}); code(err) != connect.CodePermissionDenied {
		t.Errorf("a workload token approving a CLI sign-in: %v", err)
	}

	signIn := &relicv1.LoginRequest{
		Username: "ci-bot",
		Password: workloadToken,
	}
	signInCtx, signInInfo := connect.NewClientContext(ctx)
	signInInfo.RequestHeader().Set(auth.UIHeader, "1")

	if _, err := session.Login(signInCtx, signIn); err != nil {
		t.Fatal(err)
	}

	if cookie, err := http.ParseSetCookie(signInInfo.ResponseHeader().Get("Set-Cookie")); err != nil ||
		cookie.MaxAge <= 0 || cookie.MaxAge > 3600 {
		t.Errorf("a UI session signed in with a workload token = %v, %v; want it to end within the hour", cookie, err)
	}

	if _, err := session.ExchangeWorkloadToken(ctx, &relicv1.ExchangeWorkloadTokenRequest{
		Identity: "github",
		Token:    "not-a-token",
	}); code(err) != connect.CodeUnauthenticated || !strings.HasSuffix(err.Error(), ": the token exchange was refused") {
		t.Errorf("a malformed token: %v, want a refusal that names no reason", err)
	}

	log := relicv1connect.NewAuditServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	events, err := log.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Action: "workload.exchange",
	})
	if err != nil || events.GetTotal() != 4 {
		t.Errorf("exchange audit events = %v, %v", events, err)
	}
}
