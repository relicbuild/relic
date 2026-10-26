package relicapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestServeFileAnswersRangesAndConditions(t *testing.T) {
	t.Parallel()

	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name   string
		header http.Header
		status int
		body   string
	}{
		{
			name:   "the whole file",
			header: http.Header{},
			status: http.StatusOK,
			body:   "abcdefgh",
		},
		{
			name: "a range",
			header: http.Header{
				"Range": {
					"bytes=2-4",
				},
			},
			status: http.StatusPartialContent,
			body:   "cde",
		},
		{
			name: "a matching ETag",
			header: http.Header{
				"If-None-Match": {
					`"etag"`,
				},
			},
			status: http.StatusNotModified,
		},
		{
			name: "unmodified since",
			header: http.Header{
				"If-Modified-Since": {
					modified.Format(http.TimeFormat),
				},
			},
			status: http.StatusNotModified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/f", nil)
			req.Header = tt.header
			rec := httptest.NewRecorder()
			rec.Header().Set("ETag", `"etag"`)

			serveFile(rec, req, "f.bin", modified, strings.NewReader("abcdefgh"))

			if rec.Code != tt.status || rec.Body.String() != tt.body {
				t.Errorf("= %d %q, want %d %q", rec.Code, rec.Body, tt.status, tt.body)
			}
		})
	}
}

type discardResponse struct {
	header http.Header
}

func (d discardResponse) Header() http.Header { return d.header }

func (discardResponse) Write(b []byte) (int, error) { return len(b), nil }

func (discardResponse) WriteHeader(int) {}

//nolint:paralleltest // counts the bytes the process allocates
func TestServeFileReusesItsCopyBuffer(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 1<<20)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/f", nil)
	w := discardResponse{
		header: http.Header{},
	}

	serve := func() {
		clear(w.header)
		serveFile(w, req, "f.bin", time.Time{}, bytes.NewReader(content))
	}

	serve()

	const runs = 50

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	for range runs {
		serve()
	}

	runtime.ReadMemStats(&after)

	if perRun := (after.TotalAlloc - before.TotalAlloc) / runs; perRun > maxDownloadAllocation {
		t.Errorf("a download allocates %d bytes, want the copy buffer reused", perRun)
	}
}
