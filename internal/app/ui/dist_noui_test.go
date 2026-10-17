//go:build noui

package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoUIBuildServesNoUI(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	Handler("/").ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
