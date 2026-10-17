package main

import (
	"os"
	"testing"
)

func TestMetricsReferenceUpToDate(t *testing.T) {
	got, err := os.ReadFile("../../" + page)
	if err != nil || string(got) != render() {
		t.Errorf("%s is stale (%v): run go run ./tools/metricsdoc", page, err)
	}
}
