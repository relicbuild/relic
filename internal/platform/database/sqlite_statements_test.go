package database

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"
)

func newSQLiteTestDB(tb testing.TB) *DB {
	tb.Helper()

	db, err := Open(tb.Context(), "sqlite://"+filepath.Join(tb.TempDir(), "relic.db"))
	if err != nil {
		tb.Fatal(err)
	}

	tb.Cleanup(db.Close)

	if _, err := db.Migrate(tb.Context()); err != nil {
		tb.Fatal(err)
	}

	return db
}

func seedSQLiteItem(tb testing.TB, db *DB) GetItemParams {
	tb.Helper()

	ctx := tb.Context()
	digest := strings.Repeat("a", 64)
	sha1 := strings.Repeat("b", 40)
	md5 := strings.Repeat("c", 32)

	if err := db.CreateRepository(ctx, CreateRepositoryParams{
		Key:     "libs",
		Class:   "local",
		Format:  "generic",
		Project: "default",
		Config:  []byte("{}"),
	}); err != nil {
		tb.Fatal(err)
	}

	if err := db.AcquireBlob(ctx, AcquireBlobParams{
		Sha256: digest,
		Sha1:   sha1,
		Md5:    md5,
		Size:   1,
	}); err != nil {
		tb.Fatal(err)
	}

	if _, err := db.InsertFile(ctx, InsertFileParams{
		ID:       uuid.New(),
		RepoKey:  "libs",
		Path:     "org/app",
		Name:     "app.jar",
		Size:     1,
		Sha1:     &sha1,
		Sha256:   &digest,
		Md5:      &md5,
		Now:      time.Now(),
		Username: "admin",
	}); err != nil {
		tb.Fatal(err)
	}

	return GetItemParams{
		RepoKey: "libs",
		Path:    "org/app",
		Name:    "app.jar",
	}
}

func TestSQLiteQueriesReuseOnePreparedStatementEach(t *testing.T) {
	t.Parallel()

	db := newSQLiteTestDB(t)
	item := seedSQLiteItem(t, db)
	engine := db.engine.(*sqliteEngine)
	before := engine.reads.prepared()

	for range 3 {
		if _, err := db.GetItem(t.Context(), item); err != nil {
			t.Fatal(err)
		}

		if _, err := db.GetRepository(t.Context(), "libs"); err != nil {
			t.Fatal(err)
		}
	}

	if got := engine.reads.prepared() - before; got != 2 {
		t.Errorf("three calls of two queries prepared %d statements, want 2", got)
	}
}

func TestSQLiteTransactionsUseThePreparedStatements(t *testing.T) {
	t.Parallel()

	db := newSQLiteTestDB(t)
	item := seedSQLiteItem(t, db)
	engine := db.engine.(*sqliteEngine)
	before := engine.writes.prepared()

	for range 3 {
		if err := db.WithTx(t.Context(), func(q Querier) error {
			if _, err := q.GetItem(t.Context(), item); err != nil {
				return err
			}

			_, err := q.DeleteItemByID(t.Context(), uuid.New())

			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	if got := engine.writes.prepared() - before; got != 2 {
		t.Errorf("three transactions of two queries prepared %d statements, want 2", got)
	}
}

func BenchmarkSQLiteGetRepository(b *testing.B) {
	db := newSQLiteTestDB(b)
	seedSQLiteItem(b, db)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := db.GetRepository(b.Context(), "libs"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLiteGetItem(b *testing.B) {
	db := newSQLiteTestDB(b)
	item := seedSQLiteItem(b, db)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := db.GetItem(b.Context(), item); err != nil {
			b.Fatal(err)
		}
	}
}
