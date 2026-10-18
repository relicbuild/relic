//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/crewjam/saml"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/sso/ssotest"
)

func browser(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	return &http.Client{
		Jar: jar,
	}
}

func landingURL(t *testing.T, c *http.Client, u string) *url.URL {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	return resp.Request.URL
}

func TestSingleSignOn(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	idp := ssotest.NewOIDC(t, "relic", "client-secret-2")

	providers := relicv1connect.NewIdentityProviderServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	put, err := providers.PutIdentityProvider(ctx, &relicv1.PutIdentityProviderRequest{
		IdentityProvider: &relicv1.IdentityProvider{
			Name:            "corp",
			DisplayName:     "Corp SSO",
			Enabled:         true,
			AutoCreateUsers: true,
			Protocol: &relicv1.IdentityProvider_Oidc{
				Oidc: &relicv1.OidcSettings{
					Issuer:       idp.URL,
					ClientId:     "relic",
					ClientSecret: "client-secret-2",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := put.GetIdentityProvider(); got.GetOidc().GetClientSecret() != "" ||
		got.GetCallbackPath() != "/api/v1/sso/corp/callback" {
		t.Errorf("saved provider = %v; want no secret and the callback path", got)
	}

	public := relicv1connect.NewSessionServiceClient(apiClient(http.DefaultClient, srv.URL))

	list, err := public.ListSignInProviders(ctx, &relicv1.ListSignInProvidersRequest{})
	if err != nil || len(list.GetProviders()) != 1 ||
		list.GetProviders()[0].GetDisplayName() != "Corp SSO" {
		t.Fatalf("sign-in providers = %v, %v", list, err)
	}

	idp.SignIn(map[string]any{
		"preferred_username": "dana",
		"email":              "dana@example.com",
	})

	b := browser(t)

	landed := landingURL(t, b, srv.URL+list.GetProviders()[0].GetStartPath()+"?return=/browse")
	if landed.Path != "/browse" {
		t.Fatalf("the sign-in ended at %s", landed)
	}

	whoami := &relicv1.GetCurrentUserRequest{}
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("X-Relic-UI", "1")

	me, err := relicv1connect.NewSessionServiceClient(apiClient(b, srv.URL)).GetCurrentUser(ctx, whoami)
	if err != nil || me.GetPrincipal().GetUsername() != "dana" {
		t.Fatalf("after single sign-on, the session is %v, %v", me, err)
	}

	forged := landingURL(t, browser(t), srv.URL+"/api/v1/sso/corp/callback?state=forged&code=x")
	if forged.Path != "/login" || !strings.Contains(forged.Query().Get("error"), "did not start") {
		t.Errorf("a callback this browser did not start ended at %s", forged)
	}

	log := relicv1connect.NewAuditServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	events, err := log.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Action: "sso.sign-in",
	})
	if err != nil {
		t.Fatal(err)
	}

	var signedIn, refused bool
	for _, e := range events.GetEvents() {
		signedIn = signedIn || e.GetActor() == "dana" && e.GetOutcome() == relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS
		refused = refused || e.GetOutcome() == relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED
	}

	if !signedIn || !refused {
		t.Errorf("sign-in audit events = %v", events.GetEvents())
	}
}

func TestSAMLSingleSignOn(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()
	idp := ssotest.NewSAML(t)

	providers := relicv1connect.NewIdentityProviderServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	put, err := providers.PutIdentityProvider(ctx, &relicv1.PutIdentityProviderRequest{
		IdentityProvider: &relicv1.IdentityProvider{
			Name:            "corp-saml",
			Enabled:         true,
			AutoCreateUsers: true,
			Protocol: &relicv1.IdentityProvider_Saml{
				Saml: &relicv1.SamlSettings{
					MetadataXml: idp.Metadata(t),
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := put.GetIdentityProvider(); got.GetCallbackPath() != "/api/v1/sso/corp-saml/acs" ||
		!strings.Contains(got.GetSaml().GetCertificate(), "BEGIN CERTIFICATE") {
		t.Errorf("saved provider = %v; want the ACS path and Relic's certificate", got)
	}

	b := browser(t)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/sso/corp-saml/metadata", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := b.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	sp, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata = %d %v", resp.StatusCode, err)
	}

	idp.Trust(t, sp, true)
	idp.SignIn(saml.Session{
		NameID:   "frank",
		UserName: "frank",
	})

	start, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		srv.URL+"/api/v1/sso/corp-saml/start?return=/builds",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	page, err := b.Do(start)
	if err != nil {
		t.Fatal(err)
	}

	body, err := io.ReadAll(page.Body)
	_ = page.Body.Close()

	if err != nil || page.StatusCode != http.StatusOK {
		t.Fatalf("identity provider page = %d %s %v", page.StatusCode, body, err)
	}

	action, fields := ssotest.Form(t, string(body))

	post, err := http.NewRequestWithContext(ctx, http.MethodPost, action, strings.NewReader(fields.Encode()))
	if err != nil {
		t.Fatal(err)
	}

	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	done, err := b.Do(post)
	if err != nil {
		t.Fatal(err)
	}

	_ = done.Body.Close()

	if done.Request.URL.Path != "/builds" {
		t.Fatalf("the SAML sign-in ended at %s", done.Request.URL)
	}

	whoami := &relicv1.GetCurrentUserRequest{}
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("X-Relic-UI", "1")

	me, err := relicv1connect.NewSessionServiceClient(apiClient(b, srv.URL)).GetCurrentUser(ctx, whoami)
	if err != nil || me.GetPrincipal().GetUsername() != "frank" {
		t.Fatalf("after SAML sign-on, the session is %v, %v", me, err)
	}

	replay, err := http.NewRequestWithContext(ctx, http.MethodPost, action, strings.NewReader(fields.Encode()))
	if err != nil {
		t.Fatal(err)
	}

	replay.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	again, err := browser(t).Do(replay)
	if err != nil {
		t.Fatal(err)
	}

	_ = again.Body.Close()

	if again.Request.URL.Path != "/login" ||
		!strings.Contains(again.Request.URL.Query().Get("error"), "did not start") {
		t.Errorf("a replayed SAML response from another browser ended at %s", again.Request.URL)
	}
}
