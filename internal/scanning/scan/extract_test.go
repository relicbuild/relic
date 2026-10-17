package scan

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()

	f, err := os.Create(path) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}

	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}

	_ = zw.Close()
	_ = f.Close()
}

func writeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}

		_, _ = tw.Write([]byte(body))
	}

	_ = tw.Close()
	_ = zw.Close()

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnzipRefusesEscapesAndEnforcesLimits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.whl")
	writeZip(t, archive, map[string]string{
		"ok/file.txt": "fine",
		"../evil.txt": "x",
		"/abs.txt":    "x",
	})

	dest := filepath.Join(dir, "out")
	_ = os.Mkdir(dest, 0o700)

	s := NewService(nil, nil, nil, nil, Limits{}, nil)
	if err := s.unzip(archive, dest); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dest, "ok", "file.txt")); err != nil {
		t.Errorf("regular entry not extracted: %v", err)
	}

	for _, p := range []string{
		filepath.Join(dir, "evil.txt"),
		"/abs.txt",
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("escaping entry written to %s", p)
		}
	}

	tarball := filepath.Join(dir, "a.tgz")
	writeTarGz(t, tarball, map[string]string{
		"ok/file.txt":     "fine",
		"../tar-evil.txt": "x",
	})

	tarDest := filepath.Join(dir, "tar-out")
	_ = os.Mkdir(tarDest, 0o700)

	if err := s.untarGz(tarball, tarDest); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(tarDest, "ok", "file.txt")); err != nil {
		t.Errorf("regular tar entry not extracted: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "tar-evil.txt")); err == nil {
		t.Error("escaping tar entry written outside the destination")
	}

	bz2Dest := filepath.Join(dir, "bz2-out")

	source, partial, err := s.prepare("testdata/package.tar.bz2", bz2Dest)
	if err != nil || source != "dir:"+bz2Dest || partial != "" {
		t.Errorf("prepare tar.bz2 = %q, %q, %v; want the extracted directory", source, partial, err)
	}

	if _, err := os.Stat(filepath.Join(bz2Dest, "package", "package.json")); err != nil {
		t.Errorf("tar.bz2 entry not extracted: %v", err)
	}

	small := NewService(nil, nil, nil, nil, Limits{
		MaxExtractBytes: 3,
		MaxExtractFiles: 10,
	}, nil)
	dest2 := filepath.Join(dir, "out2")

	_ = os.Mkdir(dest2, 0o700)
	if err := small.unzip(archive, dest2); !errors.Is(err, ErrLimit) {
		t.Errorf("size limit error = %v", err)
	}

	few := NewService(nil, nil, nil, nil, Limits{
		MaxExtractFiles: 0,
	}, nil)
	few.limits.MaxExtractFiles = 0
	dest3 := filepath.Join(dir, "out3")

	_ = os.Mkdir(dest3, 0o700)
	if err := few.unzip(archive, dest3); !errors.Is(err, ErrLimit) {
		t.Errorf("file limit error = %v", err)
	}
}
