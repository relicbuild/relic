// Package sso signs users in through identity providers (OpenID Connect and
// SAML) and provisions the accounts and group memberships they manage.
package sso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/access/security"
	"relic/internal/platform/database"
	"relic/internal/platform/sealing"
)

// Protocol is how Relic talks to an identity provider.
type Protocol string

// Protocols.
const (
	OIDC Protocol = "oidc"
	SAML Protocol = "saml"
)

// Provider is an identity provider users sign in with.
type Provider struct {
	Name        string `json:"-"`
	DisplayName string `json:"displayName,omitempty"`
	Enabled     bool   `json:"enabled"`
	// AutoCreateUsers creates a Relic user on a first sign-in; without it,
	// only users that exist (created by SCIM or an administrator) sign in.
	AutoCreateUsers bool `json:"autoCreateUsers"`
	// UsernameClaim is the claim (OIDC) or attribute (SAML) holding the
	// username; preferred_username, or the SAML NameID, when empty.
	UsernameClaim string `json:"usernameClaim,omitempty"`
	// GroupsClaim is the claim or attribute listing the user's groups; when
	// set, each sign-in replaces the memberships the provider manages.
	GroupsClaim string        `json:"groupsClaim,omitempty"`
	OIDC        *OIDCSettings `json:"oidc,omitempty"`
	SAML        *SAMLSettings `json:"saml,omitempty"`
}

// OIDCSettings are an OpenID Connect client's settings.
type OIDCSettings struct {
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// SAMLSettings are a SAML service provider's settings.
type SAMLSettings struct {
	// MetadataURL or MetadataXML describes the identity provider.
	MetadataURL string `json:"metadataUrl,omitempty"`
	MetadataXML string `json:"metadataXml,omitempty"`
	// Key and Certificate are Relic's PEM key pair for this provider, which
	// Relic generates.
	Key         string `json:"key,omitempty"`
	Certificate string `json:"certificate,omitempty"`
}

// Protocol is the provider's protocol.
func (p Provider) Protocol() Protocol {
	if p.SAML != nil {
		return SAML
	}

	return OIDC
}

// Errors.
var (
	ErrNotFound = errors.New("identity provider not found")
	ErrInvalid  = errors.New("invalid identity provider")
	ErrRefused  = errors.New("sign-in refused")
)

// RefusedError explains a refused sign-in to the user.
type RefusedError struct {
	Reason string
}

func (e *RefusedError) Error() string { return "sign-in refused: " + e.Reason }

// Unwrap makes a RefusedError match ErrRefused.
func (e *RefusedError) Unwrap() error { return ErrRefused }

func refused(reason string) error {
	return &RefusedError{
		Reason: reason,
	}
}

var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

const requestTTL = 10 * time.Minute

// Service manages identity providers and signs users in through them.
type Service struct {
	db     *database.DB
	box    *sealing.Box
	client *http.Client
	log    *slog.Logger
	now    func() time.Time

	mu        sync.Mutex
	discovery map[string]*oidcProvider
}

const providerTimeout = 30 * time.Second

// New returns a Service; box seals client secrets and keys.
func New(db *database.DB, box *sealing.Box, log *slog.Logger) *Service {
	return &Service{
		db:  db,
		box: box,
		client: &http.Client{
			Timeout: providerTimeout,
		},
		log: log,
		now: time.Now,
	}
}

// SetHTTPClient sets the client used to reach identity providers.
func (s *Service) SetHTTPClient(c *http.Client) { s.client = c }

func secretPlace(name, field string) string { return "identity_providers/" + name + "/" + field }

// Validate reports what is wrong with p.
func (p Provider) Validate() error {
	if !providerName.MatchString(p.Name) {
		return fmt.Errorf("%w: name %q: lower-case letters, digits, '.', '_' and '-' only", ErrInvalid, p.Name)
	}

	switch {
	case (p.OIDC == nil) == (p.SAML == nil):
		return fmt.Errorf("%w: %s needs exactly one of oidc and saml", ErrInvalid, p.Name)
	case p.OIDC != nil:
		if _, err := auth.ParseProviderURL(p.OIDC.Issuer); err != nil {
			return fmt.Errorf("%w: issuer %w", ErrInvalid, err)
		}

		if p.OIDC.ClientID == "" {
			return fmt.Errorf("%w: %s needs a client ID", ErrInvalid, p.Name)
		}
	case p.SAML.MetadataURL == "" && p.SAML.MetadataXML == "":
		return fmt.Errorf("%w: %s needs the identity provider's metadata URL or XML", ErrInvalid, p.Name)
	case p.SAML.MetadataURL != "":
		if _, err := auth.ParseProviderURL(p.SAML.MetadataURL); err != nil {
			return fmt.Errorf("%w: metadata URL %w", ErrInvalid, err)
		}
	}

	return nil
}

// Providers returns every identity provider, with secrets and keys.
func (s *Service) Providers(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.ListIdentityProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list identity providers: %w", err)
	}

	out := make([]Provider, 0, len(rows))
	for _, r := range rows {
		p, err := s.decode(r.Name, r.Config)
		if err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	return out, nil
}

// Provider returns one identity provider, with its secrets and keys.
func (s *Service) Provider(ctx context.Context, name string) (Provider, error) {
	row, err := s.db.GetIdentityProvider(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Provider{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	if err != nil {
		return Provider{}, fmt.Errorf("get identity provider %s: %w", name, err)
	}

	return s.decode(row.Name, row.Config)
}

func (s *Service) decode(name string, raw []byte) (Provider, error) {
	var p Provider
	if err := json.Unmarshal(raw, &p); err != nil {
		return Provider{}, fmt.Errorf("decode identity provider %s: %w", name, err)
	}

	p.Name = name

	for field, value := range p.secrets() {
		if !sealing.SealedString(*value) {
			continue
		}

		plain, err := s.box.OpenString(*value, secretPlace(name, field))
		if err != nil {
			return Provider{}, fmt.Errorf("open the %s of identity provider %s: %w", field, name, err)
		}

		*value = plain
	}

	return p, nil
}

func (p *Provider) secrets() map[string]*string {
	out := map[string]*string{}
	if p.OIDC != nil {
		out["client_secret"] = &p.OIDC.ClientSecret
	}

	if p.SAML != nil {
		out["key"] = &p.SAML.Key
	}

	return out
}

// PutProvider creates or replaces an identity provider. An empty client
// secret keeps the stored one; a SAML provider keeps its key pair, or gets a
// new one.
func (s *Service) PutProvider(ctx context.Context, p Provider) error {
	if err := p.Validate(); err != nil {
		return err
	}

	return s.db.WithLock(ctx, "identity-provider:"+p.Name, func() error {
		return s.putProvider(ctx, p)
	})
}

func (s *Service) putProvider(ctx context.Context, p Provider) error {
	old, err := s.Provider(ctx, p.Name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	if p.OIDC != nil && p.OIDC.ClientSecret == "" && old.OIDC != nil {
		p.OIDC.ClientSecret = old.OIDC.ClientSecret
	}

	if p.SAML != nil {
		if old.SAML != nil && old.SAML.Key != "" {
			p.SAML.Key, p.SAML.Certificate = old.SAML.Key, old.SAML.Certificate
		} else if p.SAML.Key, p.SAML.Certificate, err = newKeyPair(p.Name, s.now()); err != nil {
			return err
		}
	}

	if s.box.Enabled() {
		for field, value := range p.secrets() {
			if *value == "" {
				continue
			}

			if *value, err = s.box.SealString(*value, secretPlace(p.Name, field)); err != nil {
				return fmt.Errorf("seal the %s of identity provider %s: %w", field, p.Name, err)
			}
		}
	}

	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}

	if err := s.db.UpsertIdentityProvider(ctx, database.UpsertIdentityProviderParams{
		Name:   p.Name,
		Config: raw,
		Now:    s.now().UTC(),
	}); err != nil {
		return fmt.Errorf("store identity provider %s: %w", p.Name, err)
	}

	s.forget(p.Name)

	return nil
}

// DeleteProvider removes an identity provider. Its users stay, and keep the
// memberships it gave them until an administrator changes them.
func (s *Service) DeleteProvider(ctx context.Context, name string) error {
	n, err := s.db.DeleteIdentityProvider(ctx, name)
	if err != nil {
		return fmt.Errorf("delete identity provider %s: %w", name, err)
	}

	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	if err := s.db.ReleaseSsoUsers(ctx, name); err != nil {
		return fmt.Errorf("release the accounts of identity provider %s: %w", name, err)
	}

	s.forget(name)

	return nil
}

// SealStored seals the client secrets and keys stored before an encryption
// key was set.
func (s *Service) SealStored(ctx context.Context) error {
	if !s.box.Enabled() {
		return nil
	}

	rows, err := s.db.ListIdentityProviders(ctx)
	if err != nil {
		return fmt.Errorf("list identity providers: %w", err)
	}

	var errs []error

	for _, r := range rows {
		var stored Provider
		if err := json.Unmarshal(r.Config, &stored); err != nil {
			errs = append(errs, fmt.Errorf("decode identity provider %s: %w", r.Name, err))
			continue
		}

		plain := false
		for _, value := range stored.secrets() {
			plain = plain || (*value != "" && !sealing.SealedString(*value))
		}

		if !plain {
			continue
		}

		p, err := s.decode(r.Name, r.Config)
		if err == nil {
			err = s.PutProvider(ctx, p)
		}

		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func (s *Service) enabled(ctx context.Context, name string, protocol Protocol) (Provider, error) {
	p, err := s.Provider(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return Provider{}, refused("there is no identity provider " + name)
	}

	if err != nil {
		return Provider{}, err
	}

	if !p.Enabled || p.Protocol() != protocol {
		return Provider{}, refused("identity provider " + name + " is turned off")
	}

	return p, nil
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)

	return base64.RawURLEncoding.EncodeToString(b)
}

// ReturnPath is where to send the browser after a sign-in: a path on this
// server, or /.
func ReturnPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsFunc(p, browserIgnoredOrSlash) {
		return "/"
	}

	return p
}

func browserIgnoredOrSlash(r rune) bool {
	return r < 0x20 || r == 0x7f || r == '\\'
}

func (s *Service) saveRequest(ctx context.Context, provider, verifier, nonce, returnTo string) (string, error) {
	if err := s.db.DeleteExpiredSsoRequests(ctx, s.now().Add(-requestTTL).UTC()); err != nil {
		return "", fmt.Errorf("expire sign-in requests: %w", err)
	}

	state := randomString()
	if err := s.db.InsertSsoRequest(ctx, database.InsertSsoRequestParams{
		State:    state,
		Provider: provider,
		Verifier: verifier,
		Nonce:    nonce,
		Redirect: ReturnPath(returnTo),
		Created:  s.now().UTC(),
	}); err != nil {
		return "", fmt.Errorf("store sign-in request: %w", err)
	}

	return state, nil
}

func (s *Service) takeRequest(ctx context.Context, provider, state string) (database.SsoRequest, error) {
	req, err := s.db.TakeSsoRequest(ctx, state)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return database.SsoRequest{}, refused("the sign-in request is unknown or was already used; sign in again")
	}

	if err != nil {
		return database.SsoRequest{}, fmt.Errorf("load sign-in request: %w", err)
	}

	if req.Provider != provider || s.now().Sub(req.Created) > requestTTL {
		return database.SsoRequest{}, refused("the sign-in request has expired; sign in again")
	}

	return req, nil
}

// identity is who an identity provider says signed in.
type identity struct {
	username string
	email    string
	groups   []string
}

// provision makes the Relic account of id ready to sign in: it creates it
// when p may, and replaces the memberships p manages.
func (s *Service) provision(ctx context.Context, p Provider, id identity) (string, error) {
	if !security.ValidUsername(id.username) {
		return "", refused(
			fmt.Sprintf("the identity provider's username %q is not a valid Relic username", id.username),
		)
	}

	if id.email != "" && !security.ValidEmail(id.email) {
		s.log.WarnContext(ctx, "identity provider's email ignored: not a single address", "provider", p.Name,
			"user", id.username)

		id.email = ""
	}

	user, err := s.db.GetUserByUsername(ctx, id.username)
	existed := err == nil

	switch {
	case errors.Is(database.NotFound(err), database.ErrNotFound):
		if !p.AutoCreateUsers {
			return "", refused("there is no Relic account for " + id.username)
		}

		err := createUser(ctx, s.db, id, p.Name, false)
		if errors.Is(err, security.ErrExists) {
			return "", refused(err.Error())
		}

		if err != nil {
			return "", err
		}
	case err != nil:
		return "", fmt.Errorf("look up user %s: %w", id.username, err)
	case user.Realm != auth.RealmSSO:
		return "", refused("a Relic account that does not sign in with single sign-on is named " + id.username)
	case user.Disabled:
		return "", refused("the Relic account " + id.username + " is disabled")
	}

	if err := s.claimAccount(ctx, p.Name, id.username); err != nil {
		return "", err
	}

	if existed && id.email != "" && id.email != user.Email {
		if err := s.db.SetUserEmail(ctx, database.SetUserEmailParams{
			Username: id.username,
			Email:    id.email,
		}); err != nil {
			return "", fmt.Errorf("update email of %s: %w", id.username, err)
		}
	}

	if p.GroupsClaim == "" {
		return id.username, nil
	}

	names := make([]string, 0, len(id.groups))
	for _, g := range id.groups {
		names = append(names, strings.ToLower(g))
	}

	if err := s.db.WithTx(ctx, func(q database.Querier) error {
		return q.ReplaceSsoMemberships(ctx, database.ReplaceSsoMembershipsParams{
			Username:   id.username,
			GroupNames: names,
		})
	}); err != nil {
		return "", fmt.Errorf("sync groups of %s: %w", id.username, err)
	}

	return id.username, nil
}

func (s *Service) claimAccount(ctx context.Context, provider, username string) error {
	if err := s.db.ClaimSsoUser(ctx, database.ClaimSsoUserParams{
		IdentityProvider: provider,
		Username:         username,
	}); err != nil {
		return fmt.Errorf("claim user %s: %w", username, err)
	}

	user, err := s.db.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("look up user %s: %w", username, err)
	}

	if user.IdentityProvider != provider {
		return refused("the Relic account " + username + " signs in with another identity provider")
	}

	return nil
}

// createUser creates a single sign-on user in the groups new users join; security.ErrExists when another user's
// name differs from it only by case.
func createUser(ctx context.Context, db *database.DB, id identity, provider string, disabled bool) error {
	return security.WithNewUsername(ctx, db, id.username, func() error {
		return insertUser(ctx, db, id, provider, disabled)
	})
}

func insertUser(ctx context.Context, db *database.DB, id identity, provider string, disabled bool) error {
	return db.WithTx(ctx, func(q database.Querier) error {
		created, err := q.CreateSsoUser(ctx, database.CreateSsoUserParams{
			ID:               uuid.NewV7(),
			Username:         id.username,
			Email:            id.email,
			IdentityProvider: provider,
		})
		if err != nil {
			return fmt.Errorf("create user %s: %w", id.username, err)
		}

		if created == 0 {
			existing, err := q.GetUserByUsername(ctx, id.username)
			if err != nil {
				return fmt.Errorf("look up user %s: %w", id.username, err)
			}

			if existing.Realm != auth.RealmSSO {
				return refused("a Relic account that does not sign in with single sign-on is named " + id.username)
			}

			return nil
		}

		if disabled {
			if err := q.SetUserDisabled(ctx, database.SetUserDisabledParams{
				Username: id.username,
				Disabled: true,
			}); err != nil {
				return fmt.Errorf("disable user %s: %w", id.username, err)
			}
		}

		defaults, err := q.DefaultGroups(ctx)
		if err != nil {
			return err
		}

		for _, g := range defaults {
			if err := q.AddGroupMembers(ctx, database.AddGroupMembersParams{
				GroupName: g,
				Usernames: []string{
					id.username,
				},
			}); err != nil {
				return fmt.Errorf("add %s to %s: %w", id.username, g, err)
			}
		}

		return nil
	})
}

func (s *Service) forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.discovery, name)
}
