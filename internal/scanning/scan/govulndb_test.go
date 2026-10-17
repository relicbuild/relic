package scan

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func vulndbArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write([]byte(body))
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestGoVulnerabilityDatabaseUpdate(t *testing.T) {
	t.Parallel()

	good := vulndbArchive(t, map[string]string{
		"index/db.json":        `{"modified":"2026-10-01T00:00:00Z"}`,
		"ID/GO-2021-0113.json": `{"id":"GO-2021-0113"}`,
		"../escape.json":       `{}`,
	})

	var served atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)

		if r.URL.Path == "/broken.zip" {
			_, _ = w.Write(vulndbArchive(t, map[string]string{
				"README": "no index",
			}))

			return
		}

		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)

			return
		}

		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(good)
	}))
	t.Cleanup(srv.Close)

	dir := filepath.Join(t.TempDir(), "go")
	db := GoVulnerabilityDatabase{
		Dir: dir,
		URL: srv.URL + "/vulndb.zip",
	}

	if db.Present() || !db.Modified().IsZero() {
		t.Fatal("present before any download")
	}

	if changed, err := db.Update(t.Context()); err != nil || !changed || !db.Present() {
		t.Fatalf("first update = %v, %v; present %v", changed, err, db.Present())
	}

	if got := db.Modified(); !got.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("modified = %v", got)
	}

	if _, err := os.Stat(filepath.Join(dir, "ID", "GO-2021-0113.json")); err != nil {
		t.Errorf("entry not unpacked: %v", err)
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.json")); err == nil {
		t.Error("an entry outside the database was written")
	}

	if changed, err := db.Update(t.Context()); err != nil || changed {
		t.Errorf("second update = %v, %v; want unchanged (ETag)", changed, err)
	}

	broken := GoVulnerabilityDatabase{
		Dir: dir,
		URL: srv.URL + "/broken.zip",
	}
	if _, err := broken.Update(t.Context()); err == nil {
		t.Error("an archive without index/db.json was installed")
	}

	if !db.Present() {
		t.Error("a failed update removed the installed database")
	}

	unwritableETag := GoVulnerabilityDatabase{
		Dir: filepath.Join(t.TempDir(), "go"),
		URL: srv.URL + "/vulndb.zip",
	}
	if err := os.MkdirAll(unwritableETag.etagFile(), 0o700); err != nil {
		t.Fatal(err)
	}

	if changed, err := unwritableETag.Update(t.Context()); err != nil || !changed || !unwritableETag.Present() {
		t.Errorf("update with an unwritable ETag = %v, %v; want the installed database reported", changed, err)
	}

	if n := served.Load(); n != 4 {
		t.Errorf("requests = %d, want 4", n)
	}
}
