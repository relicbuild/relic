package telemetry

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
)

var (
	// PromQL: sum by (format) (rate(relic_upstream_requests_total{code=~"5xx|error"}[15m]))
	//   / sum by (format) (rate(relic_upstream_requests_total[15m]))   (alert RelicUpstreamErrors > 0.2)
	upstreamRequests = repoCounterVec(
		"upstream_requests_total",
		"Requests to remote repositories' upstreams, by format and status class (error when no response came).",
		"format",
		"code",
	)

	// PromQL: histogram_quantile(0.95, sum by (le, format) (rate(relic_upstream_duration_seconds_bucket[5m])))
	upstreamDuration = repoHistogramVec(
		"upstream_duration_seconds",
		"Time to an upstream's response headers.",
		requestBuckets,
		"format",
	)

	// PromQL: sum by (format) (rate(relic_remote_cache_total{result="hit"}[5m]))
	//   / sum by (format) (rate(relic_remote_cache_total[5m]))           (cache hit ratio)
	remoteCache = repoCounterVec(
		"remote_cache_total",
		"Remote repository lookups: hit (served from the cache), miss (fetched), stale (cached metadata served because the upstream failed), absent (an upstream 404 remembered), error.",
		"format",
		"result",
	)
)

type upstreamKey struct{}

type upstreamInfo struct {
	repo, format string
}

// WithUpstream notes the remote repository an upstream request is made for.
func WithUpstream(ctx context.Context, repo, format string) context.Context {
	return context.WithValue(ctx, upstreamKey{}, upstreamInfo{
		repo:   repo,
		format: Format(format),
	})
}

// UpstreamTransport measures requests to upstreams and gives each a client
// span.
func UpstreamTransport(next http.RoundTripper) http.RoundTripper {
	measured := roundTripper(func(req *http.Request) (*http.Response, error) {
		info, _ := req.Context().Value(upstreamKey{}).(upstreamInfo)
		if info.format == "" {
			info.format = Other
		}

		start := time.Now()
		resp, err := next.RoundTrip(req)

		code := "error"
		if err == nil {
			code = StatusClass(resp.StatusCode)
		}

		c, lvs := upstreamRequests.current(info.repo, []string{
			info.format,
			code,
		})
		c.WithLabelValues(lvs...).Inc()
		h, lvs := upstreamDuration.current(info.repo, []string{
			info.format,
		})
		observe(req.Context(), h.WithLabelValues(lvs...), time.Since(start).Seconds())

		return resp, err
	})

	return otelhttp.NewTransport(measured, otelhttp.WithPropagators(propagation.TraceContext{}))
}

// ClientTransport gives each request a client span and passes the trace
// context on.
func ClientTransport(next http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(next, otelhttp.WithPropagators(propagation.TraceContext{}))
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// RemoteCache counts a remote repository lookup (hit, miss, stale or error).
func RemoteCache(repo, format, res string) {
	c, lvs := remoteCache.current(repo, []string{
		Format(format),
		result(res),
	})
	c.WithLabelValues(lvs...).Inc()
}

func repoHistogramVec(name, help string, buckets []float64, labels ...string) *repoFamily[*prometheus.HistogramVec] {
	describe(Metric{
		Name:            namespace + "_" + name,
		Type:            "histogram",
		Help:            help,
		Labels:          labels,
		RepositoryLabel: true,
	})
	f := newRepoFamily(registry, labels, func(l []string) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      name,
				Help:      help,
				Buckets:   buckets,
			},
			l,
		)
	})

	repoFamiliesMu.Lock()
	defer repoFamiliesMu.Unlock()

	repoFamilies = append(repoFamilies, f)

	return f
}
