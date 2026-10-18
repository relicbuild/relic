// Package compose_test checks the settings the Compose stacks give Relic.
package compose_test

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

type composeFile struct {
	Services map[string]struct {
		Environment map[string]string `yaml:"environment"`
	} `yaml:"services"`
}

func TestComposeGivesTheWorkerTheServersSettings(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../deploy/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}

	var stack composeFile
	if err := yaml.Unmarshal(raw, &stack); err != nil {
		t.Fatal(err)
	}

	server := stack.Services["relic-server"].Environment
	worker := stack.Services["relic-worker"].Environment

	for _, name := range []string{
		"RELIC_DATABASE_URL",
		"RELIC_ENCRYPTION_KEY",
		"RELIC_OBJECT_STORAGE_ENDPOINT",
		"RELIC_OBJECT_STORAGE_ACCESS_KEY",
		"RELIC_OBJECT_STORAGE_SECRET_KEY",
		"RELIC_TRASH_ENABLED",
		"RELIC_UPSTREAM_ALLOW_PRIVATE_ADDRESSES",
		"RELIC_WEBHOOK_ALLOW_PRIVATE_ADDRESSES",
	} {
		if server[name] != worker[name] {
			t.Errorf("%s: server %q, worker %q, want the same value", name, server[name], worker[name])
		}
	}
}
