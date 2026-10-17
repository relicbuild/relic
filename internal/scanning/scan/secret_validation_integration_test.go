//go:build integration

package scan_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/scripttest"
	"relic/internal/scanning/scan"
)

func TestScanValidatesSecrets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, blobs, log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "configs",
		Class:  "local",
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	body := "AWS_ACCESS_KEY_ID=AKIAEXAMPLESECRET1234\n" //nolint:gosec // G101: the fake key the scan must find
	_ = tw.WriteHeader(&tar.Header{
		Name:     "app.env",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = zw.Close()

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "configs",
		Path:       "configs-1.0.tar.gz",
		Body:       &buf,
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	syft, grype, _ := fakeScanners(t)
	dir := t.TempDir()
	args := filepath.Join(dir, "gitleaks-args")
	scripttest.Write(t, filepath.Join(dir, "gitleaks"), `#!/bin/sh
echo "$@" >`+args+`
echo '[{"RuleID":"aws-access-token","Description":"AWS credentials","File":"/in/app.env","StartLine":1,"Secret":"AKIAEXAMPLESECRET1234"}]'
`)

	svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)
	svc.SetAnalyzers(scan.Gitleaks{
		Exec: scan.Exec{
			Path: filepath.Join(dir, "gitleaks"),
		},
		Unredacted: true,
	})
	svc.SetSecretValidator(scan.NewSecretValidator(nil, nil))

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}

	sha := claimed[0].Sha256
	if err := svc.ScanArtifact(ctx, sha, claimed[0].FileName); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(args); strings.Contains(string(b), "--redact") {
		t.Errorf("gitleaks ran with %q, want the secrets to validate", b)
	}

	issues, err := db.ListIssues(ctx, sha)
	if err != nil || len(issues) != 1 {
		t.Fatalf("issues = %+v, %v", issues, err)
	}

	is := issues[0]
	if is.Validation != scan.SecretUnknown || is.Validated == nil || is.Severity != string(scan.SeverityHigh) {
		t.Errorf("issue = %+v, want an unknown outcome (no provider answers for AWS keys)", is)
	}

	for _, v := range []string{
		is.Rule,
		is.Title,
		is.File,
		is.Resource,
		is.Validation,
	} {
		if strings.Contains(v, "AKIAEXAMPLESECRET1234") {
			t.Errorf("the secret was stored: %+v", is)
		}
	}
}
