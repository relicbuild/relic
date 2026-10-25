package relicapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/localdisk"
	"relic/internal/platform/objectstore"
)

func TestWriteHTTPError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    *connect.Error
		status int
		body   string
	}{
		{
			name:   "a failed precondition is a bad request, as over REST",
			err:    codeOf(fmt.Errorf("delete: %w", repository.ErrHeld)),
			status: http.StatusBadRequest,
			body:   `{"code":9,"message":"delete: the item is under a legal hold","details":[]}`,
		},
		{
			name:   "a missing repository",
			err:    connect.NewError(connect.CodeNotFound, "repository not found"),
			status: http.StatusNotFound,
			body:   `{"code":5,"message":"repository not found","details":[]}`,
		},
		{
			name:   "an unavailable upstream",
			err:    codeOf(fmt.Errorf("%w: connection refused", repository.ErrUpstream)),
			status: http.StatusServiceUnavailable,
			body:   `{"code":14,"message":"upstream error: connection refused","details":[]}`,
		},
		{
			name:   "an upload over the size limit",
			err:    codeOf(repository.ErrTooLarge),
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name:   "full storage",
			err:    codeOf(fmt.Errorf("put: %w", objectstore.ErrStorageFull)),
			status: http.StatusInsufficientStorage,
			body:   `{"code":8,"message":"object storage is full","details":[]}`,
		},
		{
			name:   "a full database disk",
			err:    codeOf(fmt.Errorf("commit: %w: database or disk is full (13)", localdisk.ErrFull)),
			status: http.StatusInsufficientStorage,
			body:   `{"code":8,"message":"the disk is full","details":[]}`,
		},
		{
			name:   "a server failure",
			err:    codeOf(fmt.Errorf("boom")),
			status: http.StatusInternalServerError,
			body:   `{"code":13,"message":"internal error","details":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			writeHTTPError(rec, tt.err)

			if rec.Code != tt.status || rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf(
					"response = %d %q, want %d application/json",
					rec.Code,
					rec.Header().Get("Content-Type"),
					tt.status,
				)
			}

			if tt.body != "" && rec.Body.String() != tt.body {
				t.Errorf("body = %s, want %s", rec.Body, tt.body)
			}
		})
	}
}

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

func TestFileRoutesRefuseOtherMethods(t *testing.T) {
	t.Parallel()

	h := Handler(Dependencies{
		Log: slog.New(slog.DiscardHandler),
	})

	for _, path := range []string{
		FilesPath + "libs-local/a.txt",
		SBOMPath + "libs-local/a.txt",
		ArchivePath + "libs-local/a.zip?entry=x",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(
				auth.WithPrincipal(t.Context(), auth.Principal{
					Username: "alice",
				}),
				http.MethodDelete,
				path,
				nil,
			)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" ||
				rec.Body.String() != `{"code":12,"message":"method not allowed","details":[]}` {
				t.Errorf("DELETE %s = %d Allow %q %s", path, rec.Code, rec.Header().Get("Allow"), rec.Body)
			}
		})
	}
}
