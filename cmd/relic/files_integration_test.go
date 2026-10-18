//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadIntoAFolder(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t, "upload-into")

	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("into a folder"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := relic(t, config, "", "artifact", "upload", f, "upload-into/newdir/")
	if err != nil {
		t.Fatalf("upload into newdir/ = %v", err)
	}

	if want := "upload-into/newdir/a.txt "; len(out) < len(want) || out[:len(want)] != want {
		t.Errorf("upload into newdir/ printed %q, want it to start with %q", out, want)
	}

	if got, err := relic(t, config, "", "artifact", "download", "upload-into/newdir/a.txt", "-o", "-"); err != nil ||
		got != "into a folder" {
		t.Errorf("download newdir/a.txt = %q, %v", got, err)
	}
}

func TestDownloadIntoADirectory(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t, "download-into")
	put(t, config, dir, "download-into/dir/a.txt", "hi")

	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "artifact", "download", "download-into/dir/a.txt", "-o", out); err != nil {
		t.Fatalf("download -o an existing directory = %v", err)
	}

	if b, err := os.ReadFile(filepath.Join(out, "a.txt")); err != nil || string(b) != "hi" {
		t.Errorf("out/a.txt = %q, %v; want hi", b, err)
	}

	missing := filepath.Join(dir, "missing") + "/"

	_, err := relic(t, config, "", "artifact", "download", "download-into/dir/a.txt", "-o", missing)
	if err == nil || strings.Contains(err.Error(), ".relic-download") ||
		!strings.Contains(err.Error(), "no directory") {
		t.Errorf("download -o a missing directory = %v, want it to name the missing directory", err)
	}
}
