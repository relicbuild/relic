package app

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"relic/internal/app/config"
	"relic/internal/platform/telemetry"
)

func TestSettingNamesAreTheDocumentedOnes(t *testing.T) {
	t.Parallel()

	reference, err := os.ReadFile("../../web/layers/site/content/docs/2.installation/6.configuration.md")
	if err != nil {
		t.Fatal(err)
	}

	name := regexp.MustCompile("`(RELIC_[A-Z0-9_]+)`")

	var documented []string

	for line := range strings.Lines(string(reference)) {
		if !strings.HasPrefix(line, "| `") {
			continue
		}

		for _, m := range name.FindAllStringSubmatch(strings.Split(line, "|")[1], -1) {
			documented = append(documented, m[1])
		}
	}

	slices.Sort(documented)

	names := slices.Sorted(slices.Values(SettingNames))
	if !slices.Equal(names, documented) {
		t.Errorf("SettingNames = %v\nthe configuration reference documents %v", names, documented)
	}
}

func TestSettingNamesHoldEverySettingTheServerReads(t *testing.T) {
	t.Parallel()

	for _, env := range []map[string]string{
		{},
		{
			"OTEL_LOGS_EXPORTER": "otlp",
		},
	} {
		var read []string

		getenv := func(key string) string {
			read = append(read, key)

			return env[key]
		}

		_, _ = config.Load(getenv)
		_, _ = telemetry.LoadConfig(getenv, "server")

		for _, key := range read {
			if strings.HasPrefix(key, "RELIC_") && !slices.Contains(SettingNames, key) {
				t.Errorf("the server reads %s, which SettingNames lacks", key)
			}
		}
	}
}
