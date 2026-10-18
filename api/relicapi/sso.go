package relicapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/access/sso"
	"relic/internal/platform/httpserver"
)

// SSOPath is the prefix of the single sign-on endpoints browsers visit:
// {provider}/start, {provider}/callback (OIDC), {provider}/acs and
// {provider}/metadata (SAML).
const SSOPath = "/api/v1/sso/"

const (
	ssoStateCookie = "relic_sso_state"
	ssoStateMaxAge = 10 * 60
)

type identityProviderServer struct {
	d Dependencies
}

func identityProviderMessage(p sso.Provider) *relicv1.IdentityProvider {
	m := &relicv1.IdentityProvider{
		Name:            p.Name,
		DisplayName:     p.DisplayName,
		Enabled:         p.Enabled,
		AutoCreateUsers: p.AutoCreateUsers,
		UsernameClaim:   p.UsernameClaim,
		GroupsClaim:     p.GroupsClaim,
	}

	switch {
	case p.OIDC != nil:
		m.Protocol = &relicv1.IdentityProvider_Oidc{
			Oidc: &relicv1.OidcSettings{
				Issuer:   p.OIDC.Issuer,
				ClientId: p.OIDC.ClientID,
				Scopes:   p.OIDC.Scopes,
			},
		}
		m.CallbackPath = SSOPath + p.Name + "/callback"
	case p.SAML != nil:
		m.Protocol = &relicv1.IdentityProvider_Saml{
			Saml: &relicv1.SamlSettings{
				MetadataUrl: p.SAML.MetadataURL,
				MetadataXml: p.SAML.MetadataXML,
				Certificate: p.SAML.Certificate,
			},
		}
		m.CallbackPath = SSOPath + p.Name + "/acs"
	}

	return m
}

func domainIdentityProvider(m *relicv1.IdentityProvider) sso.Provider {
	p := sso.Provider{
		Name:            m.GetName(),
		DisplayName:     m.GetDisplayName(),
		Enabled:         m.GetEnabled(),
		AutoCreateUsers: m.GetAutoCreateUsers(),
		UsernameClaim:   m.GetUsernameClaim(),
		GroupsClaim:     m.GetGroupsClaim(),
	}

	if o := m.GetOidc(); o != nil {
		p.OIDC = &sso.OIDCSettings{
			Issuer:       o.GetIssuer(),
			ClientID:     o.GetClientId(),
			ClientSecret: o.GetClientSecret(),
			Scopes:       o.GetScopes(),
		}
	}

	if s := m.GetSaml(); s != nil {
		p.SAML = &sso.SAMLSettings{
			MetadataURL: s.GetMetadataUrl(),
			MetadataXML: s.GetMetadataXml(),
		}
	}

	return p
}

func (s *identityProviderServer) ListIdentityProviders(
	ctx context.Context,
	_ *relicv1.ListIdentityProvidersRequest,
) (*relicv1.ListIdentityProvidersResponse, error) {
	if s.d.SSO == nil {
		return nil, unimplemented("single sign-on")
	}

	providers, err := s.d.SSO.Providers(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListIdentityProvidersResponse{
		IdentityProviders: make([]*relicv1.IdentityProvider, 0, len(providers)),
	}
	for _, p := range providers {
		out.IdentityProviders = append(out.IdentityProviders, identityProviderMessage(p))
	}

	return out, nil
}

func (s *identityProviderServer) GetIdentityProvider(
	ctx context.Context,
	req *relicv1.GetIdentityProviderRequest,
) (*relicv1.GetIdentityProviderResponse, error) {
	if s.d.SSO == nil {
		return nil, unimplemented("single sign-on")
	}

	p, err := s.d.SSO.Provider(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	return &relicv1.GetIdentityProviderResponse{
		IdentityProvider: identityProviderMessage(p),
	}, nil
}

func (s *identityProviderServer) PutIdentityProvider(
	ctx context.Context,
	req *relicv1.PutIdentityProviderRequest,
) (*relicv1.PutIdentityProviderResponse, error) {
	if s.d.SSO == nil {
		return nil, unimplemented("single sign-on")
	}

	p := domainIdentityProvider(req.GetIdentityProvider())
	if err := s.d.SSO.PutProvider(ctx, p); err != nil {
		return nil, err
	}

	saved, err := s.d.SSO.Provider(ctx, p.Name)
	if err != nil {
		return nil, err
	}

	return &relicv1.PutIdentityProviderResponse{
		IdentityProvider: identityProviderMessage(saved),
	}, nil
}

func (s *identityProviderServer) DeleteIdentityProvider(
	ctx context.Context,
	req *relicv1.DeleteIdentityProviderRequest,
) (*relicv1.DeleteIdentityProviderResponse, error) {
	if s.d.SSO == nil {
		return nil, unimplemented("single sign-on")
	}

	if err := s.d.SSO.DeleteProvider(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return &relicv1.DeleteIdentityProviderResponse{}, nil
}

func (s *sessionServer) ListSignInProviders(
	ctx context.Context,
	_ *relicv1.ListSignInProvidersRequest,
) (*relicv1.ListSignInProvidersResponse, error) {
	out := &relicv1.ListSignInProvidersResponse{}
	if s.d.SSO == nil {
		return out, nil
	}

	providers, err := s.d.SSO.Providers(ctx)
	if err != nil {
		return nil, err
	}

	for _, p := range providers {
		if !p.Enabled {
			continue
		}

		label := p.DisplayName
		if label == "" {
			label = p.Name
		}

		out.Providers = append(out.Providers, &relicv1.SignInProvider{
			Name:        p.Name,
			DisplayName: label,
			StartPath:   SSOPath + p.Name + "/start",
		})
	}

	return out, nil
}

func (d Dependencies) ssoStart(w http.ResponseWriter, r *http.Request) {
	if d.SSO == nil {
		http.NotFound(w, r)
		return
	}

	name := r.PathValue("provider")

	authURL, state, protocol, err := d.SSO.Start(r.Context(), name, ssoBase(r, name), r.URL.Query().Get("return"))
	if err != nil {
		d.ssoFailed(w, r, name, "", err)
		return
	}

	http.SetCookie(w, ssoCookie(r, state, ssoStateMaxAge, protocol))
	//nolint:gosec // G710: the sign-in endpoint of an identity provider an administrator configured
	http.Redirect(w, r, authURL, http.StatusFound)
}

func ssoBase(r *http.Request, provider string) string {
	return httpserver.Origin(r) + SSOPath + provider
}

// ssoCookie binds a sign-in to the browser that started it. A SAML
// identity provider posts its answer from its own site, which a SameSite=Lax
// cookie does not follow.
func ssoCookie(r *http.Request, value string, maxAge int, protocol sso.Protocol) *http.Cookie {
	c := sessionCookie(r, value, maxAge) //nolint:gosec // G124: Secure is off only for plain-HTTP development
	c.Name = ssoStateCookie
	c.Path = SSOPath

	switch {
	case protocol != sso.SAML:
	case c.Secure:
		c.SameSite = http.SameSiteNoneMode
	default:
		c.SameSite = http.SameSiteDefaultMode
	}

	return c
}

func (d Dependencies) ssoAssertion(w http.ResponseWriter, r *http.Request) {
	if d.SSO == nil {
		http.NotFound(w, r)
		return
	}

	name := r.PathValue("provider")

	if err := r.ParseForm(); err != nil {
		d.ssoFailed(w, r, name, "", &sso.RefusedError{
			Reason: "the SAML response does not parse",
		})

		return
	}

	state := r.PostForm.Get("RelayState")

	http.SetCookie(w, ssoCookie(r, "", -1, sso.SAML))

	if c, err := r.Cookie(ssoStateCookie); err != nil || c.Value == "" || c.Value != state {
		d.ssoFailed(w, r, name, "", &sso.RefusedError{
			Reason: "this browser did not start the sign-in; sign in again",
		})

		return
	}

	username, returnTo, err := d.SSO.FinishSAML(
		r.Context(),
		name,
		ssoBase(r, name),
		state,
		r.PostForm.Get("SAMLResponse"),
	)
	if err != nil {
		d.ssoFailed(w, r, name, username, err)
		return
	}

	d.ssoSignedIn(w, r, name, username, returnTo)
}

func (d Dependencies) ssoMetadata(w http.ResponseWriter, r *http.Request) {
	if d.SSO == nil {
		http.NotFound(w, r)
		return
	}

	name := r.PathValue("provider")

	metadata, err := d.SSO.SAMLMetadata(r.Context(), name, ssoBase(r, name))
	if errors.Is(err, sso.ErrNotFound) {
		http.NotFound(w, r)
		return
	}

	if err != nil {
		if d.Log != nil {
			d.Log.ErrorContext(r.Context(), "SAML metadata", "provider", name, "error", err)
		}

		http.Error(w, "the metadata is not available", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(metadata) //nolint:gosec // G705: XML Relic marshals, served as SAML metadata
}

func (d Dependencies) ssoCallback(w http.ResponseWriter, r *http.Request) {
	if d.SSO == nil {
		http.NotFound(w, r)
		return
	}

	name := r.PathValue("provider")
	q := r.URL.Query()

	http.SetCookie(w, ssoCookie(r, "", -1, sso.OIDC))

	if e := q.Get("error"); e != "" {
		reason := "the identity provider refused the sign-in (" + e + ")"
		if desc := q.Get("error_description"); desc != "" {
			reason += ": " + desc
		}

		d.ssoFailed(w, r, name, "", &sso.RefusedError{
			Reason: reason,
		})

		return
	}

	if c, err := r.Cookie(ssoStateCookie); err != nil || c.Value == "" || c.Value != q.Get("state") {
		d.ssoFailed(w, r, name, "", &sso.RefusedError{
			Reason: "this browser did not start the sign-in; sign in again",
		})

		return
	}

	username, returnTo, err := d.SSO.FinishOIDC(
		r.Context(),
		name,
		ssoBase(r, name)+"/callback",
		q.Get("state"),
		q.Get("code"),
	)
	if err != nil {
		d.ssoFailed(w, r, name, username, err)
		return
	}

	d.ssoSignedIn(w, r, name, username, returnTo)
}

// ssoSignedIn starts a UI session for a user a provider signed in.
func (d Dependencies) ssoSignedIn(w http.ResponseWriter, r *http.Request, provider, username, returnTo string) {
	cookie, _, err := d.uiSession(r, auth.Principal{
		Username: username,
	})
	if err != nil {
		d.ssoFailed(w, r, provider, username, err)
		return
	}

	d.recordSignIn(r, provider, username, nil)
	http.SetCookie(w, cookie)
	//nolint:gosec // G710: ReturnPath keeps the browser on this server
	http.Redirect(w, r, sso.ReturnPath(returnTo), http.StatusFound)
}

func (d Dependencies) ssoFailed(w http.ResponseWriter, r *http.Request, provider, username string, err error) {
	d.recordSignIn(r, provider, username, err)

	reason := "single sign-on failed; try again or ask an administrator"

	var refused *sso.RefusedError
	if errors.As(err, &refused) {
		reason = refused.Reason
	} else if d.Log != nil {
		d.Log.ErrorContext(r.Context(), "single sign-on failed", "provider", provider, "error", err)
	}

	http.Redirect(w, r, "/login?"+url.Values{
		"error": []string{
			reason,
		},
	}.Encode(), http.StatusFound)
}

func (d Dependencies) recordSignIn(r *http.Request, provider, username string, err error) {
	if d.Audit == nil {
		return
	}

	e := audit.Event{
		Actor:   username,
		Action:  "sso.sign-in",
		Target:  provider,
		Outcome: audit.Success,
	}
	if e.Actor == "" {
		e.Actor = auth.AnonymousUser
	}

	if err != nil {
		e.Outcome, e.Detail = audit.Denied, err.Error()
	}

	d.Audit.Record(r.Context(), e)
}
