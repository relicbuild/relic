package main

import (
	"testing"

	relicv1 "relic/api/gen/relic/v1"
)

func TestRuleFiltersShowEveryLimit(t *testing.T) {
	t.Parallel()

	got := ruleFilters(&relicv1.IgnoreRule{
		Cves: []string{
			"CVE-2021-23337",
		},
		Watches: []string{
			"prod",
		},
		Policies: []string{
			"critical",
		},
		Builds: []*relicv1.IgnoredBuild{
			{
				Name: "app",
			},
			{
				Name:   "api",
				Number: "7",
			},
		},
	})

	if want := "CVE-2021-23337,watch:prod,policy:critical,build:app,build:api/7"; got != want {
		t.Errorf("ruleFilters = %q, want %q", got, want)
	}
}
