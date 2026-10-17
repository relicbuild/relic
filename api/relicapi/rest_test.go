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
			name:   "REST route with an encoded slash in a name, anonymous",
			method: http.MethodGet,
			path:   "/api/v1/builds/team%2Fapp/runs",
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
		{
			name:     "REST unknown query parameter",
			method:   http.MethodGet,
			path:     "/api/v1/version?bogus=1",
			want:     http.StatusBadRequest,
			contains: `"code":3,`,
		},
		{
			name:     "REST body on a method that takes none",
			method:   http.MethodGet,
			path:     "/api/v1/version",
			body:     "{}",
			want:     http.StatusBadRequest,
			contains: `"code":3,`,
		},
		{
			name:     "REST truncated JSON body",
			method:   http.MethodPost,
			path:     "/api/v1/sessions",
			body:     `{"username":`,
			want:     http.StatusBadRequest,
			contains: `"code":3,`,
		},
		{
			name:     "REST unknown JSON body field",
			method:   http.MethodPost,
			path:     "/api/v1/sessions",
			body:     `{"usernam":"alice"}`,
			want:     http.StatusBadRequest,
			contains: `"code":3,`,
		},
		{
			name:     "REST truncated JSON body of a field",
			method:   http.MethodPost,
			path:     "/api/v1/repositories",
			body:     `{"name":`,
			want:     http.StatusBadRequest,
			contains: `"code":3,`,
		},
		{
			name:     "REST unknown JSON field in the body of a field",
			method:   http.MethodPost,
			path:     "/api/v1/repositories",
			body:     `{"nam":"libs"}`,
			want:     http.StatusBadRequest,
			contains: `"code":3,`,
		},
		{
			name:     "Connect truncated JSON body",
			method:   http.MethodPost,
			path:     "/api/relic.v1.SessionService/Login",
			body:     `{"username":`,
			want:     http.StatusBadRequest,
			contains: `"code":"invalid_argument"`,
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

func TestRESTDecodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, path, body string
		status           int
		want             int
		wantBody         string
	}{
		{
			name:     "transcoder decode failure",
			path:     "/api/v1/version",
			status:   http.StatusInternalServerError,
			body:     `{"code":2,"message":"proto: unexpected EOF","details":[]}`,
			want:     http.StatusBadRequest,
			wantBody: `{"code":3,"message":"proto: unexpected EOF","details":[]}`,
		},
		{
			name:     "internal error",
			path:     "/api/v1/version",
			status:   http.StatusInternalServerError,
			body:     `{"code":13,"message":"internal error","details":[]}`,
			want:     http.StatusInternalServerError,
			wantBody: `{"code":13,"message":"internal error","details":[]}`,
		},
		{
			name:     "success",
			path:     "/api/v1/version",
			status:   http.StatusOK,
			body:     `{"version":"1.2.3"}`,
			want:     http.StatusOK,
			wantBody: `{"version":"1.2.3"}`,
		},
		{
			name:     "Connect path",
			path:     "/api/relic.v1.SystemService/GetVersion",
			status:   http.StatusInternalServerError,
			body:     `{"code":"unknown","message":"x"}`,
			want:     http.StatusInternalServerError,
			wantBody: `{"code":"unknown","message":"x"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := restDecodeErrors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))

			if rec.Code != tt.want || rec.Body.String() != tt.wantBody {
				t.Errorf("response = %d %s, want %d %s", rec.Code, rec.Body, tt.want, tt.wantBody)
			}
		})
	}
}
