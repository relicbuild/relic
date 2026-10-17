package database_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"relic/internal/platform/database"
)

func TestOpeningAMissingSQLiteDatabaseThatMustExistCreatesNothing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ghost.db")

	db, err := database.OpenWith(t.Context(), "sqlite://"+path, database.Options{
		MustExist: true,
	})
	if err == nil {
		db.Close()
		t.Fatal("opened a database that does not exist")
	}

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want one that says the database does not exist", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the missing database was created: %v", err)
	}
}

func TestOpeningAnExistingSQLiteDatabaseThatMustExist(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "relic.db")

	created, err := database.Open(t.Context(), "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}

	created.Close()

	db, err := database.OpenWith(t.Context(), "sqlite://"+path, database.Options{
		MustExist: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	db.Close()
}
