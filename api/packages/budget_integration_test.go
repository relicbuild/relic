//go:build integration

package packages

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestBufferedRequestsWaitForTheBudget(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)

	repos := repository.NewService(db, objectstoretest.NewMemory(), slog.New(slog.DiscardHandler))
	if err := repos.CreateRepository(t.Context(), repository.Repository{
		Name:   "npm-local",
		Class:  repository.ClassLocal,
		Format: npm.Format,
	}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		repos:   repos,
		npm:     npm.NewService(db, repos, slog.New(slog.DiscardHandler)),
		buffers: newBufferPool(1 << 20),
		log:     slog.New(slog.DiscardHandler),
	}
	if !h.buffers.held.TryAcquire(1 << 20) {
		t.Fatal("the budget is not free")
	}

	tests := []struct {
		name  string
		serve func(w http.ResponseWriter, r *http.Request, req npmRequest)
	}{
		{
			name: "npm unpublish of some versions",
			serve: func(w http.ResponseWriter, r *http.Request, req npmRequest) {
				h.npmUnpublish(w, r, req, "pkg")
			},
		},
		{
			name: "npm audit",
			serve: func(w http.ResponseWriter, r *http.Request, req npmRequest) {
				h.npmAudit(w, r, req, "-/npm/v1/security/advisories/bulk")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()

			ctx = auth.WithPrincipal(ctx, auth.Principal{
				Username: "admin",
				Admin:    true,
			})
			body := `{"name":"pkg","versions":{"1.0.0":{}}}`
			r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/", io.NopCloser(strings.NewReader(body)))
			w := httptest.NewRecorder()

			tt.serve(w, r, npmRequest{
				repo: "npm-local",
			})

			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "server busy") {
				t.Errorf("a buffered request on a spent budget answered %d %q, want 503 server busy", w.Code, w.Body)
			}
		})
	}
}
