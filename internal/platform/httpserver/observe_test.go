package httpserver

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"relic/internal/access/auth"
	"relic/internal/platform/telemetry"
	"relic/internal/platform/telemetry/telemetrytest"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func observed(t *testing.T) http.Handler {
	t.Helper()

	rt := NewRouter()
	rt.HandleFunc("GET /observe-test/{id}", auth.Public, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	})
	rt.HandleFunc("PUT /observe-test/{id}", auth.Public, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		w.WriteHeader(http.StatusCreated)
	})

	return Observe(slog.New(slog.DiscardHandler), rt)
}

func TestObserveRecordsRoutePattern(t *testing.T) {
	t.Parallel()
	h := observed(t)

	const route = "/observe-test/{id}"

	get := func() float64 {
		return httpRequests(t, "ui", route, "GET", "2xx")
	}
	before := get()

	for i := range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(
			rec,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/observe-test/%d", i), nil),
		)
	}

	if got := get() - before; got != 3 {
		t.Errorf("GET %s counted %v times, want 3 (one series for every id)", route, got)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(
		rec,
		httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/observe-test/x", strings.NewReader("12345")),
	)

	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT = %d", rec.Code)
	}

	if got := httpRequests(t, "ui", route, "PUT", "2xx"); got < 1 {
		t.Errorf("PUT not counted")
	}
}

func TestObserveLabelsAreBounded(t *testing.T) {
	t.Parallel()
	h := observed(t)

	junk := []string{
		"/etc/passwd-junk",
		"/wp-login-junk.php",
		"/packages/junk-repo/secret-junk.txt",
		"/api/junk-rpc",
		"/v2/junk-image/manifests/junk-tag",
	}
	for i := range 50 {
		for _, p := range junk {
			for _, m := range []string{
				"GET",
				"PROPFIND",
				"BREW",
				"DELETE",
			} {
				req := httptest.NewRequestWithContext(t.Context(), m, fmt.Sprintf("%s/%d?token=abc", p, i), nil)
				h.ServeHTTP(httptest.NewRecorder(), req)
			}
		}
	}

	mfs, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}

	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "relic_http_") {
			continue
		}

		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				v := l.GetValue()
				if strings.Contains(v, "junk") || strings.Contains(v, "token") {
					t.Errorf("%s{%s=%q}: request data became a label value", mf.GetName(), l.GetName(), v)
				}

				switch l.GetName() {
				case "family":
					ok(t, telemetry.Families, v, mf.GetName())
				case "method":
					ok(t, append(slices.Clone(telemetry.Methods), "OTHER"), v, mf.GetName())
				case "code":
					ok(t, telemetry.StatusClasses, v, mf.GetName())
				case "route":
					if v != telemetry.Other && !strings.HasPrefix(v, "/") {
						t.Errorf("%s route %q", mf.GetName(), v)
					}
				}
			}
		}
	}

	if got := httpRequests(t, "ui", telemetry.Other, "OTHER", "4xx"); got < 1 {
		t.Errorf("unmatched requests with odd methods were not counted under route other, method OTHER")
	}
}

func httpRequests(t *testing.T, family, route, method, code string) float64 {
	t.Helper()

	return telemetrytest.Value(
		t,
		"relic_http_requests_total",
		map[string]string{
			"family": family,
			"route":  route,
			"method": method,
			"code":   code,
		},
	)
}

func ok(t *testing.T, allowed []string, v, name string) {
	t.Helper()

	if !slices.Contains(allowed, v) {
		t.Errorf("%s: label value %q not in %v", name, v, allowed)
	}
}

func TestRoutedNamesServerSpan(t *testing.T) {
	t.Parallel()

	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	ctx, span := tp.Tracer("test").Start(t.Context(), "relic")
	// PUT: TestObserveRecordsRoutePattern counts the GETs in parallel.
	put := httptest.NewRequestWithContext(ctx, http.MethodPut, "/observe-test/1", nil)
	observed(t).ServeHTTP(httptest.NewRecorder(), put)
	span.End()

	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Name() != "PUT /observe-test/{id}" {
		t.Fatalf("spans %v", ended)
	}
}

func TestObserveFailsAWriteToAReaderThatStalls(t *testing.T) {
	t.Parallel()

	written := make(chan error, 1)
	srv := httptest.NewServer(observe(
		slog.New(slog.DiscardHandler),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			chunk := make([]byte, 64<<10)
			for {
				if _, err := w.Write(chunk); err != nil {
					written <- err

					return
				}
			}
		}),
		200*time.Millisecond,
		time.Minute,
	))
	t.Cleanup(srv.Close)

	var dialer net.Dialer

	conn, err := dialer.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
	})

	if _, err := io.WriteString(conn, "GET /stream HTTP/1.1\r\nHost: relic\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-written:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("write error = %v, want a deadline error", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a write to a client that reads nothing never failed")
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestObserveWarnsOfASlowRequestWhileItRuns(t *testing.T) {
	t.Parallel()

	var logs lockedBuffer

	slowCount := func() float64 {
		return telemetrytest.Value(
			t,
			"relic_http_slow_requests_total",
			map[string]string{
				"family": "ui",
			},
		)
	}
	before := slowCount()

	h := observe(
		slog.New(slog.NewJSONHandler(&logs, nil)),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/slow" {
				for !strings.Contains(logs.String(), `"msg":"slow request"`) {
					time.Sleep(10 * time.Millisecond)
				}
			}

			w.WriteHeader(http.StatusNoContent)
		}),
		time.Minute,
		50*time.Millisecond,
	)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/slow", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/fast", nil))

	var warnings []string

	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, `"msg":"slow request"`) {
			warnings = append(warnings, line)
		}
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0], `"path":"/slow"`) ||
		!strings.Contains(warnings[0], `"level":"WARN"`) || !strings.Contains(warnings[0], `"request_id"`) {
		t.Errorf("slow request warnings %q, want one for /slow", warnings)
	}

	if got := slowCount() - before; got != 1 {
		t.Errorf("slow requests counted %v times, want 1", got)
	}
}
