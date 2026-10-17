package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write([]byte(content))
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func tgzOf(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)

	tw := tar.NewWriter(gz)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		})
		_, _ = tw.Write([]byte(content))
	}

	_ = tw.WriteHeader(&tar.Header{
		Name:     "package/link",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeSymlink,
	})
	_ = tw.Close()
	_ = gz.Close()

	return buf.Bytes()
}

func TestKindOf(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"a.jar":    Zip,
		"A.WHL":    Zip,
		"x.tgz":    TarGz,
		"x.tar.gz": TarGz,
		"c.crate":  TarGz,
		"g.gem":    Tar,
		"r.txt":    "",
	} {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestZip(t *testing.T) {
	t.Parallel()
	b := zipOf(t, map[string]string{
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n",
		"./org/App.class":      "cafe",
	})

	entries, truncated, err := ListZip(bytes.NewReader(b), int64(len(b)))
	if err != nil || truncated || len(entries) != 2 {
		t.Fatalf("ListZip = %v %v %v", entries, truncated, err)
	}

	got, err := ReadZip(bytes.NewReader(b), int64(len(b)), "org/App.class")
	if err != nil || string(got) != "cafe" {
		t.Errorf("ReadZip = %q %v", got, err)
	}

	if _, err := ReadZip(bytes.NewReader(b), int64(len(b)), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing entry = %v", err)
	}

	big := zipOf(t, map[string]string{
		"big.txt": strings.Repeat("x", MaxEntryBytes+1),
	})
	if _, err := ReadZip(bytes.NewReader(big), int64(len(big)), "big.txt"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized entry = %v", err)
	}

	exact := zipOf(t, map[string]string{
		"exact.txt": strings.Repeat("x", MaxEntryBytes),
	})

	got, err = ReadZip(bytes.NewReader(exact), int64(len(exact)), "exact.txt")
	if err != nil || len(got) != MaxEntryBytes {
		t.Errorf("entry at the limit: len=%d err=%v", len(got), err)
	}
}

func TestTar(t *testing.T) {
	t.Parallel()
	b := tgzOf(t, map[string]string{
		"package/package.json": `{"name":"x"}`,
		"./package/x.json":     `{"x":1}`,
	})

	entries, _, err := ListTar(bytes.NewReader(b), true)
	if err != nil || len(entries) != 2 {
		t.Fatalf("ListTar = %+v %v (links must be skipped)", entries, err)
	}

	paths := map[string]bool{}
	for _, entry := range entries {
		paths[entry.Path] = true
	}

	if !paths["package/package.json"] || !paths["package/x.json"] {
		t.Errorf("ListTar paths = %v", paths)
	}

	got, err := ReadTar(bytes.NewReader(b), true, "package/package.json")
	if err != nil || string(got) != `{"name":"x"}` {
		t.Errorf("ReadTar = %q %v", got, err)
	}

	got, err = ReadTar(bytes.NewReader(b), true, "package/x.json")
	if err != nil || string(got) != `{"x":1}` {
		t.Errorf("ReadTar of a ./ entry = %q %v", got, err)
	}

	if _, err := ReadTar(bytes.NewReader(b), true, "package/link"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a symlink entry is not readable: %v", err)
	}
}

func TestListTarPastTheStreamCapIsTruncated(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()

	go func() {
		tw := tar.NewWriter(pw)
		for _, f := range []struct {
			name string
			size int64
		}{
			{
				name: "small.txt",
				size: 5,
			},
			{
				name: "large.bin",
				size: maxStreamBytes,
			},
		} {
			if err := tw.WriteHeader(&tar.Header{
				Name:     f.name,
				Mode:     0o644,
				Size:     f.size,
				Typeflag: tar.TypeReg,
			}); err != nil {
				pw.CloseWithError(err)

				return
			}

			if _, err := io.CopyN(tw, zeros{}, f.size); err != nil {
				pw.CloseWithError(err)

				return
			}
		}

		pw.CloseWithError(tw.Close())
	}()

	entries, truncated, err := ListTar(pr, false)
	_ = pr.Close()

	if err != nil || !truncated || len(entries) != 2 {
		t.Errorf(
			"ListTar of a tar past the cap = %d entries, truncated %v, %v; want both entries, truncated",
			len(entries),
			truncated,
			err,
		)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)

	return len(p), nil
}
