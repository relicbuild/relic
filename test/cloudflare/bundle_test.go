// Package cloudflare_test checks the Cloudflare deployment a release publishes.
package cloudflare_test

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	repository = "../.."
	tag        = "v1.2.3-beta.4"
	bundle     = "relic-cloudflare_" + tag
)

func bundleFiles(t *testing.T) map[string]string {
	t.Helper()

	dir := t.TempDir()

	command := exec.CommandContext(t.Context(), "sh", "deploy/cloudflare/bundle.sh", tag, dir)
	command.Dir = repository

	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bundle.sh: %v\n%s", err, out)
	}

	archive, err := os.Open(filepath.Join(dir, bundle+".tar.gz")) //nolint:gosec // the test's own output
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = archive.Close()
	})

	gz, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{}
	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}

		files[header.Name] = string(body)
	}

	return files
}

func TestBundleRunsItsRelease(t *testing.T) {
	t.Parallel()

	files := bundleFiles(t)

	wrangler, ok := files[bundle+"/wrangler.jsonc"]
	if !ok {
		t.Fatalf("the bundle has no wrangler.jsonc: %v", slices.Sorted(maps.Keys(files)))
	}

	if !strings.Contains(wrangler, `"RELIC_VERSION": "`+tag+`"`) {
		t.Errorf("wrangler.jsonc does not run %s:\n%s", tag, wrangler)
	}

	if !strings.Contains(files[bundle+"/Dockerfile"], "FROM ghcr.io/relicbuild/relic:${RELIC_VERSION}\n") {
		t.Errorf("the Dockerfile does not build from the image RELIC_VERSION names:\n%s", files[bundle+"/Dockerfile"])
	}

	for _, want := range []string{
		"package.json",
		"pnpm-lock.yaml",
		"src/index.ts",
	} {
		if _, ok := files[bundle+"/"+want]; !ok {
			t.Errorf("the bundle lacks %s", want)
		}
	}

	for name := range files {
		if strings.Contains(name, "node_modules/") || strings.HasSuffix(name, "/bundle.sh") ||
			strings.HasSuffix(name, ".dev.vars") {
			t.Errorf("the bundle holds %s", name)
		}
	}
}

func TestReleasePublishesTheBundle(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join(repository, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var config struct {
		Before struct {
			Hooks []string `yaml:"hooks"`
		} `yaml:"before"`
		Checksum struct {
			ExtraFiles []struct {
				Glob string `yaml:"glob"`
			} `yaml:"extra_files"`
		} `yaml:"checksum"`
		Release struct {
			ExtraFiles []struct {
				Glob string `yaml:"glob"`
			} `yaml:"extra_files"`
		} `yaml:"release"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(config.Before.Hooks, "sh deploy/cloudflare/bundle.sh {{ .Tag }} bin") {
		t.Errorf("no release hook builds the bundle: %q", config.Before.Hooks)
	}

	const glob = "./bin/relic-cloudflare_*.tar.gz"

	if len(config.Release.ExtraFiles) != 1 || config.Release.ExtraFiles[0].Glob != glob {
		t.Errorf("release.extra_files = %+v, want %s", config.Release.ExtraFiles, glob)
	}

	if len(config.Checksum.ExtraFiles) != 1 || config.Checksum.ExtraFiles[0].Glob != glob {
		t.Errorf("checksum.extra_files = %+v, want %s", config.Checksum.ExtraFiles, glob)
	}
}

func TestReleaseTagsTheImageWithItsTag(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"ghcr.io/${{ github.repository }}\n",
		"type=semver,pattern={{raw}}\n",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("release.yml lacks %q, so ghcr.io/relicbuild/relic:<tag> may not exist", want)
		}
	}
}
