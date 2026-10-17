package artifactory

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteTag(t *testing.T) {
	t.Parallel()

	for pattern, want := range map[string]string{
		"/artifactory/api/repositories": "Artifactory: repositories",
		"/artifactory/{repo}/{path...}": "Artifactory: files",
		"/v2/{name...}":                 "Docker registry",
		"/xray/api/v1/system/ping":      "Xray",
		"/onemodel/api/v1/graphql":      "Evidence",
		"/api/v1/files/{repo}":          "",
	} {
		if got := RouteTag(pattern); got != want {
			t.Errorf("RouteTag(%q) = %q, want %q", pattern, got, want)
		}
	}
}

func TestUIRedirect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path, want string
	}{
		{
			"/ui/",
			"/",
		},
		{
			"/ui/login?jfClientSession=abc&jfClientName=JFrog-CLI&jfClientCode=1",
			"/login?cliName=JFrog-CLI&cliSession=abc",
		},
		{
			"/ui/repositories/libs",
			"/repositories/libs",
		},
		{
			"/ui/builds?page=2",
			"/builds?page=2",
		},
		{
			"/ui/builds/app/7/1792254834424/published?buildRepo=artifactory-build-info",
			"/builds/app/7",
		},
		{
			"/ui/builds/team%2Fapp/7/1792254834424/published?buildRepo=ci-build-info&projectKey=ci",
			"/builds/team%2Fapp/7",
		},
		{
			"/ui/builds/app/7",
			"/builds/app/7",
		},
		{
			"/ui//evil.example",
			"/evil.example",
		},
		{
			`/ui/\evil.example`,
			"/evil.example",
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			uiRedirect(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))

			if got := rec.Header().Get("Location"); rec.Code != http.StatusMovedPermanently || got != tt.want {
				t.Errorf("%d Location = %q, want %q", rec.Code, got, tt.want)
			}
		})
	}
}
