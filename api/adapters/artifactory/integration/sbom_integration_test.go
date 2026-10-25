//go:build integration

package integration

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

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

	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	sbom, err := scans.GetItemSbom(ctx, &relicv1.GetItemSbomRequest{
		Repository: "sbom-local",
		Path:       "app/app.jar",
	})
	if err != nil || len(sbom.GetComponents()) != 1 || sbom.GetComponents()[0].GetName() != "log4j-core" {
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
			status: http.StatusBadRequest,
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

	security := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	//nolint:gosec // G101: a test user's password
	if _, err := security.PutUser(ctx, &relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "reader",
			Email:    "reader@example.com",
			Password: "Reader-pw-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := security.PutPermission(ctx, &relicv1.PutPermissionRequest{
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
	}); err != nil {
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

func TestSBOMImportNeedsDeployOnEveryCopyOfTheContent(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	for _, key := range []string{
		"sbom-mine",
		"sbom-theirs",
	} {
		do(t, srv, call{
			method: http.MethodPut,
			path:   "/artifactory/api/repositories/" + key,
			body:   `{"rclass":"local","packageType":"generic"}`,
		})
	}

	for path, body := range map[string]string{
		"/artifactory/sbom-theirs/prod/app.jar": "shared bytes",
		"/artifactory/sbom-mine/copy/app.jar":   "shared bytes",
		"/artifactory/sbom-mine/own/app.jar":    "bytes stored only here",
	} {
		do(t, srv, call{
			method: http.MethodPut,
			path:   path,
			body:   body,
		})
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPut,
		path:   "/api/v1/sbom/sbom-theirs/prod/app.jar",
		body:   importedCycloneDX,
	}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("an administrator imports = %d %s", resp.StatusCode, body)
	}

	security := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	//nolint:gosec // G101: a test user's password
	if _, err := security.PutUser(ctx, &relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "deployer",
			Email:    "deployer@example.com",
			Password: "Deployer-pw-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := security.PutPermission(ctx, &relicv1.PutPermissionRequest{
		Permission: &relicv1.Permission{
			Name: "sbom-mine-deploy",
			Rules: []*relicv1.PermissionRule{
				{
					Resource: relicv1.ResourceKind_RESOURCE_KIND_REPOSITORY,
					Selector: &relicv1.RepositorySelector{
						Repositories: []string{
							"sbom-mine",
						},
					},
					Include: []string{
						"**",
					},
					Grants: []*relicv1.PermissionGrant{
						{
							Principal: &relicv1.PermissionGrant_User{
								User: "deployer",
							},
							Actions: []relicv1.Action{
								relicv1.Action_ACTION_READ,
								relicv1.Action_ACTION_WRITE,
							},
						},
					},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	deployer := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("deployer:Deployer-pw-1")),
	}

	tests := []struct {
		name, path string
		status     int
	}{
		{
			name:   "content also stored where the deployer may not deploy",
			path:   "/api/v1/sbom/sbom-mine/copy/app.jar",
			status: http.StatusForbidden,
		},
		{
			name:   "content stored only where the deployer may deploy",
			path:   "/api/v1/sbom/sbom-mine/own/app.jar",
			status: http.StatusNoContent,
		},
	}
	for _, tt := range tests {
		if resp, body := do(t, srv, call{
			method:  http.MethodPut,
			path:    tt.path,
			body:    `{"bomFormat": "CycloneDX", "specVersion": "1.6", "version": 1, "components": []}`,
			headers: deployer,
			noAuth:  true,
		}); resp.StatusCode != tt.status {
			t.Errorf("%s: import = %d %s, want %d", tt.name, resp.StatusCode, body, tt.status)
		}
	}

	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	sbom, err := scans.GetItemSbom(ctx, &relicv1.GetItemSbomRequest{
		Repository: "sbom-theirs",
		Path:       "prod/app.jar",
	})
	if err != nil || len(sbom.GetComponents()) != 1 {
		t.Errorf("components of the copy the deployer may not deploy to = %v, %v", sbom, err)
	}

	audits := relicv1connect.NewAuditServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	events, err := audits.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Actor:  "deployer",
		Action: "sbom.import",
	})
	if err != nil {
		t.Fatal(err)
	}

	outcomes := map[string]relicv1.AuditOutcome{}
	for _, e := range events.GetEvents() {
		outcomes[e.GetTarget()] = e.GetOutcome()
	}

	if outcomes["sbom-mine/copy/app.jar"] != relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED ||
		outcomes["sbom-mine/own/app.jar"] != relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS {
		t.Errorf("audited SBOM imports = %v", outcomes)
	}
}
