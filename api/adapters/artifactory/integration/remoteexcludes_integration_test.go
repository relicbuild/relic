//go:build integration

package integration

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRemoteIndexExcludes(t *testing.T) {
	t.Parallel()

	var upstream *httptest.Server

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/npm/left-pad":
			_, _ = io.WriteString(
				w,
				`{"name":"left-pad","dist-tags":{"latest":"2.0.0","old":"1.0.0"},"time":{"created":"2020-01-01T00:00:00Z","1.0.0":"2020-01-01T00:00:00Z","2.0.0":"2021-01-01T00:00:00Z"},
				"versions":{"1.0.0":{"name":"left-pad","version":"1.0.0","dist":{"tarball":"`+upstream.URL+`/npm/left-pad/-/left-pad-1.0.0.tgz"}},
				"2.0.0":{"name":"left-pad","version":"2.0.0","dist":{"tarball":"`+upstream.URL+`/npm/left-pad/-/left-pad-2.0.0.tgz"}}}}`,
			)
		case "/simple/demo/":
			_, _ = io.WriteString(
				w,
				`<a href="`+upstream.URL+`/pkgs/demo-1.0.0.tar.gz">demo-1.0.0.tar.gz</a><a href="`+upstream.URL+`/pkgs/demo-2.0.0.tar.gz">demo-2.0.0.tar.gz</a>`,
			)
		case "/flatcontainer/relic.demo/index.json":
			_, _ = io.WriteString(w, `{"versions":["1.0.0","2.0.0"]}`)
		case "/go/example.com/lib/@v/list":
			_, _ = io.WriteString(w, "v1.0.0\nv2.0.0\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	srv := server(t)

	for _, body := range []string{
		`{"key":"npm-remote","rclass":"remote","packageType":"npm","url":"` + upstream.URL + `/npm"}`,
		`{"key":"pypi-remote","rclass":"remote","packageType":"pypi","url":"` + upstream.URL + `","pyPIRegistryUrl":"` + upstream.URL + `"}`,
		`{"key":"nuget-remote","rclass":"remote","packageType":"nuget","url":"` + upstream.URL + `"}`,
		`{"key":"go-remote","rclass":"remote","packageType":"go","url":"` + upstream.URL + `/go"}`,
		`{"key":"npm-local","rclass":"local","packageType":"npm"}`,
	} {
		key := strings.Split(strings.Split(body, `"key":"`)[1], `"`)[0]
		if resp, b := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + key,
				body:   body,
			},
		); resp.StatusCode >= 300 {
			t.Fatalf("create %s = %d %s", key, resp.StatusCode, b)
		}
	}

	for _, p := range []struct {
		name, version string
	}{
		{
			"shown-pkg",
			"1.0.0",
		},
		{
			"shown-pkg",
			"2.0.0",
		},
		{
			"hidden-pkg",
			"2.0.0",
		},
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/npm/npm-local/" + p.name,
				body:   publishDoc(p.name, p.version, npmTarball(t, p.name, p.version)),
			},
		)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/security/users/iris",
			body:   `{"email":"i@example.com","password":"Iris-pass-1"}`,
		},
	)

	perm := `{"name":"iris","repo":{"repositories":["npm-remote","pypi-remote","nuget-remote","go-remote","npm-local"],"include-patterns":["**"],
		"exclude-patterns":["**/*-2.0.0.tgz","**/*-2.0.0.tar.gz","**/*.2.0.0.nupkg","**/v2.0.0.zip"],"actions":{"users":{"iris":["read"]}}}}`
	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/iris",
			body:   perm,
		},
	); resp.StatusCode >= 300 {
		t.Fatalf("permission target = %d %s", resp.StatusCode, body)
	}

	iris := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("iris:Iris-pass-1")),
	}

	for _, tt := range []struct {
		name, path, seen, hidden string
	}{
		{
			"npm remote packument",
			"/artifactory/api/npm/npm-remote/left-pad",
			`"1.0.0"`,
			`"2.0.0"`,
		},
		{
			"PyPI remote page",
			"/artifactory/api/pypi/pypi-remote/simple/demo/",
			"demo-1.0.0.tar.gz",
			"demo-2.0.0.tar.gz",
		},
		{
			"NuGet remote versions",
			"/artifactory/api/nuget/v3/nuget-remote/flatcontainer/relic.demo/index.json",
			`"1.0.0"`,
			`"2.0.0"`,
		},
		{
			"Go remote list",
			"/artifactory/api/go/go-remote/example.com/lib/@v/list",
			"v1.0.0",
			"v2.0.0",
		},
		{
			"npm search",
			"/artifactory/api/npm/npm-local/-/v1/search?text=pkg",
			`"shown-pkg"`,
			`"hidden-pkg"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, admin := do(t, srv, call{
				method: "GET",
				path:   tt.path,
			})
			if !strings.Contains(admin, tt.seen) || !strings.Contains(admin, tt.hidden) {
				t.Fatalf("admin answer lacks an entry:\n%s", admin)
			}

			resp, body := do(t, srv, call{
				method:  "GET",
				path:    tt.path,
				noAuth:  true,
				headers: iris,
			})
			if resp.StatusCode != 200 || !strings.Contains(body, tt.seen) || strings.Contains(body, tt.hidden) {
				t.Errorf("iris = %d, want %s without %s:\n%s", resp.StatusCode, tt.seen, tt.hidden, body)
			}
		})
	}

	_, body := do(
		t,
		srv,
		call{
			method:  "GET",
			path:    "/artifactory/api/npm/npm-remote/left-pad",
			noAuth:  true,
			headers: iris,
		},
	)
	if strings.Contains(body, `"latest"`) || !strings.Contains(body, `"old"`) {
		t.Errorf("dist-tags for iris: %s", body)
	}
}

func TestStatementTimeout(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)
	base := databasetest.NewDB(t)

	db, err := database.OpenWith(
		t.Context(),
		base.Pool().Config().ConnString(),
		database.Options{
			StatementTimeout: 200 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	_, err = db.Pool().Exec(t.Context(), "SELECT pg_sleep(2)")
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "57014" {
		t.Errorf("slow query err = %v, want query_canceled (57014)", err)
	}

	if _, err := db.Pool().Exec(t.Context(), "SELECT 1"); err != nil {
		t.Errorf("fast query: %v", err)
	}
}
