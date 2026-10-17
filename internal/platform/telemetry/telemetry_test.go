package telemetry

// The tests that call Setup do not run in parallel.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestSetupWithoutEndpointIsNoop(t *testing.T) {
	tel, err := Setup(t.Context(), Options{
		Getenv:    envOf(nil),
		Stderr:    io.Discard,
		Component: "server",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = tel.Shutdown(context.Background())
	})

	_, span := tel.Tracer.Start(t.Context(), "op")
	defer span.End()

	if span.IsRecording() || span.SpanContext().IsValid() {
		t.Errorf("span without an endpoint: recording %v, valid %v", span.IsRecording(), span.SpanContext().IsValid())
	}

	if len(tel.shutdown) != 0 {
		t.Errorf("%d providers to shut down without an endpoint", len(tel.shutdown))
	}
}

func TestTraceparentYieldsChildSpanAndLogIDs(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()

	var logs bytes.Buffer

	tel, err := Setup(
		t.Context(),
		Options{
			Getenv:    envOf(nil),
			Stderr:    &logs,
			Component: "server",
			SpanExporter: keepSpans{
				spans,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	h := HTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tel.Logger.InfoContext(r.Context(), "request")
		w.WriteHeader(http.StatusNoContent)
	}))

	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		parent  = "00f067aa0ba902b7"
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-"+parent+"-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := spans.GetSpans()
	if len(got) != 1 {
		t.Fatalf("%d spans, want 1", len(got))
	}

	s := got[0]
	if s.SpanContext.TraceID().String() != traceID || s.Parent.SpanID().String() != parent ||
		s.SpanKind != trace.SpanKindServer {
		t.Errorf("span trace %s parent %s kind %s", s.SpanContext.TraceID(), s.Parent.SpanID(), s.SpanKind)
	}

	var line struct {
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
	}
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("log line %q: %v", logs.String(), err)
	}

	if line.TraceID != traceID || line.SpanID != s.SpanContext.SpanID().String() {
		t.Errorf("log IDs %+v, want trace %s span %s", line, traceID, s.SpanContext.SpanID())
	}
}

func TestProbesAreNotTraced(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()

	tel, err := Setup(
		t.Context(),
		Options{
			Getenv:    envOf(nil),
			Stderr:    io.Discard,
			Component: "server",
			SpanExporter: keepSpans{
				spans,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	h := HTTPServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{
		"/healthz",
		"/readyz",
		"/api/v1/files/x",
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	}

	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := spans.GetSpans(); len(got) != 1 {
		t.Errorf("%d spans, want only the API request's", len(got))
	}
}

type keepSpans struct {
	*tracetest.InMemoryExporter
}

func (keepSpans) Shutdown(context.Context) error { return nil }

func TestLogLevelAndFormat(t *testing.T) {
	var logs bytes.Buffer

	env := map[string]string{
		"RELIC_LOG_LEVEL":  "warn",
		"RELIC_LOG_FORMAT": "text",
	}

	tel, err := Setup(t.Context(), Options{
		Getenv:    envOf(env),
		Stderr:    &logs,
		Component: "worker",
	})
	if err != nil {
		t.Fatal(err)
	}

	tel.Logger.Info("hidden")
	tel.Logger.Warn("shown")

	if out := logs.String(); strings.Contains(out, "hidden") || !strings.Contains(out, "level=WARN msg=shown") {
		t.Errorf("logs = %q", out)
	}
}

func TestServeMetrics(t *testing.T) {
	env := map[string]string{
		"RELIC_METRICS_LISTEN_ADDRESS": "127.0.0.1:0",
		"RELIC_PROFILING_ENABLED":      "true",
	}

	tel, err := Setup(
		t.Context(),
		Options{
			Getenv:    envOf(env),
			Stderr:    io.Discard,
			Component: "server",
			Version:   "1.2.3",
			Revision:  "abc",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	addr, err := tel.ServeMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}

	get := func(path string) (int, string) {
		t.Helper()

		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr.String()+path, nil)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		b, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(b)
	}

	code, body := get("/metrics")
	for _, want := range []string{
		`relic_build_info{component="server",revision="abc",version="1.2.3"} 1`,
		"go_goroutines",
		"process_cpu_seconds_total",
	} {
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("/metrics %d lacks %q", code, want)
		}
	}

	if code, _ := get("/debug/pprof/"); code != http.StatusOK {
		t.Errorf("pprof = %d", code)
	}

	if code, body := get("/debug/pprof/profile?seconds=1"); code != http.StatusOK {
		t.Errorf("a one-second CPU profile = %d %s", code, body)
	}

	if srv := newMetricsServer(http.NotFoundHandler(), slog.New(slog.DiscardHandler)); srv.ReadTimeout <= 0 ||
		srv.WriteTimeout <= 0 {
		t.Errorf("metrics server timeouts: read %v, write %v", srv.ReadTimeout, srv.WriteTimeout)
	}

	if code, _ := get("/"); code != http.StatusNotFound {
		t.Errorf("/ = %d on the metrics listener", code)
	}
}

func TestSetupRepositoryLabel(t *testing.T) {
	for _, on := range []bool{
		true,
		false,
	} {
		env := map[string]string{
			"RELIC_METRICS_REPOSITORY_LABEL": strconv.FormatBool(on),
		}
		if _, err := Setup(
			t.Context(),
			Options{
				Getenv:    envOf(env),
				Stderr:    io.Discard,
				Component: "server",
			},
		); err != nil {
			t.Fatal(err)
		}

		ArtifactUploaded("libs-local", "npm", "local", 10, false)

		for _, name := range []string{
			"relic_artifact_uploads_total",
			"relic_artifact_bytes_total",
		} {
			got := labelNames(t, registry, name)
			if len(got) == 0 {
				t.Fatalf("%s: no series", name)
			}

			for _, labels := range got {
				if slices.Contains(labels, "repository") != on {
					t.Errorf("repository label %v: %s has labels %v", on, name, labels)
				}
			}
		}
	}
}

func TestServeMetricsOff(t *testing.T) {
	tel, err := Setup(
		t.Context(),
		Options{
			Getenv: envOf(map[string]string{
				"RELIC_METRICS_LISTEN_ADDRESS": "off",
			}),
			Stderr:    io.Discard,
			Component: "server",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if addr, err := tel.ServeMetrics(t.Context()); addr != nil || err != nil {
		t.Errorf("ServeMetrics = %v, %v with the listener off", addr, err)
	}
}

func TestTraceHandlerWithoutSpan(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	log := slog.New(newLogHandler(Config{
		LogFormat: "json",
	}, &logs)).With("k", "v")
	log.InfoContext(t.Context(), "plain")

	if strings.Contains(logs.String(), "trace_id") || !strings.Contains(logs.String(), `"k":"v"`) {
		t.Errorf("log = %q", logs.String())
	}
}

func TestEgressSendsTraceContextWithoutBaggage(t *testing.T) {
	t.Parallel()

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	headers := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()

		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		t.Fatal(err)
	}

	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}

	member, err := baggage.NewMember("tenant", "internal")
	if err != nil {
		t.Fatal(err)
	}

	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}

	ctx := baggage.ContextWithBaggage(
		trace.ContextWithRemoteSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    tid,
			SpanID:     sid,
			TraceFlags: trace.FlagsSampled,
			Remote:     true,
		})),
		bag,
	)

	for _, tc := range []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name:      "upstream",
			transport: UpstreamTransport(http.DefaultTransport),
		},
		{
			name:      "client",
			transport: ClientTransport(http.DefaultTransport),
		},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}

		resp, err := tc.transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()
		got := <-headers

		if !strings.Contains(got.Get("traceparent"), traceID) {
			t.Errorf("%s: traceparent = %q, want trace %s", tc.name, got.Get("traceparent"), traceID)
		}

		if b := got.Get("baggage"); b != "" {
			t.Errorf("%s: baggage %q left Relic", tc.name, b)
		}
	}
}
