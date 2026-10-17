//go:build integration

package scan_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/scripttest"
	"relic/internal/scanning/scan"
)

func shellQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fakeTool(t *testing.T, name, report string) scan.Exec {
	t.Helper()

	body := "#!/bin/sh\necho broken >&2\nexit 2\n"

	if report != "" {
		abs, err := filepath.Abs(report)
		if err != nil {
			t.Fatal(err)
		}

		body = "#!/bin/sh\ncat " + shellQuoted(abs) + "\n"
	}

	p := filepath.Join(t.TempDir(), name)

	scripttest.Write(t, p, body)

	return scan.Exec{
		Path: p,
	}
}

func TestAnalyzers(t *testing.T) {
	t.Parallel()

	for name, tt := range map[string]struct {
		gitleaks, trivy string
		wantStatus      string
		wantSecrets     bool
	}{
		"both analyzers": {
			gitleaks:    "testdata/gitleaks.json",
			trivy:       "testdata/trivy-config.json",
			wantStatus:  scan.StatusDone,
			wantSecrets: true,
		},
		"gitleaks fails": {
			trivy:      "testdata/trivy-config.json",
			wantStatus: scan.StatusPartial,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			db := databasetest.NewDB(t)
			blobs := objectstoretest.NewMemory()
			log := slog.New(slog.DiscardHandler)
			repos := repository.NewService(db, blobs, log)

			if err := repos.CreateRepository(ctx, repository.Repository{
				Name:   "charts",
				Class:  "local",
				Format: "generic",
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := repos.Deploy(ctx, repository.DeployInput{
				Repository: "charts",
				Path:       "demo-0.1.0.tgz",
				Body: strings.NewReader(tgz(t, map[string]string{
					"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 0.1.0\n",
				})),
				User: "admin",
			}); err != nil {
				t.Fatal(err)
			}

			syft, grype, _ := fakeScanners(t)
			svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)
			svc.SetAnalyzers(
				scan.Gitleaks{
					Exec: fakeTool(t, "gitleaks", tt.gitleaks),
				},
				scan.Trivy{
					Exec: fakeTool(t, "trivy", tt.trivy),
				},
			)

			claimed, err := svc.Claim(ctx, 10)
			if err != nil || len(claimed) != 1 {
				t.Fatalf("claim = %v %v", claimed, err)
			}

			if err := svc.ScanArtifact(ctx, claimed[0].Sha256, claimed[0].FileName); err != nil {
				t.Fatal(err)
			}

			a, err := db.GetScanArtifact(ctx, claimed[0].Sha256)
			if err != nil || a.Status != tt.wantStatus {
				t.Errorf("status = %s (%s) %v, want %s", a.Status, a.Reason, err, tt.wantStatus)
			}

			issues, err := scan.NewResults(db).Issues(ctx, claimed[0].Sha256)
			if err != nil {
				t.Fatal(err)
			}

			var secrets, misconfigs int

			for _, i := range issues {
				switch i.Analyzer {
				case scan.AnalyzerSecrets:
					secrets++
				case scan.AnalyzerMisconfiguration:
					misconfigs++
				}
			}

			if (secrets > 0) != tt.wantSecrets || misconfigs == 0 {
				t.Errorf("secrets %d, misconfigurations %d", secrets, misconfigs)
			}

			for i := 1; i < len(issues); i++ {
				if issues[i-1].Severity.Rank() < issues[i].Severity.Rank() {
					t.Errorf("order: %s before %s", issues[i-1].Severity, issues[i].Severity)
				}
			}

			if err := svc.ScanArtifact(ctx, claimed[0].Sha256, claimed[0].FileName); err != nil {
				t.Fatal(err)
			}

			again, _ := scan.NewResults(db).Issues(ctx, claimed[0].Sha256)
			if len(again) != len(issues) {
				t.Errorf("rescan: %d issues, then %d", len(issues), len(again))
			}
		})
	}
}
