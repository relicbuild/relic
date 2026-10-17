//go:build integration

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvidenceCommands(t *testing.T) {
	t.Parallel()

	config, dir := adminCLI(t, "ev-local")
	put(t, config, dir, "ev-local/app/1.0/app.jar", "jar")

	keys := filepath.Join(dir, "ci")
	other := filepath.Join(dir, "other")

	out, err := relic(t, config, "", "evidence", "keygen", "--file", keys, "--alias", "ci")
	if err != nil || !strings.Contains(out, "trusted as ci (fingerprint ") {
		t.Fatalf("keygen = %q %v", out, err)
	}

	if st, err := os.Stat(keys + ".key"); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("private key: %v %v", st, err)
	}

	if _, err := relic(t, config, "", "evidence", "keygen", "--file", keys); err == nil {
		t.Error("keygen replaced an existing key without --force")
	}

	lone := filepath.Join(dir, "lone")
	if err := os.WriteFile(lone+".pub", []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := relic(t, config, "", "evidence", "keygen", "--file", lone); err == nil {
		t.Error("keygen replaced an existing public key without --force")
	}

	if _, err := os.Stat(lone + ".key"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed keygen left a private key: %v", err)
	}

	if _, err := relic(t, config, "", "evidence", "keygen", "--file", other, "--type", "ecdsa"); err != nil {
		t.Fatal(err)
	}

	predicate := filepath.Join(dir, "tests.json")
	if err := os.WriteFile(predicate, []byte(`{"passed": 42, "failed": 0}`), 0o600); err != nil {
		t.Fatal(err)
	}

	create := func(key, alias string) (string, error) {
		return relic(
			t,
			config,
			"",
			"evidence",
			"create",
			"ev-local/app/1.0/app.jar",
			"--predicate",
			predicate,
			"--predicate-type",
			"https://relic.build/evidence/tests/v1",
			"--key",
			key,
			"--alias",
			alias,
		)
	}

	if out, err := create(keys+".key", "ci"); err != nil || !strings.Contains(out, "stored, verified true") {
		t.Fatalf("create = %q %v", out, err)
	}

	if _, err := create(other+".key", "nobody"); err == nil {
		t.Error("evidence signed by an untrusted alias was stored")
	}

	if out, err := relic(t, config, "", "evidence", "get", "ev-local/app/1.0/app.jar"); err != nil ||
		!hasTableRowWith(out, "https://relic.build/evidence/tests/v1", "ci", "yes") {
		t.Errorf("get = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "evidence", "get", "ev-local/app/1.0/app.jar", "-o", "json"); err != nil ||
		!strings.Contains(out, `"keyAlias": "ci"`) {
		t.Errorf("get -o json = %q %v", out, err)
	}

	if out, err := relic(t, config, "", "evidence", "verify", "ev-local/app/1.0/app.jar"); err != nil ||
		!hasTableRowWith(out, "verified", "signed by ci") {
		t.Errorf("verify = %q %v", out, err)
	}

	if _, err := relic(
		t,
		config,
		"",
		"evidence",
		"verify",
		"ev-local/app/1.0/app.jar",
		"--public-key",
		keys+".pub",
	); err != nil {
		t.Errorf("verify --public-key: %v", err)
	}

	var ec exitCode

	out, err = relic(t, config, "", "evidence", "verify", "ev-local/app/1.0/app.jar", "--public-key", other+".pub")
	if !errors.As(err, &ec) || ec != 1 || !strings.Contains(out, "the signature does not verify") {
		t.Errorf("verify with another key = %q %v", out, err)
	}

	put(t, config, dir, "ev-local/app/1.0/app.jar", "rebuilt jar")

	out, err = relic(t, config, "", "evidence", "verify", "ev-local/app/1.0/app.jar")
	if !errors.As(err, &ec) || ec != 1 || !strings.Contains(out, "other content") {
		t.Errorf("verify after a change = %q %v", out, err)
	}

	if _, err := relic(t, config, "", "evidence", "verify", "ev-local/app/1.0/missing.jar"); err == nil {
		t.Error("verify of a file without evidence succeeded")
	}
}
