//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

const importedCycloneDX = `{"bomFormat": "CycloneDX", "specVersion": "1.5", "components": [
  {"type": "library", "name": "log4j-core", "version": "2.14.1",
   "purl": "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1", "licenses": [{"license": {"id": "Apache-2.0"}}]}
]}`

func TestSBOMExportAndImport(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/sbom-local",
		body:   `{"rclass":"local","packageType":"generic"}`,
	})
	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/sbom-local/app/app.jar",
		body:   "not really a jar",
	})

	if resp, _ := do(t, srv, call{
		method: http.MethodGet,
		path:   "/api/v1/sbom/sbom-local/app/app.jar",
	}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("export before any SBOM = %d", resp.StatusCode)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/api/v1/sbom/sbom-local/app/app.jar",
		body:   `{"hello": "world"}`,
	}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("import of a document that is no SBOM = %d %s", resp.StatusCode, body)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/api/v1/sbom/sbom-local/app/app.jar",
		body:   importedCycloneDX,
	}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("import = %d %s", resp.StatusCode, body)
	}

	scans := relicv1connect.NewScanServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	sbom, err := scans.GetItemSbom(ctx, connect.NewRequest(&relicv1.GetItemSbomRequest{
		Repository: "sbom-local",
		Path:       "app/app.jar",
	}))
	if err != nil || len(sbom.Msg.GetComponents()) != 1 || sbom.Msg.GetComponents()[0].GetName() != "log4j-core" {
		t.Fatalf("components after the import = %v, %v", sbom, err)
	}

	tests := []struct {
		format, contentType, contains string
		status                        int
	}{
		{
			format:      "",
			contentType: "application/vnd.cyclonedx+json",
			contains:    `"bomFormat": "CycloneDX"`,
			status:      http.StatusOK,
		},
		{
			format:      "spdx",
			contentType: "application/spdx+json",
			contains:    `"referenceLocator": "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1"`,
			status:      http.StatusOK,
		},
		{
			format: "syft",
			status: http.StatusConflict,
		},
		{
			format: "xml",
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		resp, body := do(t, srv, call{
			method: http.MethodGet,
			path:   "/api/v1/sbom/sbom-local/app/app.jar?format=" + tt.format,
		})
		if resp.StatusCode != tt.status || resp.Header.Get("Content-Type") != tt.contentType && tt.contentType != "" ||
			!strings.Contains(body, tt.contains) {
			t.Errorf("export as %q = %d %s %s", tt.format, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}

	security := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	//nolint:gosec // G101: a test user's password
	if _, err := security.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "reader",
			Email:    "reader@example.com",
			Password: "Reader-pw-1",
		},
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := security.PutPermission(ctx, connect.NewRequest(&relicv1.PutPermissionRequest{
		Permission: &relicv1.Permission{
			Name: "sbom-read",
			Rules: []*relicv1.PermissionRule{
				{
					Resource: relicv1.ResourceKind_RESOURCE_KIND_REPOSITORY,
					Selector: &relicv1.RepositorySelector{
						Repositories: []string{
							"sbom-local",
						},
					},
					Include: []string{
						"**",
					},
					Grants: []*relicv1.PermissionGrant{
						{
							Principal: &relicv1.PermissionGrant_User{
								User: "reader",
							},
							Actions: []relicv1.Action{
								relicv1.Action_ACTION_READ,
							},
						},
					},
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	asReader := func(method, body string) int {
		req, err := http.NewRequestWithContext(
			ctx,
			method,
			srv.URL+"/api/v1/sbom/sbom-local/app/app.jar",
			strings.NewReader(body),
		)
		if err != nil {
			t.Fatal(err)
		}

		req.SetBasicAuth("reader", "Reader-pw-1")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		return resp.StatusCode
	}

	if got := asReader(http.MethodGet, ""); got != http.StatusOK {
		t.Errorf("a reader exports = %d", got)
	}

	if got := asReader(http.MethodPut, importedCycloneDX); got != http.StatusForbidden && got != http.StatusNotFound {
		t.Errorf("a reader imports = %d", got)
	}
}
