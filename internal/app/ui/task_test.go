package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestUITaskRebuildsWhenAnyEmbeddedSourceChanges(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}

	var taskfile struct {
		Tasks map[string]struct {
			Dir     string   `yaml:"dir"`
			Sources []string `yaml:"sources"`
		} `yaml:"tasks"`
	}

	if err := yaml.Unmarshal(raw, &taskfile); err != nil {
		t.Fatal(err)
	}

	task, ok := taskfile.Tasks["ui"]
	if !ok {
		t.Fatal("Taskfile.yml has no ui task")
	}

	web := filepath.Join("../../..", task.Dir)

	roots := make([]string, 0, len(task.Sources))

	for _, source := range task.Sources {
		root, _, _ := strings.Cut(source, "/**")
		roots = append(roots, root)

		if _, err := os.Stat(filepath.Join(web, root)); err != nil {
			t.Errorf("source %s: %v", source, err)
		}
	}

	layers, err := os.ReadDir(filepath.Join(web, "layers"))
	if err != nil {
		t.Fatal(err)
	}

	for _, layer := range layers {
		if !slices.Contains(roots, "layers") && !slices.Contains(roots, "layers/"+layer.Name()) {
			t.Errorf("the ui task's sources leave out the Nuxt layer layers/%s", layer.Name())
		}
	}
}
