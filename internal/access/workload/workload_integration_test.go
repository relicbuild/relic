//go:build integration

package workload_test

import (
	"errors"
	"log/slog"
	"testing"
	"time"
	"uuid"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/access/sso/ssotest"
	"relic/internal/access/workload"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestExchange(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range []string{
		"ci-bot",
		"docs-bot",
		"gone-bot",
	} {
		if _, err := db.UpsertUser(ctx, database.UpsertUserParams{
			ID:           uuid.NewV7(),
			Username:     u,
			PasswordHash: "!",
			Extensions:   []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.SetUserDisabled(ctx, database.SetUserDisabledParams{
		Username: "gone-bot",
		Disabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	issuer := ssotest.NewOIDC(t, "unused", "unused")
	s := workload.New(db, tokens, slog.New(slog.DiscardHandler))

	if err := s.PutIdentity(ctx, workload.Identity{
		Name:     "github",
		Enabled:  true,
		Issuer:   issuer.URL,
		Audience: "relic",
		Rules: []workload.Rule{
			{
				Claims: map[string]string{
					"repository": "acme/*",
					"ref":        "refs/heads/main",
				},
				User: "ci-bot",
			},
			{
				Claims: map[string]string{
					"repository": "acme/docs",
				},
				User: "docs-bot",
				Groups: []string{
					"docs",
				},
			},
			{
				Claims: map[string]string{
					"repository": "acme/retired",
				},
				User: "gone-bot",
			},
			{
				Claims: map[string]string{
					"repository_id": "1234567",
				},
				User: "ci-bot",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	claims := func(repo, ref string) map[string]any {
		return map[string]any{
			"sub":        "repo:" + repo + ":ref:" + ref,
			"aud":        "relic",
			"repository": repo,
			"ref":        ref,
		}
	}

	tests := []struct {
		name     string
		identity string
		token    string
		user     string
		groups   bool
	}{
		{
			name:     "main branch",
			identity: "github",
			token:    issuer.Mint(t, claims("acme/app", "refs/heads/main")),
			user:     "ci-bot",
		},
		{
			name:     "second rule, narrowed to groups",
			identity: "github",
			token:    issuer.Mint(t, claims("acme/docs", "refs/heads/dev")),
			user:     "docs-bot",
			groups:   true,
		},
		{
			name:     "no rule matches",
			identity: "github",
			token:    issuer.Mint(t, claims("acme/app", "refs/heads/dev")),
		},
		{
			name:     "another organization",
			identity: "github",
			token:    issuer.Mint(t, claims("evil/app", "refs/heads/main")),
		},
		{
			name:     "numeric claim",
			identity: "github",
			token: issuer.Mint(t, map[string]any{
				"sub":           "x",
				"aud":           "relic",
				"repository":    "other/app",
				"repository_id": 1234567,
			}),
			user: "ci-bot",
		},
		{
			name:     "disabled user",
			identity: "github",
			token:    issuer.Mint(t, claims("acme/retired", "refs/heads/dev")),
		},
		{
			name:     "wrong audience",
			identity: "github",
			token: issuer.Mint(t, map[string]any{
				"sub":        "x",
				"aud":        "someone-else",
				"repository": "acme/app",
				"ref":        "refs/heads/main",
			}),
		},
		{
			name:     "expired",
			identity: "github",
			token: issuer.Mint(t, map[string]any{
				"sub":        "x",
				"aud":        "relic",
				"repository": "acme/app",
				"ref":        "refs/heads/main",
				"exp":        time.Now().Add(-time.Hour).Unix(),
			}),
		},
		{
			name:     "signed by another key",
			identity: "github",
			token: ssotest.NewOIDC(t, "x", "x").
				Mint(t, mergeIssuer(claims("acme/app", "refs/heads/main"), issuer.URL)),
		},
		{
			name:     "unknown identity",
			identity: "gitlab",
			token:    issuer.Mint(t, claims("acme/app", "refs/heads/main")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Exchange(ctx, tt.identity, tt.token)
			if tt.user == "" {
				if !errors.Is(err, workload.ErrRefused) {
					t.Fatalf("exchange = %+v, %v; want refused", got, err)
				}

				return
			}

			if err != nil || got.User != tt.user {
				t.Fatalf("exchange = %+v, %v; want a token for %s", got, err, tt.user)
			}

			verified, err := tokens.Verify(ctx, got.Token.AccessToken)
			if err != nil || verified.Username != tt.user ||
				(verified.Scope.Kind == auth.ScopeGroups) != tt.groups {
				t.Errorf("issued token = %+v, %v", verified, err)
			}

			if got.Token.ExpiresIn > workload.MaxTokenLifetime {
				t.Errorf("issued token lives %s", got.Token.ExpiresIn)
			}
		})
	}
}

func mergeIssuer(claims map[string]any, iss string) map[string]any {
	claims["iss"] = iss
	return claims
}

func TestExchangeWithInlineKeys(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.UpsertUser(ctx, database.UpsertUserParams{
		ID:           uuid.NewV7(),
		Username:     "ci-bot",
		PasswordHash: "!",
		Extensions:   []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	keys := ssotest.NewOIDC(t, "unused", "unused")
	s := workload.New(db, tokens, slog.New(slog.DiscardHandler))

	if err := s.PutIdentity(ctx, workload.Identity{
		Name:     "plain-http",
		Enabled:  true,
		Issuer:   "http://ci.example.com",
		Audience: "relic",
		Rules: []workload.Rule{
			{
				Claims: map[string]string{
					"sub": "**",
				},
				User: "ci-bot",
			},
		},
	}); !errors.Is(err, workload.ErrInvalid) {
		t.Errorf("an issuer reached over plain http = %v, want ErrInvalid", err)
	}

	const offline = "https://ci.unreachable.invalid"
	if err := s.PutIdentity(ctx, workload.Identity{
		Name:     "offline",
		Enabled:  true,
		Issuer:   offline,
		Audience: "relic",
		JWKS:     keys.JWKS(t),
		Rules: []workload.Rule{
			{
				Claims: map[string]string{
					"sub": "pipeline/**",
				},
				User: "ci-bot",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Exchange(ctx, "offline", keys.Mint(t, map[string]any{
		"iss": offline,
		"sub": "pipeline/acme/build",
		"aud": "relic",
	}))
	if err != nil || got.User != "ci-bot" {
		t.Fatalf("exchange with inline keys = %+v, %v", got, err)
	}

	rotated := ssotest.NewOIDC(t, "unused", "unused")
	otherReplica := workload.New(db, tokens, slog.New(slog.DiscardHandler))

	if err := otherReplica.PutIdentity(ctx, workload.Identity{
		Name:     "offline",
		Enabled:  true,
		Issuer:   offline,
		Audience: "relic",
		JWKS:     rotated.JWKS(t),
		Rules: []workload.Rule{
			{
				Claims: map[string]string{
					"sub": "pipeline/**",
				},
				User: "ci-bot",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Exchange(ctx, "offline", keys.Mint(t, map[string]any{
		"iss": offline,
		"sub": "pipeline/acme/build",
		"aud": "relic",
	})); !errors.Is(err, workload.ErrRefused) {
		t.Errorf("a token signed with a key another replica removed = %v, want refused", err)
	}

	if got, err := s.Exchange(ctx, "offline", rotated.Mint(t, map[string]any{
		"iss": offline,
		"sub": "pipeline/acme/build",
		"aud": "relic",
	})); err != nil || got.User != "ci-bot" {
		t.Errorf("a token signed with the new key = %+v, %v", got, err)
	}
}
