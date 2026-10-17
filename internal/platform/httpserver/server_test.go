package httpserver

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
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

type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})

	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return &net.TCPAddr{
		IP: net.IPv4(127, 0, 0, 1),
	}
}

func TestServeDrainsRequestsWithinThePodGracePeriod(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		serverConn, clientConn := net.Pipe()

		ln := &pipeListener{
			conns:  make(chan net.Conn, 1),
			closed: make(chan struct{}),
		}
		ln.conns <- serverConn

		entered := make(chan struct{})
		mux := http.NewServeMux()
		mux.HandleFunc("GET /upload", func(w http.ResponseWriter, _ *http.Request) {
			close(entered)
			time.Sleep(100 * time.Second)

			_, _ = io.WriteString(w, "stored")
		})

		srv := NewServer("relic:8090", mux, slog.New(slog.DiscardHandler))
		ctx, cancel := context.WithCancel(t.Context())

		done := make(chan error, 1)
		go func() {
			done <- Serve(ctx, srv, ln)
		}()

		response := make(chan string, 1)

		go func() {
			_, _ = io.WriteString(clientConn, "GET /upload HTTP/1.1\r\nHost: relic\r\n\r\n")

			resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
			if err != nil {
				response <- err.Error()
				return
			}

			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			response <- string(body)
		}()

		<-entered
		cancel()

		if got := <-response; got != "stored" {
			t.Errorf("the request in flight at shutdown got %q, want its response", got)
		}

		if err := <-done; err != nil {
			t.Errorf("Serve() = %v, want nil once the request finished", err)
		}

		_ = clientConn.Close()
	})
}
