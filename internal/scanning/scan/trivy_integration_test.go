//go:build integration

package scan_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"relic/internal/platform/scripttest"
	"relic/internal/scanning/scan"
)

// trivyImage is the Trivy image the Dockerfile bundles.
func trivyImage(t *testing.T) string {
	t.Helper()

	b, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	m := regexp.MustCompile(`aquasec/trivy:\S+`).Find(b)
	if m == nil {
		t.Fatal("no aquasec/trivy image in the Dockerfile")
	}

	return string(m)
}

func writeBlob(t *testing.T, dir string, b []byte) (string, int) {
	t.Helper()

	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])

	if err := os.WriteFile(filepath.Join(dir, "blobs", "sha256", digest), b, 0o600); err != nil {
		t.Fatal(err)
	}

	return "sha256:" + digest, len(b)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// ociImage writes an OCI layout of one layer, built as root without a
// health check.
func ociImage(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o750); err != nil {
		t.Fatal(err)
	}

	var layer bytes.Buffer

	tw := tar.NewWriter(&layer)
	if err := tw.WriteHeader(&tar.Header{
		Name: "hello.txt",
		Mode: 0o644,
		Size: 6,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tw.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	layerDigest, layerSize := writeBlob(t, dir, layer.Bytes())
	configDigest, configSize := writeBlob(t, dir, mustJSON(t, map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"config": map[string]any{
			"Env": []string{
				"PATH=/bin",
			},
		},
		"rootfs": map[string]any{
			"type": "layers",
			"diff_ids": []string{
				layerDigest,
			},
		},
		"history": []map[string]any{
			{
				"created_by": "COPY hello.txt / # buildkit",
			},
		},
	}))
	manifestDigest, manifestSize := writeBlob(t, dir, mustJSON(t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      configSize,
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.oci.image.layer.v1.tar",
				"digest":    layerDigest,
				"size":      layerSize,
			},
		},
	}))

	files := map[string][]byte{
		"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`),
		"index.json": mustJSON(t, map[string]any{
			"schemaVersion": 2,
			"manifests": []map[string]any{
				{
					"mediaType": "application/vnd.oci.image.manifest.v1+json",
					"digest":    manifestDigest,
					"size":      manifestSize,
				},
			},
		}),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestTrivyAnalyzesAnImageConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("needs docker")
	}

	img := ociImage(t)

	wrapper := filepath.Join(t.TempDir(), "trivy")
	script := "#!/bin/sh\nexec docker run --rm -v " + shellQuoted(
		img+":"+img+":ro",
	) + " " + trivyImage(
		t,
	) + ` "$@"` + "\n"

	scripttest.Write(t, wrapper, script)

	issues, err := scan.Trivy{
		Exec: scan.Exec{
			Path: wrapper,
		},
	}.Analyze(t.Context(), "oci-dir:"+img)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	if len(issues) == 0 {
		t.Fatal("no issues for an image that runs as root without a health check")
	}
}
