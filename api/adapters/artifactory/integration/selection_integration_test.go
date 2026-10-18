//go:build integration

package integration

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
)

func TestVersionSelectionSkipsRefusedVersions(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()

	old := time.Now().Add(-60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	mid := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-20 * 24 * time.Hour).UTC().Format(time.RFC3339)

	var upstream *httptest.Server

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pad":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"pad","dist-tags":{"latest":"1.2.0"},
			  "time":{"1.0.0":"`+old+`","1.1.0":"`+mid+`","1.2.0":"`+recent+`"},
			  "versions":{
			    "1.0.0":{"dist":{"tarball":"`+upstream.URL+`/pad/-/pad-1.0.0.tgz"}},
			    "1.1.0":{"dist":{"tarball":"`+upstream.URL+`/pad/-/pad-1.1.0.tgz"}},
			    "1.2.0":{"dist":{"tarball":"`+upstream.URL+`/pad/-/pad-1.2.0.tgz"}}}}`)
		default:
			_, _ = io.WriteString(w, "tarball "+html.EscapeString(r.URL.Path))
		}
	}))
	t.Cleanup(upstream.Close)

	repos := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := repos.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
		Repository: &relicv1.Repository{
			Name:   "npm-selected",
			Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
			Format: relicv1.Format_FORMAT_NPM,
			Remote: &relicv1.RemoteRepositorySettings{
				Url:        upstream.URL,
				Quarantine: true,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertMaliciousPackage(ctx, database.UpsertMaliciousPackageParams{
		ID:        "MAL-2026-9",
		Ecosystem: "npm",
		Name:      "pad",
		Versions: []string{
			"1.2.0",
		},
		Ranges:   []byte(`[]`),
		Summary:  "steals tokens",
		Modified: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if resp, _ := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/api/npm/npm-selected/pad/-/pad-1.1.0.tgz",
	}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a freshly cached tarball = %d, want held by the quarantine", resp.StatusCode)
	}

	resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/api/npm/npm-selected/pad",
	})

	var doc struct {
		DistTags map[string]string          `json:"dist-tags"`
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("packument = %d %s", resp.StatusCode, body)
	}

	if len(doc.Versions) != 1 || doc.Versions["1.0.0"] == nil || doc.DistTags["latest"] != "1.0.0" {
		t.Errorf("versions = %v, tags = %v; want the malicious 1.2.0 and the held 1.1.0 left out, latest 1.0.0",
			keys(doc.Versions), doc.DistTags)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
