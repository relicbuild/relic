package apispec

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

type parameter struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

type operation struct {
	Parameters  []parameter `json:"parameters"`
	RequestBody struct {
		Content map[string]struct {
			Schema struct {
				AllOf []struct {
					Ref string `json:"$ref"`
				} `json:"allOf"`
			} `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

func TestRequestBodiesKeepTheirNestedMessages(t *testing.T) {
	t.Parallel()

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(JSON, &spec); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		method  string
		wantRef string
	}{
		{
			name:    "PublishBuildRun",
			path:    "/api/v1/builds/{run.name}/runs",
			method:  "post",
			wantRef: "PublishedBuildRun",
		},
		{
			name:    "CreateRelease",
			path:    "/api/v1/releases/{spec.name}/versions",
			method:  "post",
			wantRef: "ReleaseSpec",
		},
		{
			name:    "UpdateRelease",
			path:    "/api/v1/releases/{spec.name}/versions/{spec.version}",
			method:  "patch",
			wantRef: "ReleaseSpec",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, ok := spec.Paths[tt.path][tt.method]
			if !ok {
				t.Fatalf("no %s %s in the description", tt.method, tt.path)
			}

			var op operation
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatal(err)
			}

			body := op.RequestBody.Content["application/json"].Schema.AllOf
			if len(body) != 1 || !strings.HasSuffix(body[0].Ref, "."+tt.wantRef) {
				t.Errorf("request body = %+v, want a reference to %s", body, tt.wantRef)
			}

			if !slices.Contains(op.Parameters, parameter{
				Name: "project",
				In:   "query",
			}) {
				t.Errorf("parameters = %+v, want the query parameter project", op.Parameters)
			}
		})
	}
}
