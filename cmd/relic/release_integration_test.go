//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/internal/delivery/evidence"
)

func TestReleaseCommands(t *testing.T) {
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
kind: Repository
metadata: {name: rel-dev}
spec: {class: local, format: generic}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: rel-prod}
spec: {class: local, format: generic, environments: [PROD]}
`, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}

	put(t, config, dir, "rel-dev/app/1.0/app.jar", "jar")
	put(t, config, dir, "rel-dev/app/1.0/guide.pdf", "pdf")

	if _, err := relic(t, config, "", "release", "create", "app", "1.0"); err == nil {
		t.Error("create without artifacts succeeded")
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"create",
		"app",
		"1.0",
		"--file",
		"rel-dev/app/1.0/app.jar",
		"--draft",
	); err != nil ||
		out != "app/1.0 draft, 1 artifacts\n" {
		t.Fatalf("create = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"- files:\n    - path: rel-dev/app/1.0/guide.pdf\n",
		"release",
		"update",
		"app",
		"1.0",
		"-f",
		"-",
	); err != nil ||
		!strings.Contains(out, "2 artifacts") {
		t.Fatalf("update = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"finalize",
		"app",
		"1.0",
	); err != nil ||
		!strings.Contains(out, "app/1.0 completed") {
		t.Fatalf("finalize = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"tag",
		"app",
		"1.0",
		"stable",
	); err != nil ||
		out != "app/1.0 tagged stable\n" {
		t.Errorf("tag = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"list",
	); err != nil ||
		!hasTableRow(out, "app", "default", "1.0", "1") {
		t.Errorf("list = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "release", "list", "--project", ""); err != nil ||
		!hasTableRow(out, "app", "default", "1.0", "1") {
		t.Errorf("list --project '' = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "release", "list", "--project", "acme"); err != nil ||
		hasTableRowWith(out, "app") {
		t.Errorf("list --project acme = %q %v; want no release outside acme", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"list",
		"app",
	); err != nil ||
		!hasTableRowWith(out, "1.0", "completed", "stable") {
		t.Errorf("list app = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "release", "get", "app", "1.0"); err != nil ||
		!hasTableRowWith(out, "rel-dev/app/1.0/guide.pdf") {
		t.Errorf("get = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"promote",
		"app",
		"1.0",
		"PROD",
	); err != nil ||
		out != "app/1.0 promoted to PROD\n" {
		t.Fatalf("promote = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"promote",
		"app",
		"1.0",
		"PROD",
		"--move",
	); err != nil ||
		out != "app/1.0 promoted to PROD\n  promotion type move is performed as copy\n" {
		t.Errorf("promote --move = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"api",
		"releases/app/versions/1.0/promotions",
	); err != nil ||
		!strings.Contains(out, "PROD") {
		t.Errorf("promotions = %q %v", out, err)
	}

	keyJSON, err := relic(t, config, "", "api", "key-pairs/release-bundles/public-key")
	if err != nil {
		t.Fatal(err)
	}

	var key struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(keyJSON), &key); err != nil {
		t.Fatal(err)
	}

	publicKey, _, err := evidence.ParsePublicKey(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	fingerprint, err := evidence.Fingerprint(publicKey)
	if err != nil {
		t.Fatal(err)
	}

	keyLine := "signing key fingerprint " + fingerprint + "\n"

	if out, err := relic(t, config, "", "release", "public-key"); err != nil || out != key.PublicKey {
		t.Errorf("release public-key = %q %v; want the release-bundles public key", out, err)
	}

	keyFile := filepath.Join(dir, "release-key.pem")
	if err := os.WriteFile(keyFile, []byte(key.PublicKey), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := relic(t, config, "", "release", "trust-key", "self", keyFile); err != nil ||
		out != "trusted as self (fingerprint "+fingerprint+")\n" {
		t.Errorf("release trust-key = %q %v", out, err)
	}

	archive := filepath.Join(dir, "app-1.0.zip")
	if out, err := relic(t, config, "", "release", "export", "app", "1.0", "--file", archive); err != nil ||
		out != "app/1.0 exported to "+archive+"\n"+keyLine {
		t.Fatalf("export = %q %v; want the key line %q", out, err, keyLine)
	}

	if st, err := os.Stat(archive); err != nil || st.Size() == 0 {
		t.Fatalf("archive: %v", err)
	}

	if _, err := relic(t, config, "", "release", "delete", "app", "1.0"); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "release", "get", "app", "1.0"); err == nil {
		t.Error("the deleted version is still there")
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"import",
		archive,
	); err != nil ||
		out != "app/1.0 imported\n"+keyLine {
		t.Fatalf("import = %q %v", out, err)
	}

	if out, err := relic(
		t,
		config,
		"",
		"release",
		"get",
		"app",
		"1.0",
	); err != nil ||
		!strings.Contains(out, "2 artifacts") ||
		!hasTableRowWith(out, "rel-dev/app/1.0/app.jar") ||
		!hasTableRowWith(out, "rel-dev/app/1.0/guide.pdf") {
		t.Errorf("after import = %q %v", out, err)
	}
}
