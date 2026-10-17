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
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/scripttest"
	"relic/internal/scanning/scan"
)

const goGrypeReport = `{"matches":[
 {"vulnerability":{"id":"GHSA-ppp9-7jff-5vj2","severity":"High","description":"Out-of-bounds read in golang.org/x/text/language"},
  "relatedVulnerabilities":[{"id":"CVE-2021-38561"}],
  "artifact":{"name":"golang.org/x/text","version":"v0.3.6","type":"go-module","purl":"pkg:golang/golang.org/x/text@v0.3.6"}},
 {"vulnerability":{"id":"GHSA-69ch-w2m2-3vjp","severity":"High","description":"Denial of service in golang.org/x/text/language"},
  "relatedVulnerabilities":[{"id":"CVE-2022-32149"}],
  "artifact":{"name":"golang.org/x/text","version":"v0.3.6","type":"go-module","purl":"pkg:golang/golang.org/x/text@v0.3.6"}}],
 "descriptor":{"db":{"status":{"built":"2026-10-01T00:00:00Z"}}}}`

func TestScanReachability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(db, blobs, log)

	if err := repos.CreateRepository(ctx, repository.Repository{
		Name:   "go-apps",
		Class:  "local",
		Format: "generic",
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	body := "a Go executable"
	_ = tw.WriteHeader(&tar.Header{
		Name:     "bin/app",
		Mode:     0o755,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = zw.Close()

	if _, err := repos.Deploy(ctx, repository.DeployInput{
		Repository: "go-apps",
		Path:       "app-1.0.tar.gz",
		Body:       &buf,
		User:       "admin",
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	fixture, err := filepath.Abs("testdata/govulncheck-reach.json")
	if err != nil {
		t.Fatal(err)
	}

	report := filepath.Join(dir, "grype.json")
	if err := os.WriteFile(report, []byte(goGrypeReport), 0o600); err != nil {
		t.Fatal(err)
	}

	noreach, err := filepath.Abs("testdata/govulncheck-noreach.json")
	if err != nil {
		t.Fatal(err)
	}

	current := filepath.Join(dir, "govulncheck-report")
	if err := os.WriteFile(current, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	called := filepath.Join(dir, "govulncheck-args")
	scripttest.Write(t, filepath.Join(dir, "syft"), `#!/bin/sh
echo '{"artifacts":[{"type":"go-module","metadataType":"go-module-buildinfo-entry","name":"golang.org/x/text","version":"v0.3.6","purl":"pkg:golang/golang.org/x/text@v0.3.6","locations":[{"path":"/bin/app"}]}]}'
`)
	scripttest.Write(t, filepath.Join(dir, "grype"), "#!/bin/sh\ncat "+shellQuoted(report)+"\n")
	scripttest.Write(t, filepath.Join(dir, "govulncheck"), `#!/bin/sh
echo "$@" >>`+shellQuoted(called)+`
for last; do :; done
[ -f "$last" ] || exit 3
[ "$1" = -mode=extract ] && { echo "extract of $(cat "$last")"; exit 0; }
grep -q "extract of a Go executable" "$last" || exit 4
cat "$(cat `+shellQuoted(current)+`)"
`)

	goDB := filepath.Join(dir, "go")
	if err := os.MkdirAll(filepath.Join(goDB, "index"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(goDB, "index", "db.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	svc := scan.NewService(db, blobs, scan.Syft{
		Path: filepath.Join(dir, "syft"),
	}, scan.Grype{
		Exec: scan.Exec{
			Path: filepath.Join(dir, "grype"),
		},
		DatabaseDir: dir,
	}, scan.Limits{}, log)
	svc.SetReachability(&scan.GoReachability{
		Exec: scan.Exec{
			Path: filepath.Join(dir, "govulncheck"),
		},
		DatabaseDir: goDB,
	})

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}

	sha := claimed[0].Sha256
	if err := svc.ScanArtifact(ctx, sha, claimed[0].FileName); err != nil {
		t.Fatal(err)
	}

	if a, _ := db.GetScanArtifact(ctx, sha); a.Status != scan.StatusDone {
		t.Fatalf("status = %s (%s)", a.Status, a.Reason)
	}

	if args, _ := os.ReadFile(called); !strings.Contains(string(args), "-mode=binary") ||
		!strings.Contains(string(args), "-db=file://"+goDB) {
		t.Errorf("govulncheck ran with %q", args)
	}

	extracts, err := db.ListGoExtracts(ctx, sha)
	if err != nil || len(extracts) != 1 || extracts[0].Executable != "/bin/app" {
		t.Fatalf("extracts = %+v, %v", extracts, err)
	}

	results := scan.NewResults(db)

	findings, err := results.Findings(ctx, sha)
	if err != nil || len(findings) != 2 {
		t.Fatalf("findings = %v, %v", findings, err)
	}

	index, err := results.Reachability(ctx, []string{
		sha,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}

	for _, f := range findings {
		state, trace := index.State(sha, f.ID, f.CVEs(), f.Package.PURL)
		got[f.ID] = state

		if state == scan.Reachable && !strings.Contains(trace, "language.Parse") {
			t.Errorf("trace of %s = %q", f.ID, trace)
		}
	}

	if got["GHSA-ppp9-7jff-5vj2"] != scan.Reachable || got["GHSA-69ch-w2m2-3vjp"] != scan.NotReachable {
		t.Errorf("reachability = %v", got)
	}

	if err := os.WriteFile(current, []byte(noreach), 0o600); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.CheckReachability(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("a check before the database changed analyzed %d scans, %v", n, err)
	}

	changed := time.Now()

	if n, err := svc.CheckReachability(ctx, changed); err != nil || n != 1 {
		t.Fatalf("checked %d scans, %v", n, err)
	}

	if n, err := svc.CheckReachability(ctx, changed); err != nil || n != 0 {
		t.Fatalf("checked %d scans again, %v", n, err)
	}

	index, err = results.Reachability(ctx, []string{
		sha,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range findings {
		if state, _ := index.State(sha, f.ID, f.CVEs(), f.Package.PURL); state != scan.NotReachable {
			t.Errorf("after the database changed, %s is %s", f.ID, state)
		}
	}

	if err := os.WriteFile(current, []byte(filepath.Join(dir, "no-such-report")), 0o600); err != nil {
		t.Fatal(err)
	}

	failedAt := time.Now()

	if n, err := svc.CheckReachability(ctx, failedAt); err == nil || n != 1 {
		t.Fatalf("a check whose analysis fails = %d scans, %v; want the one scan and its error", n, err)
	}

	if n, err := svc.CheckReachability(ctx, failedAt); err != nil || n != 0 {
		t.Errorf("the check after a failed one = %d scans, %v; want the failed scan not selected again", n, err)
	}
}
