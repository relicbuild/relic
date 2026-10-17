//go:build integration

package repository

import (
	"archive/tar"
	"bytes"
	"errors"
	"testing"
)

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
