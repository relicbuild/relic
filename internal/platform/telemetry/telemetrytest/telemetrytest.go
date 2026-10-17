// Package telemetrytest reads Relic's metrics in tests.
package telemetrytest

import (
	"testing"

	"relic/internal/platform/telemetry"
)

// Value sums the series of the family name whose labels include every pair
// in labels.
func Value(t testing.TB, name string, labels map[string]string) float64 {
	t.Helper()

	mfs, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}

	var sum float64

	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}

	metrics:
		for _, m := range mf.GetMetric() {
			have := map[string]string{}
			for _, l := range m.GetLabel() {
				have[l.GetName()] = l.GetValue()
			}

			for k, v := range labels {
				if have[k] != v {
					continue metrics
				}
			}

			switch {
			case m.GetCounter() != nil:
				sum += m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				sum += m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				sum += float64(m.GetHistogram().GetSampleCount())
			}
		}
	}

	return sum
}
