package telemetry

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// HTTPServer opens a server span per request, continuing the caller's trace
// from its traceparent header.
func HTTPServer(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "relic", otelhttp.WithPropagators(propagator),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return Family(r.URL.Path) != "health"
		}))
}

// RouteMatched names the request's server span after the router pattern
// ("GET /api/v1/files/{repo}/{path...}") and records it as http.route.
func RouteMatched(ctx context.Context, pattern string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() || pattern == "" {
		return
	}

	span.SetName(pattern)
	span.SetAttributes(semconv.HTTPRoute(Route(pattern)))
}
