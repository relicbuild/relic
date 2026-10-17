package repository

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type freezer struct {
	ln     net.Listener
	target string

	mu     sync.Mutex
	frozen []*atomic.Bool
	dials  atomic.Int32
}

func newFreezer(t *testing.T, target string) *freezer {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	f := &freezer{
		ln:     ln,
		target: target,
	}

	t.Cleanup(func() {
		_ = ln.Close()
	})

	go f.serve()

	return f
}

func (f *freezer) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}

		f.dials.Add(1)

		up, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", f.target)
		if err != nil {
			_ = c.Close()

			continue
		}

		frozen := &atomic.Bool{}

		f.mu.Lock()
		f.frozen = append(f.frozen, frozen)
		f.mu.Unlock()

		pipe := func(dst, src net.Conn) {
			buf := make([]byte, 32<<10)

			for {
				n, err := src.Read(buf)
				if n > 0 && !frozen.Load() {
					if _, err := dst.Write(buf[:n]); err != nil {
						return
					}
				}

				if err != nil {
					_ = dst.Close()

					return
				}
			}
		}

		go pipe(up, c)
		go pipe(c, up)
	}
}

func (f *freezer) freeze() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, b := range f.frozen {
		b.Store(true)
	}
}

func TestUpstreamRecoversFromStalledConnection(t *testing.T) {
	t.Parallel()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	proxy := newFreezer(t, srv.Listener.Addr().String())

	base := srv.Client().Transport.(*http.Transport).Clone()
	base.TLSClientConfig.ServerName = "example.com"
	base.TLSClientConfig.NextProtos = []string{
		"h2",
		"http/1.1",
	}
	tr := upstreamTransportFrom(base, time.Second, true)

	if base.TLSClientConfig.NextProtos[0] != "h2" {
		t.Errorf("the base transport's protocols changed: %v", base.TLSClientConfig.NextProtos)
	}

	client := &http.Client{
		Transport: retryLostConnection{
			next: tr,
		},
	}
	url := "https://" + proxy.ln.Addr().String() + "/"

	get := func() error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			return err
		}

		resp, err := client.Do(req)
		if err != nil {
			return err
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		if resp.ProtoMajor != 1 {
			t.Errorf("protocol %s, want HTTP/1.1", resp.Proto)
		}

		_, err = io.ReadAll(resp.Body)

		return err
	}

	if err := get(); err != nil {
		t.Fatal(err)
	}

	proxy.freeze()

	start := time.Now()

	if err := get(); err != nil {
		t.Fatalf("request after the stall: %v (dials %d)", err, proxy.dials.Load())
	}

	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s, want about one timeout", d)
	}

	if n := proxy.dials.Load(); n != 2 {
		t.Errorf("dials = %d, want 2", n)
	}
}
