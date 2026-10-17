//go:build integration

package integration

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
	"relic/internal/scanning/scan"
)

func TestReachability(t *testing.T) {
	t.Parallel()

	srv, db := serverDB(t)
	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/go-bins",
		body:   `{"rclass":"local","packageType":"generic"}`,
	})

	const content = "a Go executable built with golang.org/x/text v0.3.6"

	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/go-bins/app-1.0",
		body:   content,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d %s", resp.StatusCode, body)
	}

	sum := sha256.Sum256([]byte(content))
	sha := hexSum(sum[:])

	if err := scan.NewResults(db).
		ImportSBOM(ctx, sha, "app-1.0", []byte(`{"bomFormat":"CycloneDX","components":[]}`)); err != nil {
		t.Fatal(err)
	}

	for _, f := range []struct {
		id, cve string
	}{
		{
			"GHSA-ppp9-7jff-5vj2",
			"CVE-2021-38561",
		},
		{
			"GHSA-69ch-w2m2-3vjp",
			"CVE-2022-32149",
		},
	} {
		if err := db.InsertFinding(ctx, database.InsertFindingParams{
			Sha256:         sha,
			VulnID:         f.id,
			Purl:           "pkg:golang/golang.org/x/text@v0.3.6",
			PackageName:    "golang.org/x/text",
			PackageVersion: "v0.3.6",
			Severity:       "high",
			Cves: []string{
				f.cve,
			},
			FixedVersions: []string{},
			Cvss:          []byte(`[]`),
			Urls:          []string{},
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, r := range []database.InsertReachabilityParams{
		{
			Sha256: sha,
			OsvID:  "GO-2021-0113",
			Module: "golang.org/x/text",
			Aliases: []string{
				"CVE-2021-38561",
				"GHSA-ppp9-7jff-5vj2",
			},
			Reachable: true,
			Trace:     "golang.org/x/text/language.Parse",
		},
		{
			Sha256: sha,
			OsvID:  "GO-2022-1059",
			Module: "golang.org/x/text",
			Aliases: []string{
				"CVE-2022-32149",
				"GHSA-69ch-w2m2-3vjp",
			},
		},
	} {
		if err := db.InsertReachability(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	item, err := scans.GetItemScan(ctx, connect.NewRequest(&relicv1.GetItemScanRequest{
		Repository: "go-bins",
		Path:       "app-1.0",
	}))
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]*relicv1.Finding{}
	for _, f := range item.Msg.GetScan().GetFindings() {
		got[f.GetId()] = f
	}

	if f := got["GHSA-ppp9-7jff-5vj2"]; f.GetReachability() != scan.Reachable ||
		f.GetReachabilityTrace() != "golang.org/x/text/language.Parse" {
		t.Errorf("the called vulnerability = %v", f)
	}

	if f := got["GHSA-69ch-w2m2-3vjp"]; f.GetReachability() != scan.NotReachable || f.GetReachabilityTrace() != "" {
		t.Errorf("the uncalled vulnerability = %v", f)
	}

	list, err := scans.ListFindings(ctx, connect.NewRequest(&relicv1.ListFindingsRequest{
		Repository: "go-bins",
	}))
	if err != nil || len(list.Msg.GetFindings()) != 2 {
		t.Fatalf("findings = %v, %v", list, err)
	}

	for _, f := range list.Msg.GetFindings() {
		if want := got[f.GetId()].GetReachability(); f.GetReachability() != want {
			t.Errorf("listed %s reachability = %q, want %q", f.GetId(), f.GetReachability(), want)
		}
	}

	vex, err := scans.ExportVex(ctx, connect.NewRequest(&relicv1.ExportVexRequest{
		Format:             relicv1.VexFormat_VEX_FORMAT_CYCLONEDX,
		IncludeUnreachable: true,
	}))
	if err != nil {
		t.Fatal(err)
	}

	if doc := vex.Msg.GetDocument(); !strings.Contains(doc, `"id": "GHSA-69ch-w2m2-3vjp"`) ||
		!strings.Contains(doc, `"justification": "code_not_reachable"`) ||
		!strings.Contains(doc, `"ref": "pkg:golang/golang.org/x/text@v0.3.6"`) ||
		strings.Contains(doc, "GHSA-ppp9-7jff-5vj2") {
		t.Errorf("VEX with unreachable findings = %s", doc)
	}

	if plain, err := scans.ExportVex(ctx, connect.NewRequest(&relicv1.ExportVexRequest{})); err != nil ||
		strings.Contains(plain.Msg.GetDocument(), "GHSA-69ch-w2m2-3vjp") {
		t.Errorf("VEX without asking for unreachable findings = %v %v", plain, err)
	}

	if _, err := scans.PutPolicy(ctx, connect.NewRequest(&relicv1.PutPolicyRequest{
		Policy: &relicv1.Policy{
			Name: "reachable-high",
			Kind: relicv1.PolicyKind_POLICY_KIND_SECURITY,
			Rules: []*relicv1.PolicyRule{
				{
					Name:              "high",
					MinSeverity:       relicv1.Severity_SEVERITY_HIGH,
					IgnoreUnreachable: true,
					BlockDownload:     true,
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := scans.PutWatch(ctx, connect.NewRequest(&relicv1.PutWatchRequest{
		Watch: &relicv1.Watch{
			Name:   "go",
			Active: true,
			Scopes: []*relicv1.WatchScope{
				{
					Kind: relicv1.WatchScopeKind_WATCH_SCOPE_KIND_REPOSITORY,
					Names: []string{
						"go-bins",
					},
				},
			},
			Policies: []string{
				"reachable-high",
			},
		},
	})); err != nil {
		t.Fatal(err)
	}

	resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/go-bins/app-1.0",
	})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "GHSA-ppp9-7jff-5vj2") {
		t.Errorf("download = %d %s, want blocked by the reachable vulnerability only", resp.StatusCode, body)
	}
}
