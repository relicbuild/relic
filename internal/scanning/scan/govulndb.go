package scan

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"relic/internal/platform/telemetry"
)

// DefaultGoVulnerabilityDatabase is the Go vulnerability database as one
// archive.
const DefaultGoVulnerabilityDatabase = "https://vuln.go.dev/vulndb.zip"

const maxGoVulnerabilityArchive = 1 << 30

// GoVulnerabilityDatabase keeps a copy of the Go vulnerability database for
// govulncheck: URL's archive unpacked into Dir.
type GoVulnerabilityDatabase struct {
	Dir    string
	URL    string
	Client *http.Client
}

// Present reports whether a database is unpacked.
func (d GoVulnerabilityDatabase) Present() bool {
	_, err := os.Stat(filepath.Join(d.Dir, "index", "db.json"))

	return err == nil
}

func (d GoVulnerabilityDatabase) etagFile() string { return d.Dir + ".etag" }

// Modified is when the unpacked database's content last changed, from its
// index; the zero time without one.
func (d GoVulnerabilityDatabase) Modified() time.Time {
	var index struct {
		Modified time.Time `json:"modified"`
	}

	if readJSON(filepath.Join(d.Dir, "index", "db.json"), &index) != nil {
		return time.Time{}
	}

	return index.Modified
}

// Update downloads the archive when it changed since the last download and
// replaces the unpacked database with it.
func (d GoVulnerabilityDatabase) Update(ctx context.Context) (changed bool, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "scan.UpdateGoVulnerabilityDatabase")

	defer func() {
		telemetry.End(span, err)
	}()

	if d.URL == "" {
		return false, nil
	}

	client := d.Client
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Minute,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return false, err
	}

	if etag, err := os.ReadFile(d.etagFile()); err == nil && d.Present() {
		req.Header.Set("If-None-Match", strings.TrimSpace(string(etag)))
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return false, nil
	case http.StatusOK:
	default:
		return false, fmt.Errorf("the Go vulnerability database's source answered %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(d.Dir), 0o700); err != nil {
		return false, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(d.Dir), "govulndb-*.zip")
	if err != nil {
		return false, err
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	size, err := io.Copy(tmp, io.LimitReader(resp.Body, maxGoVulnerabilityArchive+1))
	if err != nil {
		return false, fmt.Errorf("download the Go vulnerability database: %w", err)
	}

	if size > maxGoVulnerabilityArchive {
		return false, errors.New("the Go vulnerability database archive is larger than 1 GiB")
	}

	if err := d.install(tmp, size); err != nil {
		return false, err
	}

	if etag := resp.Header.Get("ETag"); etag != "" {
		if err := os.WriteFile(d.etagFile(), []byte(etag), 0o600); err != nil {
			span.RecordError(fmt.Errorf("keep the Go vulnerability database's ETag: %w", err))
		}
	}

	return true, nil
}

// install unpacks the archive next to Dir and swaps it in.
func (d GoVulnerabilityDatabase) install(archive io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(archive, size)
	if err != nil {
		return fmt.Errorf("read the Go vulnerability database: %w", err)
	}

	next := d.Dir + ".new"
	if err := os.RemoveAll(next); err != nil {
		return err
	}

	for _, f := range zr.File {
		name := path.Clean(f.Name)
		if f.FileInfo().IsDir() || strings.HasPrefix(name, "../") || path.IsAbs(name) ||
			f.UncompressedSize64 > 64<<20 ||
			(!strings.HasPrefix(name, "ID/") && !strings.HasPrefix(name, "index/")) {
			continue
		}

		if err := unzipEntry(f, filepath.Join(next, filepath.FromSlash(name))); err != nil {
			return err
		}
	}

	if _, err := os.Stat(filepath.Join(next, "index", "db.json")); err != nil {
		return errors.New("the archive is not a Go vulnerability database (no index/db.json)")
	}

	old := d.Dir + ".old"
	_ = os.RemoveAll(old)

	if err := os.Rename(d.Dir, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.Rename(next, d.Dir); err != nil {
		if _, statErr := os.Stat(old); statErr == nil {
			return errors.Join(err, os.Rename(old, d.Dir))
		}

		return err
	}

	return os.RemoveAll(old)
}

func unzipEntry(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}

	rc, err := f.Open()
	if err != nil {
		return err
	}

	defer func() {
		_ = rc.Close()
	}()

	return writeFile(target, io.LimitReader(rc, 64<<20))
}
