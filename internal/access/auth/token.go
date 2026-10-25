package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/sealing"
	"relic/internal/platform/telemetry"
)

// ErrInvalidToken covers malformed, expired, revoked and forged tokens.
var ErrInvalidToken = errors.New("invalid token")

// ErrTokenNotFound is returned for a token ID that names no token the caller
// may manage.
var ErrTokenNotFound = errors.New("token not found")

// ErrUnknownUser is returned for a token asked for a user that does not
// exist.
var ErrUnknownUser = errors.New("user not found")

// ErrUnknownGroup is returned for a token scoped to a group that does not
// exist.
var ErrUnknownGroup = errors.New("group not found")

// claims are the JWT claims of Relic's tokens. Clients decode them and need
// numeric exp and iat.
type claims struct {
	Subject   string `json:"sub"`
	Scope     string `json:"scp"`
	Audience  string `json:"aud"`
	Issuer    string `json:"iss"`
	ExpiresAt int64  `json:"exp,omitempty"`
	IssuedAt  int64  `json:"iat"`
	ID        string `json:"jti"`
}

// Token is a verified token.
type Token struct {
	ID               string
	Username         string
	Scope            TokenScope
	WorkloadIdentity string
	Expires          time.Time
}

// IssuedToken is a token just issued, with its refresh token when it is
// refreshable.
type IssuedToken struct {
	ID           string
	AccessToken  string
	RefreshToken string
	// ExpiresIn is 0 for a token that never expires.
	ExpiresIn   time.Duration
	Scope       TokenScope
	Refreshable bool
}

// IssueParams describes a token to issue.
type IssueParams struct {
	Username         string
	Scope            TokenScope
	ExpiresIn        time.Duration
	Refreshable      bool
	Description      string
	Project          string
	WorkloadIdentity string

	kind string
}

// Tokens issues and verifies access tokens.
type Tokens struct {
	db     *database.DB
	box    *sealing.Box
	format TokenFormat
	now    func() time.Time

	mu     sync.RWMutex
	kid    string
	signer ed25519.PrivateKey
	keys   map[string]ed25519.PublicKey
}

// NewTokens loads the signing keys, creating the first one if none exist.
func NewTokens(ctx context.Context, db *database.DB, box *sealing.Box, format TokenFormat) (*Tokens, error) {
	t := &Tokens{
		db:     db,
		box:    box,
		format: format,
		now:    time.Now,
	}
	if err := t.load(ctx); err != nil {
		return nil, err
	}

	if t.signer != nil {
		return t, nil
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}

	kid := uuid.NewV7().String()

	sealed, err := box.Seal(priv.Seed(), signingKeyPlace(kid))
	if err != nil {
		return nil, err
	}

	if err := db.InsertSigningKey(ctx, database.InsertSigningKeyParams{
		Kid:        kid,
		PrivateKey: sealed,
	}); err != nil {
		return nil, fmt.Errorf("store signing key: %w", err)
	}

	return t, t.load(ctx)
}

func signingKeyPlace(kid string) string { return "signing_keys/" + kid }

func (t *Tokens) load(ctx context.Context) error {
	rows, err := t.db.GetSigningKeys(ctx)
	if err != nil {
		return fmt.Errorf("load signing keys: %w", err)
	}

	keys := make(map[string]ed25519.PublicKey, len(rows))

	var (
		kid    string
		signer ed25519.PrivateKey
	)

	for i, r := range rows {
		seed, err := t.box.Open(r.PrivateKey, signingKeyPlace(r.Kid))
		if err != nil {
			return fmt.Errorf("signing key %s: %w", r.Kid, err)
		}

		if len(seed) != ed25519.SeedSize {
			return fmt.Errorf("signing key %s: bad seed length %d", r.Kid, len(seed))
		}

		if t.box.Enabled() && !sealing.Sealed(r.PrivateKey) {
			sealed, err := t.box.Seal(seed, signingKeyPlace(r.Kid))
			if err != nil {
				return err
			}

			if err := t.db.UpdateSigningKey(
				ctx,
				database.UpdateSigningKeyParams{
					Kid:        r.Kid,
					PrivateKey: sealed,
				},
			); err != nil {
				return fmt.Errorf("seal signing key %s: %w", r.Kid, err)
			}
		}

		priv := ed25519.NewKeyFromSeed(seed)

		keys[r.Kid] = priv.Public().(ed25519.PublicKey)
		if i == 0 {
			kid, signer = r.Kid, priv
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.kid, t.signer, t.keys = kid, signer, keys

	return nil
}

// Issue creates a token for an existing user.
func (t *Tokens) Issue(ctx context.Context, p IssueParams) (IssuedToken, error) {
	_, err := t.db.GetUserByUsername(ctx, p.Username)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return IssuedToken{}, fmt.Errorf("%w: %s", ErrUnknownUser, p.Username)
	}

	if err != nil {
		return IssuedToken{}, fmt.Errorf("look up user: %w", err)
	}

	for _, group := range p.Scope.Groups {
		_, err := t.db.GetGroup(ctx, group)
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return IssuedToken{}, fmt.Errorf("%w: %s", ErrUnknownGroup, group)
		}

		if err != nil {
			return IssuedToken{}, fmt.Errorf("look up group: %w", err)
		}
	}

	out, err := t.issue(ctx, t.db, p)
	if err != nil {
		return IssuedToken{}, err
	}

	telemetry.TokenIssued(p.kind)

	return out, nil
}

func (t *Tokens) issue(ctx context.Context, q database.Querier, p IssueParams) (IssuedToken, error) {
	if !p.Scope.Valid() {
		return IssuedToken{}, fmt.Errorf("%w: %q", ErrUnknownScope, p.Scope.Kind)
	}

	now := t.now()
	id := uuid.NewV7()
	c := claims{
		Subject:  t.format.Subject(p.Username),
		Scope:    t.format.Scope(p.Scope),
		Audience: t.format.Audience(),
		Issuer:   t.format.Issuer(),
		IssuedAt: now.Unix(),
		ID:       id.String(),
	}

	var expires *time.Time

	if p.ExpiresIn > 0 {
		e := now.Add(p.ExpiresIn)
		c.ExpiresAt, expires = e.Unix(), &e
	}

	token, err := t.sign(c)
	if err != nil {
		return IssuedToken{}, err
	}

	out := IssuedToken{
		ID:          id.String(),
		AccessToken: token,
		ExpiresIn:   p.ExpiresIn.Truncate(time.Second),
		Scope:       p.Scope,
		Refreshable: p.Refreshable,
	}

	var refreshHash *string

	if p.Refreshable {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return IssuedToken{}, err
		}

		out.RefreshToken = hex.EncodeToString(b[:])
		h := hashToken(out.RefreshToken)
		refreshHash = &h
	}

	err = q.InsertAccessToken(ctx, database.InsertAccessTokenParams{
		ID:               id,
		Username:         p.Username,
		ScopeKind:        string(p.Scope.Kind),
		ScopeGroups:      nonNilGroups(p.Scope.Groups),
		RefreshTokenHash: refreshHash,
		Expires:          expires,
		Description:      p.Description,
		Project:          p.Project,
		WorkloadIdentity: p.WorkloadIdentity,
	})
	if err != nil {
		return IssuedToken{}, fmt.Errorf("store token: %w", err)
	}

	return out, nil
}

func nonNilGroups(g []string) []string {
	if g == nil {
		return []string{}
	}

	return g
}

// Refresh exchanges a refresh token for a new token pair and revokes the old
// one.
func (t *Tokens) Refresh(ctx context.Context, refreshToken string) (IssuedToken, error) {
	var out IssuedToken

	err := t.db.WithTx(ctx, func(q database.Querier) error {
		old, err := q.GetAccessTokenByRefreshHash(ctx, new(hashToken(refreshToken)))
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return ErrInvalidToken
		}

		if err != nil {
			return fmt.Errorf("find the refresh token: %w", err)
		}

		scope := TokenScope{
			Kind:   ScopeKind(old.ScopeKind),
			Groups: old.ScopeGroups,
		}
		if !scope.Valid() {
			return ErrInvalidToken
		}

		var lifetime time.Duration
		if old.Expires != nil {
			lifetime = old.Expires.Sub(old.Created).Round(time.Second)
		}

		if err := q.RevokeAccessToken(ctx, old.ID); err != nil {
			return err
		}

		out, err = t.issue(
			ctx,
			q,
			IssueParams{
				Username:    old.Username,
				Scope:       scope,
				ExpiresIn:   lifetime,
				Refreshable: true,
				Description: old.Description,
				Project:     old.Project,
			},
		)

		return err
	})
	if err != nil {
		return IssuedToken{}, err
	}

	telemetry.TokenIssued("refresh")

	return out, nil
}

// Revoke invalidates a token (UI sign-out).
func (t *Tokens) Revoke(ctx context.Context, token string) error {
	tok, err := t.Verify(ctx, token)
	if errors.Is(err, ErrInvalidToken) {
		return nil
	}

	if err != nil {
		return err
	}

	id, err := uuid.Parse(tok.ID)
	if err != nil {
		return nil //nolint:nilerr // Verify already checked the id
	}

	return t.db.RevokeAccessToken(ctx, id)
}

// TokenInfo is an issued token without its secret.
type TokenInfo struct {
	ID          string
	Username    string
	Scope       TokenScope
	Description string
	Project     string
	Created     time.Time
	Expires     *time.Time // nil for a token that never expires
	Revoked     bool
	Refreshable bool
}

// List returns the tokens issued to username ("" for everyone's), newest
// first; revoked ones only with includeRevoked.
func (t *Tokens) List(ctx context.Context, username string, includeRevoked bool) ([]TokenInfo, error) {
	var user *string
	if username != "" {
		user = &username
	}

	rows, err := t.db.ListAccessTokens(
		ctx,
		database.ListAccessTokensParams{
			Username:       user,
			IncludeRevoked: includeRevoked,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}

	out := make([]TokenInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, TokenInfo{
			ID:       r.ID.String(),
			Username: r.Username,
			Scope: TokenScope{
				Kind:   ScopeKind(r.ScopeKind),
				Groups: r.ScopeGroups,
			},
			Description: r.Description,
			Project:     r.Project,
			Created:     r.Created,
			Expires:     r.Expires,
			Revoked:     r.Revoked,
			Refreshable: r.RefreshTokenHash != nil,
		})
	}

	return out, nil
}

// RevokeID revokes the token with id.
func (t *Tokens) RevokeID(ctx context.Context, id, owner string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return ErrTokenNotFound
	}

	row, err := t.db.GetAccessToken(ctx, uid)
	if err != nil {
		if errors.Is(database.NotFound(err), database.ErrNotFound) {
			return ErrTokenNotFound
		}

		return fmt.Errorf("get token: %w", err)
	}

	if owner != "" && row.Username != owner {
		return ErrTokenNotFound
	}

	if err := t.db.RevokeAccessToken(ctx, uid); err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}

	return nil
}

// Verify checks a token's signature, expiry and revocation.
func (t *Tokens) Verify(ctx context.Context, token string) (Token, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Token{}, ErrInvalidToken
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil || header.Alg != "EdDSA" {
		return Token{}, ErrInvalidToken
	}

	t.mu.RLock()
	key, ok := t.keys[header.Kid]
	t.mu.RUnlock()

	if !ok {
		return Token{}, ErrInvalidToken
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return Token{}, ErrInvalidToken
	}

	var claims claims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return Token{}, ErrInvalidToken
	}

	if claims.ExpiresAt != 0 && t.now().Unix() >= claims.ExpiresAt {
		return Token{}, ErrInvalidToken
	}

	id, err := uuid.Parse(claims.ID)
	if err != nil {
		return Token{}, ErrInvalidToken
	}

	row, err := t.db.GetAccessToken(ctx, id)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Token{}, ErrInvalidToken
	}

	if err != nil {
		return Token{}, fmt.Errorf("look up token: %w", err)
	}

	if row.Revoked {
		return Token{}, ErrInvalidToken
	}

	username := t.format.Username(claims.Subject)
	if username == "" {
		return Token{}, ErrInvalidToken
	}

	verified := Token{
		ID:               claims.ID,
		Username:         username,
		Scope:            t.format.ParseScope(claims.Scope),
		WorkloadIdentity: row.WorkloadIdentity,
	}
	if row.Expires != nil {
		verified.Expires = *row.Expires
	}

	return verified, nil
}

func (t *Tokens) sign(c claims) (string, error) {
	t.mu.RLock()
	kid, signer := t.kid, t.signer
	t.mu.RUnlock()

	header, err := json.Marshal(map[string]string{
		"alg": "EdDSA",
		"typ": "JWT",
		"kid": kid,
	})
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}

	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(
		StdSafeJSON(payload),
	)

	return signing + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(signer, []byte(signing))), nil
}

// StdSafeJSON makes a JSON document's base64url encoding free of '-' and
// '_'.
func StdSafeJSON(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b))

	for _, r := range string(b) {
		switch {
		case r == '>' || r == '?' || r == '~' || r == 0x7f:
			fmt.Fprintf(&out, `\u%04x`, r)
		case r >= utf8.RuneSelf:
			for _, u := range utf16.Encode([]rune{
				r,
			}) {
				fmt.Fprintf(&out, `\u%04x`, u)
			}
		default:
			out.WriteRune(r)
		}
	}

	return out.Bytes()
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}

	return json.Unmarshal(b, v)
}

func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
