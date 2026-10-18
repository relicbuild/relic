//go:build integration

package sso

import (
	"errors"
	"testing"
	"uuid"

	"relic/internal/access/security"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestCreateUserRefusesANameTakenByAnotherRealm(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	if err := db.CreateUser(ctx, database.CreateUserParams{
		ID:           uuid.NewV7(),
		Username:     "alice",
		PasswordHash: "x",
	}); err != nil {
		t.Fatal(err)
	}

	var refusal *RefusedError
	if err := createUser(ctx, db, identity{
		username: "alice",
	}, "corp", false); !errors.As(err, &refusal) {
		t.Errorf("createUser over an internal account = %v, want a refusal", err)
	}

	for range 2 {
		if err := createUser(ctx, db, identity{
			username: "bob",
		}, "corp", false); err != nil {
			t.Errorf("createUser of a single sign-on account = %v", err)
		}
	}
}

func TestCreateUserRefusesANameThatDiffersOnlyByCase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	if err := db.CreateUser(ctx, database.CreateUserParams{
		ID:           uuid.NewV7(),
		Username:     "alice",
		PasswordHash: "x",
	}); err != nil {
		t.Fatal(err)
	}

	if err := createUser(ctx, db, identity{
		username: "Alice",
	}, "corp", false); !errors.Is(err, security.ErrExists) {
		t.Errorf("createUser(Alice) = %v, want %v", err, security.ErrExists)
	}

	_, err := NewDirectory(db, nil).CreateUser(ctx, DirectoryUser{
		UserName: "ALICE",
		Active:   true,
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("SCIM creating ALICE = %v, want %v", err, ErrConflict)
	}

	if _, err := db.GetUserByUsername(ctx, "Alice"); err == nil {
		t.Error("Alice was created next to alice")
	}
}
