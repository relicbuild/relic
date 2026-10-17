package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("disk full")
}

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "npmrc")
	link := filepath.Join(dir, ".npmrc")

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(target, []byte("always-auth=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(link, io.MultiReader(strings.NewReader("half"), failingReader{}), 0o600); err == nil {
		t.Fatal("a failed write was reported as written")
	}

	if got, err := os.ReadFile(target); err != nil || string(got) != "always-auth=true\n" {
		t.Errorf("after a failed write the file holds %q %v, want its old content", got, err)
	}

	if err := writeFileAtomic(link, strings.NewReader("registry=https://relic.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symbolic link was replaced: %v %v", fi.Mode(), err)
	}

	if got, err := os.ReadFile(target); err != nil || string(got) != "registry=https://relic.example.com\n" {
		t.Errorf("the link's target holds %q %v", got, err)
	}

	if entries, err := os.ReadDir(filepath.Dir(target)); err != nil || len(entries) != 1 {
		t.Errorf("the target's folder holds %d entries %v, want no temporary file left", len(entries), err)
	}
}
