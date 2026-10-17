//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

func TestRelicAPIRejectsUnknownValues(t *testing.T) {
	t.Parallel()
	srv := server(t)
	client := relicHTTP("admin", "password")

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{
			name:   "connect, known enum",
			method: "POST",
			path:   "/api/relic.v1.ScanService/ListFindings",
			body:   `{"severity":"SEVERITY_HIGH"}`,
			want:   200,
		},
		{
			name:   "connect, unknown enum",
			method: "POST",
			path:   "/api/relic.v1.ScanService/ListFindings",
			body:   `{"severity":"SEVERITY_BOGUS"}`,
			want:   400,
		},
		{
			name:   "connect, unknown field",
			method: "POST",
			path:   "/api/relic.v1.ScanService/ListFindings",
			body:   `{"sevrity":"SEVERITY_HIGH"}`,
			want:   400,
		},
		{
			name:   "connect, charset, unknown enum",
			method: "POST",
			path:   "/api/relic.v1.ScanService/ListFindings",
			body:   `{"severity":"SEVERITY_BOGUS"}`,
			want:   400,
		},
		{
			name:   "rest, known enum",
			method: "GET",
			path:   "/api/v1/findings?severity=SEVERITY_HIGH",
			want:   200,
		},
		{
			name:   "rest, unknown enum",
			method: "GET",
			path:   "/api/v1/findings?severity=SEVERITY_BOGUS",
			want:   400,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tt.method, srv.URL+tt.path, strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}

			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")

				if strings.Contains(tt.name, "charset") {
					req.Header.Set("Content-Type", "application/json; charset=utf-8")
				}
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}

			_ = resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, resp.StatusCode, tt.want)
			}
		})
	}
}
