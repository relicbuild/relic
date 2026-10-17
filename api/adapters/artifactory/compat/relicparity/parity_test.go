// Package relicparity keeps the relic CLI level with the client commands the
// adapter supports.
package relicparity

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var matrixRow = regexp.MustCompile("^\\| `jf ([^`]+)`[^|]*\\|[^|]*\\|[^|]*\\|[^|]*\\| *(✅)? *\\|")

type jfEquivalent struct {
	Relic   string `yaml:"relic"`
	Planned string `yaml:"planned"`
	None    string `yaml:"none"`
}

func TestJfCommandsHaveRelicCommands(t *testing.T) {
	t.Parallel()

	matrix, err := os.ReadFile("../docs/MATRIX.md")
	if err != nil {
		t.Fatal(err)
	}

	var (
		covered []string
		known   = map[string]bool{}
	)

	for line := range strings.Lines(string(matrix)) {
		m := matrixRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		name := m[1]
		known[name] = true

		if m[2] != "" {
			covered = append(covered, name)
		}
	}

	if len(covered) < 50 {
		t.Fatalf("read %d jf commands with e2e from MATRIX.md; the table's layout changed", len(covered))
	}

	raw, err := os.ReadFile("commands.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var file struct {
		Commands map[string]jfEquivalent `yaml:"commands"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}

	for _, name := range covered {
		if _, ok := file.Commands[name]; !ok {
			t.Errorf(
				"jf %s has an e2e scenario but no entry in commands.yaml: give relic a command that does it",
				name,
			)
		}
	}

	relic := buildRelic(t)

	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range file.Commands {
			if !yield(k) {
				return
			}
		}
	}) {
		e := file.Commands[name]

		switch set := boolCount(e.Relic != "", e.Planned != "", e.None != ""); {
		case !known[name]:
			t.Errorf("commands.yaml: %q is not a jf command in MATRIX.md", name)
		case set != 1:
			t.Errorf("commands.yaml: jf %s needs exactly one of relic, planned and none", name)
		case e.Relic != "" && !hasCommand(t, relic, e.Relic):
			t.Errorf("commands.yaml: jf %s names relic %s, which does not exist", name, e.Relic)
		}
	}
}

func buildRelic(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "relic")

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "relic/cmd/relic")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build relic: %v\n%s", err, out)
	}

	return bin
}

func hasCommand(t *testing.T, relic, path string) bool {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), relic, append(strings.Fields(path), "--help")...)

	out, err := cmd.Output()
	if err != nil {
		return false
	}

	_, usage, ok := strings.Cut(string(out), "Usage:\n  ")
	if !ok {
		return false
	}

	line, _, _ := strings.Cut(usage, "\n")
	want := "relic " + path

	return line == want || strings.HasPrefix(line, want+" ")
}

func boolCount(bs ...bool) int {
	n := 0

	for _, b := range bs {
		if b {
			n++
		}
	}

	return n
}
