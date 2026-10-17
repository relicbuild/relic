//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"relic/api/adapters/artifactory/transfer"
)

type asAdmin struct{}

func (asAdmin) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth("admin", "password")

	return http.DefaultTransport.RoundTrip(r)
}

type relicAPIOnly struct{}

func (relicAPIOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.Path, "/artifactory/") {
		return nil, fmt.Errorf("the import wrote %s %s through the compatibility adapter", r.Method, r.URL.Path)
	}

	return asAdmin{}.RoundTrip(r)
}

type jarPropertiesUnavailable struct{}

func (jarPropertiesUnavailable) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.RawQuery == "properties" && strings.HasSuffix(r.URL.Path, "/app-1.0.jar") {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       http.NoBody,
			Request:    r,
		}, nil
	}

	return asAdmin{}.RoundTrip(r)
}

func TestImportArtifactory(t *testing.T) {
	t.Parallel()

	src, dst := server(t), server(t)

	for _, c := range []call{
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs-local",
			body:   `{"rclass":"local","packageType":"maven","description":"libraries"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-remote",
			body:   `{"rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"virtual","packageType":"maven","repositories":["libs-local"],"defaultDeploymentRepo":"libs-local"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/security/groups/devs",
			body:   `{"description":"developers"}`,
		},
		{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/devs-libs",
			body:   `{"name":"devs-libs","repo":{"repositories":["libs-local"],"include-patterns":["**"],"actions":{"groups":{"devs":["read","write"]}}}}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/libs-local/org/app/1.0/app-1.0.jar",
			body:   "jar bytes",
		},
		{
			method: "PUT",
			path:   "/artifactory/libs-local/org/app/1.0/app-1.0.pom",
			body:   "<project/>",
		},
		{
			method: "PUT",
			path:   "/artifactory/api/storage/libs-local/org/app/1.0/app-1.0.jar?properties=release=1.0;qa=passed,signed",
		},
	} {
		if resp, body := do(t, src, c); resp.StatusCode/100 != 2 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	servers := func() (transfer.Server, transfer.Server) {
		source := transfer.Server{
			URL: src.URL,
			Client: &http.Client{
				Transport: asAdmin{},
			},
		}
		destination := transfer.Server{
			URL: dst.URL,
			Client: &http.Client{
				Transport: relicAPIOnly{},
			},
		}

		return source, destination
	}

	s, d := servers()

	report, err := transfer.Import(t.Context(), s, d, transfer.Options{
		DryRun: true,
	})
	if err != nil || report.Repositories.Created != 3 || report.Files.Copied != 2 || len(report.Errors) != 0 {
		t.Fatalf("dry run = %+v %v", report, err)
	}

	if resp, _ := do(t, dst, call{
		method: "GET",
		path:   "/artifactory/api/repositories/libs-local",
	}); resp.StatusCode != 400 {
		t.Errorf("the dry run created a repository: %d", resp.StatusCode)
	}

	report, err = transfer.Import(t.Context(), transfer.Server{
		URL: src.URL,
		Client: &http.Client{
			Transport: jarPropertiesUnavailable{},
		},
	}, d, transfer.Options{})
	if err != nil || len(report.Errors) == 0 || report.Groups.Created < 1 || report.Repositories.Created != 3 ||
		report.Permissions.Created != 1 || report.Files.Copied != 1 {
		t.Fatalf("import without the jar's properties = %+v %v, want the jar refused", report, err)
	}

	report, err = transfer.Import(t.Context(), s, d, transfer.Options{})
	if err != nil || len(report.Errors) != 0 || report.Files.Copied != 1 || report.Files.Skipped != 1 {
		t.Fatalf("import run again = %+v %v, want the jar copied", report, err)
	}

	copied := 2

	for path, want := range map[string]string{
		"/artifactory/api/repositories/libs":                                     `"defaultDeploymentRepo":"libs-local"`,
		"/artifactory/api/v2/security/permissions/devs-libs":                     `"devs"`,
		"/artifactory/libs-local/org/app/1.0/app-1.0.jar":                        "jar bytes",
		"/artifactory/api/storage/libs-local/org/app/1.0/app-1.0.jar?properties": "\"qa\": [\n      \"passed\",\n      \"signed\"\n    ]",
	} {
		resp, body := do(t, dst, call{
			method: "GET",
			path:   path,
		})
		if resp.StatusCode != 200 || !strings.Contains(body, want) {
			t.Errorf("GET %s = %d %q; want %q", path, resp.StatusCode, body, want)
		}
	}

	report, err = transfer.Import(t.Context(), s, d, transfer.Options{})
	if err != nil || report.Repositories.Created != 0 || report.Repositories.Skipped != 3 || report.Files.Copied != 0 ||
		report.Files.Skipped != copied {
		t.Errorf("second import = %+v %v", report, err)
	}

	report, err = transfer.Import(t.Context(), s, d, transfer.Options{
		Repositories: []string{
			"npm-remote",
		},
		Update: true,
	})
	if err != nil || report.Repositories.Updated != 1 || report.Files.Copied+report.Files.Skipped != 0 {
		t.Errorf("one repository = %+v %v", report, err)
	}
}
