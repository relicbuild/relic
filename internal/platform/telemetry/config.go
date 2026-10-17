// Package telemetry is Relic's observability.
package telemetry

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
)

// Config is the telemetry configuration, read from the environment.
type Config struct {
	MetricsAddr     string
	Pprof           bool
	RepositoryLabel bool
	LogLevel        slog.Level
	LogFormat       string
	ServiceName     string
	TracesEndpoint  string
	LogsEndpoint    string
	Sampler         string
	SamplerArg      float64
}

var samplers = []string{
	"always_on",
	"always_off",
	"traceidratio",
	"parentbased_always_on",
	"parentbased_always_off",
	"parentbased_traceidratio",
}

// LoadConfig reads the telemetry configuration for component ("server",
// "worker" or "migrate"). getenv is usually os.Getenv.
func LoadConfig(getenv func(string) string, component string) (Config, error) {
	untrimmed := getenv
	getenv = func(key string) string {
		return strings.TrimSpace(untrimmed(key))
	}

	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}

		return def
	}

	var errs []error

	cfg := Config{
		MetricsAddr:     env("RELIC_METRICS_LISTEN_ADDRESS", ":8091"),
		Pprof:           getenv("RELIC_PROFILING_ENABLED") == "true",
		RepositoryLabel: getenv("RELIC_METRICS_REPOSITORY_LABEL") == "true",
		LogFormat:       env("RELIC_LOG_FORMAT", "json"),
		ServiceName:     env("OTEL_SERVICE_NAME", "relic-"+component),
		Sampler:         env("OTEL_TRACES_SAMPLER", "parentbased_always_on"),
		SamplerArg:      1,
	}
	if cfg.MetricsAddr == "off" {
		cfg.MetricsAddr = ""
	} else if _, _, err := net.SplitHostPort(cfg.MetricsAddr); err != nil {
		errs = append(errs, fmt.Errorf("RELIC_METRICS_LISTEN_ADDRESS %q: %w", cfg.MetricsAddr, err))
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(env("RELIC_LOG_LEVEL", "info"))); err != nil {
		errs = append(
			errs,
			fmt.Errorf("RELIC_LOG_LEVEL %q must be debug, info, warn or error", getenv("RELIC_LOG_LEVEL")),
		)
	}

	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("RELIC_LOG_FORMAT %q must be json or text", cfg.LogFormat))
	}

	if !slices.Contains(samplers, cfg.Sampler) {
		errs = append(
			errs,
			fmt.Errorf("OTEL_TRACES_SAMPLER %q must be one of %s", cfg.Sampler, strings.Join(samplers, ", ")),
		)
	}

	if v := getenv("OTEL_TRACES_SAMPLER_ARG"); v != "" && strings.HasSuffix(cfg.Sampler, "traceidratio") {
		ratio, err := strconv.ParseFloat(v, 64)
		if err != nil || ratio < 0 || ratio > 1 {
			errs = append(errs, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG %q must be a ratio between 0 and 1", v))
		}

		cfg.SamplerArg = ratio
	}

	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
	} {
		if v := getenv(key); v != "" && v != "http/protobuf" {
			errs = append(errs, fmt.Errorf("%s %q: Relic exports OTLP over http/protobuf only", key, v))
		}
	}

	if getenv("OTEL_SDK_DISABLED") == "true" {
		return cfg, errors.Join(errs...)
	}

	base := strings.TrimSuffix(getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), "/")

	signal := func(own, path string) string {
		if v := getenv(own); v != "" {
			return v
		}

		if base == "" {
			return ""
		}

		return base + path
	}
	if exp := env("OTEL_TRACES_EXPORTER", "otlp"); exp == "otlp" {
		cfg.TracesEndpoint = signal("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "/v1/traces")
	} else if exp != "none" {
		errs = append(errs, fmt.Errorf("OTEL_TRACES_EXPORTER %q must be otlp or none", exp))
	}

	if exp := env("OTEL_LOGS_EXPORTER", "none"); exp == "otlp" {
		cfg.LogsEndpoint = signal("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "/v1/logs")
		if cfg.LogsEndpoint == "" {
			errs = append(
				errs,
				errors.New(
					"OTEL_LOGS_EXPORTER=otlp needs OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
				),
			)
		}
	} else if exp != "none" {
		errs = append(errs, fmt.Errorf("OTEL_LOGS_EXPORTER %q must be otlp or none", exp))
	}

	return cfg, errors.Join(errs...)
}
