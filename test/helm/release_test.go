package helm_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestUnpublishedChartNeedsAnImageTag(t *testing.T) {
	t.Parallel()

	_, stderr := render(t, "--set", "image.tag=")
	if !strings.Contains(stderr, "image.tag is required") {
		t.Errorf("rendering the chart from the repository without image.tag: %q, want it refused", stderr)
	}
}

func TestPublishedChartPullsItsReleaseImage(t *testing.T) {
	t.Parallel()

	render(t)

	dir := t.TempDir()

	out, err := exec.CommandContext(
		t.Context(),
		"helm",
		"package",
		chart,
		"--version",
		"1.2.3-beta.4",
		"--app-version",
		"1.2.3-beta.4",
		"--destination",
		dir,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("helm package: %v\n%s", err, out)
	}

	out, err = exec.CommandContext(
		t.Context(),
		"helm",
		"template",
		"r",
		filepath.Join(dir, "relic-1.2.3-beta.4.tgz"),
		"--set",
		"credentials.encryptionKey="+testEncryptionKey,
		"--set",
		"credentials.adminPassword="+testAdminPassword,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}

	if !strings.Contains(string(out), "image: ghcr.io/relicbuild/relic:v1.2.3-beta.4") {
		t.Error("the published chart does not pull its release's image")
	}
}

var imageReference = regexp.MustCompile(`relicbuild/relic:([^\s"'` + "`" + `]+)`)

func TestDocsPullTheReleasePlaceholder(t *testing.T) {
	t.Parallel()

	err := filepath.WalkDir("../../web/layers/site/content", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}

		raw, err := os.ReadFile(path) //nolint:gosec // the repository's own docs
		if err != nil {
			return err
		}

		for _, match := range imageReference.FindAllStringSubmatch(string(raw), -1) {
			if match[1] != "vX.Y.Z" {
				t.Errorf(
					"%s pulls relicbuild/relic:%s, want vX.Y.Z, which the docs show as the latest release",
					path,
					match[1],
				)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
