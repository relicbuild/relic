//go:build integration

package integration

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestIndexExcludes(t *testing.T) {
	t.Parallel()
	srv := server(t)

	for _, r := range []string{
		`npm-local","packageType":"npm`,
		`pypi-local","packageType":"pypi`,
		`nuget-local","packageType":"nuget`,
		`helm-local","packageType":"helm`,
	} {
		key, _, _ := strings.Cut(r, `"`)
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/repositories/" + key,
				body:   `{"rclass":"local","key":"` + r + `"}`,
			},
		)
	}

	for _, v := range []string{
		"1.0.0",
		"2.0.0",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/npm/npm-local/left-pad",
				body:   publishDoc("left-pad", v, npmTarball(t, "left-pad", v)),
			},
		)
		twineUpload(t, srv, "pypi-local", "demo", v, "demo-"+v+".tar.gz", "sdist "+v)
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/nuget-local/Relic.Demo." + v + ".nupkg",
				body:   string(nupkg(t, "Relic.Demo", v)),
			},
		)
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/helm-local/demo-" + v + ".tgz",
				body:   chartTgz(t, "demo", v),
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

	perm := `{"name":"iris","repo":{"repositories":["npm-local","pypi-local","nuget-local","helm-local"],"include-patterns":["**"],
		"exclude-patterns":["**/*-2.0.0.tgz","*/2.0.0/**","*.2.0.0.nupkg"],"actions":{"users":{"iris":["read"]}}}}`
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
			"npm packument",
			"/artifactory/api/npm/npm-local/left-pad",
			`"1.0.0"`,
			`"2.0.0"`,
		},
		{
			"PyPI simple index",
			"/artifactory/api/pypi/pypi-local/simple/demo/",
			"demo-1.0.0.tar.gz",
			"demo-2.0.0.tar.gz",
		},
		{
			"NuGet versions",
			"/artifactory/api/nuget/v3/nuget-local/flatcontainer/relic.demo/index.json",
			`"1.0.0"`,
			`"2.0.0"`,
		},
		{
			"NuGet V2 feed",
			"/artifactory/api/nuget/nuget-local/FindPackagesById()?id='Relic.Demo'",
			"<d:Version>1.0.0<",
			"<d:Version>2.0.0<",
		},
		{
			"Helm index",
			"/artifactory/helm-local/index.yaml",
			"demo-1.0.0.tgz",
			"demo-2.0.0.tgz",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, admin := do(t, srv, call{
				method: "GET",
				path:   tt.path,
			})
			if !strings.Contains(admin, tt.seen) || !strings.Contains(admin, tt.hidden) {
				t.Fatalf("admin answer lacks a version:\n%s", admin)
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
}
