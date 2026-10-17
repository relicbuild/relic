package relicapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRESTRoutes(t *testing.T) {
	t.Parallel()

	h := Handler(Dependencies{
		Version:  "1.2.3",
		Revision: "abc",
		Log:      slog.New(slog.DiscardHandler),
	})

	tests := []struct {
		name, method, path, body string
		want                     int
		contains                 string
	}{
		{
			name:     "REST public method",
			method:   http.MethodGet,
			path:     "/api/v1/version",
			want:     http.StatusOK,
			contains: `"version":"1.2.3"`,
		},
		{
			name:     "Connect public method",
			method:   http.MethodPost,
			path:     "/api/relic.v1.SystemService/GetVersion",
			body:     "{}",
			want:     http.StatusOK,
			contains: `"version":"1.2.3"`,
		},
		{
			name:   "REST admin method, anonymous",
			method: http.MethodGet,
			path:   "/api/v1/users",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "Connect admin method, anonymous",
			method: http.MethodPost,
			path:   "/api/relic.v1.SecurityService/ListUsers",
			body:   "{}",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "REST authenticated method with a path, anonymous",
			method: http.MethodGet,
			path:   "/api/v1/repositories/libs/items/org/app/1.0/app.jar",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "unknown REST route",
			method: http.MethodGet,
			path:   "/api/v1/nope",
			want:   http.StatusNotFound,
		},
		{
			name:   "wrong verb",
			method: http.MethodDelete,
			path:   "/api/v1/version",
			want:   http.StatusMethodNotAllowed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			b, _ := io.ReadAll(rec.Body)
			if rec.Code != tt.want || !strings.Contains(string(b), tt.contains) {
				t.Errorf("%s %s = %d %s, want %d containing %q", tt.method, tt.path, rec.Code, b, tt.want, tt.contains)
			}
		})
	}
}
