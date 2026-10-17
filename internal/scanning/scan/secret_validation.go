package scan

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"relic/internal/access/audit"
	"relic/internal/platform/telemetry"
)

// Outcomes of a secret's validation.
const (
	SecretLive     = "live"
	SecretInactive = "inactive"
	SecretUnknown  = "unknown"
	SecretError    = "error"
)

// Providers whose secrets Relic can validate.
const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
	ProviderSlack  = "slack"
	ProviderNpm    = "npm"
	ProviderStripe = "stripe"
)

// SecretProviders lists the providers whose secrets Relic can validate.
var SecretProviders = []string{
	ProviderGitHub,
	ProviderGitLab,
	ProviderSlack,
	ProviderNpm,
	ProviderStripe,
}

// secretProviders maps Gitleaks rules to the providers that answer for
// their secrets.
var secretProviders = map[string]string{
	"github-pat":              ProviderGitHub,
	"github-fine-grained-pat": ProviderGitHub,
	"github-oauth":            ProviderGitHub,
	"github-app-token":        ProviderGitHub,
	"gitlab-pat":              ProviderGitLab,
	"slack-bot-token":         ProviderSlack,
	"slack-user-token":        ProviderSlack,
	"npm-access-token":        ProviderNpm,
	"stripe-access-token":     ProviderStripe,
}

// providerURLs are each provider's read-only identity endpoint: the only
// hosts validation contacts.
var providerURLs = map[string]string{
	ProviderGitHub: "https://api.github.com/user",
	ProviderGitLab: "https://gitlab.com/api/v4/personal_access_tokens/self",
	ProviderSlack:  "https://slack.com/api/auth.test",
	ProviderNpm:    "https://registry.npmjs.org/-/whoami",
	ProviderStripe: "https://api.stripe.com/v1/account",
}

// slackRefusals are auth.test errors that say the token is not accepted.
var slackRefusals = []string{
	"invalid_auth",
	"token_revoked",
	"token_expired",
	"account_inactive",
	"not_authed",
}

const (
	maxSecretChecks   = 50
	secretCheckPeriod = 500 * time.Millisecond
)

// SecretValidator asks providers whether secrets found in content are still
// accepted. It sends each secret to its provider only, and keeps no secret.
type SecretValidator struct {
	providers map[string]bool
	urls      map[string]string
	client    *http.Client
	audit     audit.Recorder
	// period spaces checks out.
	period time.Duration

	mu   sync.Mutex
	next time.Time
}

// NewSecretValidator validates the secrets of providers (SecretProviders
// when empty), recording each check in rec.
func NewSecretValidator(providers []string, rec audit.Recorder) *SecretValidator {
	if len(providers) == 0 {
		providers = SecretProviders
	}

	v := &SecretValidator{
		providers: map[string]bool{},
		urls:      providerURLs,
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		audit:  rec,
		period: secretCheckPeriod,
	}
	for _, p := range providers {
		v.providers[p] = true
	}

	return v
}

// validateSecrets checks the secrets the analyzers found in content
// sha256Hex, a bounded number per scan, records the outcomes on the issues
// (a live secret is critical) and clears every secret.
func (s *Service) validateSecrets(ctx context.Context, sha256Hex string, issues []Issue) {
	checks := 0
	outcomes := map[[32]byte]string{}

	for i := range issues {
		is := &issues[i]
		secret := is.secret
		is.secret = ""

		if s.secrets == nil || secret == "" {
			continue
		}

		key := sha256.Sum256([]byte(is.Rule + "\x00" + secret))

		outcome, seen := outcomes[key]
		if !seen && checks < maxSecretChecks {
			outcome, seen = s.secrets.Validate(ctx, sha256Hex, *is, secret), true
			outcomes[key] = outcome
			checks++
		}

		if !seen {
			continue
		}

		now := s.now()
		is.Validation, is.Validated = outcome, &now

		if outcome == SecretLive {
			is.Severity = SeverityCritical
		}
	}
}

// Validate asks the provider of the issue's rule whether secret is
// accepted: live, inactive, unknown (no provider, or an answer that proves
// neither) or error.
func (v *SecretValidator) Validate(ctx context.Context, sha256Hex string, is Issue, secret string) (outcome string) {
	provider := secretProviders[is.Rule]
	if provider == "" || !v.providers[provider] {
		return SecretUnknown
	}

	ctx, span := telemetry.Tracer().Start(ctx, "scan.ValidateSecret")

	defer func() {
		telemetry.SecretValidated(provider, outcome)
		span.End()

		if v.audit != nil {
			recorded := audit.Success
			if outcome == SecretError {
				recorded = audit.Failure
			}

			v.audit.Record(ctx, audit.Event{
				Actor:   "relic-scan",
				Action:  "secret.validated",
				Target:  fmt.Sprintf("sha256:%s %s:%d", sha256Hex, is.File, is.Line),
				Outcome: recorded,
				Detail:  provider + ": " + outcome,
			})
		}
	}()

	if err := v.wait(ctx); err != nil {
		return SecretError
	}

	req, err := v.request(ctx, provider, secret)
	if err != nil {
		return SecretError
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return SecretError
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	return answer(provider, resp)
}

// wait spaces checks out, a check per period across scans.
func (v *SecretValidator) wait(ctx context.Context) error {
	v.mu.Lock()
	now := time.Now()

	at := now
	if v.next.After(now) {
		at = v.next
	}

	v.next = at.Add(v.period)
	v.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(at.Sub(now)):
		return nil
	}
}

func (v *SecretValidator) request(ctx context.Context, provider, secret string) (*http.Request, error) {
	method := http.MethodGet
	if provider == ProviderSlack {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(ctx, method, v.urls[provider], nil)
	if err != nil {
		return nil, err
	}

	switch provider {
	case ProviderGitLab:
		req.Header.Set("PRIVATE-TOKEN", secret)
	case ProviderStripe:
		req.SetBasicAuth(secret, "")
	default:
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	req.Header.Set("User-Agent", "Relic secret validation")

	return req, nil
}

// answer reads a provider's response. GitHub and GitLab tokens of
// self-hosted servers look the same as theirs, so a refusal there proves
// nothing: it is unknown.
func answer(provider string, resp *http.Response) string {
	if provider == ProviderSlack {
		var body struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}

		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body) != nil {
			return SecretError
		}

		switch {
		case body.OK:
			return SecretLive
		case slices.Contains(slackRefusals, body.Error):
			return SecretInactive
		default:
			return SecretUnknown
		}
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return SecretLive
	case resp.StatusCode == http.StatusUnauthorized && provider != ProviderGitHub && provider != ProviderGitLab:
		return SecretInactive
	case resp.StatusCode >= http.StatusInternalServerError:
		return SecretError
	default:
		return SecretUnknown
	}
}
