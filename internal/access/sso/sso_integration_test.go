//go:build integration

package sso_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"uuid"

	"relic/internal/access/sso"
	"relic/internal/access/sso/ssotest"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/sealing"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

const callback = "https://relic.example.com/api/v1/sso/corp/callback"

// signIn runs a browser through the provider and returns what FinishOIDC
// returned.
func signIn(t *testing.T, s *sso.Service, returnTo string) (string, string, error) {
	t.Helper()

	return signInWith(t, s, "corp", returnTo)
}

func signInWith(t *testing.T, s *sso.Service, provider, returnTo string) (string, string, error) {
	t.Helper()

	callback := "https://relic.example.com/api/v1/sso/" + provider + "/callback"

	authURL, state, err := s.StartOIDC(t.Context(), provider, callback, returnTo)
	if err != nil {
		t.Fatal(err)
	}

	browser := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, authURL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(back.String(), callback) || back.Query().Get("state") != state {
		t.Fatalf("provider sent the browser to %q (state %q)", back, state)
	}

	return s.FinishOIDC(t.Context(), provider, callback, state, back.Query().Get("code"))
}

func groups(t *testing.T, db *database.DB, user string) []string {
	t.Helper()

	rows, err := db.UserGroups(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}

	return names
}

func TestOIDCSignIn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	box, err := sealing.New(bytes.Repeat([]byte{
		7,
	}, 32))
	if err != nil {
		t.Fatal(err)
	}

	idp := ssotest.NewOIDC(t, "relic", "client-secret-1")
	s := sso.New(db, box, slog.New(slog.DiscardHandler))

	if err := s.PutProvider(ctx, sso.Provider{
		Name:            "corp",
		Enabled:         true,
		AutoCreateUsers: true,
		GroupsClaim:     "groups",
		OIDC: &sso.OIDCSettings{
			Issuer:       idp.URL,
			ClientID:     "relic",
			ClientSecret: "client-secret-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	row, err := db.GetIdentityProvider(ctx, "corp")
	if err != nil || bytes.Contains(row.Config, []byte("client-secret-1")) {
		t.Fatalf("stored provider = %s, %v; want its client secret sealed", row.Config, err)
	}

	for _, g := range []string{
		"devs",
		"ops",
	} {
		if _, err := db.UpsertGroup(ctx, database.UpsertGroupParams{
			Name: g,
		}); err != nil {
			t.Fatal(err)
		}
	}

	idp.SignIn(map[string]any{
		"preferred_username": "alice",
		"email":              "alice@example.com",
		"groups": []string{
			"Devs",
			"unknown",
		},
	})

	user, returnTo, err := signIn(t, s, "/browse")
	if err != nil || user != "alice" || returnTo != "/browse" {
		t.Fatalf("first sign-in = %q %q %v", user, returnTo, err)
	}

	alice, err := db.GetUserByUsername(ctx, "alice")
	if err != nil || alice.Realm != "sso" || alice.Email != "alice@example.com" {
		t.Fatalf("alice = %+v, %v", alice, err)
	}

	if got := groups(t, db, "alice"); !slices.Equal(got, []string{
		"devs",
	}) {
		t.Errorf("alice's groups = %v, want [devs]", got)
	}

	if err := db.AddGroupMembers(ctx, database.AddGroupMembersParams{
		GroupName: "ops",
		Usernames: []string{
			"alice",
		},
	}); err != nil {
		t.Fatal(err)
	}

	idp.SignIn(map[string]any{
		"preferred_username": "alice",
		"groups":             []string{},
	})

	if _, returnTo, err := signIn(t, s, "//evil.example.com"); err != nil || returnTo != "/" {
		t.Fatalf("second sign-in returns to %q, %v", returnTo, err)
	}

	if got := groups(t, db, "alice"); !slices.Equal(got, []string{
		"ops",
	}) {
		t.Errorf("after a sign-in without groups, alice's groups = %v, want the one an administrator set", got)
	}

	if err := db.CreateLdapUser(ctx, database.CreateLdapUserParams{
		ID:       uuid.NewV7(),
		Username: "bob",
	}); err != nil {
		t.Fatal(err)
	}

	idp.SignIn(map[string]any{
		"preferred_username": "bob",
	})

	var refused *sso.RefusedError
	if _, _, err := signIn(t, s, "/"); !errors.As(err, &refused) {
		t.Errorf("a provider signed in an LDAP account: %v", err)
	}

	if err := db.SetUserDisabled(ctx, database.SetUserDisabledParams{
		Disabled: true,
		Username: "alice",
	}); err != nil {
		t.Fatal(err)
	}

	idp.SignIn(map[string]any{
		"preferred_username": "alice",
	})

	if _, _, err := signIn(t, s, "/"); !errors.As(err, &refused) {
		t.Errorf("a disabled account signed in: %v", err)
	}

	if err := db.SetUserDisabled(ctx, database.SetUserDisabledParams{
		Disabled: false,
		Username: "alice",
	}); err != nil {
		t.Fatal(err)
	}

	idp.SignIn(map[string]any{
		"preferred_username": "alice",
	})

	idp.Tamper("", "another")

	if _, _, err := signIn(t, s, "/"); !errors.Is(err, sso.ErrRefused) {
		t.Errorf("an ID token with another nonce was accepted: %v", err)
	}

	idp.Tamper("another-client", "")

	if _, _, err := signIn(t, s, "/"); !errors.Is(err, sso.ErrRefused) {
		t.Errorf("an ID token for another client was accepted: %v", err)
	}

	idp.Tamper("", "")

	if _, _, err := s.FinishOIDC(ctx, "corp", callback, "forged-state", "code"); !errors.Is(err, sso.ErrRefused) {
		t.Errorf("an unknown state was accepted: %v", err)
	}

	for i := range 50 {
		secret := fmt.Sprintf("client-secret-%d", i+2)
		settings := func(secret string) sso.Provider {
			return sso.Provider{
				Name:    "corp",
				Enabled: true,
				OIDC: &sso.OIDCSettings{
					Issuer:       idp.URL,
					ClientID:     "relic",
					ClientSecret: secret,
				},
			}
		}

		var wg sync.WaitGroup

		wg.Go(func() {
			if err := s.PutProvider(ctx, settings("")); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := s.PutProvider(ctx, settings(secret)); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()

		if p, err := s.Provider(ctx, "corp"); err != nil || p.OIDC.ClientSecret != secret {
			t.Fatalf("a save that keeps the secret, concurrent with one setting %q, left %+v %v", secret, p.OIDC, err)
		}
	}
}

func TestOIDCSignInWithoutAutoCreate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	idp := ssotest.NewOIDC(t, "relic", "s")
	s := sso.New(db, nil, slog.New(slog.DiscardHandler))

	if err := s.PutProvider(ctx, sso.Provider{
		Name:    "corp",
		Enabled: true,
		OIDC: &sso.OIDCSettings{
			Issuer:       idp.URL,
			ClientID:     "relic",
			ClientSecret: "s",
		},
	}); err != nil {
		t.Fatal(err)
	}

	idp.SignIn(map[string]any{
		"preferred_username": "carol",
	})

	if _, _, err := signIn(t, s, "/"); !errors.Is(err, sso.ErrRefused) {
		t.Errorf("an unknown user signed in although the provider creates no users: %v", err)
	}

	if _, err := db.GetUserByUsername(ctx, "carol"); !errors.Is(database.NotFound(err), database.ErrNotFound) {
		t.Errorf("carol was created: %v", err)
	}
}

func TestOIDCSignInKeepsAccountsToTheirProvider(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	s := sso.New(db, nil, slog.New(slog.DiscardHandler))

	corp := ssotest.NewOIDC(t, "relic", "s")
	rogue := ssotest.NewOIDC(t, "relic", "s")

	for name, idp := range map[string]*ssotest.OIDC{
		"corp":  corp,
		"rogue": rogue,
	} {
		if err := s.PutProvider(ctx, sso.Provider{
			Name:            name,
			Enabled:         true,
			AutoCreateUsers: true,
			GroupsClaim:     "groups",
			OIDC: &sso.OIDCSettings{
				Issuer:       idp.URL,
				ClientID:     "relic",
				ClientSecret: "s",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.UpsertGroup(ctx, database.UpsertGroupParams{
		Name: "admins",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := sso.NewDirectory(db, nil).CreateUser(ctx, sso.DirectoryUser{
		UserName: "dave",
		Active:   true,
	}); err != nil {
		t.Fatal(err)
	}

	for _, user := range []string{
		"alice",
		"dave",
	} {
		corp.SignIn(map[string]any{
			"preferred_username": user,
			"email":              user + "@example.com",
			"groups": []string{
				"admins",
			},
		})

		if got, _, err := signInWith(t, s, "corp", "/"); err != nil || got != user {
			t.Fatalf("%s signing in with corp = %q %v", user, got, err)
		}

		rogue.SignIn(map[string]any{
			"preferred_username": user,
			"email":              "attacker@example.com",
			"groups":             []string{},
		})

		if _, _, err := signInWith(t, s, "rogue", "/"); !errors.Is(err, sso.ErrRefused) {
			t.Errorf("another provider signed in %s, whose account belongs to corp: %v", user, err)
		}

		account, err := db.GetUserByUsername(ctx, user)
		if err != nil || account.Email != user+"@example.com" ||
			!slices.Contains(groups(t, db, user), "admins") {
			t.Errorf(
				"%s after the other provider's attempt = %+v %v, groups %v",
				user,
				account,
				err,
				groups(t, db, user),
			)
		}
	}

	if err := s.DeleteProvider(ctx, "corp"); err != nil {
		t.Fatal(err)
	}

	rogue.SignIn(map[string]any{
		"preferred_username": "alice",
	})

	if got, _, err := signInWith(t, s, "rogue", "/"); err != nil || got != "alice" {
		t.Errorf("alice signing in with rogue after corp was deleted = %q %v", got, err)
	}
}
