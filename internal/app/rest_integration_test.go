//go:build integration

package app

import (
	"net/http"
	"strings"
	"testing"
)

func TestRelicAPIREST(t *testing.T) {
	t.Parallel()
	srv := server(t)

	steps := []struct {
		name string
		c    call
		want int
		has  string
	}{
		{
			"create a repository",
			call{
				method: http.MethodPost,
				path:   "/api/v1/repositories",
				body:   `{"name":"rest-libs","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC","description":"via REST"}`,
			},
			http.StatusOK,
			`"name":"rest-libs"`,
		},
		{
			"get it",
			call{
				method: http.MethodGet,
				path:   "/api/v1/repositories/rest-libs",
			},
			http.StatusOK,
			`"description":"via REST"`,
		},
		{
			"list with an enum filter in the query",
			call{
				method: http.MethodGet,
				path:   "/api/v1/repositories?class=REPOSITORY_CLASS_LOCAL",
			},
			http.StatusOK,
			`"rest-libs"`,
		},
		{
			"replace it",
			call{
				method: http.MethodPut,
				path:   "/api/v1/repositories/rest-libs",
				body:   `{"name":"rest-libs","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC","description":"updated"}`,
			},
			http.StatusOK,
			`"updated"`,
		},
		{
			"upload a file (plain HTTP)",
			call{
				method: http.MethodPut,
				path:   "/api/v1/files/rest-libs/org/app/1.0/app.txt",
				body:   "hello",
			},
			http.StatusCreated,
			"",
		},
		{
			"stat the file",
			call{
				method: http.MethodGet,
				path:   "/api/v1/repositories/rest-libs/items/org/app/1.0/app.txt",
			},
			http.StatusOK,
			`"name":"app.txt"`,
		},
		{
			"stat the file with its path's slashes percent-encoded",
			call{
				method: http.MethodGet,
				path:   "/api/v1/repositories/rest-libs/details/org%2Fapp%2F1.0%2Fapp.txt",
			},
			http.StatusOK,
			`"name":"app.txt"`,
		},
		{
			"list the root's children",
			call{
				method: http.MethodGet,
				path:   "/api/v1/repositories/rest-libs/children",
			},
			http.StatusOK,
			`"org"`,
		},
		{
			"list a folder's children",
			call{
				method: http.MethodGet,
				path:   "/api/v1/repositories/rest-libs/children/org/app",
			},
			http.StatusOK,
			`"1.0"`,
		},
		{
			"put a group by name",
			call{
				method: http.MethodPut,
				path:   "/api/v1/groups/rest-group",
				body:   `{"name":"rest-group","description":"via REST"}`,
			},
			http.StatusOK,
			"",
		},
		{
			"get the group",
			call{
				method: http.MethodGet,
				path:   "/api/v1/groups/rest-group",
			},
			http.StatusOK,
			`"via REST"`,
		},
		{
			"delete the group",
			call{
				method: http.MethodDelete,
				path:   "/api/v1/groups/rest-group",
			},
			http.StatusOK,
			"",
		},
		{
			"a missing group is 404",
			call{
				method: http.MethodGet,
				path:   "/api/v1/groups/rest-group",
			},
			http.StatusNotFound,
			"",
		},
		{
			"an invalid argument is 400",
			call{
				method: http.MethodPost,
				path:   "/api/v1/repositories",
				body:   `{"name":"","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC"}`,
			},
			http.StatusBadRequest,
			"",
		},
		{
			"anonymous admin call is 401",
			call{
				method: http.MethodGet,
				path:   "/api/v1/users",
				noAuth: true,
			},
			http.StatusUnauthorized,
			"",
		},
		{
			"public call works anonymously",
			call{
				method: http.MethodGet,
				path:   "/api/v1/version",
				noAuth: true,
			},
			http.StatusOK,
			`"version"`,
		},
		{
			"delete the file",
			call{
				method: http.MethodDelete,
				path:   "/api/v1/repositories/rest-libs/items/org/app/1.0/app.txt",
			},
			http.StatusOK,
			"",
		},
		{
			"delete the repository",
			call{
				method: http.MethodDelete,
				path:   "/api/v1/repositories/rest-libs",
			},
			http.StatusOK,
			"",
		},
		{
			"Connect path still works",
			call{
				method: http.MethodPost,
				path:   "/api/relic.v1.SystemService/GetVersion",
				body:   "{}",
			},
			http.StatusOK,
			`"version"`,
		},
	}
	for _, s := range steps {
		if s.c.body != "" && s.c.headers == nil && !strings.HasPrefix(s.c.path, "/api/v1/files/") {
			s.c.headers = map[string]string{
				"Content-Type": "application/json",
			}
		}

		resp, body := do(t, srv, s.c)
		if resp.StatusCode != s.want || !strings.Contains(body, s.has) {
			t.Fatalf(
				"%s: %s %s = %d %s, want %d containing %q",
				s.name,
				s.c.method,
				s.c.path,
				resp.StatusCode,
				body,
				s.want,
				s.has,
			)
		}
	}
}
