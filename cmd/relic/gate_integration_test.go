//go:build integration

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromotionGates(t *testing.T) {
	t.Parallel()

	srv := testServer(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")

	if _, err := relic(
		t,
		config,
		"password\n",
		"auth",
		"login",
		srv.URL,
		"--user",
		"admin",
		"--password-stdin",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: PromotionGate
metadata: {name: bad}
spec: {environment: PROD, condition: "release.name + 1"}
`, "apply", "-f", "-"); err == nil || !strings.Contains(err.Error(), "condition") {
		t.Errorf("a condition that does not compile: %v", err)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: gate-dev}
spec: {class: local, format: generic}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: gate-prod}
spec: {class: local, format: generic, environments: [PROD]}
---
apiVersion: relic.build/v1
kind: PromotionGate
metadata: {name: prod-approved}
spec:
  environment: prod
  condition: release.tag == "approved" && signed && scan.critical == 0
  description: tag the version approved first
---
apiVersion: relic.build/v1
kind: PromotionGate
metadata: {name: staging-only}
spec: {environment: STAGING, condition: "false"}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	put(t, config, dir, "gate-dev/app/1.0/app.jar", "jar")

	if _, err := relic(
		t,
		config,
		"",
		"release",
		"create",
		"app",
		"1.0",
		"--file",
		"gate-dev/app/1.0/app.jar",
	); err != nil {
		t.Fatal(err)
	}

	_, err := relic(t, config, "", "release", "promote", "app", "1.0", "PROD")
	if err == nil || !strings.Contains(err.Error(), "refused") ||
		!strings.Contains(err.Error(), "prod-approved (tag the version approved first)") {
		t.Fatalf("an unapproved promotion: %v", err)
	}

	if out, err := relic(t, config, "", "api", "releases/app/versions/1.0/promotions"); err != nil ||
		strings.Contains(out, "PROD") {
		t.Errorf("a refused promotion was recorded: %q %v", out, err)
	}

	if out, err := relic(t, config, "", "security", "decisions", "--kind", "release"); err != nil ||
		!strings.Contains(out, "app/1.0") || !strings.Contains(out, "prod-approved") {
		t.Errorf("the refusal's decision record = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "release", "tag", "app", "1.0", "approved"); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "release", "promote", "app", "1.0", "PROD"); err != nil ||
		out != "app/1.0 promoted to PROD\n" {
		t.Fatalf("an approved promotion = %q %v", out, err)
	}

	out, err := relic(t, config, "", "release", "get", "app", "1.0", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}

	var stored struct {
		Repository string `json:"repository"`
		Manifest   string `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(out), &stored); err != nil || stored.Manifest == "" {
		t.Fatalf("release get = %q %v", out, err)
	}

	out, err = relic(t, config, "", "api", "repositories/"+stored.Repository+"/evidence/"+stored.Manifest)
	if err != nil || strings.Count(out, "https://relic.build/promotion-gate/v1") != 2 {
		t.Fatalf("gate decisions recorded as evidence = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "get", "promotiongates"); err != nil ||
		!strings.Contains(out, "prod-approved") || !strings.Contains(out, "staging-only") {
		t.Errorf("get promotiongates = %q %v", out, err)
	}
}

func TestPolicyPreview(t *testing.T) {
	t.Parallel()

	config, _ := adminCLI(t)

	out, err := relic(t, config, `apiVersion: relic.build/v1
kind: Policy
metadata: {name: draft-critical}
spec:
  kind: security
  rules:
    - {name: critical, minSeverity: critical, failBuild: true}
`, "security", "policy", "preview", "-f", "-")
	if err != nil || !strings.HasSuffix(out, "0 violations\n") {
		t.Errorf("preview = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "get", "policy", "draft-critical"); err == nil {
		t.Errorf("the previewed policy was stored: %q", out)
	}

	if _, err := relic(t, config, `apiVersion: relic.build/v1
kind: Repository
metadata: {name: not-a-policy}
spec: {class: local, format: generic}
`, "security", "policy", "preview", "-f", "-"); err == nil || !strings.Contains(err.Error(), "no Policy") {
		t.Errorf("a file without policies: %v", err)
	}
}
