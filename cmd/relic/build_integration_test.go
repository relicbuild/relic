//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"relic/api/adapters/artifactory/access"
	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/app/backgroundjobs"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/localdisk"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"
)

type syncQueue struct {
	scans *scan.Service
}

func (q syncQueue) Insert(
	ctx context.Context,
	args river.JobArgs,
	_ *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	a, ok := args.(backgroundjobs.GraphScanArgs)
	if !ok {
		return nil, errors.New("unexpected job " + args.Kind())
	}

	return &rivertype.JobInsertResult{}, q.scans.RunGraph(ctx, a.ID, true)
}

func scanningServer(t *testing.T) *httptest.Server {
	t.Helper()

	db := databasetest.NewDB(t)
	log := slog.New(slog.DiscardHandler)

	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	report, _ := filepath.Abs("../../internal/scanning/scan/testdata/grype.json")
	grype := filepath.Join(t.TempDir(), "grype")

	//nolint:gosec // a test script
	if err := os.WriteFile(grype, []byte("#!/bin/sh\ncat "+report+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	blobs := objectstoretest.NewMemory()
	svc := scan.NewService(db, blobs, nil, scan.Grype{
		Exec: scan.Exec{
			Path: grype,
		},
	}, scan.Limits{}, log)
	srv := httptest.NewServer(app.NewHandler(db, blobs, tokens, syncQueue{
		scans: svc,
	}, app.Options{}, log))
	t.Cleanup(srv.Close)

	return srv
}

//nolint:paralleltest // collect-env reads the process environment, set here with t.Setenv
func TestBuildCommands(t *testing.T) {
	srv := scanningServer(t)
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
		"apiVersion: relic.build/v1\nkind: Repository\nmetadata: {name: b-dev}\nspec: {class: local, format: generic}\n"+
			"---\napiVersion: relic.build/v1\nkind: Repository\nmetadata: {name: b-rel}\nspec: {class: local, format: generic}\n",
		"apply",
		"-f",
		"-",
	); err != nil {
		t.Fatal(err)
	}

	jar := filepath.Join(dir, "app.jar")
	dep := filepath.Join(dir, "lodash-4.17.15.tgz")

	for f, content := range map[string]string{
		jar: "jar",
		dep: "tgz",
	} {
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("RELIC_TEST_BUILD_VAR", "visible")
	t.Setenv("RELIC_TEST_TOKEN", "hidden")
	t.Setenv("RELIC_TEST_GPG_PASSPHRASE", "hidden")
	t.Setenv("RELIC_TEST_DB_PASS", "hidden")
	t.Setenv("RELIC_TEST_CREDENTIALS", "hidden")

	steps := [][]string{
		{
			"build",
			"collect-env",
			"app",
			"7",
			"--include",
			"RELIC_TEST_*",
		},
		{
			"artifact",
			"upload",
			jar,
			"b-dev/org/app/1.0/app.jar",
			"--build",
			"app/7",
		},
		{
			"build",
			"add-dependencies",
			"app",
			"7",
			dep,
			"--module",
			"web",
			"--type",
			"npm",
		},
	}
	for _, s := range steps {
		if out, err := relic(t, config, "", s...); err != nil {
			t.Fatalf("%v: %q %v", s, out, err)
		}
	}

	if out, err := relic(t, config, "", "artifact", "props", "get", "b-dev/org/app/1.0/app.jar"); err != nil ||
		!hasTableRow(out, "build.name", "app") || !hasTableRow(out, "build.number", "7") ||
		!strings.Contains(out, "build.timestamp") {
		t.Errorf("properties of the file uploaded for app/7 = %q %v", out, err)
	}

	run, err := (&globals{
		getenv: func(string) string {
			return ""
		},
		config: config,
	}).loadBuild("app", "7")
	if err != nil {
		t.Fatal(err)
	}

	if run.GetProperties()["env.RELIC_TEST_BUILD_VAR"] != "visible" ||
		slices.Contains(slices.Collect(maps.Values(run.GetProperties())), "hidden") {
		t.Errorf("properties = %v", run.GetProperties())
	}

	if len(run.GetModules()) != 2 || run.GetModules()[0].GetArtifacts()[0].GetSha256() == "" {
		t.Fatalf("modules = %v", run.GetModules())
	}

	run.Modules[1].Dependencies[0].Id = "lodash:4.17.15"

	if err := (&globals{
		getenv: func(string) string {
			return ""
		},
		config: config,
	}).saveBuild(run); err != nil {
		t.Fatal(err)
	}

	if _, err := exec.LookPath("git"); err == nil {
		work := filepath.Join(dir, "work")
		for _, a := range [][]string{
			{
				"init",
				"-q",
				"-b",
				"main",
				work,
			},
			{
				"-C",
				work,
				"remote",
				"add",
				"origin",
				"https://ci:secret@git.example/app.git",
			},
			{
				"-C",
				work,
				"-c",
				"user.name=t",
				"-c",
				"user.email=t@example.com",
				"commit",
				"-q",
				"--allow-empty",
				"-m",
				"x",
			},
		} {
			if out, err := exec.CommandContext(t.Context(), "git", a...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", a, err, out)
			}
		}

		out, err := relic(t, config, "", "build", "add-git", "app", "7", work)
		if err != nil || !strings.Contains(out, "https://git.example/app.git main ") ||
			strings.Contains(out, "secret") {
			t.Errorf("add-git = %q %v", out, err)
		}
	} else {
		t.Log("git is not installed: build add-git is not exercised")
	}

	if out, err := relic(t, config, "", "build", "publish", "app", "7", "--dry-run"); err != nil ||
		!strings.Contains(out, "would publish: 2 modules, 1 artifacts, 1 dependencies") {
		t.Errorf("dry run = %q %v", out, err)
	}

	unlockBuild, err := localdisk.Lock(filepath.Join(dir, "builds", "app", "7.json.lock"))
	if err != nil {
		t.Fatal(err)
	}

	type publishResult struct {
		out string
		err error
	}

	published := make(chan publishResult, 1)

	go func() {
		out, err := relic(t, config, "", "build", "publish", "app", "7")
		published <- publishResult{
			out: out,
			err: err,
		}
	}()

	var result publishResult

	select {
	case result = <-published:
		t.Error("publish went ahead while another relic build command held the build")
		unlockBuild()
	case <-time.After(2 * time.Second):
		unlockBuild()

		result = <-published
	}

	if result.err != nil || !strings.HasPrefix(result.out, "app/7 published") {
		t.Fatalf("publish = %q %v", result.out, result.err)
	}

	if out, _ := relic(
		t,
		config,
		"",
		"build",
		"publish",
		"app",
		"7",
		"--dry-run",
	); !strings.Contains(
		out,
		"0 modules",
	) {
		t.Errorf("after publish = %q", out)
	}

	out, err := relic(t, config, "", "build", "scan", "app", "7")
	if err != nil || !strings.Contains(out, "2 findings (critical 0, high 2") ||
		!strings.Contains(out, "pkg:npm/lodash@4.17.15") {
		t.Errorf("scan = %q %v", out, err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Policy
metadata: {name: b-sec}
spec: {kind: security, rules: [{name: any, priority: 1, minSeverity: low, failBuild: true}]}
---
apiVersion: relic.build/v1
kind: Watch
metadata: {name: b-watch}
spec: {active: true, scopes: [{kind: build, names: [app]}], policies: [b-sec]}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	out, err = relic(t, config, "", "build", "scan", "app", "7", "-o", "json")

	var ec exitCode

	var res struct {
		FailBuild  bool
		Violations []struct {
			Policy string
		}
	}
	if !errors.As(err, &ec) || ec != 1 || json.Unmarshal([]byte(out), &res) != nil || !res.FailBuild ||
		len(res.Violations) != 2 {
		t.Errorf("scan with a policy = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "build", "scan", "app", "7", "--fail=false"); err != nil {
		t.Errorf("scan --fail=false: %v", err)
	}

	if out, err := relic(t, config, "", "build", "promote", "app", "7", "b-rel", "--dry-run"); err != nil ||
		!strings.Contains(out, "would move b-dev/org/app/1.0/app.jar to b-rel/org/app/1.0/app.jar") ||
		!strings.Contains(out, "app/7: dry run, nothing changed") || strings.Contains(out, "promoted") {
		t.Errorf("promote --dry-run = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "artifact", "download", "b-rel/org/app/1.0/app.jar", "-o", "-"); err == nil {
		t.Error("promote --dry-run copied")
	}

	if out, err := relic(
		t,
		config,
		"",
		"build",
		"promote",
		"app",
		"7",
		"b-rel",
		"--copy",
		"--status",
		"released",
		"--props",
		"release=1.0",
	); err != nil {
		t.Fatalf("promote = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "artifact", "props", "get", "b-rel/org/app/1.0/app.jar"); err != nil ||
		!hasTableRow(out, "release", "1.0") {
		t.Errorf("promoted file = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "build", "append", "release", "1", "app", "7"); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "build", "publish", "release", "1", "--dry-run", "-o", "json"); err != nil ||
		!strings.Contains(out, `"id": "app/7"`) || !strings.Contains(out, `"type": "build"`) {
		t.Errorf("append = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "build", "clean", "release", "1"); err != nil || out != "release/1 cleaned\n" {
		t.Errorf("clean = %q %v", out, err)
	}

	for _, n := range []string{
		"8",
		"9",
	} {
		if _, err := relic(t, config, "", "build", "publish", "app", n); err != nil {
			t.Fatal(err)
		}
	}

	for _, left := range []string{
		"release/1.json",
		"release/1.json.lock",
		"app/8.json",
		"app/8.json.lock",
	} {
		if _, err := os.Stat(filepath.Join(dir, "builds", left)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("builds/%s after clean or publish: %v, want it removed", left, err)
		}
	}

	if _, err := relic(t, config, "", "build", "discard", "app"); err == nil {
		t.Error("discard without a limit succeeded")
	}

	if out, err := relic(t, config, "", "build", "discard", "app", "--max-days", "-5"); err == nil {
		t.Errorf("discard with a negative --max-days = %q, want an error", out)
	}

	if out, err := relic(t, config, "", "build", "discard", "missing", "--max-runs", "1"); err == nil {
		t.Errorf("discard of a build without runs = %q, want an error", out)
	}

	if _, err := relic(t, config, "", "build", "discard", "app", "--max-runs", "1"); err != nil {
		t.Fatal(err)
	}

	if out, _ := relic(t, config, "", "api", "builds/app/runs"); strings.Count(out, `"number"`) != 1 {
		t.Errorf("runs after discard = %s", out)
	}
}
