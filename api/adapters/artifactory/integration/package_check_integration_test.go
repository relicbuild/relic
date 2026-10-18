//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
	"relic/internal/scanning/scan"
)

func TestCheckPackage(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()

	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)

	var upstream *httptest.Server

	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pad" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"pad","dist-tags":{"latest":"2.0.0"},
		  "time":{"1.0.0":"`+old+`","1.1.0":"`+old+`","1.5.0":"`+old+`","2.0.0":"`+fresh+`"},
		  "versions":{
		    "1.0.0":{"dist":{"tarball":"`+upstream.URL+`/pad/-/pad-1.0.0.tgz"}},
		    "1.1.0":{"dist":{"tarball":"`+upstream.URL+`/pad/-/pad-1.1.0.tgz"}},
		    "1.5.0":{"dist":{"tarball":"`+upstream.URL+`/pad/-/pad-1.5.0.tgz"}},
		    "2.0.0":{"dist":{"tarball":"`+upstream.URL+`/pad/-/pad-2.0.0.tgz"}}}}`)
	}))
	t.Cleanup(upstream.Close)

	repos := relicv1connect.NewRepositoryServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if _, err := repos.CreateRepository(ctx, connect.NewRequest(&relicv1.CreateRepositoryRequest{
		Repository: &relicv1.Repository{
			Name:   "npm-checked",
			Class:  relicv1.RepositoryClass_REPOSITORY_CLASS_REMOTE,
			Format: relicv1.Format_FORMAT_NPM,
			Remote: &relicv1.RemoteRepositorySettings{
				Url:          upstream.URL,
				CooldownDays: 7,
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	if err := db.UpsertMaliciousPackage(ctx, database.UpsertMaliciousPackageParams{
		ID:        "MAL-2026-11",
		Ecosystem: "npm",
		Name:      "pad",
		Versions: []string{
			"1.5.0",
		},
		Ranges:   []byte(`[]`),
		Summary:  "steals tokens",
		Modified: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/pad-builds",
		body:   `{"rclass":"local","packageType":"generic"}`,
	})
	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/pad-builds/app-1.0.tgz",
		body:   "bundles pad 1.1.0",
	})

	claimed, err := db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         time.Now(),
		NamePattern: `^app-1\.0\.tgz$`,
		MaxRows:     1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v %v", claimed, err)
	}

	if err := db.FinishScanArtifact(ctx, database.FinishScanArtifactParams{
		Sha256: claimed[0].Sha256,
		Status: scan.StatusDone,
		Now:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertFinding(ctx, database.InsertFindingParams{
		Sha256:         claimed[0].Sha256,
		VulnID:         "CVE-2026-1",
		Purl:           "pkg:npm/pad@1.1.0",
		PackageName:    "pad",
		PackageVersion: "1.1.0",
		Severity:       string(scan.SeverityCritical),
		Summary:        "remote code execution",
		Cves:           []string{},
		FixedVersions:  []string{},
		Cvss:           []byte(`[]`),
		Urls:           []string{},
	}); err != nil {
		t.Fatal(err)
	}

	scans := relicv1connect.NewScanServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")
	if _, err := scans.PutPolicy(ctx, connect.NewRequest(&relicv1.PutPolicyRequest{
		Policy: &relicv1.Policy{
			Name: "no-critical",
			Kind: relicv1.PolicyKind_POLICY_KIND_SECURITY,
			Rules: []*relicv1.PolicyRule{
				{
					Name:        "critical",
					MinSeverity: relicv1.Severity_SEVERITY_CRITICAL,
					FailBuild:   true,
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := scans.PutWatch(ctx, connect.NewRequest(&relicv1.PutWatchRequest{
		Watch: &relicv1.Watch{
			Name:   "npm",
			Active: true,
			Scopes: []*relicv1.WatchScope{
				{
					Kind: relicv1.WatchScopeKind_WATCH_SCOPE_KIND_REPOSITORY,
					Names: []string{
						"npm-checked",
					},
				},
			},
			Policies: []string{
				"no-critical",
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	check := func(purl, repo string) (*relicv1.CheckPackageResponse, error) {
		r, err := scans.CheckPackage(ctx, connect.NewRequest(&relicv1.CheckPackageRequest{
			Purl:       purl,
			Repository: repo,
		}))
		if err != nil {
			return nil, err
		}

		return r.Msg, nil
	}

	kinds := func(r *relicv1.CheckPackageResponse) map[relicv1.PackageRefusalKind]bool {
		out := map[relicv1.PackageRefusalKind]bool{}
		for _, ref := range r.GetRefusals() {
			out[ref.GetKind()] = true
		}

		return out
	}

	tests := []struct {
		name, purl, repo string
		allowed          bool
		kinds            []relicv1.PackageRefusalKind
	}{
		{
			name:    "an old clean version",
			purl:    "pkg:npm/pad@1.0.0",
			repo:    "npm-checked",
			allowed: true,
		},
		{
			name: "a version cooling down",
			purl: "pkg:npm/pad@2.0.0",
			repo: "npm-checked",
			kinds: []relicv1.PackageRefusalKind{
				relicv1.PackageRefusalKind_PACKAGE_REFUSAL_KIND_NOT_OFFERED,
			},
		},
		{
			name: "a malicious version",
			purl: "pkg:npm/pad@1.5.0",
			repo: "npm-checked",
			kinds: []relicv1.PackageRefusalKind{
				relicv1.PackageRefusalKind_PACKAGE_REFUSAL_KIND_MALICIOUS,
				relicv1.PackageRefusalKind_PACKAGE_REFUSAL_KIND_NOT_OFFERED,
			},
		},
		{
			name: "a version with a finding a watch refuses",
			purl: "pkg:npm/pad@1.1.0",
			repo: "npm-checked",
			kinds: []relicv1.PackageRefusalKind{
				relicv1.PackageRefusalKind_PACKAGE_REFUSAL_KIND_POLICY,
			},
		},
		{
			name:    "a cooling-down version without a repository",
			purl:    "pkg:npm/pad@2.0.0",
			allowed: true,
		},
	}
	for _, tt := range tests {
		got, err := check(tt.purl, tt.repo)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}

		if got.GetAllowed() != tt.allowed {
			t.Errorf("%s: allowed = %v, want %v (%v)", tt.name, got.GetAllowed(), tt.allowed, got.GetRefusals())
		}

		k := kinds(got)
		if len(k) != len(tt.kinds) {
			t.Errorf("%s: refusals %v, want exactly the kinds %v", tt.name, got.GetRefusals(), tt.kinds)
		}

		for _, want := range tt.kinds {
			if !k[want] {
				t.Errorf("%s: refusals %v lack %s", tt.name, got.GetRefusals(), want)
			}
		}
	}

	if _, err := check("pkg:npm/pad", "npm-checked"); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a package URL without a version: %v", err)
	}

	if _, err := check("pkg:pypi/six@99.99.99", "npm-checked"); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a PyPI package checked against an npm repository: %v", err)
	}

	if _, err := check("pkg:npm/pad@1.0.0", "pad-builds"); err != nil {
		t.Errorf("an npm package checked against a generic repository: %v", err)
	}
}
