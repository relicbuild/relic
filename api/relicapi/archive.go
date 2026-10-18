package relicapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/archive"
	"relic/internal/artifacts/repository"
)

func archiveItem(ctx context.Context, d Dependencies, key, p string) (repository.Item, string, error) {
	if err := can(ctx, d, key, p, auth.ActionRead); err != nil {
		return repository.Item{}, "", err
	}

	it, err := d.Repositories.Stat(ctx, key, p)
	if err != nil {
		return repository.Item{}, "", err
	}

	kind := archive.KindOf(it.Name)
	if it.IsFolder() || kind == "" {
		return repository.Item{}, "", connect.NewError(connect.CodeInvalidArgument, "not an archive")
	}

	return it, kind, nil
}

type archiveStorageError struct {
	err error
}

func (e archiveStorageError) Error() string { return e.err.Error() }

func (e archiveStorageError) Unwrap() error { return e.err }

type storageReaderAt struct {
	r io.ReaderAt
}

func (s storageReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.r.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, archiveStorageError{
			err: err,
		}
	}

	return n, err
}

type storageReader struct {
	r io.Reader
}

func (s storageReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, archiveStorageError{
			err: err,
		}
	}

	return n, err
}

func withArchive(
	ctx context.Context,
	d Dependencies,
	it repository.Item,
	kind string,
	fn func(io.ReaderAt, io.Reader) error,
) error {
	if kind == archive.Zip {
		return fn(storageReaderAt{
			r: d.Repositories.ReaderAt(ctx, it),
		}, nil)
	}

	rc, err := d.Repositories.Open(ctx, it, nil)
	if err != nil {
		return archiveStorageError{
			err: err,
		}
	}

	defer func() {
		_ = rc.Close()
	}()

	return fn(nil, storageReader{
		r: rc,
	})
}

func archiveError(err error) error {
	if _, fromStorage := errors.AsType[archiveStorageError](err); fromStorage {
		return fmt.Errorf("read the archive: %w", err)
	}

	return connect.NewError(
		connect.CodeFailedPrecondition,
		"cannot read the archive: "+err.Error(),
	)
}

func (s *artifactServer) ListArchiveEntries(
	ctx context.Context,
	req *relicv1.ListArchiveEntriesRequest,
) (*relicv1.ListArchiveEntriesResponse, error) {
	it, kind, err := archiveItem(ctx, s.d, req.GetRepository(), cleanPath(req.GetPath()))
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListArchiveEntriesResponse{
		Kind: kind,
	}

	var entries []archive.Entry

	err = withArchive(ctx, s.d, it, kind, func(ra io.ReaderAt, rd io.Reader) error {
		var err error
		if ra != nil {
			entries, out.Truncated, err = archive.ListZip(ra, it.Size)
		} else {
			entries, out.Truncated, err = archive.ListTar(rd, kind == archive.TarGz)
		}

		return err
	})
	if err != nil {
		return nil, archiveError(err)
	}

	for _, e := range entries {
		out.Entries = append(
			out.Entries,
			&relicv1.ArchiveEntry{
				Path:     e.Path,
				Size:     e.Size,
				Modified: timestamppb.New(e.Modified),
				Folder:   e.Folder,
			},
		)
	}

	return out, nil
}

// ArchivePath serves one archive entry's content as text:
// GET ArchivePath + "<repository>/<path>?entry=<entry>".
const ArchivePath = "/api/v1/archive/"

func (d Dependencies) archiveEntry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key, p, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, ArchivePath), "/")

	entry := strings.Trim(r.URL.Query().Get("entry"), "/")
	switch {
	case auth.FromContext(ctx).Anonymous():
		writeHTTPError(w, connect.NewError(connect.CodeUnauthenticated, "sign in first"))
		return
	case r.Method != http.MethodGet:
		writeHTTPError(w, connect.NewError(connect.CodeUnimplemented, "method not allowed"))
		return
	case entry == "":
		writeHTTPError(
			w,
			connect.NewError(
				connect.CodeInvalidArgument,
				"entry is required; list entries with ArtifactService.ListArchiveEntries",
			),
		)

		return
	}

	it, kind, err := archiveItem(ctx, d, key, cleanPath(p))
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	if d.DownloadBlocker != nil {
		decision, err := d.DownloadBlocker.Check(ctx, it.Repository, it.Path(), it.Checksums.SHA256)
		if err != nil {
			writeHTTPError(w, d.logged(r, err))
			return
		}

		if decision.Blocked {
			writeHTTPError(
				w,
				connect.NewError(
					connect.CodePermissionDenied,
					"download blocked: "+decision.Reason,
				),
			)

			return
		}
	}

	var content []byte

	err = withArchive(ctx, d, it, kind, func(ra io.ReaderAt, rd io.Reader) error {
		var err error
		if ra != nil {
			content, err = archive.ReadZip(ra, it.Size, entry)
		} else {
			content, err = archive.ReadTar(rd, kind == archive.TarGz, entry)
		}

		return err
	})
	switch {
	case errors.Is(err, archive.ErrNotFound):
		writeHTTPError(w, connect.NewError(connect.CodeNotFound, "no such entry"))
	case errors.Is(err, archive.ErrTooLarge):
		writeHTTPError(
			w,
			connect.NewError(
				connect.CodeFailedPrecondition,
				"the entry is larger than 1 MiB; download the archive instead",
			),
		)
	case err != nil:
		writeHTTPError(w, d.logged(r, archiveError(err)))
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(content) //nolint:gosec // G705: text/plain with nosniff
	}
}
