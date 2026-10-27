//go:build integration

package sso

import (
	"errors"
	"log/slog"
	"strings"
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
	if !errors.Is(err, ErrConflict) || !errors.Is(err, security.ErrExists) {
		t.Errorf("SCIM creating ALICE = %v, want %v and %v", err, ErrConflict, security.ErrExists)
	}

	if err != nil && strings.Count(err.Error(), "already exists") != 1 {
		t.Errorf("SCIM creating ALICE = %q, want one %q", err, "already exists")
	}

	if _, err := db.GetUserByUsername(ctx, "Alice"); err == nil {
		t.Error("Alice was created next to alice")
	}
}

func TestProvisionStoresNoInvalidEmail(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	s := New(db, nil, slog.New(slog.DiscardHandler))
	corp := Provider{
		Name:            "corp",
		Enabled:         true,
		AutoCreateUsers: true,
	}

	steps := []struct {
		name      string
		email     string
		wantEmail string
	}{
		{
			name:      "a new user with an invalid email",
			email:     "not-an-email",
			wantEmail: "",
		},
		{
			name:      "a valid email",
			email:     "carol@example.com",
			wantEmail: "carol@example.com",
		},
		{
			name:      "an invalid email for a user who has one",
			email:     "Carol <carol@example.com>",
			wantEmail: "carol@example.com",
		},
	}
	for _, step := range steps {
		if _, err := s.provision(ctx, corp, identity{
			username: "carol",
			email:    step.email,
		}); err != nil {
			t.Fatalf("%s: provision = %v", step.name, err)
		}

		u, err := db.GetUserByUsername(ctx, "carol")
		if err != nil {
			t.Fatal(err)
		}

		if u.Email != step.wantEmail {
			t.Errorf("%s: email = %q, want %q", step.name, u.Email, step.wantEmail)
		}
	}
}

func TestDirectoryRefusesInvalidEmails(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dir := NewDirectory(databasetest.NewDB(t), nil)

	if _, err := dir.CreateUser(ctx, DirectoryUser{
		UserName: "gina",
		Email:    "not-an-email",
		Active:   true,
	}); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("CreateUser with an invalid email = %v, want %v", err, ErrInvalidValue)
	}

	gina, err := dir.CreateUser(ctx, DirectoryUser{
		UserName: "gina",
		Email:    "gina@example.com",
		Active:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	invalid := gina
	invalid.Email = "gina at example.com"

	if _, err := dir.UpdateUser(ctx, invalid); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("UpdateUser with an invalid email = %v, want %v", err, ErrInvalidValue)
	}

	if _, err := dir.PatchUser(ctx, gina, invalid); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("PatchUser with an invalid email = %v, want %v", err, ErrInvalidValue)
	}

	got, err := dir.User(ctx, gina.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Email != "gina@example.com" {
		t.Errorf("email = %q, want the one created", got.Email)
	}
}
