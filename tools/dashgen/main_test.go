package main

import (
	"bytes"
	"os"
	"testing"
)

func TestDashboardsUpToDate(t *testing.T) {
	t.Chdir("../..")

	files, err := generate()
	if err != nil {
		t.Fatal(err)
	}

	for p, want := range files {
		got, err := os.ReadFile(p) //nolint:gosec // a path in the repository
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is stale (%v): run go run ./tools/dashgen", p, err)
		}
	}

	stale, err := orphans(files)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range stale {
		t.Errorf("%s is no longer generated: run go run ./tools/dashgen", p)
	}
}
