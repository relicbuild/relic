package pypi

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"path"
	"strings"

	"relic/internal/artifacts/repository"
)

const maxMetadata = 1 << 20

// ErrNoMetadata is returned for distributions without core metadata.
var ErrNoMetadata = errors.New("no package metadata")

// Metadata is a distribution's core metadata (PEP 566), as the UI's PyPI
// tab shows it.
type Metadata struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Summary        string   `json:"summary,omitempty"`
	License        string   `json:"license,omitempty"`
	RequiresPython string   `json:"requiresPython,omitempty"`
	Author         string   `json:"author,omitempty"`
	HomePage       string   `json:"homePage,omitempty"`
	ProjectURLs    []string `json:"projectUrls"`
	RequiresDist   []string `json:"requiresDist"`
	Classifiers    []string `json:"classifiers"`
}

// Metadata reads the core metadata of a wheel (*.dist-info/METADATA, by
// ranged reads) or an sdist (<dir>/PKG-INFO).
func (s *Service) Metadata(ctx context.Context, item repository.Item) (Metadata, error) {
	var (
		raw []byte
		err error
	)

	switch name := strings.ToLower(item.Name); {
	case strings.HasSuffix(name, ".whl"):
		raw, err = wheelMetadata(s.repos.ReaderAt(ctx, item), item.Size)
	case strings.HasSuffix(name, ".tar.gz"):
		var rc io.ReadCloser
		if rc, err = s.repos.Open(ctx, item, nil); err != nil {
			return Metadata{}, err
		}
		defer func() {
			_ = rc.Close()
		}()

		raw, err = sdistMetadata(rc)
	default:
		return Metadata{}, ErrNoMetadata
	}

	if err != nil {
		return Metadata{}, err
	}

	return ParseMetadata(raw)
}

func wheelMetadata(r io.ReaderAt, size int64) ([]byte, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("read wheel: %w", err)
	}

	for _, f := range zr.File {
		dir, file := path.Split(f.Name)
		if file == "METADATA" && strings.HasSuffix(dir, ".dist-info/") && strings.Count(dir, "/") == 1 {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", f.Name, err)
			}
			defer func() {
				_ = rc.Close()
			}()

			return io.ReadAll(io.LimitReader(rc, maxMetadata))
		}
	}

	return nil, ErrNoMetadata
}

func sdistMetadata(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("read sdist: %w", err)
	}
	defer func() {
		_ = gz.Close()
	}()

	tr := tar.NewReader(io.LimitReader(gz, 1<<30))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, ErrNoMetadata
		}

		if err != nil {
			return nil, fmt.Errorf("read sdist: %w", err)
		}

		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == "PKG-INFO" &&
			strings.Count(strings.Trim(hdr.Name, "./"), "/") == 1 {
			return io.ReadAll(io.LimitReader(tr, maxMetadata))
		}
	}
}

// ParseMetadata parses core metadata: RFC 822 style headers, the
// description in the body is ignored.
func ParseMetadata(raw []byte) (Metadata, error) {
	h, err := textproto.NewReader(bufio.NewReader(strings.NewReader(string(raw) + "\r\n\r\n"))).ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return Metadata{}, fmt.Errorf("parse metadata: %w", err)
	}

	m := Metadata{
		Name:           h.Get("Name"),
		Version:        h.Get("Version"),
		Summary:        h.Get("Summary"),
		License:        firstNonEmpty(h.Get("License-Expression"), h.Get("License")),
		RequiresPython: h.Get("Requires-Python"),
		Author:         firstNonEmpty(h.Get("Author"), h.Get("Author-Email")),
		HomePage:       h.Get("Home-Page"),
		ProjectURLs:    nonNil(h.Values("Project-Url")),
		RequiresDist:   nonNil(h.Values("Requires-Dist")),
		Classifiers:    nonNil(h.Values("Classifier")),
	}
	if m.Name == "" {
		return Metadata{}, ErrNoMetadata
	}

	return m, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}

	return ""
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
