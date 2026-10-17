package telemetry

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStartObjectStorageNamesTheOperationNotABackend(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	previous := otel.GetTracerProvider()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans)))
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
	})

	_, end := StartObjectStorage(t.Context(), "put", "blobs")
	end("error")

	got := spans.GetSpans()
	if len(got) != 1 {
		t.Fatalf("spans = %d, want 1", len(got))
	}

	span := got[0]
	if span.Name != "object storage put" || span.Status.Code != codes.Error ||
		span.Status.Description != "object storage put failed" {
		t.Errorf("span = %q, status %+v", span.Name, span.Status)
	}

	attrs := attribute.NewSet(span.Attributes...)
	if v, _ := attrs.Value("object_storage.operation"); v.AsString() != "put" {
		t.Errorf("object_storage.operation = %q", v.AsString())
	}

	if v, _ := attrs.Value("object_storage.area"); v.AsString() != "blobs" {
		t.Errorf("object_storage.area = %q", v.AsString())
	}
}
