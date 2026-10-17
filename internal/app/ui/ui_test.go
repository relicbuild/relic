package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	t.Parallel()

	root := fstest.MapFS{
		"index.html": {
			Data: []byte("<html>spa</html>"),
		},
		"_nuxt/app.js": {
			Data: []byte("console.log(1)"),
		},
		"_nuxt/sub/x.js": {
			Data: []byte("x"),
		},
	}
	tests := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{
			"/",
			http.StatusOK,
			"spa",
		},
		{
			"/_nuxt/app.js",
			http.StatusOK,
			"console.log(1)",
		},
		{
			"/artifacts/some/deep/route",
			http.StatusOK,
			"spa",
		},
		{
			"/_nuxt/sub",
			http.StatusOK,
			"spa",
		},
		{
			"/../../etc/passwd",
			http.StatusOK,
			"spa",
		},
	}
	h := handler("/", root)

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantCode || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf(
					"GET %s = %d %q, want %d containing %q",
					tt.path,
					rec.Code,
					rec.Body.String(),
					tt.wantCode,
					tt.wantBody,
				)
			}
		})
	}
}

func TestHandlerWithoutBuild(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	handler("/", fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestScriptHashes(t *testing.T) {
	t.Parallel()

	root := fstest.MapFS{
		"index.html": {
			Data: []byte(
				`<script type="importmap">{"imports":{}}</script><script type="module" src="/_nuxt/a.js"></script>` +
					`<script>window.__NUXT__={}</script><script type="application/json" id="__NUXT_DATA__">[1]</script>`,
			),
		},
		"200.html": {
			Data: []byte(`<script>window.__NUXT__={}</script>`),
		},
		"_nuxt/app.js": {
			Data: []byte(`"<script>not html</script>"`),
		},
	}
	got := scriptHashes(root)

	want := []string{
		"'sha256-9t9kOTpBXQ6UK6UoTUcY2ezm5KT7oB5pbv8ZFsTUBAU='",
		"'sha256-URrTy+Il/Nz0lHojVUx275hWqAWhkSF0VsHbUM4/6Hw='",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("scriptHashes = %v, want %v", got, want)
	}
}
