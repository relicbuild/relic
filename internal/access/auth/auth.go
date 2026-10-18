package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/httperror"
	"relic/internal/platform/sealing"
	"relic/internal/platform/telemetry"
)

// Access is the minimum access level a route requires.
type Access int

const (
	// Public routes need no credentials.
	Public Access = iota + 1
	// Authenticated routes need any valid user.
	Authenticated
	// Admin routes need an administrator.
	Admin
)

func (a Access) String() string {
	switch a {
	case Public:
		return "public"
	case Authenticated:
		return "authenticated"
	case Admin:
		return "admin"
	}

	return fmt.Sprintf("Access(%d)", int(a))
}

// Principal is the caller of a request.
type Principal struct {
	Username string
	Admin    bool
	// Viewer may read what administrators read, without changing it.
	Viewer bool
	Groups []string
	// Scoped is set when a token narrowed the principal to explicit groups:
	// it acts with them alone, without the user's own grants, and tokens it
	// mints stay within them (CeilScope).
	Scoped bool
	// WorkloadIdentity names the workload identity whose token signed the
	// principal in; tokens it mints expire with that token (LimitIssue).
	WorkloadIdentity     string
	WorkloadTokenExpires time.Time
}

// MaxUserTokenLifetime bounds tokens non-administrators mint for
// themselves.
const MaxUserTokenLifetime = 365 * 24 * time.Hour

// Subject returns the principal as an authorization subject; a scoped
// principal is its groups alone.
func (p Principal) Subject() Subject {
	s := Subject{
		User:   p.Username,
		Groups: p.Groups,
		Admin:  p.Admin,
	}
	if p.Scoped {
		s.User = ""
	}

	return s
}

// Anonymous reports whether the request carried no credentials.
func (p Principal) Anonymous() bool { return p.Username == "" }

// AnonymousUser is the name recorded for unauthenticated actions.
const AnonymousUser = "anonymous"

// Name returns the username, or AnonymousUser.
func (p Principal) Name() string {
	if p.Anonymous() {
		return AnonymousUser
	}

	return p.Username
}

type (
	principalKey struct{}
	slotKey      struct{}
)

// NewSlot returns a context in which Middleware will also record the
// authenticated principal into the returned pointer.
func NewSlot(ctx context.Context) (context.Context, *Principal) {
	p := new(Principal)
	return context.WithValue(ctx, slotKey{}, p), p
}

// FromContext returns the request's principal (anonymous if none).
func FromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// ErrBadCredentials is returned for unknown users and wrong passwords alike.
var ErrBadCredentials = errors.New("bad credentials")

// UserStore is the user lookup the authenticator needs.
type UserStore interface {
	GetUserByUsername(ctx context.Context, username string) (database.User, error)
}

const (
	verifiedTTL     = 5 * time.Minute
	verifiedMaxSize = 10_000
)

// Privileges are the rights a user's groups grant beyond their permissions.
type Privileges struct {
	Admin  bool
	Viewer bool
}

// GroupResolver returns a user's groups and the privileges they grant.
type GroupResolver interface {
	Groups(ctx context.Context, username string) ([]string, Privileges, error)
}

// TokenVerifier validates bearer tokens.
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (Token, error)
}

// ExternalAuthenticator checks a password against a directory (LDAP) and,
// on success, provisions the user and syncs their groups in Relic's store.
type ExternalAuthenticator interface {
	Authenticate(ctx context.Context, username, password string) error
}

// RealmLDAP marks users whose credentials a directory checks.
const RealmLDAP = "ldap"

// RealmSSO marks users who sign in through an identity provider.
const RealmSSO = "sso"

// Authenticator resolves request credentials to a Principal.
type Authenticator struct {
	users     UserStore
	groups    GroupResolver
	tokens    TokenVerifier
	external  ExternalAuthenticator
	passwords *sealing.Box
	log       *slog.Logger
	now       func() time.Time

	mu       sync.Mutex
	verified map[[32]byte]time.Time
	backoff  *Backoff

	apiKeys        []apiKeyHeader
	expiredBearers map[string]bool
	leftBearers    []leftBearer
}

type leftBearer struct {
	prefix, pathPart string
}

type apiKeyHeader struct {
	header, prefix string
}

// AcceptAPIKey makes requests under prefix without an Authorization header
// authenticate with header, whose value is "<user>:<password or token>".
func (a *Authenticator) AcceptAPIKey(header, prefix string) {
	a.apiKeys = append(a.apiKeys, apiKeyHeader{
		header: header,
		prefix: prefix,
	})
}

// LeaveBearer makes requests to a path containing pathPart whose bearer
// token starts with prefix anonymous: the handlers they reach verify those
// tokens themselves. Elsewhere such a token is verified as any other.
func (a *Authenticator) LeaveBearer(prefix, pathPart string) {
	a.leftBearers = append(a.leftBearers, leftBearer{
		prefix:   prefix,
		pathPart: pathPart,
	})
}

// AllowExpiredBearer lets POST requests to path through with a bearer token
// that no longer verifies.
func (a *Authenticator) AllowExpiredBearer(path string) {
	if a.expiredBearers == nil {
		a.expiredBearers = map[string]bool{}
	}

	a.expiredBearers[path] = true
}

// SetFailedSignInsBeforeBackoff sets how many failed password attempts a
// client address gets before it has to wait (Backoff).
func (a *Authenticator) SetFailedSignInsBeforeBackoff(n int) { a.backoff.SetFree(n) }

// NewAuthenticator returns an Authenticator backed by users and tokens.
func NewAuthenticator(users UserStore, groups GroupResolver, tokens TokenVerifier, log *slog.Logger) *Authenticator {
	return &Authenticator{
		users:    users,
		groups:   groups,
		tokens:   tokens,
		log:      log,
		now:      time.Now,
		verified: map[[32]byte]time.Time{},
		backoff:  NewBackoff(),
	}
}

// SetExternal makes the authenticator check unknown users and LDAP-realm
// users against a directory.
func (a *Authenticator) SetExternal(e ExternalAuthenticator) { a.external = e }

// SetPasswordSealing makes password authentication accept passwords that
// SealPassword sealed with box in place of the passwords themselves.
func (a *Authenticator) SetPasswordSealing(box *sealing.Box) { a.passwords = box }

// SealPassword seals username's password with box, for a client to keep and
// send in place of the password; without an encryption key it returns the
// password unchanged.
func SealPassword(box *sealing.Box, username, password string) (string, error) {
	sealed, err := box.SealString(password, sealedPasswordPlace(username))
	if err != nil {
		return "", fmt.Errorf("seal password: %w", err)
	}

	return sealed, nil
}

func sealedPasswordPlace(username string) string {
	return "basic_passwords/" + strings.ToLower(username)
}

func (a *Authenticator) principal(ctx context.Context, user database.User) (Principal, error) {
	if user.Disabled {
		return Principal{}, ErrBadCredentials
	}

	groups, granted, err := a.groups.Groups(ctx, user.Username)
	if err != nil {
		return Principal{}, err
	}

	return Principal{
		Username: user.Username,
		Admin:    user.Admin || granted.Admin,
		Viewer:   user.Viewer || granted.Viewer,
		Groups:   groups,
	}, nil
}

// Authenticate returns the principal for r.
func (a *Authenticator) Authenticate(r *http.Request) (Principal, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		for _, k := range a.apiKeys {
			if key := r.Header.Get(k.header); key != "" && strings.HasPrefix(r.URL.Path, k.prefix) {
				user, secret, ok := strings.Cut(key, ":")
				if !ok {
					return Principal{}, ErrBadCredentials
				}

				return a.Password(r, user, secret)
			}
		}

		return a.cookie(r)
	}

	if token, ok := BearerToken(header); ok {
		for _, left := range a.leftBearers {
			if strings.HasPrefix(token, left.prefix) && strings.Contains(r.URL.Path, left.pathPart) {
				return Principal{}, nil
			}
		}

		p, err := a.bearer(r.Context(), token)
		telemetry.AuthAttempt("bearer", attemptResult(err))

		return p, err
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		telemetry.AuthAttempt("basic", "failure")
		return Principal{}, ErrBadCredentials
	}

	return a.Password(r, username, password)
}

// BearerToken returns the token of an Authorization header that uses the
// Bearer scheme, whose name ignores case.
func BearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	return strings.TrimSpace(token), true
}

func attemptResult(err error) string {
	var throttled *ThrottledError
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrBadCredentials):
		return "failure"
	case errors.As(err, &throttled):
		return "throttled"
	default:
		return "error"
	}
}

// SessionCookie holds the UI session token.
const SessionCookie = "relic_session"

// UIHeader must accompany cookie-authenticated writes.
const UIHeader = "X-Relic-UI"

func browserAPI(path string) bool {
	return httperror.RelicAPI(path)
}

func (a *Authenticator) cookie(r *http.Request) (Principal, error) {
	if !browserAPI(r.URL.Path) {
		return Principal{}, nil
	}

	c, err := r.Cookie(SessionCookie)
	if errors.Is(err, http.ErrNoCookie) || (err == nil && c.Value == "") {
		return Principal{}, nil
	}

	if err != nil {
		return Principal{}, fmt.Errorf("read session cookie: %w", err)
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(UIHeader) == "" {
		return Principal{}, nil
	}

	p, err := a.bearer(r.Context(), c.Value)
	telemetry.AuthAttempt("cookie", attemptResult(err))

	if errors.Is(err, ErrBadCredentials) {
		return Principal{}, nil //nolint:nilerr // an expired session is simply signed out
	}

	return p, err
}

// Password verifies a username and password sent with r (Basic, UI and npm
// sign-in).
func (a *Authenticator) Password(r *http.Request, username, password string) (Principal, error) {
	client := clientAddr(r)
	if wait := a.backoff.Wait(client); wait > 0 {
		telemetry.AuthAttempt("basic", "throttled")

		return Principal{}, &ThrottledError{
			Wait: wait,
		}
	}

	p, err := a.password(r.Context(), username, password)
	if errors.Is(err, ErrBadCredentials) {
		a.backoff.Fail(client)
	}

	telemetry.AuthAttempt("basic", attemptResult(err))

	return p, err
}

func (a *Authenticator) bearer(ctx context.Context, token string) (Principal, error) {
	tok, err := a.tokens.Verify(ctx, token)
	if errors.Is(err, ErrInvalidToken) {
		return Principal{}, ErrBadCredentials
	}

	if err != nil {
		return Principal{}, fmt.Errorf("verify token: %w", err)
	}

	user, err := a.users.GetUserByUsername(ctx, tok.Username)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Principal{}, ErrBadCredentials
	}

	if err != nil {
		return Principal{}, fmt.Errorf("look up user: %w", err)
	}

	p, err := a.principal(ctx, user)
	if err != nil {
		return Principal{}, err
	}

	p = tok.Scope.Apply(p)
	if tok.WorkloadIdentity != "" {
		p.WorkloadIdentity = tok.WorkloadIdentity
		p.WorkloadTokenExpires = tok.Expires
	}

	return p, nil
}

func (a *Authenticator) password(ctx context.Context, username, password string) (Principal, error) {
	if sealing.SealedString(password) {
		plain, err := a.passwords.OpenString(password, sealedPasswordPlace(username))
		if err != nil {
			return Principal{}, ErrBadCredentials
		}

		password = plain
	}

	if strings.Count(password, ".") == 2 && a.tokens != nil {
		if tok, err := a.tokens.Verify(ctx, password); err == nil && strings.EqualFold(tok.Username, username) {
			return a.bearer(ctx, password)
		}
	}

	user, err := a.users.GetUserByUsername(ctx, username)
	switch {
	case errors.Is(database.NotFound(err), database.ErrNotFound) && a.external != nil:
		return a.directory(ctx, username, password)
	case errors.Is(database.NotFound(err), database.ErrNotFound):
		_, _ = VerifyPassword(password, dummyHash()) // same cost as a real user
		return Principal{}, ErrBadCredentials
	case err != nil:
		return Principal{}, fmt.Errorf("look up user: %w", err)
	case user.Realm == RealmLDAP:
		if a.external == nil {
			return Principal{}, ErrBadCredentials
		}

		return a.directory(ctx, username, password)
	case user.Realm == RealmSSO:
		_, _ = VerifyPassword(password, dummyHash()) // same cost as a real user
		return Principal{}, ErrBadCredentials
	}
	// The stored hash is part of the key, so a password change invalidates it.
	key := sha256.Sum256([]byte(user.ID.String() + "\x00" + user.PasswordHash + "\x00" + password))
	if a.recentlyVerified(key) {
		return a.principal(ctx, user)
	}

	ok, err := VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return Principal{}, fmt.Errorf("verify password for %s: %w", username, err)
	}

	if !ok {
		return Principal{}, ErrBadCredentials
	}

	a.remember(key)

	return a.principal(ctx, user)
}

func (a *Authenticator) directory(ctx context.Context, username, password string) (Principal, error) {
	key := sha256.Sum256([]byte("ldap\x00" + username + "\x00" + password))

	cached := a.recentlyVerified(key)
	for {
		if !cached {
			err := a.external.Authenticate(ctx, username, password)
			telemetry.AuthAttempt("ldap", attemptResult(err))

			if errors.Is(err, ErrBadCredentials) {
				return Principal{}, ErrBadCredentials
			}

			if err != nil {
				a.log.WarnContext(ctx, "directory sign-in unavailable", "user", username, "error", err)
				return Principal{}, ErrBadCredentials
			}

			a.remember(key)
		}

		user, err := a.users.GetUserByUsername(ctx, username)
		switch {
		case err == nil:
			return a.principal(ctx, user)
		case !errors.Is(database.NotFound(err), database.ErrNotFound):
			return Principal{}, fmt.Errorf("look up user: %w", err)
		case cached:
			cached = false
		default:
			return Principal{}, ErrBadCredentials
		}
	}
}

func (a *Authenticator) recentlyVerified(key [32]byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	exp, ok := a.verified[key]

	return ok && a.now().Before(exp)
}

func (a *Authenticator) remember(key [32]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.verified) >= verifiedMaxSize {
		clear(a.verified)
	}

	a.verified[key] = a.now().Add(verifiedTTL)
}

// Middleware authenticates every request and stores the principal in its
// context.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Authenticate(r)

		_, hasBearer := BearerToken(r.Header.Get("Authorization"))
		if errors.Is(err, ErrBadCredentials) && r.Method == http.MethodPost && a.expiredBearers[r.URL.Path] &&
			hasBearer {
			p, err = Principal{}, nil
		}

		switch {
		case errors.Is(err, ErrBadCredentials):
			Unauthorized(w, r)
			return
		case WriteThrottled(w, r, err):
			return
		case err != nil:
			a.log.ErrorContext(r.Context(), "authentication failed", "error", err)
			httperror.Write(w, r, http.StatusInternalServerError, "authentication failed")

			return
		}

		if slot, ok := r.Context().Value(slotKey{}).(*Principal); ok {
			*slot = p
		}

		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// Require wraps h so it only runs for callers with at least the given access.
func Require(access Access, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		switch {
		case access == Public:
		case p.Anonymous():
			Unauthorized(w, r)
			return
		case access == Admin && !p.Admin:
			httperror.Write(w, r, http.StatusForbidden, "Forbidden")
			return
		}

		h.ServeHTTP(w, r)
	})
}

// Unauthorized writes a 401 with a Basic challenge, except for the APIs the
// web UI calls.
func Unauthorized(w http.ResponseWriter, r *http.Request) {
	if !browserAPI(r.URL.Path) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Relic"`)
	}

	httperror.Write(w, r, http.StatusUnauthorized, "Bad credentials")
}
