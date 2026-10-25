//go:build integration

package database_test

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestMain(m *testing.M) { databasetest.Main(m) }

func TestMigrateConcurrently(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDBAt(t, math.MaxInt64)

	var wg sync.WaitGroup

	errs := make(chan error, 8)

	for range 8 {
		wg.Go(func() {
			_, err := db.Migrate(t.Context())
			errs <- err
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent migrate: %v", err)
		}
	}
}

func TestMigrateReportsTheAppliedMigrations(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDBAt(t, 1)

	applied, err := db.Migrate(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(applied) == 0 || applied[0].Version <= 1 || applied[0].Name == "" {
		t.Fatalf("applied = %+v, want the migrations after version 1", applied)
	}

	for i := 1; i < len(applied); i++ {
		if applied[i].Version <= applied[i-1].Version {
			t.Errorf("applied out of order: %+v", applied)
		}
	}

	again, err := db.Migrate(t.Context())
	if err != nil || len(again) != 0 {
		t.Errorf("migrating a migrated database = %+v, %v; want none applied", again, err)
	}
}

func TestMigrateRefusesASchemaNewerThanTheBinary(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDB(t)

	rows, err := db.Query(
		t.Context(),
		"INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, true) RETURNING version_id",
	)
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	_, err = db.Migrate(t.Context())

	newer, ok := errors.AsType[*database.NewerSchemaError](err)
	if !ok {
		t.Fatalf("Migrate on a newer schema = %v, want a NewerSchemaError", err)
	}

	if newer.Database != 99999 || newer.Binary <= 1 || newer.Binary >= newer.Database {
		t.Errorf("NewerSchemaError = %+v", newer)
	}

	for _, want := range []string{
		"99999",
		strconv.FormatInt(newer.Binary, 10),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name version %s", err, want)
		}
	}
}
