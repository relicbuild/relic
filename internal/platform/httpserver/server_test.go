package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeShutsDownOnCancel(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	entered := make(chan struct{})

	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		time.Sleep(200 * time.Millisecond)

		_, _ = io.WriteString(w, "slow")
	})

	srv := NewServer(ln.Addr().String(), mux, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, srv, ln)
	}()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/ok", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	slow := make(chan string, 1)

	go func() {
		req, err := http.NewRequestWithContext(
			context.WithoutCancel(t.Context()),
			http.MethodGet,
			"http://"+ln.Addr().String()+"/slow",
			nil,
		)
		if err != nil {
			slow <- err.Error()
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			slow <- err.Error()
			return
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		body, _ := io.ReadAll(resp.Body)
		slow <- string(body)
	}()

	<-entered
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Serve() = %v, want nil after cancel", err)
	}

	if got := <-slow; got != "slow" {
		t.Errorf("the request in flight at shutdown got %q, want its response", got)
	}
}
