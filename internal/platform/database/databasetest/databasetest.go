// Package databasetest provides real databases for integration tests.
package databasetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"relic/internal/platform/database"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the PostgreSQL image used for tests (matches deploy/).
const Image = "postgres:18-alpine"

const (
	templateDatabase = "relic_template"
	sqliteFile       = "relic.db"
)

var (
	once      sync.Once
	container *postgres.PostgresContainer
	adminURL  string
	startErr  error
	counter   atomic.Int64

	sqliteOnce     sync.Once
	sqliteTemplate string
	sqliteErr      error
)

// Main runs m, stopping the shared container afterwards. Call it from TestMain.
func Main(m *testing.M) {
	code := m.Run()

	if container != nil {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintln(os.Stderr, "storetest: terminate:", err)
		}
	}

	if sqliteTemplate != "" {
		_ = os.RemoveAll(sqliteTemplate)
	}

	os.Exit(code)
}

// NewDB returns a migrated, empty database that is dropped when t ends.
func NewDB(t *testing.T) *database.DB {
	t.Helper()
	return newDB(t, 0)
}

// NewDBAt returns an empty database migrated up to version only.
func NewDBAt(t *testing.T, version int64) *database.DB {
	t.Helper()
	return newDB(t, version)
}

// SQLite reports whether the tests run on SQLite (RELIC_TEST_DATABASE=sqlite).
func SQLite() bool {
	return os.Getenv("RELIC_TEST_DATABASE") == "sqlite"
}

// RequirePostgreSQL skips t on SQLite.
func RequirePostgreSQL(t *testing.T) {
	t.Helper()

	if SQLite() {
		t.Skip("PostgreSQL only")
	}
}

func newDB(t *testing.T, version int64) *database.DB {
	t.Helper()

	if SQLite() {
		return newSQLiteDB(t, version)
	}

	ctx := t.Context()

	once.Do(func() {
		startErr = startPostgreSQL(context.Background())
	})

	if startErr != nil {
		t.Fatalf("start postgres: %v", startErr)
	}

	name := fmt.Sprintf("test_%d", counter.Add(1))

	create := "CREATE DATABASE " + name
	if version == 0 {
		create += " TEMPLATE " + templateDatabase
	}

	if err := adminExec(ctx, create); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_ = adminExec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	url, err := databaseURL(name)
	if err != nil {
		t.Fatal(err)
	}

	db, err := database.Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(db.Close)

	if version > 0 {
		if err := db.MigrateTo(ctx, version); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}

	return db
}

func startPostgreSQL(ctx context.Context) error {
	var err error

	container, err = postgres.Run(
		ctx,
		Image,
		postgres.WithDatabase("relic"),
		postgres.WithUsername("relic"),
		postgres.WithPassword("relic"),
		postgres.BasicWaitStrategies(),
		testcontainers.WithCmdArgs("-c", "max_connections="+strconv.Itoa(postgreSQLMaxConnections())),
	)
	if err != nil {
		return err
	}

	adminURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return err
	}

	if err := adminExec(ctx, "CREATE DATABASE "+templateDatabase); err != nil {
		return fmt.Errorf("create template: %w", err)
	}

	url, err := databaseURL(templateDatabase)
	if err != nil {
		return err
	}

	db, err := database.Open(ctx, url)
	if err != nil {
		return fmt.Errorf("open template: %w", err)
	}

	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}

	return nil
}

func postgreSQLMaxConnections() int {
	connectionsPerTest := max(4, runtime.NumCPU()) + 1

	return runtime.GOMAXPROCS(0)*connectionsPerTest + 100
}

func adminExec(ctx context.Context, statement string) error {
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}

	defer func() {
		_ = admin.Close(context.WithoutCancel(ctx))
	}()

	_, err = admin.Exec(ctx, statement)

	return err
}

func databaseURL(name string) (string, error) {
	cfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("postgres://relic:relic@%s:%d/%s?sslmode=disable", cfg.Host, cfg.Port, name), nil
}

func newSQLiteDB(t *testing.T, version int64) *database.DB {
	t.Helper()

	ctx := t.Context()
	dir := t.TempDir()

	if version == 0 {
		sqliteOnce.Do(func() {
			sqliteTemplate, sqliteErr = migratedSQLite(context.Background())
		})

		if sqliteErr != nil {
			t.Fatalf("sqlite template: %v", sqliteErr)
		}

		if err := os.CopyFS(dir, os.DirFS(sqliteTemplate)); err != nil {
			t.Fatalf("copy template: %v", err)
		}
	}

	db, err := database.Open(ctx, "sqlite://"+filepath.Join(dir, sqliteFile))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(db.Close)

	if version > 0 {
		if err := db.MigrateTo(ctx, version); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}

	return db
}

func migratedSQLite(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "relic-sqlite-template-")
	if err != nil {
		return "", err
	}

	db, err := database.Open(ctx, "sqlite://"+filepath.Join(dir, sqliteFile))
	if err != nil {
		return dir, err
	}

	err = db.Migrate(ctx)

	db.Close()

	return dir, err
}
