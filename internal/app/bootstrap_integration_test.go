//go:build integration

package app

import (
	"errors"
	"log/slog"
	"testing"

	"relic/internal/access/security"
	"relic/internal/platform/database/databasetest"
)

func TestBootstrapAdminRefusesAnInvalidUsername(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)

	err := BootstrapAdmin(t.Context(), db, "first admin", "Admin-pass-12345", slog.New(slog.DiscardHandler))
	if !errors.Is(err, security.ErrInvalid) {
		t.Fatalf("BootstrapAdmin(%q) = %v, want %v", "first admin", err, security.ErrInvalid)
	}

	if n, err := db.CountUsers(t.Context()); err != nil || n != 0 {
		t.Errorf("users after a refused bootstrap = %d %v, want none", n, err)
	}
}

func TestBootstrapAdminRefusesAShortPassword(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)

	err := BootstrapAdmin(t.Context(), db, "admin", "short", slog.New(slog.DiscardHandler))
	if !errors.Is(err, security.ErrInvalid) {
		t.Fatalf("BootstrapAdmin with a 5-character password = %v, want %v", err, security.ErrInvalid)
	}

	if n, err := db.CountUsers(t.Context()); err != nil || n != 0 {
		t.Errorf("users after a refused bootstrap = %d %v, want none", n, err)
	}
}
