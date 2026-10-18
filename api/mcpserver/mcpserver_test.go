package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesAProxiedHostOnLoopback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(Handler(http.NotFoundHandler(), "test"))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		srv.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	req.Host = "relic.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("initialize with Host relic.example.com on a loopback listener = %d, want 200", resp.StatusCode)
	}
}
