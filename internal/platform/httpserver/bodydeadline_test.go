package httpserver

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBodyDeadline(t *testing.T) {
	t.Parallel()

	read := make(chan error, 2)
	srv := httptest.NewServer(bodyDeadline(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		read <- err
	}), 300*time.Millisecond, 1024))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL, "text/plain", strings.NewReader("a whole body")) //nolint:noctx // a test request
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if err := <-read; err != nil {
		t.Errorf("reading a prompt body = %v", err)
	}

	var dialer net.Dialer

	conn, err := dialer.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
	})

	if _, err := io.WriteString(
		conn,
		"PUT / HTTP/1.1\r\nHost: relic\r\nContent-Length: 100\r\n\r\nten bytes!",
	); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-read:
		if err == nil {
			t.Error("a stalled body was read to the end")
		}
	case <-time.After(10 * time.Second):
		t.Error("a stalled body held its handler for 10s")
	}
}
