package packages

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
)

type serviceIndexResource struct {
	ID   string `json:"@id"`
	Type string `json:"@type"`
}

func TestNuGetIndexResources(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	(&Handler{}).nugetIndex(rec, "https://relic.example.com/nuget/v3/nuget")

	var index struct {
		Resources []serviceIndexResource `json:"resources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		typeName string
		id       string
	}{
		{
			name:     "dotnet package search",
			typeName: "SearchQueryService/3.0.0-beta",
			id:       "https://relic.example.com/nuget/v3/nuget/query",
		},
		{
			name:     "release candidate search clients",
			typeName: "SearchQueryService/3.0.0-rc",
			id:       "https://relic.example.com/nuget/v3/nuget/query",
		},
		{
			name:     "versioned search clients",
			typeName: "SearchQueryService/3.5.0",
			id:       "https://relic.example.com/nuget/v3/nuget/query",
		},
		{
			name:     "restore",
			typeName: "PackageBaseAddress/3.0.0",
			id:       "https://relic.example.com/nuget/v3/nuget/flatcontainer/",
		},
		{
			name:     "symbol push",
			typeName: "SymbolPackagePublish/4.9.0",
			id:       "https://relic.example.com/nuget/v3/nuget",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if !slices.Contains(index.Resources, serviceIndexResource{
				ID:   tt.id,
				Type: tt.typeName,
			}) {
				t.Errorf("service index has no %s at %s: %+v", tt.typeName, tt.id, index.Resources)
			}
		})
	}
}
