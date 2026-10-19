// Package e2e runs Relic's end-to-end suites against the shipped Compose
// stacks or a kind cluster: the jf and package-manager scenarios, the
// jfrog-client-go suite, the UI tests and the Kubernetes checks.
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Tiers lists the scenario tiers: core runs on the single binary, platform
// on the Compose stack with PostgreSQL, SeaweedFS and a separate worker.
var Tiers = []string{
	"core",
	"platform",
}

// Tier returns the tier the scenario script at path declares on its
// "# tier:" line, or core when it declares none.
func Tier(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // a scenario script of this repository
	if err != nil {
		return "", err
	}

	for line := range strings.Lines(string(b)) {
		rest, ok := strings.CutPrefix(line, "# tier:")
		if !ok {
			continue
		}

		fields := strings.Fields(rest)
		if len(fields) == 0 || !slices.Contains(Tiers, fields[0]) {
			return "", fmt.Errorf("%s: tier line %q names none of %v", path, strings.TrimSpace(line), Tiers)
		}

		return fields[0], nil
	}

	return "core", nil
}

// Paths of the compatibility suite, relative to the repository.
const (
	scenarioDir  = "api/adapters/artifactory/compat/fixtures/scenarios"
	requiredList = "api/adapters/artifactory/compat/e2e/required.txt"
)

// sdkScenario is the jfrog-client-go suite's name in the required list; it
// runs as a Go test, not as a script.
const sdkScenario = "sdk-artifactory-rest"

// Serial names the scenarios that cannot share the server with others, with
// the reason.
var Serial = map[string]string{
	"telemetry-smoke": "its counters are server-wide, so other scenarios' requests would move them",
}

// OwnDaemon names the scenarios that get a Docker daemon of their own, with
// the reason.
var OwnDaemon = map[string]string{
	"docker":        "it pushes and removes images the other scenarios' daemon must not see",
	"native-docker": "it pushes and removes images the other scenarios' daemon must not see",
}

// Names lists the scenario scripts in dir (without lib.sh), sorted, or only
// those in only when it is not empty.
func Names(dir string, only []string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.sh"))
	if err != nil {
		return nil, err
	}

	var all []string

	for _, p := range paths {
		if n := strings.TrimSuffix(filepath.Base(p), ".sh"); n != "lib" {
			all = append(all, n)
		}
	}

	slices.Sort(all)

	if len(only) == 0 {
		return all, nil
	}

	var picked []string

	for _, n := range only {
		if !slices.Contains(all, n) {
			return nil, fmt.Errorf("no scenario %q in %s", n, dir)
		}

		picked = append(picked, n)
	}

	return picked, nil
}

// Required lists the scenarios in the file at path, one per line, skipping
// blank lines and # comments.
func Required(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the suite's own list
	if err != nil {
		return nil, err
	}

	out := map[string]bool{}

	for line := range strings.Lines(string(b)) {
		name := strings.TrimSpace(line)
		if name != "" && !strings.HasPrefix(name, "#") {
			out[name] = true
		}
	}

	return out, nil
}
