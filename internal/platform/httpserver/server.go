// Package httpserver provides the HTTP server lifecycle shared by Relic's APIs.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	maxHeaderBytes    = 64 << 10
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 30 * time.Second
	writeStallTimeout = 2 * time.Minute
)

// NewServer returns an http.Server with Relic's baseline limits.
// There is deliberately no global WriteTimeout: artifact streams can be long.
func NewServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           bodyDeadline(h, bodyReadGrace, minBodyReadRate),
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// Serve accepts connections on ln, over TLS when srv has a TLSConfig, until
// ctx is cancelled, then shuts srv down gracefully, waiting up to
// shutdownTimeout for in-flight requests.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errc := make(chan error, 1)

	go func() {
		if srv.TLSConfig != nil {
			errc <- srv.ServeTLS(ln, "", "")

			return
		}

		errc <- srv.Serve(ln)
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}
