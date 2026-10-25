//go:build integration

package integration

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/blocking"
)

func TestQuarantineAppliesAsSoonAsItIsTurnedOn(t *testing.T) {
	t.Parallel()

	srv := server(t)
	ctx := t.Context()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "jar "+html.EscapeString(r.URL.Path))
	}))
	t.Cleanup(upstream.Close)

	remote := &relicv1.Repository{
		Name:   "late-remote",
		Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
		Format: relicv1.Format_FORMAT_MAVEN,
		Remote: &relicv1.RemoteRepositorySettings{
			Url: upstream.URL,
		},
	}

	repos := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := repos.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
		Repository: remote,
	}); err != nil {
		t.Fatal(err)
	}

	get := func(p string) (int, string) {
		resp, body := do(t, srv, call{
			method: http.MethodGet,
			path:   "/artifactory/late-remote/" + p,
		})

		return resp.StatusCode, body
	}

	if status, body := get("g/a/1.0/a-1.0.jar"); status != http.StatusOK {
		t.Fatalf("a file of a repository without quarantine = %d %s", status, body)
	}

	remote.Remote.Quarantine = true
	if _, err := repos.UpdateRepository(ctx, &relicv1.UpdateRepositoryRequest{
		Repository: remote,
	}); err != nil {
		t.Fatal(err)
	}

	if status, body := get(
		"g/b/1.0/b-1.0.jar",
	); status != http.StatusForbidden ||
		!strings.Contains(body, "quarantined") {
		t.Errorf("a fresh file right after quarantine was turned on = %d %s, want held", status, body)
	}
}

func TestReleaseBundleRefusesQuarantinedFiles(t *testing.T) {
	t.Parallel()

	srv := server(t)
	ctx := t.Context()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "jar "+html.EscapeString(r.URL.Path))
	}))
	t.Cleanup(upstream.Close)

	repos := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := repos.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
		Repository: &relicv1.Repository{
			Name:   "bundled-remote",
			Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
			Format: relicv1.Format_FORMAT_MAVEN,
			Remote: &relicv1.RemoteRepositorySettings{
				Url:        upstream.URL,
				Quarantine: true,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodGet,
		path:   "/artifactory/bundled-remote/g/a/1.0/a-1.0.jar",
	}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a fresh file = %d %s, want held", resp.StatusCode, body)
	}

	resp, body := do(t, srv, call{
		method: http.MethodPost,
		path:   "/lifecycle/api/v2/release_bundle?async=false",
		body: `{"release_bundle_name":"held","release_bundle_version":"1.0.0","source_type":"artifacts",
			"source":{"artifacts":[{"path":"bundled-remote/g/a/1.0/a-1.0.jar"}]}}`,
	})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "quarantined") {
		t.Errorf("a release bundle holding a quarantined file = %d %s, want 403", resp.StatusCode, body)
	}
}

func TestQuarantine(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "jar "+html.EscapeString(r.URL.Path))
	}))
	t.Cleanup(upstream.Close)

	repos := relicv1connect.NewRepositoryServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := repos.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
		Repository: &relicv1.Repository{
			Name:   "held-remote",
			Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
			Format: relicv1.Format_FORMAT_MAVEN,
			Remote: &relicv1.RemoteRepositorySettings{
				Url:        upstream.URL,
				Quarantine: true,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	get := func(p string) (int, string) {
		resp, body := do(t, srv, call{
			method: http.MethodGet,
			path:   "/artifactory/held-remote/" + p,
		})

		return resp.StatusCode, body
	}

	for _, p := range []string{
		"g/a/1.0/a-1.0.jar",
		"g/b/1.0/b-1.0.jar",
		"g/c/1.0/c-1.0.jar",
		"g/d/1.0/d-1.0.jar",
	} {
		if status, body := get(p); status != http.StatusForbidden || !strings.Contains(body, "quarantined") {
			t.Errorf("a fresh %s = %d %s, want held", p, status, body)
		}
	}

	if status, _ := get("g/a/1.0/a-1.0.pom"); status != http.StatusOK {
		t.Errorf("a file Relic Scan does not scan = %d, want served", status)
	}

	explained, err := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL)).
		ExplainDownload(ctx, &relicv1.ExplainDownloadRequest{
			Repository: "held-remote",
			Path:       "g/a/1.0/a-1.0.pom",
		})
	if err != nil || explained.GetBlocked() || explained.GetPolicy() != "quarantine" ||
		explained.GetRule() != blocking.RuleNotHeld {
		t.Errorf("explanation of a file quarantine does not hold = %v, %v; want it served and said so", explained, err)
	}

	quarantine := relicv1connect.NewQuarantineServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	list, err := quarantine.ListQuarantined(ctx, &relicv1.ListQuarantinedRequest{})
	if err != nil || len(list.GetFiles()) != 4 ||
		list.GetFiles()[0].GetState() != relicv1.QuarantineState_QUARANTINE_STATE_AWAITING_SCAN {
		t.Fatalf("quarantine = %v, %v", list, err)
	}

	candidates, err := db.ListQuarantineCandidates(ctx, database.ListQuarantineCandidatesParams{
		RepoKeys: []string{
			"held-remote",
		},
		NamePattern: scan.PackagePattern,
		MaxRows:     100,
	})
	if err != nil || len(candidates) != 4 {
		t.Errorf("the listing reads %d rows, %v; want only the 4 files Relic Scan scans", len(candidates), err)
	}

	sha := map[string]string{}
	for _, f := range list.GetFiles() {
		sha[f.GetPath()] = f.GetSha256()
	}

	if _, err := quarantine.ReleaseQuarantined(ctx, &relicv1.ReleaseQuarantinedRequest{
		Repository: "held-remote",
		Path:       "g/a/1.0/a-1.0.jar",
		Reason:     "vetted",
	}); err != nil {
		t.Fatal(err)
	}

	if status, _ := get("g/a/1.0/a-1.0.jar"); status != http.StatusOK {
		t.Errorf("a released file = %d", status)
	}

	for _, p := range []string{
		"g/b/1.0/b-1.0.jar",
		"g/c/1.0/c-1.0.jar",
		"g/d/1.0/d-1.0.jar",
	} {
		importMatchedSBOM(t, db, sha[p], "x.jar", `{"bomFormat":"CycloneDX","components":[]}`)
	}

	if err := db.InsertFinding(ctx, database.InsertFindingParams{
		Sha256:        sha["g/c/1.0/c-1.0.jar"],
		VulnID:        "MAL-2026-7",
		PackageName:   "c",
		Severity:      "critical",
		Cves:          []string{},
		FixedVersions: []string{},
		Cvss:          []byte(`[]`),
		Urls:          []string{},
	}); err != nil {
		t.Fatal(err)
	}

	if status, _ := get("g/b/1.0/b-1.0.jar"); status != http.StatusOK {
		t.Errorf("a file scanned clean = %d, want released", status)
	}

	if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
		Sha256: sha["g/d/1.0/d-1.0.jar"],
		Status: scan.StatusSkipped,
		Reason: "too large",
		Now:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if status, body := get("g/d/1.0/d-1.0.jar"); status != http.StatusForbidden || !strings.Contains(body, "skipped") {
		t.Errorf("a file whose scan was skipped = %d %s, want held", status, body)
	}

	held, err := quarantine.ListQuarantined(ctx, &relicv1.ListQuarantinedRequest{})
	if err != nil || !slices.ContainsFunc(held.GetFiles(), func(f *relicv1.QuarantinedFile) bool {
		return f.GetPath() == "g/d/1.0/d-1.0.jar" &&
			f.GetState() == relicv1.QuarantineState_QUARANTINE_STATE_SCAN_SKIPPED
	}) {
		t.Errorf("the quarantine does not list the file whose scan was skipped: %v, %v", held, err)
	}

	if status, body := get(
		"g/c/1.0/c-1.0.jar",
	); status != http.StatusForbidden ||
		!strings.Contains(body, "malicious") {
		t.Errorf("a malicious file = %d %s", status, body)
	}

	if _, err := quarantine.RejectQuarantined(ctx, &relicv1.RejectQuarantinedRequest{
		Repository: "held-remote",
		Path:       "g/b/1.0/b-1.0.jar",
		Reason:     "license",
	}); err != nil {
		t.Fatal(err)
	}

	if status, body := get("g/b/1.0/b-1.0.jar"); status != http.StatusForbidden || !strings.Contains(body, "license") {
		t.Errorf("a rejected file = %d %s", status, body)
	}

	if _, err := repos.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
		Repository: &relicv1.Repository{
			Name:   "copies-local",
			Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_LOCAL,
			Format: relicv1.Format_FORMAT_MAVEN,
		},
	}); err != nil {
		t.Fatal(err)
	}

	artifacts := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := artifacts.CopyItem(ctx, &relicv1.CopyItemRequest{
		Repository:   "held-remote",
		Path:         "g/c/1.0/c-1.0.jar",
		ToRepository: "copies-local",
		ToPath:       "g/c/1.0/c-1.0.jar",
	}); code(err) != connect.CodePermissionDenied {
		t.Errorf("copying a malicious file out of quarantine through the Relic API = %v, want refused", err)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPost,
		path:   "/artifactory/api/move/held-remote/g/b?to=/copies-local/g/b",
	}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("moving a folder that holds a rejected file through the Artifactory API = %d %s, want 403",
			resp.StatusCode, body)
	}

	if resp, body := do(t, srv, call{
		method: http.MethodPost,
		path:   "/artifactory/api/copy/held-remote/g/a?to=/copies-local/g/a",
	}); resp.StatusCode != http.StatusOK {
		t.Errorf("copying a released file = %d %s", resp.StatusCode, body)
	}

	for p, want := range map[string]int{
		"g/a/1.0/a-1.0.jar": http.StatusCreated,
		"g/b/1.0/b-1.0.jar": http.StatusForbidden,
		"g/c/1.0/c-1.0.jar": http.StatusForbidden,
	} {
		if resp, body := do(t, srv, call{
			method: http.MethodPut,
			path:   "/artifactory/copies-local/by-checksum/" + p,
			headers: map[string]string{
				"X-Checksum-Deploy": "true",
				"X-Checksum-Sha256": sha[p],
			},
		}); resp.StatusCode != want {
			t.Errorf(
				"a checksum deploy of the content of held-remote/%s = %d %s, want %d",
				p,
				resp.StatusCode,
				body,
				want,
			)
		}
	}

	for p, want := range map[string]int{
		"g/a/1.0/a-1.0.jar": http.StatusOK,
		"g/b/1.0/b-1.0.jar": http.StatusNotFound,
		"g/c/1.0/c-1.0.jar": http.StatusNotFound,
	} {
		if resp, _ := do(t, srv, call{
			method: http.MethodGet,
			path:   "/artifactory/copies-local/" + p,
		}); resp.StatusCode != want {
			t.Errorf("copies-local/%s = %d, want %d", p, resp.StatusCode, want)
		}
	}

	viewer := relicv1connect.NewQuarantineServiceClient(apiClient(relicHTTP("nobody", "x"), srv.URL))
	if _, err := viewer.ReleaseQuarantined(ctx, &relicv1.ReleaseQuarantinedRequest{
		Repository: "held-remote",
		Path:       "g/c/1.0/c-1.0.jar",
	}); code(err) != connect.CodeUnauthenticated {
		t.Errorf("an anonymous release = %v", err)
	}
}
