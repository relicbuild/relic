package scan

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const maxImageFile = 512 << 20

// imageFiles copies the files at paths (absolute in the image) out of the
// layers of the OCI layout dir into out, applying the layers in order and
// their whiteouts, and returns the copies that exist in the final image by
// their path.
func imageFiles(dir string, paths []string, out string) (map[string]string, error) {
	layers, err := imageLayers(dir)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}

	wanted := map[string]string{}
	for i, p := range paths {
		wanted[path.Clean("/"+p)] = filepath.Join(out, strconv.Itoa(i))
	}

	present := map[string]bool{}

	for _, layer := range layers {
		if err := applyLayer(filepath.Join(dir, "blobs", "sha256", layer), wanted, present); err != nil {
			return nil, err
		}
	}

	files := map[string]string{}

	for _, p := range paths {
		if present[path.Clean("/"+p)] {
			files[p] = wanted[path.Clean("/"+p)]
		}
	}

	return files, nil
}

func imageLayers(dir string) ([]string, error) {
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}

	if err := readJSON(filepath.Join(dir, "index.json"), &index); err != nil {
		return nil, fmt.Errorf("not an OCI layout: %w", err)
	}

	if len(index.Manifests) == 0 {
		return nil, errors.New("not an OCI layout")
	}

	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}

	h, _ := strings.CutPrefix(index.Manifests[0].Digest, "sha256:")
	if !isHex64(h) {
		return nil, errors.New("not an OCI layout")
	}

	if err := readJSON(filepath.Join(dir, "blobs", "sha256", h), &manifest); err != nil {
		return nil, fmt.Errorf("read the image manifest: %w", err)
	}

	out := make([]string, 0, len(manifest.Layers))

	for _, l := range manifest.Layers {
		h, _ := strings.CutPrefix(l.Digest, "sha256:")
		if !isHex64(h) {
			return nil, fmt.Errorf("layer %q", l.Digest)
		}

		out = append(out, h)
	}

	return out, nil
}

func readJSON(file string, into any) error {
	b, err := os.ReadFile(file) //nolint:gosec // a file of the scan's own layout
	if err != nil {
		return err
	}

	return json.Unmarshal(b, into)
}

// applyLayer writes the wanted files a layer holds, and forgets those it
// deletes (whiteouts), marking what is present after it.
func applyLayer(file string, wanted map[string]string, present map[string]bool) error {
	f, err := os.Open(file) //nolint:gosec // a blob of the scan's own layout
	if err != nil {
		return err
	}

	defer func() {
		_ = f.Close()
	}()

	br := bufio.NewReader(f)

	var r io.Reader = br

	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}

		defer func() {
			_ = gz.Close()
		}()

		r = gz
	}

	tr := tar.NewReader(r)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("read layer: %w", err)
		}

		name := path.Clean("/" + h.Name)
		base := path.Base(name)

		if deleted, ok := strings.CutPrefix(base, ".wh."); ok {
			gone := path.Join(path.Dir(name), deleted)
			if deleted == ".wh..opq" {
				gone = path.Dir(name)
			}

			for p := range present {
				if p == gone || strings.HasPrefix(p, strings.TrimSuffix(gone, "/")+"/") {
					delete(present, p)
				}
			}

			continue
		}

		target, ok := wanted[name]
		if !ok || h.Typeflag != tar.TypeReg || h.Size > maxImageFile {
			continue
		}

		if err := writeFile(target, io.LimitReader(tr, maxImageFile)); err != nil {
			return err
		}

		present[name] = true
	}
}

func writeFile(target string, r io.Reader) error {
	//nolint:gosec // a path under the scan's directory
	out, err := os.OpenFile(
		target,
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()

		return err
	}

	return out.Close()
}
