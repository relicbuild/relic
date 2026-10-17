// Package harness runs the e2e scenarios against a running Relic in parallel.
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Tiers lists the scenario tiers: core runs on the single binary, platform
// on the Compose stack.
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

// Serial names the scenarios that cannot share the server with others, with
// the reason.
var Serial = map[string]string{
	"telemetry-smoke": "its counters are server-wide, so other scenarios' requests would move them",
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

// Env is the environment a scenario runs with: base, with the variables that
// isolate it replaced.
func Env(base []string, name, home, jfHome, tmp string) []string {
	set := map[string]string{
		"HOME":               home,
		"JFROG_CLI_HOME_DIR": jfHome,
		"TMPDIR":             tmp,
		"SCENARIO_SUFFIX":    "-" + name,
		"SCENARIO_NAME":      name,
	}
	for _, kv := range base {
		if own, ok := strings.CutPrefix(kv, "E2E_OWN_DAEMON="); ok && slices.Contains(strings.Fields(own), name) {
			set["DOCKER_HOST"] = "tcp://relic-e2e-dind-" + name + ":2375"
			set["SCENARIO_DOCKER_HOSTNAME"] = "relic-e2e-dind-" + name
		}
	}

	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[k]; !replaced {
			out = append(out, kv)
		}
	}

	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range set {
			if !yield(k) {
				return
			}
		}
	}) {
		out = append(out, k+"="+set[k])
	}

	return out
}

// CopyDir copies the regular files under src to dst.
func CopyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == src {
				return nil
			}

			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}

		if !d.Type().IsRegular() {
			return nil
		}

		b, err := os.ReadFile(p) //nolint:gosec // the runner's own configuration
		if err != nil {
			return err
		}

		return os.WriteFile(target, b, 0o600) //nolint:gosec // a path under dst, from the walk of src
	})
}
