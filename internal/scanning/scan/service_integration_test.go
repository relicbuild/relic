//go:build integration

package scan_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/scripttest"
	"relic/internal/platform/telemetry/telemetrytest"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/scan"

	"github.com/ulikunitz/xz"
)

func fakeScanners(t *testing.T) (scan.Syft, scan.Grype, string) {
	t.Helper()
	dir := t.TempDir()

	report, err := filepath.Abs("testdata/grype.json")
	if err != nil {
		t.Fatal(err)
	}

	seen := filepath.Join(dir, "sources")
	syft := "#!/bin/sh\necho \"$2\" >>" + seen + "\necho '{\"artifacts\":[{\"name\":\"lodash\"}]}'\n"

	grype := "#!/bin/sh\ncat " + report + "\n"
	for name, body := range map[string]string{
		"syft":  syft,
		"grype": grype,
	} {
		scripttest.Write(t, filepath.Join(dir, name), body)
	}

	return scan.Syft{
			Exec: scan.Exec{
				Path: filepath.Join(dir, "syft"),
			},
		},
		scan.Grype{
			Exec: scan.Exec{
				Path: filepath.Join(dir, "grype"),
			},
			DatabaseDir: dir,
		}, seen
}

func tgz(t *testing.T, files map[string]string) string {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)

	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		})
		_, _ = io.WriteString(tw, body)
	}

	_ = tw.Close()
	_ = gz.Close()

	return buf.String()
}

func TestScanPipeline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)

	repos := repository.NewService(db, blobs, log)
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "npm-local",
			Class:  "local",
			Format: "npm",
		},
	); err != nil {
		t.Fatal(err)
	}

	pkg := tgz(
		t,
		map[string]string{
			"package/package.json": `{"name":"x","version":"1.0.0"}`,
			"../../escape.txt":     "nope",
		},
	)
	for _, p := range []string{
		"x/-/x-1.0.0.tgz",
		"copy/x-1.0.0.tgz",
		"readme.md",
	} {
		if _, err := repos.Deploy(
			ctx,
			repository.DeployInput{
				Repository: "npm-local",
				Path:       p,
				Body:       strings.NewReader(pkg),
				User:       "admin",
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	syft, grype, seen := fakeScanners(t)
	svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v; want the one scannable content once", claimed, err)
	}

	if again, _ := svc.Claim(ctx, 10); len(again) != 0 {
		t.Errorf("content claimed twice: %v", again)
	}

	c := claimed[0]
	value := func(name string, labels map[string]string) float64 {
		return telemetrytest.Value(t, name, labels)
	}
	done, catalog, match := value("relic_scans_total", map[string]string{
		"result": "done",
	}),
		value("relic_scan_duration_seconds", map[string]string{
			"stage": "catalog",
		}),
		value("relic_scan_duration_seconds", map[string]string{
			"stage": "match",
		})

	if err := svc.ScanArtifact(ctx, c.Sha256, c.FileName); err != nil {
		t.Fatal(err)
	}

	if value("relic_scans_total", map[string]string{
		"result": "done",
	}) <= done ||
		value("relic_scan_duration_seconds", map[string]string{
			"stage": "catalog",
		}) <= catalog ||
		value("relic_scan_duration_seconds", map[string]string{
			"stage": "match",
		}) <= match {
		t.Error("scan not counted, or a scanner stage not timed")
	}

	a, err := db.GetScanArtifact(ctx, c.Sha256)
	if err != nil || a.Status != scan.StatusDone || a.Components != 1 || a.DbBuilt == nil || len(a.Sbom) == 0 {
		t.Fatalf("scan record = %+v, %v", a, err)
	}

	findings, err := db.ListFindings(ctx, c.Sha256)
	if err != nil || len(findings) != 3 {
		t.Fatalf("findings = %d, %v", len(findings), err)
	}

	src, _ := os.ReadFile(seen)
	if !strings.HasPrefix(string(src), "dir:") {
		t.Errorf("npm tarball not cataloged as an extracted directory: %q", src)
	}

	if err := svc.Rematch(ctx, c.Sha256); err != nil {
		t.Fatal(err)
	}

	if again, _ := db.ListFindings(ctx, c.Sha256); len(again) != 3 {
		t.Errorf("rematch findings = %d", len(again))
	}

	imported := `{"bomFormat": "CycloneDX", "specVersion": "1.5", "components": [
	  {"type": "library", "name": "lodash", "version": "4.17.15", "purl": "pkg:npm/lodash@4.17.15"},
	  {"type": "library", "name": "minimist", "version": "1.2.0", "purl": "pkg:npm/minimist@1.2.0"}]}`
	if err := scan.NewResults(db).ImportSBOM(ctx, c.Sha256, c.FileName, []byte(imported)); err != nil {
		t.Fatal(err)
	}

	if a, err := db.GetScanArtifact(ctx, c.Sha256); err != nil || a.Components != 2 || a.Reason != "SBOM imported" ||
		a.DbBuilt != nil {
		t.Fatalf("after the import, scan record = %+v, %v; want it stale until matched", a, err)
	}

	if cleared, _ := db.ListFindings(ctx, c.Sha256); len(cleared) != 0 {
		t.Errorf("findings of the replaced SBOM kept: %d", len(cleared))
	}

	if err := svc.Rematch(ctx, c.Sha256); err != nil {
		t.Fatal(err)
	}

	if matched, _ := db.ListFindings(ctx, c.Sha256); len(matched) != 3 {
		t.Errorf("findings of the imported SBOM = %d, want it matched", len(matched))
	}

	past, total, err := scan.NewResults(db).FindingsPage(ctx, scan.FindingsQuery{
		Skip: 1 << 32,
		Max:  50,
	})
	if err != nil || len(past) != 0 || total != 3 {
		t.Errorf("findings page past the end = %d rows of %d, %v; want none of 3", len(past), total, err)
	}

	gate := filepath.Join(t.TempDir(), "gate")
	started := filepath.Join(t.TempDir(), "started")
	slow := filepath.Join(t.TempDir(), "syft")
	scripttest.Write(t, slow, "#!/bin/sh\ntouch "+started+"\nwhile [ ! -e "+gate+" ]; do sleep 0.05; done\n"+
		"echo '{\"artifacts\":[{\"name\":\"lodash\"}]}'\n")
	slowSvc := scan.NewService(db, blobs, scan.Syft{
		Exec: scan.Exec{
			Path: slow,
		},
	}, grype, scan.Limits{}, log)

	scanned := make(chan error, 1)
	go func() {
		scanned <- slowSvc.ScanArtifact(ctx, c.Sha256, c.FileName)
	}()

	for _, err := os.Stat(started); err != nil; _, err = os.Stat(started) {
		time.Sleep(20 * time.Millisecond)
	}

	if err := scan.NewResults(db).ImportSBOM(ctx, c.Sha256, c.FileName, []byte(imported)); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := <-scanned; err != nil {
		t.Fatal(err)
	}

	if a, err := db.GetScanArtifact(ctx, c.Sha256); err != nil || a.Components != 2 || a.Reason != "SBOM imported" {
		t.Errorf("a scan that finished after an SBOM import replaced it: %+v, %v", a, err)
	}

	id, err := svc.QueueGraph(ctx, "admin", scan.GraphRequest{
		PURLs: []string{
			"pkg:npm/lodash@4.17.15",
			"pkg:npm/%40types/node@20.0.0",
		},
		Licenses: map[string][]string{
			"pkg:npm/lodash@4.17.15": {
				"mit OR apache-2.0",
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RunGraph(ctx, id, false); err != nil {
		t.Fatal(err)
	}

	g, err := db.GetGraphScan(ctx, id)

	var rep scan.Report
	if err != nil || g.Status != scan.StatusDone || json.Unmarshal(g.Result, &rep) != nil || len(rep.Findings) != 3 {
		t.Errorf("graph scan = %s %s, %v", g.Status, g.Result, err)
	}

	wantComponents := []scan.Component{
		{
			Name:    "lodash",
			Version: "4.17.15",
			Type:    "npm",
			PURL:    "pkg:npm/lodash@4.17.15",
			Licenses: []string{
				"mit OR apache-2.0",
			},
			LicenseIDs: []string{
				"MIT",
				"Apache-2.0",
			},
			Locations: []scan.Location{},
		},
		{
			Name:       "@types/node",
			Version:    "20.0.0",
			Type:       "npm",
			PURL:       "pkg:npm/%40types/node@20.0.0",
			Licenses:   []string{},
			LicenseIDs: []string{},
			Locations:  []scan.Location{},
		},
	}
	if !reflect.DeepEqual(rep.Components, wantComponents) {
		t.Errorf("graph scan components = %+v, want %+v", rep.Components, wantComponents)
	}
}

func TestScanStoredImage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)

	repos := repository.NewService(db, blobs, log)
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "docker-local",
			Class:  "local",
			Format: "docker",
		},
	); err != nil {
		t.Fatal(err)
	}

	deploy := func(p, body string) string {
		it, err := repos.Deploy(
			ctx,
			repository.DeployInput{
				Repository: "docker-local",
				Path:       p,
				Body:       strings.NewReader(body),
				User:       "admin",
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		return it.Checksums.SHA256
	}
	config := deploy("app/1.0/config", `{"architecture":"amd64","os":"linux"}`)
	layer := deploy("app/1.0/layer", tgz(t, map[string]string{
		"etc/os-release": "ID=alpine\n",
	}))
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",` +
		`"config":{"digest":"sha256:` + config + `"},"layers":[{"digest":"sha256:` + layer + `"}]}`
	deploy("app/1.0/manifest.json", manifest)

	syft, grype, seen := fakeScanners(t)
	svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)

	claimed, err := svc.Claim(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}

	var m string

	for _, c := range claimed {
		if c.FileName == "manifest.json" {
			m = c.Sha256
		}
	}

	if m == "" {
		t.Fatalf("manifest not claimed: %v", claimed)
	}

	if err := svc.ScanArtifact(ctx, m, "manifest.json"); err != nil {
		t.Fatal(err)
	}

	if a, _ := db.GetScanArtifact(ctx, m); a.Status != scan.StatusDone {
		t.Errorf("image scan status = %s (%s)", a.Status, a.Reason)
	}

	src, _ := os.ReadFile(seen)
	if !strings.Contains(string(src), "oci-dir:") {
		t.Errorf("image not cataloged as an OCI layout: %q", src)
	}
}

func TestScanWaitsForImageLayers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)

	repos := repository.NewService(db, blobs, log)
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "docker-local",
			Class:  "local",
			Format: "docker",
		},
	); err != nil {
		t.Fatal(err)
	}

	deploy := func(p, body string) string {
		it, err := repos.Deploy(
			ctx,
			repository.DeployInput{
				Repository: "docker-local",
				Path:       p,
				Body:       strings.NewReader(body),
				User:       "admin",
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		return it.Checksums.SHA256
	}
	layerBody := tgz(t, map[string]string{
		"etc/os-release": "ID=alpine\n",
	})
	layerSum := sha256.Sum256([]byte(layerBody))
	layer := hex.EncodeToString(layerSum[:])
	config := deploy("app/1.0/config", `{"architecture":"amd64","os":"linux"}`)
	m := deploy(
		"app/1.0/manifest.json",
		`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",`+
			`"config":{"digest":"sha256:`+config+`"},"layers":[{"digest":"sha256:`+layer+`"}]}`,
	)

	syft, grype, _ := fakeScanners(t)

	svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)
	if _, err := svc.Claim(ctx, 10); err != nil {
		t.Fatal(err)
	}

	if err := svc.ScanArtifact(ctx, m, "manifest.json"); err != nil {
		t.Fatal(err)
	}

	a, err := db.GetScanArtifact(ctx, m)
	if err != nil || a.Status != scan.StatusQueued || !strings.HasPrefix(a.Reason, "waiting") ||
		!strings.Contains(a.Reason, layer) {
		t.Fatalf("scan without layers = %s %q, %v; want queued, waiting for the layer", a.Status, a.Reason, err)
	}

	if again, _ := svc.Claim(ctx, 10); len(again) != 0 {
		t.Errorf("claimed again before the retry delay: %v", again)
	}

	deploy("app/1.0/layer", layerBody)

	if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
		Sha256: m,
		Status: scan.StatusQueued,
		Reason: a.Reason,
		Now:    time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	again, err := svc.Claim(ctx, 10)
	if err != nil || len(again) != 1 || again[0].Sha256 != m {
		t.Fatalf("reclaim = %v, %v; want the waiting manifest", again, err)
	}

	if more, _ := svc.Claim(ctx, 10); len(more) != 0 {
		t.Errorf("claimed twice: %v", more)
	}

	if err := svc.ScanArtifact(ctx, m, "manifest.json"); err != nil {
		t.Fatal(err)
	}

	if a, _ := db.GetScanArtifact(ctx, m); a.Status != scan.StatusDone {
		t.Errorf("scan with layers = %s (%s)", a.Status, a.Reason)
	}
}

func TestScanTarXz(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)

	repos := repository.NewService(db, blobs, log)
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "firmware",
			Class:  "local",
			Format: "generic",
		},
	); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	tw := tar.NewWriter(xw)
	body := `{"name":"x","version":"1.0.0"}`
	_ = tw.WriteHeader(
		&tar.Header{
			Name:     "app/package.json",
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		},
	)
	_, _ = io.WriteString(tw, body)
	_ = tw.Close()
	_ = xw.Close()

	if _, err := repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: "firmware",
			Path:       "sw/sd_copy.tar.xz",
			Body:       &buf,
			User:       "admin",
		},
	); err != nil {
		t.Fatal(err)
	}

	syft, grype, seen := fakeScanners(t)
	svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}

	if err := svc.ScanArtifact(ctx, claimed[0].Sha256, claimed[0].FileName); err != nil {
		t.Fatal(err)
	}

	if a, _ := db.GetScanArtifact(ctx, claimed[0].Sha256); a.Status != scan.StatusDone {
		t.Errorf("status = %s (%s)", a.Status, a.Reason)
	}

	if src, _ := os.ReadFile(seen); !strings.HasPrefix(string(src), "dir:") {
		t.Errorf("tar.xz not cataloged as an extracted directory: %q", src)
	}
}

func TestRunGraphLastAttemptFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	syft, _, _ := fakeScanners(t)
	failing := filepath.Join(t.TempDir(), "grype")
	scripttest.Write(t, failing, "#!/bin/sh\nexit 1\n")
	grype := scan.Grype{
		Exec: scan.Exec{
			Path: failing,
		},
		DatabaseDir: t.TempDir(),
	}
	svc := scan.NewService(db, objectstoretest.NewMemory(), syft, grype, scan.Limits{}, slog.New(slog.DiscardHandler))

	id, err := svc.QueueGraph(ctx, "admin", scan.GraphRequest{
		PURLs: []string{
			"pkg:npm/lodash@4.17.15",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		last       bool
		wantStatus string
	}{
		{
			"an earlier attempt leaves it for the retry",
			false,
			scan.StatusQueued,
		},
		{
			"the last attempt records the failure",
			true,
			scan.StatusFailed,
		},
	}
	for _, tt := range tests {
		if err := svc.RunGraph(ctx, id, tt.last); err == nil {
			t.Fatalf("%s: RunGraph succeeded with a failing grype", tt.name)
		}

		g, err := db.GetGraphScan(ctx, id)
		if err != nil || g.Status != tt.wantStatus {
			t.Errorf("%s: status = %q, %v; want %q", tt.name, g.Status, err, tt.wantStatus)
		}
	}

	unqueued, err := svc.QueueGraph(ctx, "admin", scan.GraphRequest{
		PURLs: []string{
			"pkg:npm/lodash@4.17.15",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.FailGraph(ctx, unqueued, errors.New("the job queue refused it")); err != nil {
		t.Fatal(err)
	}

	if g, err := db.GetGraphScan(ctx, unqueued); err != nil || g.Status != scan.StatusFailed ||
		g.Error != "the job queue refused it" {
		t.Errorf("a graph scan whose job was never queued = %+v, %v; want failed with the reason", g, err)
	}
}

func TestFailArtifactRecordsAMatchThatKeepsFailing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := databasetest.NewDB(t)
	blobs := objectstoretest.NewMemory()
	log := slog.New(slog.DiscardHandler)

	repos := repository.NewService(db, blobs, log)
	if err := repos.CreateRepository(
		ctx,
		repository.Repository{
			Name:   "npm-local",
			Class:  "local",
			Format: "npm",
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: "npm-local",
			Path:       "x/-/x-1.0.0.tgz",
			Body: strings.NewReader(tgz(t, map[string]string{
				"package/package.json": `{"name":"x","version":"1.0.0"}`,
			})),
			User: "admin",
		},
	); err != nil {
		t.Fatal(err)
	}

	syft, _, _ := fakeScanners(t)
	failing := filepath.Join(t.TempDir(), "grype")
	scripttest.Write(t, failing, "#!/bin/sh\necho 'ERROR failed to load vulnerability db' >&2\nexit 1\n")
	svc := scan.NewService(db, blobs, syft, scan.Grype{
		Exec: scan.Exec{
			Path: failing,
		},
		DatabaseDir: t.TempDir(),
	}, scan.Limits{}, log)

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}

	c := claimed[0]

	scanErr := svc.ScanArtifact(ctx, c.Sha256, c.FileName)
	if scanErr == nil {
		t.Fatal("ScanArtifact succeeded with a failing grype")
	}

	if a, _ := db.GetScanArtifact(ctx, c.Sha256); a.Status != scan.StatusQueued {
		t.Errorf("after a failed attempt, status = %s, want it queued for the retry", a.Status)
	}

	if err := svc.FailArtifact(ctx, c.Sha256, scanErr); err != nil {
		t.Fatal(err)
	}

	a, err := db.GetScanArtifact(ctx, c.Sha256)
	if err != nil || a.Status != scan.StatusFailed ||
		!strings.HasPrefix(a.Reason, "match: grype: exit status 1: ERROR failed to load vulnerability db") {
		t.Fatalf("after the last attempt, scan = %s (%s), %v; want failed with grype's error", a.Status, a.Reason, err)
	}

	if n, err := db.ForgetFailedMatches(ctx); err != nil || n != 1 {
		t.Fatalf("ForgetFailedMatches = %d, %v; want the failed match dropped", n, err)
	}

	if again, err := svc.Claim(ctx, 10); err != nil || len(again) != 1 {
		t.Errorf("claim after the failed match was dropped = %v, %v; want it scanned again", again, err)
	}
}

type failingAssessor struct {
	failing string
}

func (a failingAssessor) Assess(
	_ context.Context,
	pkgs []endoflife.Package,
	_ endoflife.Distro,
) ([]endoflife.Risk, error) {
	var out []endoflife.Risk

	for _, p := range pkgs {
		if p.Name == a.failing {
			return nil, errors.New("assessment failed")
		}

		out = append(out, endoflife.Risk{
			Kind:     endoflife.KindDeprecated,
			Severity: endoflife.SeverityMedium,
			PURL:     p.PURL,
			Name:     p.Name,
			Version:  p.Version,
		})
	}

	return out, nil
}

func (failingAssessor) Changed(context.Context) (time.Time, error) {
	return time.Now(), nil
}

func TestCheckOperationalRisksPassesAScanThatFails(t *testing.T) {
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
		"broken",
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
name=app
grep -rqs '"name":"broken"' "${2#dir:}" && name=broken
echo '{"artifacts":[{"name":"'$name'","version":"1.0.0","purl":"pkg:npm/'$name'@1.0.0"}]}'
`)
	svc := scan.NewService(db, blobs, scan.Syft{
		Exec: scan.Exec{
			Path: syftPath,
		},
	}, grype, scan.Limits{}, log)

	claimed, err := svc.Claim(ctx, 10)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim = %v %v", claimed, err)
	}

	for _, c := range claimed {
		if err := svc.ScanArtifact(ctx, c.Sha256, c.FileName); err != nil {
			t.Fatal(err)
		}
	}

	svc.SetOperationalRisk(failingAssessor{
		failing: "broken",
	})

	if n, err := svc.CheckOperationalRisks(ctx); err != nil || n != 1 {
		t.Fatalf("check = %d, %v; want the app assessed past the failing scan", n, err)
	}

	for _, c := range claimed {
		risks, err := scan.NewResults(db).OperationalRisks(ctx, c.Sha256)

		want := 1
		if c.FileName == "broken-1.0.0.tgz" {
			want = 0
		}

		if err != nil || len(risks) != want {
			t.Errorf("%s: risks = %+v, %v; want %d", c.FileName, risks, err, want)
		}
	}
}
