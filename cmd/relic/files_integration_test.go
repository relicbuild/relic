//go:build integration

package main

import (
	"os"
	"path/filepath"
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
