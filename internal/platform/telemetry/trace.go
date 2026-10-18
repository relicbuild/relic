package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

const tracerName = "relic"

func newResource(ctx context.Context, cfg Config, component, version, instance string) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version),
			semconv.ServiceInstanceID(instance),
			semconv.ServiceNamespace("relic"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	return resource.Merge(res, resource.NewSchemaless(attrComponent.String(component)))
}

func sampler(cfg Config) sdktrace.Sampler {
	switch cfg.Sampler {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(cfg.SamplerArg)
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SamplerArg))
	default:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}

func newTracerProvider(
	ctx context.Context,
	cfg Config,
	res *resource.Resource,
	exporter sdktrace.SpanExporter,
) (*sdktrace.TracerProvider, error) {
	if exporter == nil {
		var err error

		exporter, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.TracesEndpoint))
		if err != nil {
			return nil, fmt.Errorf("OTLP trace exporter: %w", err)
		}
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(endedSpans{}),
		sdktrace.WithBatcher(countedExporter{
			exporter,
		}),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler(cfg)),
	), nil
}

func newLoggerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	exporter, err := otlploghttp.New(ctx, otlploghttp.WithEndpointURL(cfg.LogsEndpoint))
	if err != nil {
		return nil, fmt.Errorf("OTLP log exporter: %w", err)
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(res),
	), nil
}
