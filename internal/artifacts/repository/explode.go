package repository

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const (
	maxExplodeEntries = 100_000
	maxExplodeBytes   = 50 << 30
)

// ErrUnsupportedArchive is returned when an archive's format is not known.
var ErrUnsupportedArchive = errors.New("unsupported archive format")

// ExplodeResult summarizes an extracted archive.
type ExplodeResult struct {
	Files int
}

// Explode extracts the archive in in.Body into the folder containing in.Path
// (X-Explode-Archive).
func (s *Service) Explode(ctx context.Context, in DeployInput) (ExplodeResult, error) {
	if _, err := s.checkDeployTarget(ctx, &in); err != nil {
		return ExplodeResult{}, err
	}

	parent, name := SplitPath(in.Path)
	if parent == RootPath {
		parent = ""
	}

	x := &exploder{
		s:     s,
		in:    in,
		dir:   parent,
		limit: maxExplodeBytes,
	}

	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip"),
		strings.HasSuffix(lower, ".jar"),
		strings.HasSuffix(lower, ".war"),
		strings.HasSuffix(lower, ".ear"):
		return x.result(x.zip(ctx))
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		gz, err := gzip.NewReader(in.Body)
		if err != nil {
			return ExplodeResult{}, fmt.Errorf("%w: %w", ErrUnsupportedArchive, err)
		}
		defer func() {
			_ = gz.Close()
		}()

		return x.result(x.tar(ctx, gz))
	case strings.HasSuffix(lower, ".tar"):
		return x.result(x.tar(ctx, in.Body))
	}

	return ExplodeResult{}, fmt.Errorf("%w: %s", ErrUnsupportedArchive, name)
}

type exploder struct {
	s     *Service
	in    DeployInput
	dir   string
	limit int64
	files int
	bytes int64
}

func (x *exploder) result(err error) (ExplodeResult, error) {
	return ExplodeResult{
		Files: x.files,
	}, err
}

// archiveEntryPath is CleanPath for an archive entry name, which may hold
// "." segments, as "tar -C dir ." writes them.
func archiveEntryPath(name string) (string, error) {
	segments := strings.Split(name, "/")

	kept := segments[:0]
	for _, s := range segments {
		if s != "." {
			kept = append(kept, s)
		}
	}

	return CleanPath(strings.Join(kept, "/"))
}

func (x *exploder) deploy(ctx context.Context, name string, r io.Reader) error {
	clean, err := archiveEntryPath(name)
	if err != nil || clean == "" {
		return fmt.Errorf("%w: archive entry %q", ErrInvalidPath, name)
	}

	x.files++
	if x.files > maxExplodeEntries {
		return fmt.Errorf("%w: more than %d entries", ErrUnsupportedArchive, maxExplodeEntries)
	}

	target := path.Join(x.dir, clean)
	if x.in.Authorize != nil {
		existing, err := x.s.Stat(ctx, x.in.Repository, target)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}

		if err := x.in.Authorize(target, err == nil && !existing.IsFolder()); err != nil {
			return err
		}
	}

	counted := &countingReader{
		r:   io.LimitReader(r, x.limit-x.bytes+1),
		max: x.limit - x.bytes,
	}
	_, err = x.s.Deploy(
		ctx,
		DeployInput{
			Repository: x.in.Repository,
			Path:       target,
			Body:       counted,
			Props:      x.in.Props,
			User:       x.in.User,
		},
	)

	x.bytes += counted.n
	if x.bytes > x.limit {
		return fmt.Errorf("%w: expands to more than %d bytes", ErrUnsupportedArchive, x.limit)
	}

	return err
}

func (x *exploder) zip(ctx context.Context) error {
	f, err := os.CreateTemp("", "relic-explode-*.zip")
	if err != nil {
		return fmt.Errorf("spool archive: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()

	size, err := io.Copy(f, io.LimitReader(x.in.Body, maxExplodeBytes+1))
	if err != nil {
		return fmt.Errorf("spool archive: %w", err)
	}

	if size > maxExplodeBytes {
		return fmt.Errorf("%w: archive larger than %d bytes", ErrUnsupportedArchive, int64(maxExplodeBytes))
	}

	zr, err := zip.NewReader(f, size)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsupportedArchive, err)
	}

	for _, e := range zr.File {
		if e.FileInfo().IsDir() || !e.Mode().IsRegular() {
			continue
		}

		rc, err := e.Open()
		if err != nil {
			return fmt.Errorf("%w: entry %s: %w", ErrUnsupportedArchive, e.Name, err)
		}

		err = x.deploy(ctx, e.Name, rc)
		_ = rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}

func (x *exploder) tar(ctx context.Context, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnsupportedArchive, err)
		}

		if h.Typeflag != tar.TypeReg {
			continue
		}

		if err := x.deploy(ctx, h.Name, tr); err != nil {
			return err
		}
	}
}

// countingReader counts what it reads and fails once it has read more than
// max bytes, so an entry over the limit is never stored.
type countingReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	if c.n > c.max {
		return n, fmt.Errorf("%w: entry over the size limit", ErrUnsupportedArchive)
	}

	return n, err
}
