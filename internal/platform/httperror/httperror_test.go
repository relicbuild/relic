package httperror

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	t.Parallel()
	Register("/legacy/", func(w http.ResponseWriter, status int, message string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"legacy": "` + message + `"}`))
	})

	tests := []struct {
		path, want string
	}{
		{
			"/legacy/api/repositories",
			`"legacy": "Bad credentials"`,
		},
		{
			"/elsewhere",
			`"code": "unauthenticated"`,
		}, // no mount registered: Relic's shape
		{
			"/api/relic.v1.SystemService/GetSystemStatus",
			`"code": "unauthenticated"`,
		},
		{
			"/api/v1/files/libs/a.txt",
			`"code": "unauthenticated"`,
		},
		{
			"/api/v1/archive/libs/a.zip",
			`"code": "unauthenticated"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			Write(
				rec,
				httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil),
				http.StatusUnauthorized,
				"Bad credentials",
			)

			if rec.Code != 401 || !strings.Contains(rec.Body.String(), tt.want) ||
				!strings.Contains(rec.Body.String(), "Bad credentials") {
				t.Errorf("%s = %d %s, want %s", tt.path, rec.Code, rec.Body, tt.want)
			}
		})
	}

	for status, want := range map[int]string{
		http.StatusMethodNotAllowed:             "unimplemented",
		http.StatusConflict:                     "already_exists",
		http.StatusUnprocessableEntity:          "invalid_argument",
		http.StatusServiceUnavailable:           "unavailable",
		http.StatusRequestEntityTooLarge:        "out_of_range",
		http.StatusRequestedRangeNotSatisfiable: "out_of_range",
		http.StatusTeapot:                       "internal",
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			Write(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/pypi/libs/", nil), status, "no")

			if rec.Code != status || !strings.Contains(rec.Body.String(), `"code": "`+want+`"`) {
				t.Errorf("status %d = %d %s, want that status with code %s", status, rec.Code, rec.Body, want)
			}
		})
	}
}
