package sso

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"relic/internal/platform/telemetry"
)

var defaultScopes = []string{
	oidc.ScopeOpenID,
	"profile",
	"email",
}

type oidcProvider struct {
	issuer   string
	provider *oidc.Provider
}

func (s *Service) discover(ctx context.Context, p Provider) (*oidc.Provider, error) {
	s.mu.Lock()
	cached, ok := s.discovery[p.Name]
	s.mu.Unlock()

	if ok && cached.issuer == p.OIDC.Issuer {
		return cached.provider, nil
	}

	// The provider keeps its context to fetch signing keys later.
	discovered, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), s.client), p.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover identity provider %s at %s: %w", p.Name, p.OIDC.Issuer, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.discovery == nil {
		s.discovery = map[string]*oidcProvider{}
	}

	s.discovery[p.Name] = &oidcProvider{
		issuer:   p.OIDC.Issuer,
		provider: discovered,
	}

	return discovered, nil
}

func oauthConfig(p Provider, discovered *oidc.Provider, callback string) *oauth2.Config {
	scopes := p.OIDC.Scopes
	if len(scopes) == 0 {
		scopes = defaultScopes
	}

	if !slices.Contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{
			oidc.ScopeOpenID,
		}, scopes...)
	}

	return &oauth2.Config{
		ClientID:     p.OIDC.ClientID,
		ClientSecret: p.OIDC.ClientSecret,
		Endpoint:     discovered.Endpoint(),
		RedirectURL:  callback,
		Scopes:       scopes,
	}
}

// StartOIDC begins a sign-in through the OIDC provider name: it returns the
// provider's URL to send the browser to, and the state the callback must
// carry. callback is Relic's callback URL; returnTo is where to go after.
func (s *Service) StartOIDC(ctx context.Context, name, callback, returnTo string) (string, string, error) {
	p, err := s.enabled(ctx, name, OIDC)
	if err != nil {
		return "", "", err
	}

	discovered, err := s.discover(ctx, p)
	if err != nil {
		return "", "", err
	}

	verifier, nonce := oauth2.GenerateVerifier(), randomString()

	state, err := s.saveRequest(ctx, p.Name, verifier, nonce, returnTo)
	if err != nil {
		return "", "", err
	}

	return oauthConfig(p, discovered, callback).AuthCodeURL(
		state,
		oauth2.S256ChallengeOption(verifier),
		oidc.Nonce(nonce),
	), state, nil
}

// FinishOIDC completes a sign-in: it exchanges code for the user's ID
// token, checks it, and provisions the user. It returns the username and
// where to send the browser.
func (s *Service) FinishOIDC(ctx context.Context, name, callback, state, code string) (string, string, error) {
	username, returnTo, err := s.finishOIDC(ctx, name, callback, state, code)
	telemetry.AuthAttempt("oidc", signInResult(err))

	return username, returnTo, err
}

func (s *Service) finishOIDC(ctx context.Context, name, callback, state, code string) (string, string, error) {
	req, err := s.takeRequest(ctx, name, state)
	if err != nil {
		return "", "", err
	}

	p, err := s.enabled(ctx, name, OIDC)
	if err != nil {
		return "", "", err
	}

	discovered, err := s.discover(ctx, p)
	if err != nil {
		return "", "", err
	}

	client := oidc.ClientContext(ctx, s.client)

	tok, err := oauthConfig(p, discovered, callback).Exchange(client, code, oauth2.VerifierOption(req.Verifier))
	if err != nil {
		s.log.WarnContext(ctx, "identity provider refused the code", "provider", name, "error", err)
		return "", "", refused("the identity provider did not accept the sign-in")
	}

	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return "", "", refused("the identity provider sent no ID token")
	}

	idToken, err := discovered.VerifierContext(client, &oidc.Config{
		ClientID: p.OIDC.ClientID,
	}).Verify(client, raw)
	if err != nil {
		s.log.WarnContext(ctx, "ID token refused", "provider", name, "error", err)
		return "", "", refused("the identity provider's ID token is not valid")
	}

	if idToken.Nonce != req.Nonce {
		return "", "", refused("the ID token does not belong to this sign-in")
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return "", "", refused("the ID token's claims do not decode")
	}

	id, err := claimedIdentity(p, claims)
	if err != nil {
		return "", "", err
	}

	username, err := s.provision(ctx, p, id)

	return username, req.Redirect, err
}

func claimedIdentity(p Provider, claims map[string]any) (identity, error) {
	claim := p.UsernameClaim
	if claim == "" {
		claim = "preferred_username"
	}

	username, _ := claims[claim].(string)
	if username == "" {
		return identity{}, refused("the ID token has no " + claim + " claim")
	}

	email, _ := claims["email"].(string)
	id := identity{
		username: strings.TrimSpace(username),
		email:    email,
	}

	if p.GroupsClaim == "" {
		return id, nil
	}

	switch groups := claims[p.GroupsClaim].(type) {
	case []any:
		for _, g := range groups {
			if name, ok := g.(string); ok {
				id.groups = append(id.groups, name)
			}
		}
	case string:
		for _, name := range strings.Split(groups, ",") {
			if name = strings.TrimSpace(name); name != "" {
				id.groups = append(id.groups, name)
			}
		}
	}

	return id, nil
}

func signInResult(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrRefused):
		return "failure"
	default:
		return "error"
	}
}
