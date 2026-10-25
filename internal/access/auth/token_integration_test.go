//go:build integration

package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestTokensRevoke(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tokens.Issue(ctx, auth.IssueParams{
		Username: "alice",
		Scope:    auth.UserScope,
	}); !errors.Is(err, auth.ErrUnknownUser) {
		t.Fatalf("token for a user who does not exist: %v", err)
	}

	if err := db.CreateUser(ctx, database.CreateUserParams{
		ID:           uuid.NewV7(),
		Username:     "alice",
		PasswordHash: "!",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tokens.Issue(ctx, auth.IssueParams{
		Username: "alice",
		Scope: auth.TokenScope{
			Kind: auth.ScopeGroups,
			Groups: []string{
				"latergroup",
			},
		},
	}); !errors.Is(err, auth.ErrUnknownGroup) {
		t.Fatalf("token for a group that does not exist: %v, want %v", err, auth.ErrUnknownGroup)
	}

	issued, err := tokens.Issue(ctx, auth.IssueParams{
		Username: "alice",
		Scope:    auth.UserScope,
	})
	if err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if err := tokens.Revoke(cancelled, issued.AccessToken); err == nil {
		t.Error("Revoke reported success although the token could not be looked up")
	}

	if _, err := tokens.Verify(ctx, issued.AccessToken); err != nil {
		t.Fatalf("token after a failed revoke: %v", err)
	}

	if err := tokens.Revoke(ctx, issued.AccessToken); err != nil {
		t.Fatal(err)
	}

	if _, err := tokens.Verify(ctx, issued.AccessToken); err == nil {
		t.Error("a revoked token still verifies")
	}

	if err := tokens.Revoke(ctx, issued.AccessToken); err != nil {
		t.Errorf("revoking a revoked token: %v", err)
	}
}

func TestRevokingATokenRevokesTheTokensItBound(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.CreateUser(ctx, database.CreateUserParams{
		ID:           uuid.NewV7(),
		Username:     "alice",
		PasswordHash: "!",
	}); err != nil {
		t.Fatal(err)
	}

	mint := func(parent auth.IssuedToken, refreshable bool) auth.IssuedToken {
		t.Helper()

		p := auth.Principal{
			Username: "alice",
		}

		if parent.AccessToken != "" {
			verified, err := tokens.Verify(ctx, parent.AccessToken)
			if err != nil {
				t.Fatal(err)
			}

			p.BoundBy, p.BoundUntil = verified.ID, verified.Expires
		}

		issued, err := tokens.Issue(ctx, p.LimitIssue(auth.IssueParams{
			Username:    "alice",
			Scope:       auth.UserScope,
			ExpiresIn:   time.Hour,
			Refreshable: refreshable,
		}, time.Now()))
		if err != nil {
			t.Fatal(err)
		}

		return issued
	}
	valid := func(tok auth.IssuedToken) bool {
		_, err := tokens.Verify(ctx, tok.AccessToken)
		return err == nil
	}

	parent := mint(auth.IssuedToken{}, true)
	child := mint(parent, true)
	grandchild := mint(child, false)
	other := mint(auth.IssuedToken{}, false)

	if child.ExpiresIn > time.Hour {
		t.Errorf("a token minted with a token = %+v, want it within the parent's life", child)
	}

	refreshed, err := tokens.Refresh(ctx, parent.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	if !valid(child) || !valid(grandchild) {
		t.Fatal("refreshing the parent revoked the tokens it bound")
	}

	if err := tokens.Revoke(ctx, refreshed.AccessToken); err != nil {
		t.Fatal(err)
	}

	if !valid(child) || !valid(grandchild) {
		t.Fatal("signing out with the parent revoked the tokens it bound")
	}

	if _, err := tokens.Refresh(ctx, child.RefreshToken); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("refresh of a token whose parent is revoked = %v, want %v", err, auth.ErrInvalidToken)
	}

	if err := tokens.RevokeID(ctx, child.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	if valid(child) || valid(grandchild) {
		t.Error("tokens bound by a revoked token, directly or through another, are still valid")
	}

	if !valid(other) {
		t.Error("revoking one token revoked an unrelated one")
	}
}

func TestTokensRefreshKeepsTheOldTokenWhenItFails(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte("refresh-secret"))
	refreshHash := hex.EncodeToString(sum[:])
	id := uuid.NewV7()

	if err := db.InsertAccessToken(ctx, database.InsertAccessTokenParams{
		ID:               id,
		Username:         "alice",
		ScopeKind:        "retired-scope",
		ScopeGroups:      []string{},
		RefreshTokenHash: &refreshHash,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tokens.Refresh(ctx, "refresh-secret"); err == nil {
		t.Fatal("refresh of a token with an unknown scope succeeded")
	}

	row, err := db.GetAccessToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if row.Revoked || row.RefreshTokenHash == nil {
		t.Error("a failed refresh revoked the old token")
	}
}

func TestWebLoginsApproveAnUppercaseSession(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	logins := auth.NewWebLogins(db, tokens)
	session := strings.ToUpper(uuid.NewV4().String())

	if err := logins.Request(ctx, session); err != nil {
		t.Fatal(err)
	}

	code := strings.ToLower(session[len(session)-4:])
	if err := logins.Approve(ctx, session, code, "alice"); err != nil {
		t.Fatalf("approve with the code the server handed out: %v", err)
	}

	if _, err := logins.Poll(ctx, session, "relic CLI"); err != nil {
		t.Errorf("poll after approval: %v", err)
	}
}

func TestWebLoginsPollKeepsTheSessionWhenTheTokenFails(t *testing.T) {
	t.Parallel()

	if databasetest.SQLite() {
		t.Skip("SQLite stores a NUL in text, so the token insert does not fail")
	}

	ctx := t.Context()
	db := databasetest.NewDB(t)

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	logins := auth.NewWebLogins(db, tokens)
	session := uuid.NewV4().String()

	if err := logins.Request(ctx, session); err != nil {
		t.Fatal(err)
	}

	if err := logins.Approve(ctx, session, session[len(session)-4:], "alice"); err != nil {
		t.Fatal(err)
	}

	if _, err := logins.Poll(ctx, session, "relic\x00CLI"); err == nil {
		t.Fatal("a token whose description the database refuses was issued")
	}

	if _, err := logins.Poll(ctx, session, "relic CLI"); err != nil {
		t.Errorf("poll after a failed one: %v, want the approved session still there", err)
	}
}
