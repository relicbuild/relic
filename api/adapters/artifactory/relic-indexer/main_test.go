package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionEndsWithTheReportedVersion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if code := run(context.Background(), []string{
		"version",
	}, &out, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}

	fields := strings.Fields(out.String())
	if fields[len(fields)-1] != "3.131.18" {
		t.Errorf("version output %q: the last word is not the reported version", out.String())
	}
}

func TestGraphOfPlainFile(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)

	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	code := run(context.Background(), []string{
		"graph",
		file,
		"--temp-dir",
		tempDir,
	}, &stdout, &stderr)

	var n Node
	if code != 0 || json.Unmarshal(stdout.Bytes(), &n) != nil {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}

	const helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if n.ComponentID != "generic://sha256:"+helloSHA256+"/notes.txt" || n.SHA256 != helloSHA256 {
		t.Errorf("root = %+v", n)
	}
}

func TestGraphOfBrokenImageArchive(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)

	file := filepath.Join(t.TempDir(), "image.tar")

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	manifest := `[{"Config":"missing.json","RepoTags":["broken:1"],"Layers":["missing/layer.tar"]}]`
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Mode: 0o644,
		Size: int64(len(manifest)),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tw.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{
		"graph",
		file,
		"--temp-dir",
		tempDir,
	}, &stdout, &stderr); code != 1 {
		t.Errorf("exit %d, want 1 so the caller sees the failure; stderr %q", code, stderr.String())
	}
}

func TestGraphMissingFile(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if code := run(context.Background(), []string{
		"graph",
		"/nonexistent",
	}, &out, &out); code == 0 {
		t.Error("missing file indexed")
	}
}
