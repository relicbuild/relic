package sdk

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestExclusionsAreRoutes(t *testing.T) {
	t.Parallel()

	routes, err := artifactoryRoutes()
	if err != nil {
		t.Fatal(err)
	}

	if len(routes) < 50 {
		t.Fatalf("only %d Artifactory routes in the API description", len(routes))
	}

	for r, why := range excluded {
		if !slices.Contains(routes, r) || why == "" {
			t.Errorf("exclusion %q: not a route, or no reason", r)
		}
	}
}

func TestUncovered(t *testing.T) {
	t.Parallel()

	routes := []string{
		"GET /artifactory/api/storage/{path}",
		"GET /artifactory/api/system/ping",
		"PUT /artifactory/api/storage/{path}",
	}

	got := uncovered(routes, []*http.Request{
		httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"/artifactory/api/storage/libs/a/b.txt?properties",
			nil,
		),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/artifactory/api/system/ping", nil),
	})
	if !slices.Equal(got, []string{
		"PUT /artifactory/api/storage/{path}",
	}) {
		t.Errorf("uncovered = %v", got)
	}
}
