package telemetry

import (
	"slices"
)

// PromQL: sum by (result) (increase(relic_end_of_life_refreshes_total[1d]))
var endOfLifeRefreshes = counterVec(
	"end_of_life_refreshes_total",
	"Reads of the end-of-life data by result (changed, unchanged, error).",
	"result",
)

var endOfLifeResults = []string{
	"changed",
	"unchanged",
	"error",
}

// EndOfLifeRefreshed counts a read of the end-of-life data.
func EndOfLifeRefreshed(result string) {
	if !slices.Contains(endOfLifeResults, result) {
		result = Other
	}

	endOfLifeRefreshes.WithLabelValues(result).Inc()
}

// PromQL: sum by (kind) (increase(relic_operational_risks_found_total[1d]))
var operationalRisksFound = counterVec(
	"operational_risks_found_total",
	"Operational risks found in scanned artifacts by kind (end_of_life, deprecated, unmaintained).",
	"kind",
)

var operationalRiskKinds = []string{
	"end_of_life",
	"deprecated",
	"unmaintained",
}

// OperationalRiskFound counts an operational risk a scan found.
func OperationalRiskFound(kind string) {
	if !slices.Contains(operationalRiskKinds, kind) {
		kind = Other
	}

	operationalRisksFound.WithLabelValues(kind).Inc()
}

// PromQL: sum by (result) (rate(relic_deps_dev_lookups_total[1h]))
var depsDevLookups = counterVec(
	"deps_dev_lookups_total",
	"deps.dev lookups that missed the cache, by result (success, error).",
	"result",
)

// DepsDevLookup counts a deps.dev lookup.
func DepsDevLookup(err error) {
	depsDevLookups.WithLabelValues(Result(err)).Inc()
}

// PromQL: sum by (format) (increase(relic_vex_exports_total[1d]))
var vexExports = counterVec(
	"vex_exports_total",
	"VEX documents exported by format (openvex, cyclonedx).",
	"format",
)

// VEXExported counts an exported VEX document.
func VEXExported(cycloneDX bool) {
	format := "openvex"
	if cycloneDX {
		format = "cyclonedx"
	}

	vexExports.WithLabelValues(format).Inc()
}

// PromQL: sum by (provider, result) (increase(relic_secret_validations_total[1d]))
var secretValidations = counterVec(
	"secret_validations_total",
	"Secrets found in content checked with their provider, by provider and result (live, inactive, unknown, error).",
	"provider",
	"result",
)

var (
	secretProviders = []string{
		"github",
		"gitlab",
		"slack",
		"npm",
		"stripe",
	}
	secretResults = []string{
		"live",
		"inactive",
		"unknown",
		"error",
	}
)

// SecretValidated counts a secret checked with its provider.
func SecretValidated(provider, result string) {
	if !slices.Contains(secretProviders, provider) {
		provider = Other
	}

	if !slices.Contains(secretResults, result) {
		result = Other
	}

	secretValidations.WithLabelValues(provider, result).Inc()
}
