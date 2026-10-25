package scan

import (
	"bytes"
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

// ErrNotDatabaseArchive is an upload that is no archive Grype imports.
var ErrNotDatabaseArchive = errors.New(
	"not a vulnerability database archive: Grype imports a .tar.zst, .tar.gz, .tar.xz or .tar file",
)

// DatabaseImportJob is the kind of the job that installs an imported vulnerability database.
const DatabaseImportJob = "scan_db_import"

var archiveMagic = [][]byte{
	{
		0x28,
		0xb5,
		0x2f,
		0xfd,
	},
	{
		0x1f,
		0x8b,
	},
	[]byte("\xfd7zXZ\x00"),
}

const tarMagicOffset = 257

// DatabaseArchive returns r whole if it starts like an archive Grype imports, and ErrNotDatabaseArchive otherwise.
func DatabaseArchive(r io.Reader) (io.Reader, error) {
	head := make([]byte, tarMagicOffset+len("ustar"))

	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read the vulnerability database archive: %w", err)
	}

	head = head[:n]

	known := len(head) == tarMagicOffset+len("ustar") && string(head[tarMagicOffset:]) == "ustar"
	for _, magic := range archiveMagic {
		known = known || bytes.HasPrefix(head, magic)
	}

	if !known {
		return nil, ErrNotDatabaseArchive
	}

	return io.MultiReader(bytes.NewReader(head), r), nil
}

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
