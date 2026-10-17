//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestProductsNotProvided(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for path, product := range map[string]string{
		"/pipelines/api/v1/system/info":    "JFrog Pipelines",
		"/worker/api/v1/workers":           "JFrog Workers",
		"/apptrust/api/v1/system/ping":     "JFrog AppTrust",
		"/mc/api/v1/jpds/x":                "JFrog Mission Control",
		"/distribution/api/v1/system/info": "JFrog Distribution",
	} {
		for _, method := range []string{
			"GET",
			"DELETE",
		} {
			resp, body := do(t, srv, call{
				method: method,
				path:   path,
			})
			if resp.StatusCode != 501 || !strings.Contains(body, "Relic does not provide "+product) ||
				!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
				t.Errorf("%s %s = %d %s %q", method, path, resp.StatusCode, resp.Header.Get("Content-Type"), body)
			}
		}
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/pipelines/api/v1/system/info",
		noAuth: true,
	}); resp.StatusCode != 401 {
		t.Errorf("anonymous = %d, want 401", resp.StatusCode)
	}

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/repositories",
		noAuth: true,
	}); resp.StatusCode == 501 || strings.Contains(body, "does not provide") {
		t.Errorf("the UI's path answered %d %s", resp.StatusCode, body)
	}
}
