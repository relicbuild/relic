//go:build integration

package security_test

import (
	"log/slog"
	"testing"
	"time"
	"uuid"

	"relic/internal/access/security"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestDeleteUserRevokesTheirTokens(t *testing.T) {
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

	refresh := "refresh-hash"
	token := uuid.NewV7()

	if err := db.InsertAccessToken(ctx, database.InsertAccessTokenParams{
		ID:               token,
		Username:         "alice",
		ScopeKind:        "user",
		ScopeGroups:      []string{},
		RefreshTokenHash: &refresh,
	}); err != nil {
		t.Fatal(err)
	}

	if err := security.NewService(db, slog.New(slog.DiscardHandler)).DeleteUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetAccessToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	if !got.Revoked || got.RefreshTokenHash != nil {
		t.Errorf(
			"token of the deleted user = revoked %v, refresh hash %v; want revoked without one",
			got.Revoked,
			got.RefreshTokenHash,
		)
	}
}

func TestDeleteUserWaitsForOtherAdministratorChanges(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	for _, name := range []string{
		"first-admin",
		"second-admin",
	} {
		if err := db.CreateUser(ctx, database.CreateUserParams{
			ID:           uuid.NewV7(),
			Username:     name,
			PasswordHash: "x",
			Admin:        true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	held := make(chan struct{})
	unlock := make(chan struct{})
	lockErr := make(chan error, 1)

	go func() {
		lockErr <- db.WithLock(ctx, "security:administrators", func() error {
			close(held)
			<-unlock

			return nil
		})
	}()

	<-held

	deleted := make(chan error, 1)

	go func() {
		deleted <- security.NewService(db, slog.New(slog.DiscardHandler)).DeleteUser(ctx, "first-admin")
	}()

	select {
	case err := <-deleted:
		t.Errorf("DeleteUser of an administrator went ahead while the administrators were locked (err %v)", err)
		close(unlock)
	case <-time.After(2 * time.Second):
		close(unlock)

		if err := <-deleted; err != nil {
			t.Fatal(err)
		}
	}

	if err := <-lockErr; err != nil {
		t.Fatal(err)
	}
}
