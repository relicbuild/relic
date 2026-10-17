//go:build integration

package main

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
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

func TestOperationalRiskCommands(t *testing.T) {
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

	if out, err := relic(t, config, "", "security", "end-of-life", "status"); err != nil ||
		!strings.Contains(out, "no end-of-life data yet") {
		t.Errorf("status before an import = %q %v", out, err)
	}

	dump := filepath.Join(dir, "eol.json")
	if err := os.WriteFile(dump, []byte(`{"result":[{"name":"django","label":"Django",
		"identifiers":[{"type":"purl","id":"pkg:pypi/django"}],
		"releases":[{"name":"4.2","eolFrom":"2026-04-30","isEol":true,"isMaintained":false,"latest":{"name":"4.2.25"}}]}]}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "security", "end-of-life", "import", dump); err != nil ||
		!strings.Contains(out, "imported") {
		t.Fatalf("import = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "security", "end-of-life", "status"); err != nil ||
		!strings.HasPrefix(out, "1 products, import, changed ") {
		t.Errorf("status = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"apiVersion: relic.build/v1\nkind: Repository\nmetadata: {name: apps}\nspec: {class: local, format: generic}\n",
		"apply",
		"-f",
		"-",
	); err != nil {
		t.Fatal(err)
	}

	put(t, config, dir, "apps/site-1.0.tgz", "site")

	it, err := relic(t, config, "", "api", "repositories/apps/items/site-1.0.tgz")
	if err != nil {
		t.Fatal(err)
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

	if err := db.InsertOperationalRisk(ctx, database.InsertOperationalRiskParams{
		Sha256:         sha,
		Purl:           "pkg:pypi/django@4.2.11",
		Kind:           "end_of_life",
		Severity:       "high",
		PackageName:    "Django",
		PackageVersion: "4.2.11",
		Product:        "django",
		Cycle:          "4.2",
		EolFrom:        "2026-04-30",
		Detail:         "Django 4.2 reached end of life on 2026-04-30",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := relic(t, config, "", "security", "risks", "--kind", "end_of_life")
	if err != nil || !strings.Contains(out, "apps/site-1.0.tgz") ||
		!strings.Contains(out, "Django 4.2 reached end of life on 2026-04-30") || !strings.Contains(out, "1 of 1") {
		t.Errorf("risks = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "security", "risks", "--kind", "ancient"); err == nil {
		t.Error("an unknown kind was accepted")
	}

	out, err = relic(t, config, "", "security", "report", "apps/site-1.0.tgz", "--fail-on", "high")
	if !hasTableRowWith(out, "end_of_life", "Django", "4.2.11") || err == nil {
		t.Errorf("report = %q %v; want the risk listed and --fail-on high to fail", out, err)
	}
}
