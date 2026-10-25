// Package databasetest provides real databases for integration tests.
package databasetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"relic/internal/platform/database"
	"relic/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the PostgreSQL image used for tests (matches deploy/).
const Image = "postgres:18-alpine"

// SharedServer is the environment variable naming a PostgreSQL server
// (an admin URL, user and password relic) that every test process shares
// instead of starting its own container; its databases share one template
// per migration set.
const SharedServer = "RELIC_TEST_POSTGRESQL_URL"

const (
	templateDatabase = "relic_template"
	sqliteFile       = "relic.db"
)

var (
	once      sync.Once
	container *postgres.PostgresContainer
	adminURL  string
	template  string
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
func NewDB(t testing.TB) *database.DB {
	t.Helper()
	return newDB(t, 0)
}

// NewDBAt returns an empty database migrated up to version only.
func NewDBAt(t testing.TB, version int64) *database.DB {
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

func newDB(t testing.TB, version int64) *database.DB {
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

	name := fmt.Sprintf("test_%d_%d", os.Getpid(), counter.Add(1))

	create := "CREATE DATABASE " + name
	if version == 0 {
		create += " TEMPLATE " + template
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
		if _, err := db.MigrateTo(ctx, version); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}

	return db
}

func startPostgreSQL(ctx context.Context) error {
	if shared := os.Getenv(SharedServer); shared != "" {
		adminURL = shared

		return sharedTemplate(ctx)
	}

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

	template = templateDatabase

	return createTemplate(ctx, template)
}

// sharedTemplate names the template of the current migrations on the shared
// server, creating it when no process has: under an advisory lock, so one
// process migrates it and the others wait.
func sharedTemplate(ctx context.Context) error {
	sum := sha256.New()

	if err := fs.WalkDir(migrations.PostgreSQL, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := fs.ReadFile(migrations.PostgreSQL, p)
		if err != nil {
			return err
		}

		sum.Write([]byte(p))
		sum.Write(b)

		return nil
	}); err != nil {
		return err
	}

	template = templateDatabase + "_" + hex.EncodeToString(sum.Sum(nil))[:16]

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}

	defer func() {
		_ = admin.Close(context.WithoutCancel(ctx))
	}()

	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", template); err != nil {
		return err
	}

	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", template).
		Scan(&exists); err != nil {
		return err
	}

	if !exists {
		if err := createTemplate(ctx, template); err != nil {
			_ = adminExec(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+template)

			return err
		}
	}

	_, err = admin.Exec(ctx, "SELECT pg_advisory_unlock(hashtext($1))", template)

	return err
}

// createTemplate creates the database name with every migration applied.
func createTemplate(ctx context.Context, name string) error {
	if err := adminExec(ctx, "CREATE DATABASE "+name); err != nil {
		return fmt.Errorf("create template: %w", err)
	}

	url, err := databaseURL(name)
	if err != nil {
		return err
	}

	db, err := database.Open(ctx, url)
	if err != nil {
		return fmt.Errorf("open template: %w", err)
	}

	defer db.Close()

	if _, err := db.Migrate(ctx); err != nil {
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

func newSQLiteDB(t testing.TB, version int64) *database.DB {
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
		if _, err := db.MigrateTo(ctx, version); err != nil {
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

	_, err = db.Migrate(ctx)

	db.Close()

	return dir, err
}
