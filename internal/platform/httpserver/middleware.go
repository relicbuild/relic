package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"relic/internal/access/auth"
	"relic/internal/platform/httperror"
	"relic/internal/platform/telemetry"
)

type requestIDKey struct{}

// RequestID returns the request ID stored by the Observe middleware.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

var secretQuery = regexp.MustCompile(`(?i)((?:access_)?token|password|api_?key|sig)=[^&]*`)

var secretPath = regexp.MustCompile(`(/-/user/token/)[^/]*`)

// Observe assigns a request ID, recovers panics, logs one line per request,
// warns of a request still running after slowRequestAfter, and fails a
// response write that makes no progress for writeStallTimeout.
func Observe(log *slog.Logger, next http.Handler) http.Handler {
	return observe(log, next, writeStallTimeout, slowRequestAfter)
}

func observe(log *slog.Logger, next http.Handler, stall, slow time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 {
			var b [8]byte

			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}

		w.Header().Set("X-Request-Id", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")

		ctx, principal := auth.NewSlot(context.WithValue(r.Context(), requestIDKey{}, id))

		path := secretPath.ReplaceAllString(r.URL.Path, "${1}REDACTED")
		if path != r.URL.Path {
			telemetry.RedactURLPath(ctx, path)
		}

		pattern := new(string)
		ctx = context.WithValue(ctx, routeKey{}, pattern)
		rec := &statusRecorder{
			ResponseWriter: w,
			controller:     http.NewResponseController(w),
			stall:          stall,
		}
		_ = rec.controller.SetWriteDeadline(time.Time{})

		defer rec.extendWriteDeadline()

		req := r.WithContext(ctx)

		body := &countingBody{
			ReadCloser: r.Body,
		}
		if r.Body != nil && r.Body != http.NoBody {
			req.Body = body
		}
		defer telemetry.HTTPStarted(r.URL.Path)()

		slowWarning := time.AfterFunc(slow, func() {
			telemetry.HTTPSlow(r.URL.Path)
			log.WarnContext(ctx, "slow request",
				"request_id", id,
				"method", r.Method,
				"path", path,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"remote_addr", r.RemoteAddr,
			)
		})
		defer slowWarning.Stop()

		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, not a wrapped error
					panic(v)
				}

				log.ErrorContext(ctx, "panic", "request_id", id, "panic", v, "stack", string(debug.Stack()))

				if rec.status == 0 {
					httperror.Write(rec, r, http.StatusInternalServerError, "internal server error")
				}
			}

			log.InfoContext(ctx, "request",
				"request_id", id,
				"method", r.Method,
				"path", path,
				"query", secretQuery.ReplaceAllString(r.URL.RawQuery, "${1}=REDACTED"),
				"status", rec.code(),
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"user", principal.Name(),
				"remote_addr", r.RemoteAddr,
			)
			telemetry.ObserveHTTP(ctx, telemetry.HTTPRequest{
				Path:          r.URL.Path,
				Pattern:       *pattern,
				Method:        r.Method,
				Status:        rec.code(),
				Duration:      time.Since(start),
				RequestBytes:  body.n,
				ResponseBytes: rec.bytes,
			})
		}()

		next.ServeHTTP(rec, req)
	})
}

type routeKey struct{}

func routed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := r.Context().Value(routeKey{}).(*string); ok {
			*p = r.Pattern
		}

		telemetry.RouteMatched(r.Context(), r.Pattern)
		h.ServeHTTP(w, r)
	})
}

type countingBody struct {
	io.ReadCloser
	n int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)

	return n, err
}

type statusRecorder struct {
	http.ResponseWriter
	controller *http.ResponseController
	stall      time.Duration
	status     int
	bytes      int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}

	s.extendWriteDeadline()
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}

	s.extendWriteDeadline()
	n, err := s.ResponseWriter.Write(p)
	s.bytes += int64(n)

	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) extendWriteDeadline() {
	if s.controller != nil {
		_ = s.controller.SetWriteDeadline(time.Now().Add(s.stall))
	}
}

func (s *statusRecorder) code() int {
	if s.status == 0 {
		return http.StatusOK
	}

	return s.status
}
