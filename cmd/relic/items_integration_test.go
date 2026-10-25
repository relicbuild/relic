//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func adminCLI(t *testing.T, repos ...string) (config, dir string) {
	t.Helper()

	srv := testServer(t)
	dir = t.TempDir()
	config = filepath.Join(dir, "config.yaml")

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

	docs := make([]string, 0, len(repos))
	for _, r := range repos {
		docs = append(
			docs,
			"apiVersion: relic.build/v1\nkind: Repository\nmetadata: {name: "+r+"}\nspec: {class: local, format: generic}\n",
		)
	}

	if len(docs) == 0 {
		return config, dir
	}

	if _, err := relic(t, config, strings.Join(docs, "---\n"), "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	return config, dir
}

func put(t *testing.T, config, dir, target, content string) {
	t.Helper()

	f := filepath.Join(dir, "upload")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "artifact", "upload", f, target); err != nil {
		t.Fatalf("upload %s: %v", target, err)
	}
}

func TestFileCommands(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t, "files-a", "files-b")
	put(t, config, dir, "files-a/org/app/1.0/app.jar", "jar")
	put(t, config, dir, "files-a/org/app/1.0/app.pom", "pom")

	download := func(target string) (string, error) {
		return relic(t, config, "", "artifact", "download", target, "-o", "-")
	}

	if out, err := relic(
		t,
		config,
		"",
		"artifact",
		"cp",
		"files-a/org/app/1.0/app.jar",
		"files-b/copy/app.jar",
	); err != nil || !hasTableRow(out, "files-a/org/app/1.0/app.jar", "files-b/copy/app.jar", "copied") {
		t.Errorf("cp = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"artifact",
		"cp",
		"files-a/org/app/1.0",
		"files-b/org/app/",
		"--dry-run",
	); err != nil ||
		!hasTableRowWith(out, "copied (dry run)") {
		t.Errorf("cp --dry-run = %q %v", out, err)
	}

	if _, err := download("files-b/org/app/1.0/app.pom"); err == nil {
		t.Error("cp --dry-run copied")
	}

	if _, err := relic(t, config, "", "artifact", "cp", "files-a/org/app/1.0", "files-b/org/app/"); err != nil {
		t.Fatal(err)
	}

	if out, err := download("files-b/org/app/1.0/app.pom"); err != nil || out != "pom" {
		t.Errorf("copied folder = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"artifact",
		"cp",
		"files-a/org/app/1.0/app.jar",
		"files-a/org/app/1.0/app.pom",
		"files-b/many",
	); err != nil {
		t.Fatal(err)
	}

	if out, err := download("files-b/many/app.pom"); err != nil || out != "pom" {
		t.Errorf("several sources = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "artifact", "mv", "files-b/copy/app.jar", "files-b/moved/"); err != nil {
		t.Fatal(err)
	}

	if out, err := download("files-b/moved/app.jar"); err != nil || out != "jar" {
		t.Errorf("moved = %q %v", out, err)
	}

	if _, err := download("files-b/copy/app.jar"); err == nil {
		t.Error("mv left the source")
	}

	if _, err := relic(
		t,
		config,
		"",
		"artifact",
		"props",
		"set",
		"files-a/org/app/1.0",
		"qa=passed,signed",
		"release=1.0",
		"-r",
	); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "artifact", "props", "get", "files-a/org/app/1.0/app.jar"); err != nil ||
		!hasTableRow(out, "qa", "passed,signed") || !hasTableRow(out, "release", "1.0") {
		t.Errorf("props get = %q %v", out, err)
	}

	out, err := relic(t, config, "", "artifact", "props", "get", "files-a/org/app/1.0/app.jar", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Properties map[string]struct {
			Values []string
		}
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Properties["qa"].Values) != 2 {
		t.Errorf("props get -o json = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"artifact",
		"props",
		"get",
		"files-a/org/app/1.0/app.jar",
		"-o",
		"yaml",
	); err != nil ||
		!strings.Contains(out, "properties:\n  qa:\n    values:\n      - passed\n") {
		t.Errorf("props get -o yaml = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "artifact", "props", "delete", "files-a/org/app/1.0", "qa", "-r"); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "artifact", "props", "get", "files-a/org/app/1.0/app.pom"); err != nil ||
		!hasTableRow(out, "release", "1.0") || len(tableRows(out)) != 2 {
		t.Errorf("props after delete = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "artifact", "props", "set", "files-a/org/app/1.0", "novalue"); err == nil {
		t.Error("props set accepted an argument without =")
	}

	if out, err := relic(t, config, "", "artifact", "rm", "files-b/many", "files-b/moved/app.jar"); err != nil ||
		!hasTableRow(out, "files-b/many", "deleted") || !hasTableRow(out, "files-b/moved/app.jar", "deleted") {
		t.Errorf("rm = %q %v", out, err)
	}

	if _, err := download("files-b/many/app.pom"); err == nil {
		t.Error("rm left the folder")
	}

	if _, err := relic(t, config, "", "artifact", "rm", "files-b/many"); err == nil {
		t.Error("rm of a missing item succeeded")
	}

	if _, err := relic(t, config, "", "artifact", "rm", "files-b/many", "--ignore-not-found"); err != nil {
		t.Errorf("rm --ignore-not-found: %v", err)
	}
}

func TestLfsClean(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	config, dir := adminCLI(t, "lfs-objects")

	keep := strings.Repeat("a", 64)
	drop := strings.Repeat("b", 64)

	put(t, config, dir, "lfs-objects/objects/aa/aa/"+keep, "kept")
	put(t, config, dir, "lfs-objects/objects/bb/bb/"+drop, "dropped")
	put(t, config, dir, "lfs-objects/README", "not an LFS object")

	work := filepath.Join(dir, "work")
	gitRun := func(args ...string) {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "git", append([]string{
			"-C",
			work,
		}, args...)...)
		cmd.Env = append(
			os.Environ(),
			"GIT_AUTHOR_NAME=t",
			"GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@example.com",
		)

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}

	gitRun("init", "-q", "-b", "main")

	files := map[string]string{
		".lfsconfig": "[lfs]\n\turl = https://relic.example/api/lfs/lfs-objects\n",
		"big.bin":    "version https://git-lfs.github.com/spec/v1\noid sha256:" + keep + "\nsize 4\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "lfs")

	out, err := relic(
		t,
		config,
		"",
		"artifact",
		"lfs",
		"clean",
		"--git-dir",
		work,
		"--refs",
		"refs/heads/*",
		"--dry-run",
		"-v",
	)
	if err != nil ||
		!hasTableRowWith(out, "lfs-objects/objects/bb/bb/"+drop, "would delete") ||
		!strings.HasSuffix(out, "lfs-objects: 1 objects referenced, 2 stored, would delete 1\n") {
		t.Errorf("dry run = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"artifact",
		"download",
		"lfs-objects/objects/bb/bb/"+drop,
		"-o",
		"-",
	); err != nil {
		t.Errorf("dry run deleted: %v", err)
	}

	out, err = relic(
		t,
		config,
		"",
		"artifact",
		"lfs",
		"clean",
		"--git-dir",
		work,
		"--refs",
		"refs/heads/*",
		"-o",
		"json",
	)
	if err != nil || !strings.Contains(out, drop) || strings.Contains(out, keep) {
		t.Errorf("clean = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"artifact",
		"download",
		"lfs-objects/objects/bb/bb/"+drop,
		"-o",
		"-",
	); err == nil {
		t.Error("the unreferenced object is still there")
	}

	if out, err := relic(
		t,
		config,
		"",
		"artifact",
		"download",
		"lfs-objects/objects/aa/aa/"+keep,
		"-o",
		"-",
	); err != nil ||
		out != "kept" {
		t.Errorf("referenced object = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "artifact", "lfs", "clean", "--git-dir", work, "--dry-run"); err != nil ||
		!strings.HasSuffix(out, "0 objects referenced, 1 stored, would delete 1\n") {
		t.Errorf("default refs = %q %v", out, err)
	}

	gitRun("update-ref", "refs/remotes/origin/main", "HEAD")

	if out, err := relic(t, config, "", "artifact", "lfs", "clean", "--git-dir", work, "--dry-run"); err != nil ||
		!strings.HasSuffix(out, "1 objects referenced, 1 stored, would delete 0\n") {
		t.Errorf("default refs with a remote-tracking branch = %q %v", out, err)
	}
}
