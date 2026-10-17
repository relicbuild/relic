//go:build integration

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"relic/api/adapters/artifactory/layout"
	"relic/internal/platform/database"
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

	defaultStorage := []struct {
		name       string
		repository string
		format     string
	}{
		{
			name:       "build info in the default project",
			repository: layout.BuildInfo.Repository(""),
			format:     "BUILD",
		},
		{
			name:       "releases in the default project",
			repository: "release-bundles-v2",
			format:     "RELEASE",
		},
	}
	for _, tt := range defaultStorage {
		t.Run(tt.name, func(t *testing.T) {
			admin := rest{
				t:   t,
				srv: send,
			}
			body := `{"name":"` + tt.repository + `","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_` + tt.format +
				`","project":"default","description":"%s"}`
			admin.call(http.MethodPost, "/api/v1/repositories", fmt.Sprintf(body, "created"), http.StatusOK)
			admin.call(
				http.MethodPut,
				"/api/v1/repositories/"+tt.repository,
				fmt.Sprintf(body, "updated"),
				http.StatusOK,
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

func TestDashboardSummaryCountsWhatTheCallerMayRead(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	send := func(c call) (reply, string) {
		return do(t, srv, c)
	}
	admin := rest{
		t:   t,
		srv: send,
	}
	nobody := rest{
		t:    t,
		srv:  send,
		user: "nobody:Nobody-pass-12345",
	}

	createRepository(t, srv, "dash-local", "LOCAL", "GENERIC", "")
	upload(t, srv, "dash-local", "app.bin", "binary")
	admin.call(
		http.MethodPost,
		"/api/v1/builds/app/runs",
		`{"run":{"name":"app","number":"1","started":"2026-09-25T10:00:00Z"}}`,
		http.StatusOK,
	)
	admin.call(
		http.MethodPut,
		"/api/v1/users/nobody",
		`{"name":"nobody","email":"nobody@example.com","password":"Nobody-pass-12345"}`,
		http.StatusOK,
	)

	now := time.Now()
	if _, err := db.ClaimUnscanned(t.Context(), database.ClaimUnscannedParams{
		Now:         now,
		NamePattern: ".*",
		RetryBefore: now,
		MaxRows:     10,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertFinding(t.Context(), database.InsertFindingParams{
		Sha256:        sha256Hex("binary"),
		VulnID:        "CVE-2026-0001",
		Severity:      "high",
		Cves:          []string{},
		FixedVersions: []string{},
		Cvss:          []byte("{}"),
		Urls:          []string{},
	}); err != nil {
		t.Fatal(err)
	}

	type summary struct {
		Builds   string           `json:"builds"`
		Scans    map[string]int64 `json:"scans"`
		Findings map[string]int64 `json:"findings"`
	}

	tests := []struct {
		name   string
		caller rest
		want   summary
	}{
		{
			name:   "administrator",
			caller: admin,
			want: summary{
				Builds: "1",
				Scans: map[string]int64{
					"queued": 2,
				},
				Findings: map[string]int64{
					"high": 1,
				},
			},
		},
		{
			name:   "user without permissions",
			caller: nobody,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.caller.t = t

			var got summary
			if err := json.Unmarshal(
				[]byte(tt.caller.call(http.MethodGet, "/api/v1/dashboard-summary", "", http.StatusOK)),
				&got,
			); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("summary = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAnUnreadableRepositoryAnswersLikeAMissingOne(t *testing.T) {
	t.Parallel()
	srv := server(t)
	send := func(c call) (reply, string) {
		return do(t, srv, c)
	}
	admin := rest{
		t:   t,
		srv: send,
	}

	createRepository(t, srv, "seen-local", "LOCAL", "GENERIC", "")
	createRepository(t, srv, "hidden-local", "LOCAL", "GENERIC", "")
	createRepository(
		t,
		srv,
		"hidden-virtual",
		"VIRTUAL",
		"GENERIC",
		`,"virtual":{"members":["hidden-local"]}`,
	)
	upload(t, srv, "hidden-local", "x", "hidden")
	admin.call(
		http.MethodPut,
		"/api/v1/users/reader",
		`{"name":"reader","email":"reader@example.com","password":"Reader-pass-12345"}`,
		http.StatusOK,
	)
	admin.call(
		http.MethodPut,
		"/api/v1/permissions/seen-reader",
		`{"name":"seen-reader","rules":[{"resource":"RESOURCE_KIND_REPOSITORY",`+
			`"selector":{"repositories":["seen-local"]},`+
			`"grants":[{"user":"reader","actions":["ACTION_READ"]}]}]}`,
		http.StatusOK,
	)

	requests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "stat an item",
			method: http.MethodGet,
			path:   "/api/v1/repositories/{repo}/items/x",
		},
		{
			name:   "list children",
			method: http.MethodGet,
			path:   "/api/v1/repositories/{repo}/children",
		},
		{
			name:   "download",
			method: http.MethodGet,
			path:   "/api/v1/files/{repo}/x",
		},
		{
			name:   "upload",
			method: http.MethodPut,
			path:   "/api/v1/files/{repo}/y",
			body:   "y",
		},
		{
			name:   "delete the repository",
			method: http.MethodDelete,
			path:   "/api/v1/repositories/{repo}",
		},
	}
	for _, rq := range requests {
		for _, repo := range []string{
			"hidden-local",
			"hidden-virtual",
		} {
			t.Run(rq.name+" in "+repo, func(t *testing.T) {
				answer := func(repo string) string {
					resp, body := send(call{
						method: rq.method,
						path:   strings.ReplaceAll(rq.path, "{repo}", repo),
						body:   rq.body,
						noAuth: true,
						headers: map[string]string{
							"Authorization": "Basic cmVhZGVyOlJlYWRlci1wYXNzLTEyMzQ1",
						},
					})

					return http.StatusText(resp.StatusCode) + " " + body
				}

				if got, want := answer(repo), answer("missing-local"); got != want {
					t.Errorf("%s = %s, a missing repository = %s", repo, got, want)
				}
			})
		}
	}
}
