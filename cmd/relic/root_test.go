package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettingsSetsTheEnvironment(t *testing.T) {
	t.Setenv("RELIC_LOG_LEVEL", "info")
	t.Setenv("AWS_REGION", "")

	path := filepath.Join(t.TempDir(), "server.env")
	settings := "# Relic server settings\n\nRELIC_LOG_LEVEL=debug\nAWS_REGION=eu-central-1\n"

	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := loadSettings(path); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{
		"RELIC_LOG_LEVEL": "debug",
		"AWS_REGION":      "eu-central-1",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if err := loadSettings(filepath.Join(t.TempDir(), "missing.env")); err == nil {
		t.Error("a missing settings file loaded")
	}
}
