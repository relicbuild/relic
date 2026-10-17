//go:build integration

package scan_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/scripttest"
	"relic/internal/scanning/malicious"
	"relic/internal/scanning/scan"
)

func osvFeed(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	archive := func(files map[string]string) []byte {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		for name, body := range files {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(body))
		}

		_ = zw.Close()

		return buf.Bytes()
	}

	npm := archive(map[string]string{
		"MAL-2025-1.json":     `{"id":"MAL-2025-1","summary":"Malicious code in evil-pkg (npm)","modified":"2025-03-01T00:00:00Z","affected":[{"package":{"ecosystem":"npm","name":"evil-pkg"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"}]}]}]}`,
		"GHSA-aaaa-bbbb.json": `{"id":"GHSA-aaaa-bbbb","affected":[{"package":{"ecosystem":"npm","name":"lodash"}}]}`,
	})
	empty := archive(nil)

	var downloads atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := empty
		if r.URL.Path == "/npm/all.zip" {
			body = npm
		}

		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)

			return
		}

		downloads.Add(1)
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv, &downloads
}

type stopOnLog struct {
	stop    context.CancelFunc
	records atomic.Int64
}

func (h *stopOnLog) Enabled(context.Context, slog.Level) bool { return true }

func (h *stopOnLog) Handle(context.Context, slog.Record) error {
	h.records.Add(1)
	h.stop()

	return nil
}

func (h *stopOnLog) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *stopOnLog) WithGroup(string) slog.Handler { return h }

func TestMaliciousPackages(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, blobs, log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "npm-local",
		Class:  "local",
		Format: "npm",
	}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"app",
		"unreadable",
		"unreadable-too",
	} {
		if _, err := repos.Deploy(ctx, repository.DeployInput{
			Repository: "npm-local",
			Path:       name + "/-/" + name + "-1.0.0.tgz",
			Body: strings.NewReader(
				tgz(t, map[string]string{
					"package/package.json": `{"name":"` + name + `","version":"1.0.0"}`,
				}),
			),
			User: "admin",
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, grype, _ := fakeScanners(t)
	syftPath := filepath.Join(t.TempDir(), "syft")

	scripttest.Write(t, syftPath, `#!/bin/sh
if grep -rqs '"name":"unreadable' "${2#dir:}"; then
  echo '{"artifacts":"not a list"}'
else
  echo '{"artifacts":[{"name":"evil-pkg","version":"1.0.0","purl":"pkg:npm/evil-pkg@1.0.0"}]}'
fi
`)

	svc := scan.NewService(db, blobs, scan.Syft{
		Exec: scan.Exec{
			Path: syftPath,
		},
	}, grype, scan.Limits{}, log)
	svc.SetMalicious(malicious.NewCatalog(db))

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("claim = %v %v", claimed, err)
	}

	var sha string

	for _, c := range claimed {
		if err := svc.ScanArtifact(ctx, c.Sha256, c.FileName); err != nil {
			t.Fatal(err)
		}

		if c.FileName == "app-1.0.0.tgz" {
			sha = c.Sha256
		}
	}

	maliciousFindings := func() int {
		t.Helper()

		fs, err := db.ListFindings(ctx, sha)
		if err != nil {
			t.Fatal(err)
		}

		n := 0

		for _, f := range fs {
			if f.VulnID == "MAL-2025-1" && f.Severity == "critical" && f.Purl == "pkg:npm/evil-pkg@1.0.0" {
				n++
			}
		}

		return n
	}

	if maliciousFindings() != 0 {
		t.Fatal("a malicious finding before the feed was read")
	}

	feed, downloads := osvFeed(t)
	refresher := malicious.NewRefresher(db, feed.URL, nil, log)

	changed, err := refresher.Refresh(ctx)
	if err != nil || !changed || downloads.Load() != int64(len(malicious.Ecosystems)) {
		t.Fatalf("refresh = %v %v, %d downloads", changed, err, downloads.Load())
	}

	if n, _ := db.CountMaliciousPackages(ctx); n != 1 {
		t.Errorf("stored %d advisories, want the MAL- one", n)
	}

	if n, err := svc.CheckMalicious(ctx); err != nil || n != 1 || maliciousFindings() != 1 {
		t.Fatalf("check = %d %v, malicious findings %d; want the app checked past the unreadable SBOM", n, err,
			maliciousFindings())
	}

	changed, err = refresher.Refresh(ctx)
	if err != nil || changed || downloads.Load() != int64(len(malicious.Ecosystems)) {
		t.Errorf("second refresh = %v %v, %d downloads", changed, err, downloads.Load())
	}

	if n, err := svc.CheckMalicious(ctx); err != nil || n != 0 {
		t.Errorf("second check = %d %v", n, err)
	}

	stopping, stop := context.WithCancel(ctx)
	warnings := &stopOnLog{
		stop: stop,
	}
	stopped := scan.NewService(db, blobs, scan.Syft{
		Exec: scan.Exec{
			Path: syftPath,
		},
	}, grype, scan.Limits{}, slog.New(warnings))
	stopped.SetMalicious(malicious.NewCatalog(db))

	if _, err := stopped.CheckMalicious(stopping); !errors.Is(err, context.Canceled) || warnings.records.Load() != 1 {
		t.Errorf("a check stopped at its first scan = %v after %d warnings; want it to end there",
			err, warnings.records.Load())
	}

	if err := svc.Rematch(ctx, sha); err != nil || maliciousFindings() != 1 {
		t.Errorf("rematch: %v, malicious findings %d", err, maliciousFindings())
	}

	id, err := svc.QueueGraph(ctx, "admin", scan.GraphRequest{
		PURLs: []string{
			"pkg:npm/evil-pkg@2.0.0",
			"pkg:npm/lodash@4.17.15",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RunGraph(ctx, id, true); err != nil {
		t.Fatal(err)
	}

	g, err := db.GetGraphScan(ctx, id)

	var rep scan.Report
	if err != nil || json.Unmarshal(g.Result, &rep) != nil {
		t.Fatalf("graph scan: %v", err)
	}

	found := false
	for _, f := range rep.Findings {
		found = found || (f.ID == "MAL-2025-1" && f.Package.PURL == "pkg:npm/evil-pkg@2.0.0")
	}

	if !found {
		t.Errorf("graph findings = %+v", rep.Findings)
	}
}
