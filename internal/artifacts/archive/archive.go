// Package archive lists and reads entries of zip and tar archives for the
// UI's Archive tab.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"
)

// Limits.
const (
	MaxEntries     = 10_000
	MaxEntryBytes  = 1 << 20
	maxStreamBytes = 1 << 30
)

// Kinds of archive.
const (
	Zip   = "zip"
	TarGz = "tar.gz"
	Tar   = "tar"
)

// ErrNotFound is returned for an entry the archive does not hold.
var ErrNotFound = errors.New("entry not found")

// ErrTooLarge is returned for an entry above MaxEntryBytes.
var ErrTooLarge = errors.New("entry too large to show")

// Entry is one file or folder of an archive.
type Entry struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Folder   bool      `json:"folder"`
}

var zipExt = []string{
	".zip",
	".jar",
	".war",
	".ear",
	".aar",
	".whl",
	".nupkg",
	".egg",
	".vsix",
	".apk",
}

// KindOf returns the kind of archive a file name denotes, or "".
func KindOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case slices.ContainsFunc(zipExt, func(e string) bool {
		return strings.HasSuffix(n, e)
	}):
		return Zip
	case strings.HasSuffix(n, ".tar.gz"), strings.HasSuffix(n, ".tgz"), strings.HasSuffix(n, ".crate"):
		return TarGz
	case strings.HasSuffix(n, ".tar"), strings.HasSuffix(n, ".gem"):
		return Tar
	}

	return ""
}

// ListZip lists a zip archive read through r (size bytes long).
func ListZip(r io.ReaderAt, size int64) (entries []Entry, truncated bool, err error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, false, fmt.Errorf("read zip: %w", err)
	}

	for i, f := range zr.File {
		if i == MaxEntries {
			return entries, true, nil
		}

		entries = append(entries, Entry{
			Path:     clean(f.Name),
			Size:     int64(f.UncompressedSize64), //nolint:gosec // display only
			Modified: f.Modified,
			Folder:   f.FileInfo().IsDir(),
		})
	}

	return entries, false, nil
}

// ReadZip returns the content of one zip entry.
func ReadZip(r io.ReaderAt, size int64, name string) ([]byte, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("read zip: %w", err)
	}

	for _, f := range zr.File {
		if clean(f.Name) != name || f.FileInfo().IsDir() {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		defer func() {
			_ = rc.Close()
		}()

		return readCapped(rc)
	}

	return nil, ErrNotFound
}

// ListTar lists a tar stream, gzip-compressed when gz is set.
func ListTar(r io.Reader, gz bool) (entries []Entry, truncated bool, err error) {
	tr, capped, closeFn, err := tarReader(r, gz)
	if err != nil {
		return nil, false, err
	}
	defer closeFn()

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries, false, nil
		}

		if err != nil && capped.reached() {
			return entries, true, nil
		}

		if err != nil {
			return entries, false, fmt.Errorf("read tar: %w", err)
		}

		if len(entries) == MaxEntries {
			return entries, true, nil
		}

		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue
		}

		entries = append(
			entries,
			Entry{
				Path:     clean(hdr.Name),
				Size:     hdr.Size,
				Modified: hdr.ModTime,
				Folder:   hdr.Typeflag == tar.TypeDir,
			},
		)
	}
}

// ReadTar returns the content of one tar entry.
func ReadTar(r io.Reader, gz bool, name string) ([]byte, error) {
	tr, _, closeFn, err := tarReader(r, gz)
	if err != nil {
		return nil, err
	}
	defer closeFn()

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, ErrNotFound
		}

		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		if hdr.Typeflag == tar.TypeReg && clean(hdr.Name) == name {
			return readCapped(tr)
		}
	}
}

type streamCap struct {
	r    io.Reader
	left int64
}

func (c *streamCap) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}

	if int64(len(p)) > c.left {
		p = p[:c.left]
	}

	n, err := c.r.Read(p)
	c.left -= int64(n)

	return n, err
}

func (c *streamCap) reached() bool { return c.left <= 0 }

func tarReader(r io.Reader, gz bool) (*tar.Reader, *streamCap, func(), error) {
	if !gz {
		capped := &streamCap{
			r:    r,
			left: maxStreamBytes,
		}

		return tar.NewReader(capped), capped, func() {}, nil
	}

	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read gzip: %w", err)
	}

	capped := &streamCap{
		r:    zr,
		left: maxStreamBytes,
	}

	return tar.NewReader(capped), capped, func() {
		_ = zr.Close()
	}, nil
}

func readCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxEntryBytes+1))
	if err != nil {
		return nil, err
	}

	if len(b) > MaxEntryBytes {
		return nil, ErrTooLarge
	}

	return b, nil
}

func clean(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}
