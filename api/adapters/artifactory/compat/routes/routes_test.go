package main

import (
	"bytes"
	"os"
	"testing"

	"relic/internal/app"
)

func TestOpenAPIUpToDate(t *testing.T) {
	t.Parallel()

	got, err := app.RouteInventory()
	if err != nil {
		t.Fatal(err)
	}

	want, err := os.ReadFile("../docs/routes.openapi.json")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Error(
			"api/adapters/artifactory/compat/docs/routes.openapi.json is out of date: run go run ./api/adapters/artifactory/compat/routes",
		)
	}
}
