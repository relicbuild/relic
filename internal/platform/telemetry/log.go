package telemetry

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

func newLogHandler(cfg Config, w io.Writer) slog.Handler {
	opts := &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}

	var h slog.Handler = slog.NewJSONHandler(w, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(w, opts)
	}

	return traceHandler{
		h,
	}
}

type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}

	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{
		h.Handler.WithAttrs(attrs),
	}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{
		h.Handler.WithGroup(name),
	}
}

type leveled struct {
	slog.Handler
	level slog.Level
}

func (h leveled) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level && h.Handler.Enabled(ctx, l)
}

func (h leveled) WithAttrs(attrs []slog.Attr) slog.Handler {
	return leveled{
		h.Handler.WithAttrs(attrs),
		h.level,
	}
}

func (h leveled) WithGroup(name string) slog.Handler {
	return leveled{
		h.Handler.WithGroup(name),
		h.level,
	}
}
