package openapi

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"relic/internal/access/auth"
	"relic/internal/platform/httpserver"
)

func TestPathOf(t *testing.T) {
	t.Parallel()

	p, params := pathOf("/api/v1/files/{repo}/{path...}")
	if p != "/api/v1/files/{repo}/{path}" || len(params) != 2 {
		t.Errorf("pathOf = %q %v", p, params)
	}

	if p, _ := pathOf("/v2/{$}"); p != "/v2/" {
		t.Errorf("exact-match pattern = %q", p)
	}
}

func TestTagOf(t *testing.T) {
	t.Parallel()

	other := func(p string) string {
		if strings.HasPrefix(p, "/mount/") {
			return "Mount"
		}

		return ""
	}
	for pattern, want := range map[string]string{
		"/api/v1/files/{repo}/{path...}": "Relic API: files",
		"/api/v1/openapi.json":           "Relic API: openapi.json",
		"/api/v1/{name}":                 "Relic API: ",
		"/mount/x":                       "Mount",
		"/healthz":                       "System",
	} {
		if got := tagOf(pattern, other); got != want {
			t.Errorf("tagOf(%q) = %q, want %q", pattern, got, want)
		}
	}

	if got := tagOf("/mount/x", nil); got != "System" {
		t.Errorf("tagOf without a tagger = %q, want System", got)
	}
}

type inner struct {
	When time.Time `json:"when"`
}

type sample struct {
	Name    string            `json:"name"`
	Note    string            `json:"note,omitempty"`
	Count   *int              `json:"count"`
	Tags    []string          `json:"tags"`
	Labels  map[string]string `json:"labels"`
	Inner   inner             `json:"inner"`
	Skipped string            `json:"-"`
	private string            //nolint:unused // checks that unexported fields are left out
}

func TestBuildSchemas(t *testing.T) {
	t.Parallel()

	doc := Build([]httpserver.Route{
		{
			Pattern: "GET /api/v1/things/{id}",
			Access:  auth.Admin,
		},
		{
			Pattern: "GET /healthz",
			Access:  auth.Public,
		},
		{
			Pattern: "/ui/",
			Access:  auth.Public,
		},
	}, map[string]Doc{
		"GET /api/v1/things/{id}": {
			Summary:  "A thing",
			Response: sample{},
		},
	}, Info{
		Title: "t",
	})

	paths := doc["paths"].(map[string]map[string]any)
	if len(paths) != 2 {
		t.Fatalf("paths = %v (method-less routes are skipped)", paths)
	}

	op := paths["/api/v1/things/{id}"]["get"].(map[string]any)
	if op["summary"] != "A thing" || op["x-relic-access"] != "admin" {
		t.Errorf("operation = %v", op)
	}

	if security, ok := paths["/healthz"]["get"].(map[string]any)["security"].([]any); !ok || len(security) != 0 {
		t.Errorf("a public route's security = %v, want an empty requirement", security)
	}

	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	s := schemas["sample"].(map[string]any)

	props := s["properties"].(map[string]any)
	if _, ok := props["Skipped"]; ok || len(props) != 6 {
		t.Errorf("properties = %v", props)
	}

	if want := []string{
		"inner",
		"labels",
		"name",
		"tags",
	}; !reflect.DeepEqual(s["required"], want) {
		t.Errorf("required = %v, want %v (omitempty and pointers are optional)", s["required"], want)
	}

	if got := schemas["inner"].(map[string]any)["properties"].(map[string]any)["when"]; !reflect.DeepEqual(
		got,
		map[string]any{
			"type":   "string",
			"format": "date-time",
		},
	) {
		t.Errorf("time.Time = %v", got)
	}
}
