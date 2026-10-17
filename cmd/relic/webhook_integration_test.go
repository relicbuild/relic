//go:build integration

package main

import (
	"strings"
	"testing"
)

func TestWebhookCommands(t *testing.T) {
	t.Parallel()

	config, _ := adminCLI(t)

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Webhook
metadata: {name: chat}
spec:
  url: https://hooks.slack.example.com/services/T0/B0/x
  events: [release.promoted, download.blocked]
  format: slack
  enabled: true
  secret: hook-secret
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	out, err := relic(t, config, "", "get", "webhook", "chat", "-o", "yaml")
	if err != nil || !strings.Contains(out, "hasSecret: true") ||
		!strings.Contains(out, "format: WEBHOOK_FORMAT_SLACK") ||
		strings.Contains(out, "hook-secret") {
		t.Errorf("get = %q %v", out, err)
	}

	if out, err := relic(t, config, `apiVersion: relic.build/v1
kind: Webhook
metadata: {name: chat}
spec:
  url: https://hooks.slack.example.com/services/T0/B0/x
  events: [release.promoted, download.blocked]
  format: slack
  enabled: true
`, "diff", "-f", "-"); err != nil || strings.Contains(out, "+") {
		t.Errorf("diff of the applied webhook = %q %v, want none", out, err)
	}

	out, err = relic(t, config, "", "webhook", "ping", "chat")
	if err != nil || !strings.HasPrefix(out, "queued delivery ") {
		t.Fatalf("ping = %q %v", out, err)
	}

	out, err = relic(t, config, "", "webhook", "deliveries", "chat")
	if err != nil || !strings.Contains(out, "webhook.ping") || !strings.Contains(out, "pending") {
		t.Errorf("deliveries = %q %v", out, err)
	}
}
