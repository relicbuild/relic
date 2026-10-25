package helm_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

const minimumHelmVersion = "v3.17.0"

const kubernetesDocs = "../../web/layers/site/content/docs/2.installation/4.kubernetes/"

type chartDependency struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
}

func dependency(t *testing.T, name string) chartDependency {
	t.Helper()

	raw, err := os.ReadFile(chart + "/Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var metadata struct {
		Dependencies []chartDependency `yaml:"dependencies"`
	}

	if err := yaml.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}

	for _, dep := range metadata.Dependencies {
		if dep.Name == name {
			return dep
		}
	}

	t.Fatalf("Chart.yaml has no dependency %s", name)

	return chartDependency{}
}

func docsPage(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(kubernetesDocs + name) //nolint:gosec // the repository's own docs
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

func TestMonitoringRefusalPrintsTheCommandsOfTheMonitoringPage(t *testing.T) {
	t.Parallel()

	_, stderr := render(t, "--set", "monitoringStack.enabled=true")

	var commands []string

	for line := range strings.Lines(stderr) {
		command := strings.TrimSpace(line)
		if strings.HasPrefix(line, "  ") &&
			(strings.HasPrefix(command, "helm ") || strings.HasPrefix(command, "kubectl ")) {
			commands = append(commands, command)
		}
	}

	if len(commands) != 2 {
		t.Fatalf("commands = %q, want the CRD download and apply in:\n%s", commands, stderr)
	}

	stack := dependency(t, "kube-prometheus-stack")

	want := "helm pull kube-prometheus-stack --repo " + stack.Repository + " --version " + stack.Version + " "
	if !strings.HasPrefix(commands[0], want) {
		t.Errorf("download = %q, want it to start with %q", commands[0], want)
	}

	page := docsPage(t, "9.monitoring.md")
	for _, command := range commands {
		if !strings.Contains(page, "```bash\n"+command+"\n```") {
			t.Errorf("9.monitoring.md does not give %q", command)
		}
	}
}

func TestHelmIsTheChartsMinimumOrLater(t *testing.T) {
	t.Parallel()

	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}

	out, err := exec.CommandContext(t.Context(), helm, "version", "--template", "{{.Version}}").Output()
	if err != nil {
		t.Fatalf("helm version: %v", err)
	}

	installed := strings.TrimSpace(string(out))
	if !semver.IsValid(installed) || semver.Compare(installed, minimumHelmVersion) < 0 {
		t.Fatalf("helm %s is older than %s, the oldest Helm that renders the chart", installed, minimumHelmVersion)
	}
}

func TestKubernetesDocsNameTheMinimumHelm(t *testing.T) {
	t.Parallel()

	want := "The chart needs Helm " + strings.TrimPrefix(minimumHelmVersion, "v") + " or later."

	for _, name := range []string{
		"1.overview.md",
		"2.quick-start.md",
	} {
		if !strings.Contains(strings.Join(strings.Fields(docsPage(t, name)), " "), want) {
			t.Errorf("%s does not say %q", name, want)
		}
	}
}
