package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/net/netutil"
)

var propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

// Options are what Setup needs besides the environment.
type Options struct {
	Getenv       func(string) string
	Stderr       io.Writer
	Component    string
	Version      string
	Revision     string
	SpanExporter sdktrace.SpanExporter
}

// Telemetry is the running telemetry of one process.
type Telemetry struct {
	Config   Config
	Logger   *slog.Logger
	Tracer   trace.Tracer
	shutdown []func(context.Context) error
}

// Setup reads the configuration, builds the logger, installs the global
// tracer provider and propagators, and sets the build_info series.
func Setup(ctx context.Context, o Options) (*Telemetry, error) {
	cfg, err := LoadConfig(o.Getenv, o.Component)
	if err != nil {
		return nil, err
	}

	t := &Telemetry{
		Config: cfg,
	}

	otel.SetTextMapPropagator(propagator)
	SetRepositoryLabel(cfg.RepositoryLabel)
	buildInfo.Reset()
	buildInfo.WithLabelValues(o.Version, o.Revision, o.Component).Set(1)

	handler := newLogHandler(cfg, o.Stderr)

	tracing := cfg.TracesEndpoint != "" || o.SpanExporter != nil
	if tracing || cfg.LogsEndpoint != "" {
		instance, _ := os.Hostname()

		res, err := newResource(ctx, cfg, o.Component, o.Version, instance)
		if err != nil {
			return nil, err
		}

		if tracing {
			tp, err := newTracerProvider(ctx, cfg, res, o.SpanExporter)
			if err != nil {
				return nil, err
			}

			otel.SetTracerProvider(tp)
			t.shutdown = append(t.shutdown, tp.Shutdown)
		}

		if cfg.LogsEndpoint != "" {
			lp, err := newLoggerProvider(ctx, cfg, res)
			if err != nil {
				return nil, errors.Join(err, t.Shutdown(ctx))
			}

			t.shutdown = append(t.shutdown, lp.Shutdown)
			bridge := leveled{
				otelslog.NewHandler(tracerName, otelslog.WithLoggerProvider(lp)),
				cfg.LogLevel,
			}
			handler = slog.NewMultiHandler(handler, bridge)
		}
	}

	if !tracing {
		otel.SetTracerProvider(noop.NewTracerProvider())
	}

	t.Tracer = otel.Tracer(tracerName)
	t.Logger = slog.New(handler)
	diskLog.Store(t.Logger)

	return t, nil
}

// Shutdown flushes pending spans and log records.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var errs []error
	for _, v := range slices.Backward(t.shutdown) {
		errs = append(errs, v(ctx))
	}

	t.shutdown = nil

	return errors.Join(errs...)
}

// MetricsHandler serves the registry in the Prometheus text format, or
// OpenMetrics (which carries exemplars) when the scraper asks for it.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Registry:          registry,
	})
}

func metricsMux(pprofOn bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", MetricsHandler())

	if pprofOn {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	return mux
}

const (
	metricsMaxConnections = 64
	metricsRequestTimeout = 2 * time.Minute
)

func newMetricsServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       metricsRequestTimeout,
		WriteTimeout:      metricsRequestTimeout,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// ServeMetrics listens on the metrics address and serves until ctx is
// cancelled.
func (t *Telemetry) ServeMetrics(ctx context.Context) (net.Addr, error) {
	if t.Config.MetricsAddr == "" {
		return nil, nil
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", t.Config.MetricsAddr)
	if err != nil {
		return nil, fmt.Errorf("metrics listener: %w", err)
	}

	ln = netutil.LimitListener(ln, metricsMaxConnections)
	srv := newMetricsServer(metricsMux(t.Config.Pprof), t.Logger)

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logger.ErrorContext(ctx, "metrics listener stopped", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		_ = srv.Shutdown(shutdownCtx)
	}()

	return ln.Addr(), nil
}
