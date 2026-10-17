package scan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/access/audit"
)

type recordedEvents struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordedEvents) Record(_ context.Context, e audit.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, e)
}

// fakeProviders answers as each provider would for the secrets "good" and
// "revoked", and records the credentials each request carried.
func fakeProviders(t *testing.T) (map[string]string, *[]string) {
	t.Helper()

	var (
		mu   sync.Mutex
		seen []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if c := r.Header.Get("PRIVATE-TOKEN"); c != "" {
			credential = c
		}

		if user, _, ok := r.BasicAuth(); ok {
			credential = user
		}

		mu.Lock()

		seen = append(seen, r.URL.Path+" "+credential)
		mu.Unlock()

		switch {
		case r.URL.Path == "/slack":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": credential == "good",
				"error": map[bool]string{
					false: "invalid_auth",
				}[credential == "good"],
			})
		case r.URL.Path == "/stripe" && credential == "down":
			w.WriteHeader(http.StatusBadGateway)
		case credential == "good":
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)

	urls := map[string]string{}
	for _, p := range SecretProviders {
		urls[p] = srv.URL + "/" + p
	}

	return urls, &seen
}

func TestValidateSecret(t *testing.T) {
	t.Parallel()

	urls, seen := fakeProviders(t)
	rec := &recordedEvents{}

	v := NewSecretValidator([]string{
		ProviderGitHub,
		ProviderSlack,
		ProviderNpm,
		ProviderStripe,
	}, rec)
	v.urls, v.period = urls, 0

	tests := []struct {
		name, rule, secret, want string
	}{
		{
			"GitHub accepts",
			"github-pat",
			"good",
			SecretLive,
		},
		{
			"GitHub refuses: maybe a GitHub Enterprise token",
			"github-pat",
			"revoked",
			SecretUnknown,
		},
		{
			"npm refuses",
			"npm-access-token",
			"revoked",
			SecretInactive,
		},
		{
			"Slack accepts",
			"slack-bot-token",
			"good",
			SecretLive,
		},
		{
			"Slack refuses",
			"slack-bot-token",
			"revoked",
			SecretInactive,
		},
		{
			"Stripe is down",
			"stripe-access-token",
			"down",
			SecretError,
		},
		{
			"a provider not turned on",
			"gitlab-pat",
			"good",
			SecretUnknown,
		},
		{
			"a rule without a provider",
			"aws-access-token",
			"good",
			SecretUnknown,
		},
	}
	for _, tt := range tests {
		got := v.Validate(t.Context(), "aa", Issue{
			Rule: tt.rule,
			File: "conf/app.env",
			Line: 3,
		}, tt.secret)
		if got != tt.want {
			t.Errorf("%s: Validate = %q, want %q", tt.name, got, tt.want)
		}
	}

	if len(*seen) != 6 {
		t.Errorf("requests = %v, want one per check of a turned-on provider", *seen)
	}

	for _, s := range *seen {
		if strings.HasPrefix(s, "/gitlab") {
			t.Errorf("GitLab was asked although it is off: %s", s)
		}
	}

	if len(rec.events) != 6 {
		t.Fatalf("audit events = %d, want 6", len(rec.events))
	}

	for _, e := range rec.events {
		wantOutcome := audit.Success
		if strings.HasSuffix(e.Detail, ": "+SecretError) {
			wantOutcome = audit.Failure
		}

		if e.Action != "secret.validated" || e.Target != "sha256:aa conf/app.env:3" ||
			strings.Contains(e.Detail, "good") || strings.Contains(e.Detail, "revoked") ||
			e.Outcome != wantOutcome {
			t.Errorf("audit event = %+v", e)
		}
	}

	if failed := slices.IndexFunc(rec.events, func(e audit.Event) bool {
		return e.Outcome == audit.Failure
	}); failed < 0 {
		t.Error("the failed Stripe check is not audited as a failure")
	}
}

func TestValidateSecrets(t *testing.T) {
	t.Parallel()

	urls, seen := fakeProviders(t)

	v := NewSecretValidator(nil, nil)
	v.urls, v.period = urls, 0

	s := &Service{
		secrets: v,
		now:     time.Now,
	}

	issues := []Issue{
		{
			Rule:     "github-pat",
			Severity: SeverityHigh,
			secret:   "good",
		},
		{
			Rule:     "github-pat",
			Severity: SeverityHigh,
			secret:   "good",
			Line:     9,
		},
		{
			Rule:     "npm-access-token",
			Severity: SeverityHigh,
			secret:   "revoked",
		},
		{
			Analyzer: AnalyzerMisconfiguration,
			Rule:     "KSV-0017",
			Severity: SeverityHigh,
		},
	}

	s.validateSecrets(t.Context(), "aa", issues)

	for i, is := range issues {
		if is.secret != "" {
			t.Errorf("issue %d kept its secret", i)
		}
	}

	if issues[0].Validation != SecretLive || issues[0].Severity != SeverityCritical || issues[0].Validated == nil {
		t.Errorf("a live secret = %+v, want live and critical", issues[0])
	}

	if issues[1].Validation != SecretLive || issues[1].Severity != SeverityCritical || issues[1].Validated == nil {
		t.Errorf("the same secret again = %+v, want live and critical", issues[1])
	}

	if issues[2].Validation != SecretInactive || issues[2].Severity != SeverityHigh {
		t.Errorf("a revoked secret = %+v", issues[2])
	}

	if issues[3].Validation != "" || issues[3].Validated != nil {
		t.Errorf("a misconfiguration was validated: %+v", issues[3])
	}

	if len(*seen) != 2 {
		t.Errorf("requests = %v, want one per distinct secret", *seen)
	}

	off := &Service{
		now: time.Now,
	}
	unchecked := []Issue{
		{
			Rule:   "github-pat",
			secret: "good",
		},
	}

	off.validateSecrets(t.Context(), "aa", unchecked)

	if unchecked[0].secret != "" || unchecked[0].Validation != "" {
		t.Errorf("with validation off = %+v, want the secret dropped and nothing checked", unchecked[0])
	}
}
