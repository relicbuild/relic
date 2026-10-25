// Package workload exchanges a CI job's OIDC token (GitHub Actions, GitLab,
// Buildkite, cloud workload identities) for a short-lived Relic token, so CI
// stores no secret.
package workload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/access/security"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// MaxTokenLifetime bounds the tokens an exchange issues.
const MaxTokenLifetime = time.Hour

const issuerTimeout = 30 * time.Second

// Rule maps the claims of a workload's token to the Relic user it acts as.
type Rule struct {
	// Claims are claim names and the Ant-style patterns their values must
	// match, all of them; for example repository: my-org/* and
	// ref: refs/heads/main.
	Claims map[string]string `json:"claims"`
	// User is the Relic user the token is for.
	User string `json:"user"`
	// Groups narrow the token to these of the user's groups when set.
	Groups []string `json:"groups,omitempty"`
}

// Identity is a trusted token issuer and the rules for its tokens.
type Identity struct {
	Name    string `json:"-"`
	Enabled bool   `json:"enabled"`
	// Issuer is the iss of the tokens; Relic reads its signing keys from its
	// discovery document unless JWKS is set.
	Issuer string `json:"issuer"`
	// Audience is the aud the tokens must name.
	Audience string `json:"audience"`
	// JWKS holds the issuer's signing keys, for an issuer Relic cannot reach.
	JWKS string `json:"jwks,omitempty"`
	// TokenLifetime is how long issued tokens live: an hour at most, and
	// when unset.
	TokenLifetime time.Duration `json:"tokenLifetime,omitempty"`
	Rules         []Rule        `json:"rules"`
}

// Errors.
var (
	ErrNotFound = errors.New("workload identity not found")
	ErrInvalid  = errors.New("invalid workload identity")
	ErrRefused  = errors.New("workload token refused")
)

var name = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Validate reports what is wrong with id.
func (id Identity) Validate() error {
	if !name.MatchString(id.Name) {
		return fmt.Errorf("%w: name %q: lower-case letters, digits, '.', '_' and '-' only", ErrInvalid, id.Name)
	}

	if _, err := auth.ParseProviderURL(id.Issuer); err != nil {
		return fmt.Errorf("%w: issuer %w", ErrInvalid, err)
	}

	if id.Audience == "" {
		return fmt.Errorf("%w: %s needs an audience", ErrInvalid, id.Name)
	}

	if id.JWKS != "" {
		if _, err := keySet(id.JWKS); err != nil {
			return err
		}
	}

	if id.TokenLifetime < 0 || id.TokenLifetime > MaxTokenLifetime {
		return fmt.Errorf("%w: token lifetime %s: at most %s", ErrInvalid, id.TokenLifetime, MaxTokenLifetime)
	}

	if len(id.Rules) == 0 {
		return fmt.Errorf("%w: %s needs at least one rule", ErrInvalid, id.Name)
	}

	for i, r := range id.Rules {
		if r.User == "" || len(r.Claims) == 0 {
			return fmt.Errorf("%w: rule %d needs a user and at least one claim", ErrInvalid, i+1)
		}

		for claim, pattern := range r.Claims {
			if claim == "" || pattern == "" {
				return fmt.Errorf("%w: rule %d has an empty claim or pattern", ErrInvalid, i+1)
			}
		}
	}

	return nil
}

func keySet(raw string) (*oidc.StaticKeySet, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(raw), &set); err != nil || len(set.Keys) == 0 {
		return nil, fmt.Errorf("%w: the JWKS holds no keys", ErrInvalid)
	}

	keys := &oidc.StaticKeySet{}

	for _, k := range set.Keys {
		if !k.IsPublic() {
			return nil, fmt.Errorf("%w: the JWKS holds a private key", ErrInvalid)
		}

		keys.PublicKeys = append(keys.PublicKeys, k.Key)
	}

	return keys, nil
}

// Tokens issues Relic tokens.
type Tokens interface {
	Issue(ctx context.Context, p auth.IssueParams) (auth.IssuedToken, error)
}

// Service manages workload identities and exchanges their tokens.
type Service struct {
	db     *database.DB
	tokens Tokens
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
	audit  audit.Recorder

	mu        sync.Mutex
	verifiers map[string]cachedVerifier
}

// New returns a Service issuing tokens from tokens.
func New(db *database.DB, tokens Tokens, log *slog.Logger) *Service {
	return &Service{
		db:     db,
		tokens: tokens,
		client: &http.Client{
			Timeout: issuerTimeout,
		},
		log: log,
		now: time.Now,
	}
}

// SetAudit makes the service record every exchange, granted or refused.
func (s *Service) SetAudit(r audit.Recorder) { s.audit = r }

// SetHTTPClient sets the client used to reach issuers.
func (s *Service) SetHTTPClient(c *http.Client) { s.client = c }

// Identities returns every workload identity.
func (s *Service) Identities(ctx context.Context) ([]Identity, error) {
	rows, err := s.db.ListWorkloadIdentities(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workload identities: %w", err)
	}

	out := make([]Identity, 0, len(rows))
	for _, r := range rows {
		id, err := decode(r.Name, r.Config)
		if err != nil {
			return nil, err
		}

		out = append(out, id)
	}

	return out, nil
}

// Identity returns one workload identity.
func (s *Service) Identity(ctx context.Context, n string) (Identity, error) {
	row, err := s.db.GetWorkloadIdentity(ctx, n)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Identity{}, fmt.Errorf("%w: %s", ErrNotFound, n)
	}

	if err != nil {
		return Identity{}, fmt.Errorf("get workload identity %s: %w", n, err)
	}

	return decode(row.Name, row.Config)
}

func decode(n string, raw []byte) (Identity, error) {
	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return Identity{}, fmt.Errorf("decode workload identity %s: %w", n, err)
	}

	id.Name = n

	return id, nil
}

// PutIdentity creates or replaces a workload identity.
func (s *Service) PutIdentity(ctx context.Context, id Identity) error {
	if err := id.Validate(); err != nil {
		return err
	}

	for i, r := range id.Rules {
		if err := security.CheckPrincipal(ctx, s.db, r.User, false); err != nil {
			return fmt.Errorf("%w: rule %d: %w", ErrInvalid, i+1, err)
		}

		for _, g := range r.Groups {
			if err := security.CheckPrincipal(ctx, s.db, g, true); err != nil {
				return fmt.Errorf("%w: rule %d: %w", ErrInvalid, i+1, err)
			}
		}
	}

	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}

	if err := s.db.UpsertWorkloadIdentity(ctx, database.UpsertWorkloadIdentityParams{
		Name:   id.Name,
		Config: raw,
		Now:    s.now().UTC(),
	}); err != nil {
		return fmt.Errorf("store workload identity %s: %w", id.Name, err)
	}

	s.forget(id.Name)

	return nil
}

// DeleteIdentity removes a workload identity. Tokens it issued stay valid
// until they expire.
func (s *Service) DeleteIdentity(ctx context.Context, n string) error {
	deleted, err := s.db.DeleteWorkloadIdentity(ctx, n)
	if err != nil {
		return fmt.Errorf("delete workload identity %s: %w", n, err)
	}

	if deleted == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, n)
	}

	s.forget(n)

	return nil
}

func (s *Service) forget(n string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.verifiers, n)
}

type verifierSettings struct {
	issuer   string
	audience string
	jwks     string
}

type cachedVerifier struct {
	settings verifierSettings
	verifier *oidc.IDTokenVerifier
}

func (s *Service) verifier(ctx context.Context, id Identity) (*oidc.IDTokenVerifier, error) {
	settings := verifierSettings{
		issuer:   id.Issuer,
		audience: id.Audience,
		jwks:     id.JWKS,
	}

	s.mu.Lock()
	cached, ok := s.verifiers[id.Name]
	s.mu.Unlock()

	if ok && cached.settings == settings {
		return cached.verifier, nil
	}

	cfg := &oidc.Config{
		ClientID: id.Audience,
	}

	var v *oidc.IDTokenVerifier

	if id.JWKS != "" {
		keys, err := keySet(id.JWKS)
		if err != nil {
			return nil, err
		}

		v = oidc.NewVerifier(id.Issuer, keys, cfg)
	} else {
		// The provider keeps its context to fetch signing keys later.
		provider, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), s.client), id.Issuer)
		if err != nil {
			return nil, fmt.Errorf("discover issuer %s: %w", id.Issuer, err)
		}

		v = provider.Verifier(cfg)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.verifiers == nil {
		s.verifiers = map[string]cachedVerifier{}
	}

	s.verifiers[id.Name] = cachedVerifier{
		settings: settings,
		verifier: v,
	}

	return v, nil
}

// Exchange is the result of a successful exchange.
type Exchange struct {
	Token   auth.IssuedToken
	User    string
	Subject string
}

// Exchange checks token against the workload identity n and, when a rule
// matches its claims, issues a Relic token for that rule's user.
func (s *Service) Exchange(ctx context.Context, n, token string) (Exchange, error) {
	out, err := s.exchange(ctx, n, token)
	telemetry.AuthAttempt("workload", attemptResult(err))

	if s.audit != nil {
		e := audit.Event{
			Actor:   out.User,
			Action:  "workload.exchange",
			Target:  n,
			Outcome: audit.Success,
			Detail:  out.Subject,
		}
		if e.Actor == "" {
			e.Actor = auth.AnonymousUser
		}

		if err != nil {
			e.Outcome, e.Detail = exchangeOutcome(err), err.Error()
		}

		s.audit.Record(ctx, e)
	}

	return out, err
}

func exchangeOutcome(err error) audit.Outcome {
	switch {
	case err == nil:
		return audit.Success
	case errors.Is(err, ErrRefused):
		return audit.Denied
	default:
		return audit.Failure
	}
}

func attemptResult(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrRefused):
		return "failure"
	default:
		return "error"
	}
}

func refused(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{
		ErrRefused,
	}, args...)...)
}

func (s *Service) exchange(ctx context.Context, n, token string) (Exchange, error) {
	id, err := s.Identity(ctx, n)
	if errors.Is(err, ErrNotFound) {
		return Exchange{}, refused("there is no workload identity %s", n)
	}

	if err != nil {
		return Exchange{}, err
	}

	if !id.Enabled {
		return Exchange{}, refused("workload identity %s is turned off", n)
	}

	v, err := s.verifier(ctx, id)
	if err != nil {
		return Exchange{}, err
	}

	verified, err := v.Verify(oidc.ClientContext(ctx, s.client), token)
	if err != nil {
		return Exchange{}, refused("the token is not valid for %s: %v", n, err)
	}

	var claims map[string]any
	if err := verified.Claims(&claims); err != nil {
		return Exchange{}, refused("the token's claims do not decode")
	}

	rule, ok := match(id.Rules, claims)
	if !ok {
		return Exchange{}, refused("no rule of %s matches the token of %s", n, verified.Subject)
	}

	user, err := s.db.GetUserByUsername(ctx, rule.User)
	if errors.Is(database.NotFound(err), database.ErrNotFound) || (err == nil && user.Disabled) {
		return Exchange{}, refused("the rule's user %s does not exist or is disabled", rule.User)
	}

	if err != nil {
		return Exchange{}, err
	}

	lifetime := id.TokenLifetime
	if lifetime <= 0 {
		lifetime = MaxTokenLifetime
	}

	scope := auth.UserScope
	if len(rule.Groups) > 0 {
		scope = auth.TokenScope{
			Kind:   auth.ScopeGroups,
			Groups: rule.Groups,
		}
	}

	issued, err := s.tokens.Issue(ctx, auth.IssueParams{
		Username:         rule.User,
		Scope:            scope,
		ExpiresIn:        lifetime,
		Description:      "workload " + n + ": " + verified.Subject,
		WorkloadIdentity: n,
	})
	if errors.Is(err, auth.ErrUnknownGroup) {
		return Exchange{}, refused("the rule's group does not exist: %v", err)
	}

	if err != nil {
		return Exchange{}, err
	}

	return Exchange{
		Token:   issued,
		User:    rule.User,
		Subject: verified.Subject,
	}, nil
}

// match returns the first rule whose every claim pattern matches.
func match(rules []Rule, claims map[string]any) (Rule, bool) {
	for _, r := range rules {
		ok := len(r.Claims) > 0
		for k, pattern := range r.Claims {
			ok = ok && matches(pattern, claims[k])
		}

		if ok {
			return r, true
		}
	}

	return Rule{}, false
}

func matches(pattern string, value any) bool {
	switch v := value.(type) {
	case string:
		return auth.MatchesAntPattern(pattern, v)
	case bool:
		return pattern == strconv.FormatBool(v)
	case float64:
		return pattern == strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		for _, item := range v {
			if matches(pattern, item) {
				return true
			}
		}
	}

	return false
}
