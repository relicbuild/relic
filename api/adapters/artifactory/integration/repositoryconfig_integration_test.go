//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestRepositoryConfigurationReadsAsPartialForReaders(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for _, c := range []call{
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/config-local",
			body:   `{"rclass":"local","packageType":"npm","description":"team packages"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/config-remote",
			body: `{"rclass":"remote","packageType":"npm","url":"https://registry.example.com",` +
				`"username":"upstream-user","password":"upstream-secret"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/config-hidden",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/security/users/config-reader",
			body:   `{"email":"config-reader@example.com","password":"Reader-pass-123"}`,
		},
		{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/config-read",
			body: `{"name":"config-read","repo":{"repositories":["config-local","config-remote"],` +
				`"actions":{"users":{"config-reader":["read","write"]}}}}`,
		},
	} {
		if resp, body := do(t, srv, c); resp.StatusCode/100 != 2 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	reader := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("config-reader:Reader-pass-123")),
	}

	tests := []struct {
		name       string
		repository string
		admin      bool
		wantStatus int
		wantFields map[string]any
		absent     []string
	}{
		{
			name:       "reader of a local repository",
			repository: "config-local",
			wantStatus: http.StatusOK,
			wantFields: map[string]any{
				"key":         "config-local",
				"rclass":      "local",
				"packageType": "npm",
				"description": "team packages",
			},
		},
		{
			name:       "reader of a remote repository",
			repository: "config-remote",
			wantStatus: http.StatusOK,
			wantFields: map[string]any{
				"key":         "config-remote",
				"rclass":      "remote",
				"packageType": "npm",
			},
			absent: []string{
				"url",
				"username",
				"password",
			},
		},
		{
			name:       "repository the reader may not read",
			repository: "config-hidden",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "repository that does not exist",
			repository: "config-missing",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "administrator of a remote repository",
			repository: "config-remote",
			admin:      true,
			wantStatus: http.StatusOK,
			wantFields: map[string]any{
				"url":      "https://registry.example.com",
				"username": "upstream-user",
				"password": "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := call{
				method: "GET",
				path:   "/artifactory/api/repositories/" + tt.repository,
			}
			if !tt.admin {
				c.noAuth = true
				c.headers = reader
			}

			resp, body := do(t, srv, c)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("GET = %d %s, want %d", resp.StatusCode, body, tt.wantStatus)
			}

			if tt.wantStatus != http.StatusOK {
				return
			}

			var doc map[string]any
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}

			for k, want := range tt.wantFields {
				if doc[k] != want {
					t.Errorf("%s = %v, want %v", k, doc[k], want)
				}
			}

			for _, k := range tt.absent {
				if v, ok := doc[k]; ok {
					t.Errorf("%s = %v, want it absent", k, v)
				}
			}
		})
	}
}
