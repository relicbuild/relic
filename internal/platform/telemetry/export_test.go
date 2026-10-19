package telemetry

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type failingExporter struct{}

func (failingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("collector unreachable")
}

func (failingExporter) Shutdown(context.Context) error { return nil }

func spansCounted(outcome string) float64 {
	return testutil.ToFloat64(telemetrySpans.WithLabelValues(outcome))
}

func TestFailedSpanExportsAreLoggedAndCounted(t *testing.T) {
	var logs bytes.Buffer

	errorsBefore := testutil.ToFloat64(telemetryErrors.WithLabelValues())
	endedBefore, failedBefore := spansCounted("ended"), spansCounted("failed")

	tel, err := Setup(t.Context(), Options{
		Getenv:       envOf(nil),
		Stderr:       &logs,
		Component:    "server",
		SpanExporter: failingExporter{},
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		_, span := tel.Tracer.Start(t.Context(), "op")
		span.End()
	}

	_ = tel.Shutdown(t.Context())

	if !strings.Contains(logs.String(), `"msg":"telemetry export failed"`) ||
		!strings.Contains(logs.String(), "collector unreachable") {
		t.Errorf("logs %q, want the export failure", logs.String())
	}

	if got := testutil.ToFloat64(telemetryErrors.WithLabelValues()) - errorsBefore; got < 1 {
		t.Errorf("telemetry errors counted %v, want at least 1", got)
	}

	if got := spansCounted("ended") - endedBefore; got != 3 {
		t.Errorf("ended spans counted %v, want 3", got)
	}

	if got := spansCounted("failed") - failedBefore; got != 3 {
		t.Errorf("failed spans counted %v, want 3", got)
	}
}
