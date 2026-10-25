package relicapi

import (
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
)

func TestWriteHTTPErrorCaching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		code         connect.Code
		cacheControl string
	}{
		{
			name:         "a server failure is not cached",
			code:         connect.CodeInternal,
			cacheControl: "no-store",
		},
		{
			name:         "an unavailable upstream is not cached",
			code:         connect.CodeUnavailable,
			cacheControl: "no-store",
		},
		{
			name: "a missing file keeps the default",
			code: connect.CodeNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			writeHTTPError(rec, connect.NewError(tt.code, "failed"))

			if got := rec.Header().Get("Cache-Control"); got != tt.cacheControl {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cacheControl)
			}
		})
	}
}
