package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// PromQL: sum(rate(relic_telemetry_errors_total[5m]))
var telemetryErrors = counterVec(
	"telemetry_errors_total",
	"Errors the OpenTelemetry SDK reported, such as a failed export of spans or log records.",
)

// PromQL: sum(rate(relic_telemetry_spans_total{outcome="ended"}[5m]))
//   - sum(rate(relic_telemetry_spans_total{outcome=~"exported|failed"}[5m]))   (spans dropped or still queued)
var telemetrySpans = counterVec(
	"telemetry_spans_total",
	"Sampled spans by outcome: ended (queued for export), exported, or failed (the export returned an error).",
	"outcome",
)

// handleSDKErrors logs and counts the errors the OpenTelemetry SDK reports,
// which it otherwise prints through the standard logger or drops.
func handleSDKErrors(log *slog.Logger) {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		telemetryErrors.WithLabelValues().Inc()
		log.Warn("telemetry export failed", "error", err)
	}))
}

// endedSpans counts the sampled spans that end, before the batch processor
// queues them; the exporter counts those it sends.
type endedSpans struct{}

func (endedSpans) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (endedSpans) OnEnd(s sdktrace.ReadOnlySpan) {
	if s.SpanContext().IsSampled() {
		telemetrySpans.WithLabelValues("ended").Inc()
	}
}

func (endedSpans) Shutdown(context.Context) error { return nil }

func (endedSpans) ForceFlush(context.Context) error { return nil }

// countedExporter counts the spans its exporter sends or fails to send.
type countedExporter struct {
	sdktrace.SpanExporter
}

func (e countedExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)

	outcome := "exported"
	if err != nil {
		outcome = "failed"
	}

	telemetrySpans.WithLabelValues(outcome).Add(float64(len(spans)))

	return err
}
