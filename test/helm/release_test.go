package helm_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

func TestChartImageIsNotOlderThanTheLatestRelease(t *testing.T) {
	t.Parallel()

	out, err := exec.CommandContext(t.Context(), "git", "describe", "--tags", "--abbrev=0", "--match", "v*").Output()
	if err != nil {
		t.Skip("no release tag in this checkout")
	}

	latest := strings.TrimSpace(string(out))

	appVersion := chartAppVersion(t)

	if semver.Compare("v"+appVersion, latest) < 0 {
		t.Errorf(
			"appVersion %q is older than the release %s, so the default image is not the release's",
			appVersion,
			latest,
		)
	}
}

var imageReference = regexp.MustCompile(`relicbuild/relic:([^\s"'` + "`" + `]+)`)

func TestImageReferencesNameTheChartRelease(t *testing.T) {
	t.Parallel()

	want := "v" + chartAppVersion(t)

	files := []string{
		"../../deploy/cloudflare/Dockerfile",
	}

	err := filepath.WalkDir("../../web/layers/site/content", func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && filepath.Ext(path) == ".md" {
			files = append(files, path)
		}

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		for _, match := range imageReference.FindAllStringSubmatch(string(raw), -1) {
			if match[1] != want {
				t.Errorf("%s pulls relicbuild/relic:%s, want %s, the chart's release", file, match[1], want)
			}
		}
	}
}

func chartAppVersion(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(chart + "/Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var meta struct {
		AppVersion string `yaml:"appVersion"`
	}

	if err := yaml.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}

	return meta.AppVersion
}
