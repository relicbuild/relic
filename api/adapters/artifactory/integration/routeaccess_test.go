package integration

import (
	"log/slog"
	"testing"

	"relic/api/adapters/artifactory"
	"relic/api/adapters/artifactory/access"
	evidenceapi "relic/api/adapters/artifactory/evidence"
	lifecycleapi "relic/api/adapters/artifactory/lifecycle"
	"relic/api/adapters/artifactory/xray"
	"relic/internal/access/auth"
	"relic/internal/platform/httpserver"
)

func TestRouteAccess(t *testing.T) {
	t.Parallel()

	rt := httpserver.NewRouter()
	artifactory.New(nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler)).Register(rt)
	access.New(nil, nil, slog.New(slog.DiscardHandler)).Register(rt)
	xray.New(nil, nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler)).Register(rt)
	lifecycleapi.New(nil, nil, slog.New(slog.DiscardHandler)).Register(rt)
	evidenceapi.New(nil, nil, nil, nil, slog.New(slog.DiscardHandler)).Register(rt)

	public := map[string]bool{
		"GET /xray/api/v1/system/ping":                                         true,
		"GET /.well-known/terraform.json":                                      true, // read before terraform authenticates
		"GET /artifactory/api/terraform/archive/":                              true, // signed URLs
		"GET /healthz":                                                         true,
		"GET /readyz":                                                          true,
		"GET /ui/":                                                             true,
		"GET /artifactory/api/system/ping":                                     true,
		"POST /artifactory/api/security/token":                                 true, // authorizes in the handler; refresh has no credentials
		"PUT /artifactory/api/npm/{repo}/-/user/{user}":                        true, // npm login: the body carries the credentials
		"GET /artifactory/api/terraform/provider-archive/":                     true, // signed URLs from the provider mirror
		"GET /access/api/v1/system/ping":                                       true,
		"POST /access/api/v1/tokens":                                           true, // same as api/security/token
		"POST /access/api/v1/oidc/token":                                       true,
		"POST /access/api/v2/authentication/jfrog_client_login/request":        true,
		"GET /access/api/v2/authentication/jfrog_client_login/token/{session}": true,
		// Docker registry: access is checked per request.
		"/v2/":                     true,
		"/artifactory/api/docker/": true,
		// Conan pings before it logs in.
		"GET /artifactory/api/conan/{repo}/v1/ping": true,
		// huggingface_hub PUTs LFS objects without credentials; signed URL.
		"PUT /artifactory/api/huggingfaceml/{repo}/lfs/{oid}": true,
		// The Xet CAS verifies the short-lived Xet tokens it issues.
		"POST /artifactory/api/huggingfaceml/{repo}/api/xet/v1/xorbs/{prefix}/{hash}": true,
		"HEAD /artifactory/api/huggingfaceml/{repo}/api/xet/v1/xorbs/{prefix}/{hash}": true,
		"POST /artifactory/api/huggingfaceml/{repo}/api/xet/v1/shards":                true,
		"/artifactory/api/huggingfaceml/{repo}/api/xet/":                              true,
	}

	for _, r := range rt.Routes() {
		switch r.Access {
		case auth.Public:
			if !public[r.Pattern] {
				t.Errorf("route %q is public but not allowlisted", r.Pattern)
			}
		case auth.Authenticated, auth.Admin:
		default:
			t.Errorf("route %q has no valid access level (%v)", r.Pattern, r.Access)
		}
	}

	adminOnly := []string{
		"PUT /artifactory/api/repositories/{key}",
		"POST /artifactory/api/repositories/{key}",
		"DELETE /artifactory/api/repositories/{key}",
		"GET /artifactory/api/security/users",
		"PUT /artifactory/api/security/users/{name}",
		"DELETE /artifactory/api/security/users/{name}",
		"PUT /artifactory/api/security/groups/{name}",
		"POST /artifactory/api/security/groups/{name}",
		"DELETE /artifactory/api/security/groups/{name}",
		"POST /artifactory/api/v2/security/permissions/{name}",
		"PUT /artifactory/api/v2/security/permissions/{name}",
		"DELETE /artifactory/api/v2/security/permissions/{name}",
		// Release changes are authorized by release rules in the handler.
		"POST /artifactory/api/release/import/",
		"POST /artifactory/api/security/keys/trusted",
		"DELETE /artifactory/api/security/keys/trusted/{kid}",
	}
	for _, mutating := range adminOnly {
		found := false

		for _, r := range rt.Routes() {
			if r.Pattern == mutating {
				found = true

				if r.Access != auth.Admin {
					t.Errorf("%q must be admin-only, is %v", mutating, r.Access)
				}
			}
		}

		if !found {
			t.Errorf("%q is not registered", mutating)
		}
	}
}
