package main

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestZipModuleKeepsFileModes(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix file modes")
	}

	dir := t.TempDir()
	for name, mode := range map[string]fs.FileMode{
		"main.tf": 0o644,
		"run.sh":  0o755,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}

	archive, err := zipModule(dir)
	if err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}

	modes := map[string]fs.FileMode{}
	for _, f := range zr.File {
		modes[f.Name] = f.Mode()

		if f.Method != zip.Deflate {
			t.Errorf("%s method = %d, want deflate", f.Name, f.Method)
		}
	}

	for name, want := range map[string]fs.FileMode{
		"main.tf": 0o644,
		"run.sh":  0o755,
	} {
		if modes[name] != want {
			t.Errorf("%s mode = %v, want %v", name, modes[name], want)
		}
	}
}
