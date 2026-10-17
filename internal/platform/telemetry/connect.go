package telemetry

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
)

var (
	// PromQL: sum by (service, method) (rate(relic_rpc_requests_total{code!="ok"}[5m]))
	rpcRequests = counterVec(
		"rpc_requests_total",
		"Relic API (Connect) calls by service, method and Connect code.",
		"service",
		"method",
		"code",
	)

	// PromQL: histogram_quantile(0.99, sum by (le, service, method) (rate(relic_rpc_duration_seconds_bucket[5m])))
	rpcDuration = histogramVec(
		"rpc_duration_seconds",
		"Relic API (Connect) call duration.",
		requestBuckets,
		"service",
		"method",
	)
)

// ConnectInterceptors instrument the Relic API: a span per call (a child of
// the HTTP server span) and the relic_rpc_* series.
func ConnectInterceptors() []connect.Interceptor {
	spans, err := otelconnect.NewInterceptor(otelconnect.WithoutMetrics(), otelconnect.WithPropagator(propagator))
	if err != nil {
		return []connect.Interceptor{
			rpcMetrics(),
		}
	}

	return []connect.Interceptor{
		spans,
		rpcMetrics(),
	}
}

func rpcMetrics() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			start := time.Now()
			resp, err := next(ctx, req)
			service, method := splitProcedure(req.Spec().Procedure)
			rpcRequests.WithLabelValues(service, method, rpcCode(err)).Inc()
			observe(ctx, rpcDuration.WithLabelValues(service, method), time.Since(start).Seconds())

			return resp, err
		}
	}
}

func splitProcedure(p string) (service, method string) {
	service, method, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	if !ok {
		return Other, Other
	}

	return service, method
}

func rpcCode(err error) string {
	if err == nil {
		return "ok"
	}

	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Code().String()
	}

	return connect.CodeUnknown.String()
}
