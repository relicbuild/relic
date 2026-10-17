//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestTerraformModules(t *testing.T) {
	t.Parallel()
	srv := server(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf-local",
			body:   `{"rclass":"local","packageType":"terraform"}`,
		},
	)

	for _, tag := range []string{
		"v1.0.0",
		"v1.2.0",
		"0.9.0",
	} {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/tf-local/acme/network/aws/" + tag + ".zip",
				body:   "zip " + tag,
			},
		)
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   "/.well-known/terraform.json",
		noAuth: true,
	}); !strings.Contains(
		body,
		`"modules.v1": "/terraform/v1/modules/"`,
	) {
		t.Errorf("discovery = %s", body)
	}

	_, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf-local__acme/network/aws/versions",
		},
	)
	if !strings.Contains(body, `"version": "0.9.0"`) || !strings.Contains(body, `"version": "1.2.0"`) {
		t.Errorf("versions = %s", body)
	}

	resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf-local__acme/network/aws/1.2.0/download",
		},
	)

	get := resp.Header.Get("X-Terraform-Get")
	if resp.StatusCode != 204 || !strings.Contains(get, "/tf-local/acme/network/aws/v1.2.0.zip?archive=zip&") {
		t.Fatalf("download = %d %q", resp.StatusCode, get)
	}

	if resp, got := do(t, srv, call{
		method: "GET",
		path:   get,
		noAuth: true,
	}); resp.StatusCode != 200 ||
		got != "zip v1.2.0" {
		t.Errorf("signed archive = %d %q", resp.StatusCode, got)
	}

	tampered := strings.Replace(get, "v1.2.0.zip", "v1.0.0.zip", 1)
	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   tampered,
		noAuth: true,
	}); resp.StatusCode != 403 {
		t.Errorf("tampered link = %d, want 403", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf-local__acme/network/aws/versions",
			noAuth: true,
		},
	); resp.StatusCode != 401 {
		t.Errorf("anonymous versions = %d", resp.StatusCode)
	}

	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/tf__mods",
			body:   `{"rclass":"local","packageType":"terraform"}`,
		},
	)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/tf__mods/acme/network/aws/v2.0.0.zip",
			body:   "zip v2.0.0",
		},
	)

	if resp, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/artifactory/api/terraform/v1/modules/tf__mods__acme/network/aws/versions",
		},
	); resp.StatusCode != 200 || !strings.Contains(body, `"version": "2.0.0"`) {
		t.Errorf("versions in a repository whose key holds __ = %d %s", resp.StatusCode, body)
	}
}
