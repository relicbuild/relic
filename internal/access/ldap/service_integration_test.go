//go:build integration

package ldap

import (
	"errors"
	"log/slog"
	"testing"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestProvisionRefusesNewNamesRelicDoesNotAccept(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
	}{
		{
			name:     "differs from an existing user only by case",
			username: "Alice",
		},
		{
			name:     "a space",
			username: "build bot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			s := NewService(db, nil, nil, slog.New(slog.DiscardHandler))

			err := s.provision(ctx, Server{
				Name:           "corp",
				AutoCreateUser: true,
			}, tt.username, Entry{
				Email: "user@example.org",
			})
			if !errors.Is(err, auth.ErrBadCredentials) {
				t.Errorf("provision(%q) = %v, want %v", tt.username, err, auth.ErrBadCredentials)
			}

			if _, err := db.GetUserByUsername(ctx, tt.username); err == nil {
				t.Errorf("provision created %q", tt.username)
			}
		})
	}
}
