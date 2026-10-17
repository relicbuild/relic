//go:build integration

package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestStorageRepositoryNamesAreReservedForTheirProject(t *testing.T) {
	t.Parallel()
	srv := server(t)
	send := func(c call) (reply, string) {
		return do(t, srv, c)
	}
	admin := rest{
		t:   t,
		srv: send,
	}
	dev := rest{
		t:    t,
		srv:  send,
		user: "dev:Dev-pass-12345",
	}

	admin.call(
		http.MethodPut,
		"/api/v1/users/dev",
		`{"name":"dev","email":"dev@example.com","password":"Dev-pass-12345"}`,
		http.StatusOK,
	)
	admin.call(
		http.MethodPut,
		"/api/v1/projects/alpha",
		`{"key":"alpha","name":"Alpha","members":[{"name":"dev","role":"PROJECT_ROLE_ADMIN"}]}`,
		http.StatusOK,
	)

	tests := []struct {
		name       string
		repository string
		format     string
	}{
		{
			name:       "another project's build info",
			repository: "beta-build-info",
			format:     "BUILD",
		},
		{
			name:       "another project's releases",
			repository: "beta-release-bundles-v2",
			format:     "RELEASE",
		},
		{
			name:       "releases outside projects",
			repository: "release-bundles-v2",
			format:     "GENERIC",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev.t = t
			dev.call(
				http.MethodPost,
				"/api/v1/repositories",
				`{"name":"`+tt.repository+`","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_`+tt.format+
					`","project":"alpha"}`,
				http.StatusBadRequest,
			)
		})
	}

	dev.t = t
	dev.call(
		http.MethodPost,
		"/api/v1/repositories",
		`{"name":"alpha-build-info","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_BUILD","project":"alpha"}`,
		http.StatusOK,
	)

	run := `{"project":"beta","run":{"name":"app","number":"1","started":"2026-09-25T10:00:00Z"}}`
	admin.call(http.MethodPost, "/api/v1/builds/app/runs", run, http.StatusBadRequest)
	admin.call(http.MethodGet, "/api/v1/repositories/beta-build-info", "", http.StatusNotFound)

	admin.call(http.MethodPut, "/api/v1/projects/beta", `{"key":"beta","name":"Beta"}`, http.StatusOK)
	admin.call(http.MethodPost, "/api/v1/builds/app/runs", run, http.StatusOK)

	if got := admin.call(
		http.MethodGet,
		"/api/v1/repositories/beta-build-info",
		"",
		http.StatusOK,
	); !strings.Contains(got, `"project":"beta"`) {
		t.Errorf("beta's build-info repository = %s", got)
	}
}
