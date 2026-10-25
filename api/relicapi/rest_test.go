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

func TestRESTResponseLeavesOutUnsetFields(t *testing.T) {
	t.Parallel()

	h := Handler(Dependencies{
		Version: "1.2.3",
		Log:     slog.New(slog.DiscardHandler),
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/version", nil))

	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `{"version":"1.2.3"}` {
		t.Errorf("GET /api/v1/version = %d %s, want 200 {\"version\":\"1.2.3\"}", rec.Code, got)
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

func TestMultiSegmentPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, method, path, want string
	}{
		{
			name:   "an item path's encoded slashes are decoded",
			method: http.MethodGet,
			path:   "/api/v1/repositories/libs-local/details/org%2Fapp%2F1.0%2Fapp.jar",
			want:   "/api/v1/repositories/libs-local/details/org/app/1.0/app.jar",
		},
		{
			name:   "lower-case escapes too",
			method: http.MethodDelete,
			path:   "/api/v1/repositories/libs-local/items/org%2fa.txt",
			want:   "/api/v1/repositories/libs-local/items/org/a.txt",
		},
		{
			name:   "a custom verb after the path stays",
			method: http.MethodPost,
			path:   "/api/v1/repositories/docker-local/images/team%2Fapp:promote",
			want:   "/api/v1/repositories/docker-local/images/team/app:promote",
		},
		{
			name:   "other escapes stay escaped",
			method: http.MethodGet,
			path:   "/api/v1/repositories/libs-local/items/a%2Fb%20c%25d",
			want:   "/api/v1/repositories/libs-local/items/a/b%20c%25d",
		},
		{
			name:   "a single-segment name keeps its encoded slash",
			method: http.MethodGet,
			path:   "/api/v1/builds/team%2Fapp/runs",
			want:   "/api/v1/builds/team%2Fapp/runs",
		},
		{
			name:   "the repository segment keeps its encoded slash",
			method: http.MethodGet,
			path:   "/api/v1/repositories/libs%2Flocal",
			want:   "/api/v1/repositories/libs%2Flocal",
		},
		{
			name:   "another method on the same path is left alone",
			method: http.MethodPut,
			path:   "/api/v1/repositories/libs-local/details/org%2Fa.txt",
			want:   "/api/v1/repositories/libs-local/details/org%2Fa.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var seen string

			h := multiSegmentPaths(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = r.URL.EscapedPath()
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))

			if seen != tt.want {
				t.Errorf("%s %s reached the transcoder as %s, want %s", tt.method, tt.path, seen, tt.want)
			}
		})
	}
}
