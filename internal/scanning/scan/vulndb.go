package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"uuid"

	"relic/internal/platform/objectstore"

	"go.opentelemetry.io/otel/trace"
)

// DatabaseImportPrefix is where an uploaded vulnerability database waits in
// object storage until the worker, which holds the database, installs it.
const DatabaseImportPrefix = "scan/vulnerability-database/"

// MaxDatabaseArchive bounds an uploaded vulnerability database archive.
const MaxDatabaseArchive = 4 << 30

// ErrInvalidDatabaseImport is an import key that names no staged archive.
var ErrInvalidDatabaseImport = errors.New("scan: not a staged vulnerability database")

// DatabaseImports moves vulnerability database archives (Grype's `db
// import` format) from an upload on the server to the worker.
type DatabaseImports struct {
	blobs objectstore.Store
	log   *slog.Logger
}

// NewDatabaseImports returns DatabaseImports over the object store.
func NewDatabaseImports(blobs objectstore.Store, log *slog.Logger) *DatabaseImports {
	return &DatabaseImports{
		blobs: blobs,
		log:   log,
	}
}

// Stage stores an uploaded archive and returns the key the worker installs
// it from.
func (d *DatabaseImports) Stage(ctx context.Context, r io.Reader, size int64) (string, error) {
	key := DatabaseImportPrefix + uuid.NewV7().String() + ".tar"
	if err := d.blobs.Put(ctx, key, r, size); err != nil {
		return "", fmt.Errorf("stage vulnerability database: %w", err)
	}

	return key, nil
}

// Discard removes a staged archive that will not be installed.
func (d *DatabaseImports) Discard(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, DatabaseImportPrefix) || strings.Contains(key[len(DatabaseImportPrefix):], "/") {
		return ErrInvalidDatabaseImport
	}

	return d.blobs.Delete(ctx, key)
}

// Install imports a staged archive into Grype's database and removes it
// from object storage; a failed removal is logged and recorded on the
// trace, not returned, since the database is installed.
func (d *DatabaseImports) Install(ctx context.Context, g Grype, key string) error {
	if !strings.HasPrefix(key, DatabaseImportPrefix) || strings.Contains(key[len(DatabaseImportPrefix):], "/") {
		return ErrInvalidDatabaseImport
	}

	body, _, err := d.blobs.Get(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("read staged vulnerability database: %w", err)
	}

	defer func() {
		_ = body.Close()
	}()

	tmp, err := os.CreateTemp("", "relic-vulnerability-database-*.tar")
	if err != nil {
		return err
	}

	defer func() {
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, body); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("read staged vulnerability database: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := g.ImportDB(ctx, tmp.Name()); err != nil {
		return fmt.Errorf("import vulnerability database: %w", err)
	}

	if err := d.blobs.Delete(ctx, key); err != nil {
		trace.SpanFromContext(ctx).RecordError(fmt.Errorf("remove the staged vulnerability database: %w", err))
		d.log.WarnContext(
			ctx,
			"vulnerability database installed; its staged archive stays in object storage",
			"key", key,
			"error", err,
		)
	}

	return nil
}
