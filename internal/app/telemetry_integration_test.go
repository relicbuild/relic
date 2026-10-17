//go:build integration

package app

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"relic/internal/platform/telemetry/telemetrytest"
)

func delta(t *testing.T, name string, labels map[string]string, f func()) float64 {
	t.Helper()
	before := telemetrytest.Value(t, name, labels)

	f()

	return telemetrytest.Value(t, name, labels) - before
}

// Not parallel: other tests' requests move the same shared metrics.
func TestTelemetryUpstreamAndCache(t *testing.T) {
	var traceparent atomic.Value

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent.Store(r.Header.Get("traceparent"))

		_, _ = io.WriteString(w, "from upstream")
	}))
	t.Cleanup(upstream.Close)
	srv := server(t)
	createRepository(t, srv, "telemetry-remote", "REMOTE", "GENERIC", `,"remote":{"url":"`+upstream.URL+`"}`)

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	get := func() {
		t.Helper()

		resp, body := do(t, srv, call{
			method: http.MethodGet,
			path:   "/api/v1/files/telemetry-remote/x/file.bin",
			headers: map[string]string{
				"traceparent": "00-" + traceID + "-00f067aa0ba902b7-01",
			},
		})
		if resp.StatusCode != http.StatusOK || body != "from upstream" {
			t.Fatalf("download = %d %q", resp.StatusCode, body)
		}
	}
	generic := func(extra map[string]string) map[string]string {
		labels := map[string]string{
			"format": "generic",
		}
		maps.Copy(labels, extra)

		return labels
	}

	var up, miss, fromUpstream float64

	queries := delta(t, "relic_db_query_duration_seconds", map[string]string{
		"query": "GetRepository",
	}, func() {
		up = delta(t, "relic_upstream_requests_total", generic(map[string]string{
			"code": "2xx",
		}), func() {
			miss = delta(t, "relic_remote_cache_total", generic(map[string]string{
				"result": "miss",
			}), func() {
				fromUpstream = delta(
					t,
					"relic_artifact_downloads_total",
					generic(map[string]string{
						"class":  "remote",
						"source": "upstream",
					}),
					get,
				)
			})
		})
	})
	if up < 1 || miss < 1 || fromUpstream < 1 || queries < 1 {
		t.Errorf(
			"first download: upstream requests %v, cache misses %v, downloads from upstream %v, GetRepository queries %v",
			up,
			miss,
			fromUpstream,
			queries,
		)
	}

	if got, _ := traceparent.Load().(string); !strings.Contains(got, traceID) {
		t.Errorf("upstream traceparent = %q, want trace %s", got, traceID)
	}

	var hit, fromCache float64

	hit = delta(t, "relic_remote_cache_total", generic(map[string]string{
		"result": "hit",
	}), func() {
		fromCache = delta(
			t,
			"relic_artifact_downloads_total",
			generic(map[string]string{
				"class":  "remote",
				"source": "cache",
			}),
			get,
		)
	})
	if hit < 1 || fromCache < 1 {
		t.Errorf("second download: cache hits %v, downloads from cache %v", hit, fromCache)
	}

	if telemetrytest.Value(t, "relic_db_pool_max", nil) < 1 {
		t.Error("relic_db_pool_max is 0 with an open pool")
	}
}

// Not parallel: other tests' requests move the same shared metrics.
func TestTelemetryMetricsMoveAfterRequests(t *testing.T) {
	srv := server(t)
	createRepository(t, srv, "telemetry-local", "LOCAL", "GENERIC", "")

	put := map[string]string{
		"family": "relic",
		"route":  "/api/",
		"method": "PUT",
		"code":   "2xx",
	}
	if d := delta(t, "relic_http_requests_total", put, func() {
		upload(t, srv, "telemetry-local", "a/file.txt", "hello")
	}); d < 1 {
		labels := map[string]string{
			"family": "relic",
			"method": "PUT",
			"code":   "2xx",
		}
		t.Errorf(
			"relic_http_requests_total%v moved %v (any route: %v)",
			put,
			d,
			telemetrytest.Value(t, "relic_http_requests_total", labels),
		)
	}

	if d := delta(t, "relic_http_request_size_bytes", map[string]string{
		"family": "relic",
	}, func() {
		upload(t, srv, "telemetry-local", "a/file2.txt", "hello")
	}); d < 1 {
		t.Errorf("request size not observed")
	}

	if d := delta(t, "relic_artifact_uploads_total", map[string]string{
		"format": "generic",
		"class":  "local",
	}, func() {
		upload(t, srv, "telemetry-local", "a/file3.txt", "hello")
	}); d < 1 {
		t.Errorf("upload not counted")
	}

	if d := delta(t, "relic_artifact_bytes_total", map[string]string{
		"direction": "down",
		"format":    "generic",
	}, func() {
		if d := delta(
			t,
			"relic_artifact_downloads_total",
			map[string]string{
				"format": "generic",
				"class":  "local",
				"source": "local",
			},
			func() {
				if resp, body := do(
					t,
					srv,
					call{
						method: http.MethodGet,
						path:   "/api/v1/files/telemetry-local/a/file.txt",
					},
				); resp.StatusCode != http.StatusOK ||
					body != "hello" {
					t.Fatalf("download = %d %q", resp.StatusCode, body)
				}
			},
		); d < 1 {
			t.Errorf("download not counted")
		}
	}); d < 5 {
		t.Errorf("downloaded bytes moved %v, want at least 5", d)
	}

	if d := delta(t, "relic_auth_attempts_total", map[string]string{
		"method": "basic",
		"result": "success",
	}, func() {
		do(t, srv, call{
			method: http.MethodGet,
			path:   "/api/v1/files/telemetry-local/a/file.txt",
		})
	}); d < 1 {
		t.Errorf("successful basic sign-in not counted")
	}

	if d := delta(t, "relic_auth_attempts_total", map[string]string{
		"method": "basic",
		"result": "failure",
	}, func() {
		do(t, srv, call{
			method: http.MethodGet,
			path:   "/api/v1/repositories",
			noAuth: true,
			headers: map[string]string{
				"Authorization": "Basic YWRtaW46d3Jvbmc=",
			},
		})
	}); d < 1 {
		t.Errorf("failed basic sign-in not counted")
	}

	rpc := map[string]string{
		"service": "relic.v1.SystemService",
		"method":  "GetVersion",
		"code":    "ok",
	}
	if d := delta(t, "relic_rpc_requests_total", rpc, func() {
		do(t, srv, call{
			method: http.MethodPost,
			path:   "/api/relic.v1.SystemService/GetVersion",
			body:   `{}`,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		})
	}); d < 1 {
		t.Errorf("relic_rpc_requests_total%v did not move", rpc)
	}

	denied := map[string]string{
		"service": "relic.v1.SystemService",
		"method":  "GetDashboardSummary",
		"code":    "unauthenticated",
	}
	if d := delta(t, "relic_rpc_requests_total", denied, func() {
		do(t, srv, call{
			method: http.MethodPost,
			path:   "/api/relic.v1.SystemService/GetDashboardSummary",
			body:   `{}`,
			noAuth: true,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		})
	}); d < 1 {
		t.Errorf("relic_rpc_requests_total%v did not move", denied)
	}

	if d := delta(t, "relic_tokens_issued_total", map[string]string{
		"kind": "access",
	}, func() {
		resp, _ := do(t, srv, call{
			method: http.MethodPost,
			path:   "/api/relic.v1.SecurityService/CreateToken",
			body:   `{"description":"telemetry"}`,
			headers: map[string]string{
				"Content-Type": "application/json",
			},
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("token = %d", resp.StatusCode)
		}
	}); d < 1 {
		t.Errorf("issued token not counted")
	}
}
