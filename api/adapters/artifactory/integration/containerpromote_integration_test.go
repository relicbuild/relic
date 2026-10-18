//go:build integration

package integration

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestPromoteContainerImageNeedsDeleteToReplaceATag(t *testing.T) {
	t.Parallel()

	srv := server(t)

	for _, c := range []call{
		{
			method: http.MethodPut,
			path:   "/artifactory/api/repositories/tagover-dev",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
		{
			method: http.MethodPut,
			path:   "/artifactory/api/repositories/tagover-prod",
			body:   `{"rclass":"local","packageType":"docker"}`,
		},
		{
			method: http.MethodPut,
			path:   "/artifactory/api/security/users/writer",
			body:   `{"email":"writer@example.com","password":"Writer-pass-123"}`,
		},
		{
			method: http.MethodPost,
			path:   "/artifactory/api/v2/security/permissions/tagover-write",
			body: `{"name":"tagover-write","repo":{"repositories":["tagover-dev","tagover-prod"],` +
				`"actions":{"users":{"writer":["read","write"]}}}}`,
		},
	} {
		if resp, body := do(t, srv, c); resp.StatusCode/100 != 2 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}

	push := func(repo, tag, layerContent string) {
		t.Helper()

		config := pushBlob(t, srv, repo+"/team/app", "{}")
		layer := pushBlob(t, srv, repo+"/team/app", layerContent)

		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   "/v2/" + repo + "/team/app/manifests/" + tag,
			body:   manifestJSON(config, layer),
			headers: map[string]string{
				"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
			},
		}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("push %s:%s = %d %s", repo, tag, resp.StatusCode, body)
		}
	}

	push("tagover-dev", "1.0", "new layer")
	push("tagover-prod", "stable", "released layer")

	manifest := func() string {
		t.Helper()

		_, body := do(t, srv, call{
			method: http.MethodGet,
			path:   "/v2/tagover-prod/team/app/manifests/stable",
			headers: map[string]string{
				"Accept": "application/vnd.docker.distribution.manifest.v2+json",
			},
		})

		return body
	}
	released := manifest()

	writer := relicv1connect.NewPackageServiceClient(apiClient(relicHTTP("writer", "Writer-pass-123"), srv.URL))

	_, err := writer.PromoteContainerImage(t.Context(), &relicv1.PromoteContainerImageRequest{
		Repository:       "tagover-dev",
		Image:            "team/app",
		Tag:              "1.0",
		TargetRepository: "tagover-prod",
		TargetTag:        "stable",
		Copy:             true,
	})
	if code(err) != connect.CodePermissionDenied {
		t.Errorf("Relic API promotion over a tag without delete = %v, want permission denied", err)
	}

	resp, body := do(t, srv, call{
		method: http.MethodPost,
		path:   "/artifactory/api/docker/tagover-dev/v2/promote",
		body: `{"targetRepo":"tagover-prod","dockerRepository":"team/app",` +
			`"tag":"1.0","targetTag":"stable","copy":true}`,
		noAuth: true,
		headers: map[string]string{
			"Authorization": "Basic d3JpdGVyOldyaXRlci1wYXNzLTEyMw==",
		},
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("adapter promotion over a tag without delete = %d %s, want 403", resp.StatusCode, body)
	}

	if got := manifest(); got != released {
		t.Error("tagover-prod/team/app:stable changed after the refused promotions")
	}

	if _, err := writer.PromoteContainerImage(t.Context(), &relicv1.PromoteContainerImageRequest{
		Repository:       "tagover-dev",
		Image:            "team/app",
		Tag:              "1.0",
		TargetRepository: "tagover-prod",
		TargetTag:        "candidate",
		Copy:             true,
	}); err != nil {
		t.Errorf("promotion to a new tag with write = %v", err)
	}
}
