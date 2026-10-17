//go:build integration

package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
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
