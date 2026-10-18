//go:build integration

package repository

import (
	"archive/tar"
	"bytes"
	"errors"
	"testing"
)

func TestExplodeAcceptsEntriesStartingWithDot(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	var archive bytes.Buffer

	tw := tar.NewWriter(&archive)

	for _, e := range []struct {
		name, body string
		dir        bool
	}{
		{
			name: "./",
			dir:  true,
		},
		{
			name: "./zero.txt",
			body: "zero",
		},
		{
			name: "./sub/",
			dir:  true,
		},
		{
			name: "./sub/two.txt",
			body: "two",
		},
	} {
		h := &tar.Header{
			Name:     e.name,
			Mode:     0o644,
			Size:     int64(len(e.body)),
			Typeflag: tar.TypeReg,
		}
		if e.dir {
			h.Typeflag = tar.TypeDir
		}

		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Explode(t.Context(), DeployInput{
		Repository: "generic-local",
		Path:       "expt/t.tar",
		Body:       &archive,
		User:       "admin",
	}); err != nil {
		t.Fatalf("Explode = %v", err)
	}

	for _, p := range []string{
		"expt/zero.txt",
		"expt/sub/two.txt",
	} {
		if _, err := s.Stat(t.Context(), "generic-local", p); err != nil {
			t.Errorf("%s after the explode: %v", p, err)
		}
	}
}

func TestArchiveEntryPath(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"./a.txt":     "a.txt",
		"a/./b.txt":   "a/b.txt",
		"././a/b.txt": "a/b.txt",
		"../a.txt":    "",
		"a/../../b":   "",
	} {
		got, err := archiveEntryPath(name)
		if want == "" {
			if !errors.Is(err, ErrInvalidPath) {
				t.Errorf("archiveEntryPath(%q) = %q, %v; want ErrInvalidPath", name, got, err)
			}

			continue
		}

		if err != nil || got != want {
			t.Errorf("archiveEntryPath(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestExplodeStoresNoEntryOverTheLimit(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	var archive bytes.Buffer

	tw := tar.NewWriter(&archive)
	for name, body := range map[string]string{
		"a.txt": "12345",
		"b.txt": "1234567890",
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	x := &exploder{
		s: s,
		in: DeployInput{
			Repository: "generic-local",
			User:       "admin",
		},
		dir:   "x",
		limit: 8,
	}

	if err := x.tar(t.Context(), &archive); !errors.Is(err, ErrUnsupportedArchive) {
		t.Fatalf("explode over the limit = %v, want ErrUnsupportedArchive", err)
	}

	if it, err := s.Stat(t.Context(), "generic-local", "x/b.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the entry over the limit was stored: %+v %v", it, err)
	}
}
