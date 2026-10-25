//go:build integration

package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"
)

func TestReport(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := app.BootstrapAdmin(ctx, db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(ctx, db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(app.NewHandler(db, objectstoretest.NewMemory(), tokens, nil, app.Options{}, log))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(
		t,
		config,
		"apiVersion: relic.build/v1\nkind: Repository\nmetadata: {name: charts}\nspec: {class: local, format: generic}\n",
		"apply",
		"-f",
		"-",
	); err != nil {
		t.Fatal(err)
	}

	put(t, config, dir, "charts/demo-0.1.0.tgz", "chart")

	put(t, config, dir, "charts/README.md", "readme")

	out, err := relic(t, config, "", "security", "report", "charts/demo-0.1.0.tgz")
	if err != nil || !strings.HasPrefix(out, "charts/demo-0.1.0.tgz: unavailable ("+
		"Relic Scan is off: no worker with Syft and Grype reported lately)\n") {
		t.Errorf("report without a worker that scans = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"security",
		"report",
		"charts/demo-0.1.0.tgz",
		"--fail-on",
		"critical",
	); err == nil || !strings.Contains(err.Error(), "not scanned yet") {
		t.Errorf("--fail-on before the scan: %v", err)
	}

	out, err = relic(t, config, "", "security", "report", "charts/README.md", "--fail-on", "critical")
	if err != nil || !strings.HasPrefix(out, "charts/README.md: none\n") {
		t.Errorf("report of a file no scanner reads = %q %v", out, err)
	}

	it, err := relic(t, config, "", "api", "repositories/charts/items/demo-0.1.0.tgz")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	var item struct {
		Item struct {
			Checksums struct {
				Sha256 string
			}
		}
	}
	if err := json.Unmarshal([]byte(it), &item); err != nil || item.Item.Checksums.Sha256 == "" {
		t.Fatalf("item = %s %v", it, err)
	}

	sha, now := item.Item.Checksums.Sha256, time.Now()
	if _, err := db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         now,
		NamePattern: ".",
		MaxRows:     10,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
		Sha256: sha,
		Status: scan.StatusDone,
		Now:    now,
	}); err != nil {
		t.Fatal(err)
	}

	for _, i := range []database.InsertIssueParams{
		{
			Sha256:     sha,
			Analyzer:   scan.AnalyzerSecrets,
			Rule:       "aws-access-token",
			Severity:   "high",
			Title:      "AWS credentials",
			File:       "demo/credentials.ini",
			Line:       1,
			Validation: scan.SecretUnknown,
			Validated:  &now,
		},
		{
			Sha256:   sha,
			Analyzer: scan.AnalyzerMisconfiguration,
			Rule:     "KSV-0017",
			Severity: "high",
			Title:    "Privileged container",
			File:     "demo/templates/deployment.yaml",
			Line:     16,
			Resource: "Deployment/demo",
		},
	} {
		if err := db.InsertIssue(ctx, i); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.InsertFinding(ctx, database.InsertFindingParams{
		Sha256:         sha,
		VulnID:         "GHSA-69ch-w2m2-3vjp",
		Purl:           "pkg:golang/golang.org/x/text@v0.3.6",
		PackageName:    "golang.org/x/text",
		PackageVersion: "v0.3.6",
		Severity:       "medium",
		Cves: []string{
			"CVE-2022-32149",
		},
		FixedVersions: []string{},
		Cvss:          []byte(`[]`),
		Urls:          []string{},
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertReachability(ctx, database.InsertReachabilityParams{
		Sha256: sha,
		OsvID:  "GO-2022-1059",
		Module: "golang.org/x/text",
		Aliases: []string{
			"CVE-2022-32149",
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, err = relic(t, config, "", "security", "report", "charts/demo-0.1.0.tgz")
	if err != nil ||
		!hasTableRowWith(out, "GHSA-69ch-w2m2-3vjp", "pkg:golang/golang.org/x/text@v0.3.6", "not reachable") ||
		!hasTableRowWith(out, "secrets", "aws-access-token", "demo/credentials.ini:1", "AWS credentials [unknown]") ||
		!hasTableRowWith(out, "demo/templates/deployment.yaml:16 (Deployment/demo)", "Privileged container") {
		t.Errorf("report = %q %v", out, err)
	}

	var ec exitCode

	if _, err := relic(
		t,
		config,
		"",
		"security",
		"report",
		"charts/demo-0.1.0.tgz",
		"--fail-on",
		"high",
	); !errors.As(err, &ec) ||
		ec != 1 {
		t.Errorf("--fail-on high: %v", err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"security",
		"report",
		"charts/demo-0.1.0.tgz",
		"--fail-on",
		"critical",
	); err != nil {
		t.Errorf("--fail-on critical: %v", err)
	}

	for _, severity := range []string{
		"unspecified",
		"unknown",
	} {
		if _, err := relic(
			t,
			config,
			"",
			"security",
			"report",
			"charts/demo-0.1.0.tgz",
			"--fail-on",
			severity,
		); err == nil || !strings.Contains(err.Error(), "unknown severity") {
			t.Errorf("--fail-on %s: %v", severity, err)
		}
	}

	if out, err := relic(t, config, "", "security", "report", "charts/demo-0.1.0.tgz", "-o", "json"); err != nil ||
		!strings.Contains(out, `"analyzer": "misconfiguration"`) {
		t.Errorf("-o json = %q %v", out, err)
	}
}
