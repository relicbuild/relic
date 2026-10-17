package telemetry

import (
	"context"
	"time"
)

var (
	// PromQL: sum by (family) (rate(relic_http_requests_total[5m]))
	// PromQL: sum by (family) (rate(relic_http_requests_total{code="5xx"}[5m]))
	//   / sum by (family) (rate(relic_http_requests_total[5m]))       (alert RelicHighErrorRate > 0.05)
	httpRequests = counterVec(
		"http_requests_total",
		"HTTP requests by API family, route pattern, method and status class.",
		"family",
		"route",
		"method",
		"code",
	)

	// PromQL: histogram_quantile(0.99, sum by (le, family) (rate(relic_http_request_duration_seconds_bucket[5m])))
	// PromQL: histogram_quantile(0.95, sum by (le, route) (rate(relic_http_request_duration_seconds_bucket{family="packages",method="GET"}[5m])))
	httpDuration = histogramVec(
		"http_request_duration_seconds",
		"Time from the request's arrival to the end of its response.",
		requestBuckets,
		"family",
		"route",
		"method",
	)

	// PromQL: sum by (family) (relic_http_requests_in_flight)
	httpInFlight = gaugeVec("http_requests_in_flight", "Requests being served, by API family.", "family")

	// PromQL: histogram_quantile(0.9, sum by (le, family) (rate(relic_http_request_size_bytes_bucket[5m])))
	httpRequestSize = histogramVec(
		"http_request_size_bytes",
		"Request body bytes read, by API family.",
		sizeBuckets,
		"family",
	)

	// PromQL: sum by (family) (rate(relic_http_response_size_bytes_sum[5m]))
	httpResponseSize = histogramVec(
		"http_response_size_bytes",
		"Response body bytes written, by API family.",
		sizeBuckets,
		"family",
	)
)

// HTTPRequest is one served request, as httpx.Observe saw it.
type HTTPRequest struct {
	Path                        string
	Pattern                     string
	Method                      string
	Status                      int
	Duration                    time.Duration
	RequestBytes, ResponseBytes int64
}

// HTTPStarted counts a request in flight until the returned function runs.
func HTTPStarted(path string) (done func()) {
	g := httpInFlight.WithLabelValues(Family(path))
	g.Inc()

	return g.Dec
}

// ObserveHTTP records a served request.
func ObserveHTTP(ctx context.Context, r HTTPRequest) {
	family, route, method := Family(r.Path), Route(r.Pattern), Method(r.Method)
	httpRequests.WithLabelValues(family, route, method, StatusClass(r.Status)).Inc()
	observe(ctx, httpDuration.WithLabelValues(family, route, method), r.Duration.Seconds())
	httpRequestSize.WithLabelValues(family).Observe(float64(r.RequestBytes))
	httpResponseSize.WithLabelValues(family).Observe(float64(r.ResponseBytes))
}
