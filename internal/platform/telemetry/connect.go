package telemetry

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
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
func ConnectInterceptors() []connect.ServerInterceptor {
	spans, err := otelconnect.NewServerInterceptor(otelconnect.WithoutMetrics(), otelconnect.WithPropagator(propagator))
	if err != nil {
		return []connect.ServerInterceptor{
			rpcMetrics(),
		}
	}

	return []connect.ServerInterceptor{
		spans,
		rpcMetrics(),
	}
}

func rpcMetrics() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			start := time.Now()
			err := next(ctx, spec, stream)
			service, method := splitProcedure(spec.Procedure)
			rpcRequests.WithLabelValues(service, method, rpcCode(err)).Inc()
			observe(ctx, rpcDuration.WithLabelValues(service, method), time.Since(start).Seconds())

			return err
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
