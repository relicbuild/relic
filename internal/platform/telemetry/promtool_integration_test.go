//go:build integration

package telemetry

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// The same version as kube-prometheus-stack's Prometheus and the Compose profile's.
const prometheusImage = "prom/prometheus:v3.15.0"

func TestPromtool(t *testing.T) {
	t.Parallel()

	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is not installed")
	}

	dir, err := filepath.Abs(filepath.Join(observability, "prometheus"))
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{
			"check",
			"rules",
			"relic-rules.yaml",
		},
		{
			"test",
			"rules",
			"relic-rules.test.yaml",
		},
	} {
		cmd := exec.CommandContext(t.Context(), docker, append([]string{ //nolint:gosec // fixed arguments
			"run",
			"--rm",
			"-v",
			dir + ":/rules:ro",
			"-w",
			"/rules",
			"--entrypoint",
			"promtool",
			prometheusImage,
		}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("promtool %v: %v\n%s", args, err, out)
		}
	}
}
