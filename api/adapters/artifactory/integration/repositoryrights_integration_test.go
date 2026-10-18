//go:build integration

package integration

import (
	"testing"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestGetRepositoryTellsTheCallerWhetherTheyMayUploadAndDelete(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for _, c := range []call{
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/rights-local",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/rights-virtual",
			body: `{"rclass":"virtual","packageType":"generic","repositories":["rights-local"],` +
				`"defaultDeploymentRepo":"rights-local"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/rights-remote",
			body:   `{"rclass":"remote","packageType":"generic","url":"https://example.com/files"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/security/users/reader",
			body:   `{"email":"reader@example.com","password":"Reader-pass-123"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/security/users/writer",
			body:   `{"email":"writer@example.com","password":"Writer-pass-123"}`,
		},
		{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/rights-read",
			body: `{"name":"rights-read","repo":{"repositories":["rights-local","rights-virtual","rights-remote"],` +
				`"actions":{"users":{"reader":["read"],"writer":["read"]}}}}`,
		},
		{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/rights-write",
			body: `{"name":"rights-write","repo":{"repositories":["rights-local"],` +
				`"actions":{"users":{"writer":["read","write","delete"]}}}}`,
		},
	} {
		if resp, body := do(t, srv, c); resp.StatusCode/100 != 2 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	tests := []struct {
		name       string
		user       string
		password   string
		repository string
		canUpload  bool
		canDelete  bool
	}{
		{
			name:       "reader on a local repository",
			user:       "reader",
			password:   "Reader-pass-123",
			repository: "rights-local",
		},
		{
			name:       "reader on a virtual repository",
			user:       "reader",
			password:   "Reader-pass-123",
			repository: "rights-virtual",
		},
		{
			name:       "reader on a remote repository",
			user:       "reader",
			password:   "Reader-pass-123",
			repository: "rights-remote",
		},
		{
			name:       "writer on a local repository",
			user:       "writer",
			password:   "Writer-pass-123",
			repository: "rights-local",
			canUpload:  true,
			canDelete:  true,
		},
		{
			name:       "writer on a virtual repository deploying to a local they may write",
			user:       "writer",
			password:   "Writer-pass-123",
			repository: "rights-virtual",
			canUpload:  true,
		},
		{
			name:       "writer on a remote repository they may only read",
			user:       "writer",
			password:   "Writer-pass-123",
			repository: "rights-remote",
		},
		{
			name:       "administrator on a local repository",
			user:       "admin",
			password:   "password",
			repository: "rights-local",
			canUpload:  true,
			canDelete:  true,
		},
		{
			name:       "administrator on a virtual repository",
			user:       "admin",
			password:   "password",
			repository: "rights-virtual",
			canUpload:  true,
		},
		{
			name:       "administrator on a remote repository",
			user:       "admin",
			password:   "password",
			repository: "rights-remote",
			canDelete:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repos := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP(tt.user, tt.password), srv.URL))

			resp, err := repos.GetRepository(t.Context(), &relicv1.GetRepositoryRequest{
				Name: tt.repository,
			})
			if err != nil {
				t.Fatal(err)
			}

			if got := resp.GetCanUpload(); got != tt.canUpload {
				t.Errorf("can_upload = %v, want %v", got, tt.canUpload)
			}

			if got := resp.GetCanDelete(); got != tt.canDelete {
				t.Errorf("can_delete = %v, want %v", got, tt.canDelete)
			}
		})
	}
}
