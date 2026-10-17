package helm_test

import (
	"os"
	"os/exec"
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

	if semver.Compare("v"+meta.AppVersion, latest) < 0 {
		t.Errorf(
			"appVersion %q is older than the release %s, so the default image is not the release's",
			meta.AppVersion,
			latest,
		)
	}
}
