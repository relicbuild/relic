//go:build integration

package databasetest

import (
	"runtime"
	"testing"
	"uuid"

	"relic/internal/platform/database"

	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) { Main(m) }

func TestNewDBReturnsSeparateMigratedDatabases(t *testing.T) {
	t.Parallel()

	first := NewDB(t)
	second := NewDB(t)

	err := first.CreateUser(t.Context(), database.CreateUserParams{
		ID:           uuid.NewV7(),
		Username:     "only-in-first",
		Email:        "first@example.com",
		PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, db := range map[string]*database.DB{
		"first":  first,
		"second": second,
	} {
		want := int64(0)
		if name == "first" {
			want = 1
		}

		got, err := db.CountUsers(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if got != want {
			t.Errorf("%s has %d users, want %d", name, got, want)
		}
	}
}

func TestPostgreSQLAcceptsTheConnectionsOfParallelTests(t *testing.T) {
	t.Parallel()
	RequirePostgreSQL(t)
	NewDB(t)

	admin, err := pgx.Connect(t.Context(), adminURL)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = admin.Close(t.Context())
	}()

	var limit int
	if err := admin.QueryRow(t.Context(), "SELECT current_setting('max_connections')::int").Scan(&limit); err != nil {
		t.Fatal(err)
	}

	if want := runtime.GOMAXPROCS(0) * max(4, runtime.NumCPU()); limit < want {
		t.Errorf("the server accepts %d connections; the parallel tests' pools can open %d", limit, want)
	}
}
