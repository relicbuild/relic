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

func TestPutLdapServerKeepsTheManagerPasswordOnItsHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		url          string
		password     string
		wantErr      error
		wantPassword string
	}{
		{
			name:         "the same host, another base",
			url:          "ldap://directory-a.example.com:389/dc=example,dc=com",
			wantPassword: "stored-secret",
		},
		{
			name:    "another host",
			url:     "ldap://directory-b.example.com:389/dc=example,dc=org",
			wantErr: ErrInvalid,
		},
		{
			name:    "another port",
			url:     "ldap://directory-a.example.com:3899/dc=example,dc=org",
			wantErr: ErrInvalid,
		},
		{
			name:         "another host with the password given again",
			url:          "ldap://directory-b.example.com:389/dc=example,dc=org",
			password:     "new-secret",
			wantPassword: "new-secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			s := NewService(databasetest.NewDB(t), nil, nil, slog.New(slog.DiscardHandler))
			server := func(url, password string) Server {
				return Server{
					Name:          "corp",
					Enabled:       true,
					URL:           url,
					UserDNPattern: "uid={0},ou=people",
					Search: Search{
						ManagerDN:       "cn=admin,dc=example,dc=org",
						ManagerPassword: password,
					},
				}
			}

			if err := s.PutLdapServer(
				ctx,
				server("ldap://directory-a.example.com:389/dc=example,dc=org", "stored-secret"),
			); err != nil {
				t.Fatal(err)
			}

			if tt.wantErr != nil {
				if _, err := s.Test(
					ctx,
					server(tt.url, tt.password),
					"alice",
					"Alice-pass-1",
				); !errors.Is(
					err,
					tt.wantErr,
				) {
					t.Errorf("Test against %s = %v, want %v", tt.url, err, tt.wantErr)
				}
			}

			err := s.PutLdapServer(ctx, server(tt.url, tt.password))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("PutLdapServer at %s = %v, want %v", tt.url, err, tt.wantErr)
			}

			stored, err := s.Server(ctx, "corp")
			if err != nil {
				t.Fatal(err)
			}

			if tt.wantErr == nil && stored.Search.ManagerPassword != tt.wantPassword {
				t.Errorf("stored manager password = %q, want %q", stored.Search.ManagerPassword, tt.wantPassword)
			}
		})
	}
}
