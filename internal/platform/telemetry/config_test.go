package telemetry

import (
	"log/slog"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string {
		return m[k]
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr string
	}{
		{
			name: "defaults: metrics on :8091, tracing and OTLP logs off",
			env:  nil,
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.MetricsAddr != ":8091" || c.Pprof || c.RepositoryLabel || c.LogLevel != slog.LevelInfo ||
					c.LogFormat != "json" {
					t.Errorf("defaults = %+v", c)
				}

				if c.TracesEndpoint != "" || c.LogsEndpoint != "" || c.ServiceName != "relic-server" ||
					c.Sampler != "parentbased_always_on" || c.SamplerArg != 1 {
					t.Errorf("OTel defaults = %+v", c)
				}
			},
		},
		{
			name: "settings with spaces around them",
			env: map[string]string{
				"RELIC_PROFILING_ENABLED":        "true ",
				"RELIC_METRICS_REPOSITORY_LABEL": " true",
				"OTEL_EXPORTER_OTLP_ENDPOINT":    "http://collector.example.com:4318/ ",
			},
			check: func(t *testing.T, c Config) {
				t.Helper()

				if !c.Pprof || !c.RepositoryLabel || c.TracesEndpoint != "http://collector.example.com:4318/v1/traces" {
					t.Errorf("config = %+v, want pprof, the repository label and the trimmed endpoint", c)
				}
			},
		},
		{
			name: "metrics off, pprof, repository label, debug text logs",
			env: map[string]string{
				"RELIC_METRICS_LISTEN_ADDRESS":   "off",
				"RELIC_PROFILING_ENABLED":        "true",
				"RELIC_METRICS_REPOSITORY_LABEL": "true",
				"RELIC_LOG_LEVEL":                "debug",
				"RELIC_LOG_FORMAT":               "text",
			},
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.MetricsAddr != "" || !c.Pprof || !c.RepositoryLabel || c.LogLevel != slog.LevelDebug ||
					c.LogFormat != "text" {
					t.Errorf("config = %+v", c)
				}
			},
		},
		{
			name: "the generic endpoint gets each signal's path",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://alloy:4318/",
				"OTEL_LOGS_EXPORTER":          "otlp",
				"OTEL_SERVICE_NAME":           "relic",
				"OTEL_TRACES_SAMPLER":         "parentbased_traceidratio",
				"OTEL_TRACES_SAMPLER_ARG":     "0.1",
			},
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TracesEndpoint != "http://alloy:4318/v1/traces" || c.LogsEndpoint != "http://alloy:4318/v1/logs" {
					t.Errorf("endpoints = %q, %q", c.TracesEndpoint, c.LogsEndpoint)
				}

				if c.ServiceName != "relic" || c.SamplerArg != 0.1 {
					t.Errorf("config = %+v", c)
				}
			},
		},
		{
			name: "a signal's own endpoint is used as it is",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT":        "http://a:4318",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://b/traces",
			},
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TracesEndpoint != "https://b/traces" || c.LogsEndpoint != "" {
					t.Errorf("endpoints = %q, %q", c.TracesEndpoint, c.LogsEndpoint)
				}
			},
		},
		{
			name: "OTEL_SDK_DISABLED turns both off",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://a:4318",
				"OTEL_LOGS_EXPORTER":          "otlp",
				"OTEL_SDK_DISABLED":           "true",
			},
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TracesEndpoint != "" || c.LogsEndpoint != "" {
					t.Errorf("endpoints = %q, %q", c.TracesEndpoint, c.LogsEndpoint)
				}
			},
		},
		{
			name: "OTEL_TRACES_EXPORTER=none",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://a:4318",
				"OTEL_TRACES_EXPORTER":        "none",
			},
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TracesEndpoint != "" {
					t.Errorf("TracesEndpoint = %q", c.TracesEndpoint)
				}
			},
		},
		{
			name: "gRPC is refused",
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
			},
			wantErr: "http/protobuf only",
		},
		{
			name: "OTLP logs need an endpoint",
			env: map[string]string{
				"OTEL_LOGS_EXPORTER": "otlp",
			},
			wantErr: "OTEL_LOGS_EXPORTER=otlp needs",
		},
		{
			name: "bad level",
			env: map[string]string{
				"RELIC_LOG_LEVEL": "loud",
			},
			wantErr: "RELIC_LOG_LEVEL",
		},
		{
			name: "bad format",
			env: map[string]string{
				"RELIC_LOG_FORMAT": "xml",
			},
			wantErr: "RELIC_LOG_FORMAT",
		},
		{
			name: "bad metrics address",
			env: map[string]string{
				"RELIC_METRICS_LISTEN_ADDRESS": "8091",
			},
			wantErr: "RELIC_METRICS_LISTEN_ADDRESS",
		},
		{
			name: "bad sampler",
			env: map[string]string{
				"OTEL_TRACES_SAMPLER": "sometimes",
			},
			wantErr: "OTEL_TRACES_SAMPLER",
		},
		{
			name: "bad ratio",
			env: map[string]string{
				"OTEL_TRACES_SAMPLER":     "traceidratio",
				"OTEL_TRACES_SAMPLER_ARG": "2",
			},
			wantErr: "OTEL_TRACES_SAMPLER_ARG",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := LoadConfig(envOf(tt.env), "server")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			tt.check(t, c)
		})
	}
}
