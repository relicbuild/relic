package packages

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relic/internal/platform/localdisk"
	"relic/internal/platform/objectstore"
)

func TestHandler_writeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{
			name:    "a full object store",
			err:     fmt.Errorf("put: %w", objectstore.ErrStorageFull),
			status:  http.StatusInsufficientStorage,
			message: "object storage is full",
		},
		{
			name:    "a full database disk",
			err:     fmt.Errorf("commit: %w: database or disk is full (13)", localdisk.ErrFull),
			status:  http.StatusInsufficientStorage,
			message: "the disk is full",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &Handler{
				log: slog.New(slog.DiscardHandler),
			}
			rec := httptest.NewRecorder()
			h.writeError(
				rec,
				httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/npm/npm-local/a", nil),
				tt.err,
			)

			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.message) {
				t.Errorf("writeError(%v) = %d %s, want %d %q", tt.err, rec.Code, rec.Body, tt.status, tt.message)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]any{
		"name": "left-pad",
		"versions": map[string]string{
			"1.0.0": "a",
		},
	})

	if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("status = %d, content type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	if want := `{"name":"left-pad","versions":{"1.0.0":"a"}}` + "\n"; rec.Body.String() != want {
		t.Errorf("body = %q, want compact %q", rec.Body.String(), want)
	}
}
