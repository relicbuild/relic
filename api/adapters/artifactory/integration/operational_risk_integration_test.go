//go:build integration

package integration

import (
	"crypto/sha256"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/scan"
)

const endOfLifeDump = `{"result":[
 {"name":"alpine-linux","label":"Alpine Linux","identifiers":[],
  "releases":[{"name":"3.17","eolFrom":"2024-11-22","isEol":true,"isMaintained":false,"latest":{"name":"3.17.10"}}]},
 {"name":"django","label":"Django","identifiers":[{"type":"purl","id":"pkg:pypi/django"}],
  "releases":[{"name":"4.2","eolFrom":"2026-04-30","isEol":true,"isMaintained":false,"latest":{"name":"4.2.25"}},
              {"name":"5.2","eolFrom":"2099-04-30","isEol":false,"isMaintained":true,"latest":{"name":"5.2.7"}}]}]}`

func TestOperationalRisk(t *testing.T) {
	t.Parallel()

	srv, db := serverDB(t)
	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/repositories/eol-libs",
		body:   `{"rclass":"local","packageType":"generic"}`,
	})
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/security/users/dev",
		body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
	})

	const content = "an application built on Django 4.2"

	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/eol-libs/app/app-1.0.tar.gz",
		body:   content,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %d %s", resp.StatusCode, body)
	}

	sum := sha256.Sum256([]byte(content))
	sha := hexSum(sum[:])

	if err := scan.NewResults(db).ImportSBOM(ctx, sha, "app-1.0.tar.gz", []byte(`{"bomFormat":"CycloneDX","components":[
		{"type":"library","name":"Django","version":"4.2.11","purl":"pkg:pypi/django@4.2.11"},
		{"type":"library","name":"requests","version":"2.32.0","purl":"pkg:pypi/requests@2.32.0"},
		{"type":"operating-system","name":"alpine","version":"3.17.4"}]}`)); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/api/v1/end-of-life-data",
		strings.NewReader(endOfLifeDump))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := relicHTTP("dev", "Dev-pass-12345").Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("import by a non-admin = %d, want 403", resp.StatusCode)
	}

	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/api/v1/end-of-life-data",
		body:   "not a dump",
	}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("import of a malformed dump = %d %s, want 400", resp.StatusCode, body)
	}

	if resp, body := do(t, srv, call{
		method: "PUT",
		path:   "/api/v1/end-of-life-data",
		body:   endOfLifeDump,
	}); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"products":2`) {
		t.Fatalf("import = %d %s", resp.StatusCode, body)
	}

	st, err := scans.GetEndOfLifeStatus(ctx, &relicv1.GetEndOfLifeStatusRequest{})
	if err != nil || st.GetSource() != endoflife.SourceImport || st.GetProducts() != 2 {
		t.Fatalf("status = %v, %v", st, err)
	}

	svc := scan.NewService(db, nil, scan.Syft{}, scan.Grype{}, scan.Limits{}, slog.New(slog.DiscardHandler))
	svc.SetOperationalRisk(endoflife.NewCatalog(db, nil))

	if n, err := svc.CheckOperationalRisks(ctx); err != nil || n == 0 {
		t.Fatalf("check = %d, %v", n, err)
	}

	item, err := scans.GetItemScan(ctx, &relicv1.GetItemScanRequest{
		Repository: "eol-libs",
		Path:       "app/app-1.0.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}

	byProduct := map[string]*relicv1.OperationalRisk{}
	for _, r := range item.GetScan().GetRisks() {
		byProduct[r.GetProduct()] = r
	}

	if r := byProduct["django"]; r == nil || r.GetKind() != endoflife.KindEndOfLife ||
		r.GetSeverity() != relicv1.Severity_SEVERITY_HIGH || r.GetCycle() != "4.2" || r.GetEolFrom() != "2026-04-30" {
		t.Errorf("django risk = %v", r)
	}

	if r := byProduct["alpine-linux"]; r == nil || r.GetCycle() != "3.17" {
		t.Errorf("alpine risk = %v", r)
	}

	if len(byProduct) != 2 {
		t.Errorf("risks = %v, want Django and Alpine only", item.GetScan().GetRisks())
	}

	list, err := scans.ListOperationalRisks(ctx, &relicv1.ListOperationalRisksRequest{
		Kind:  endoflife.KindEndOfLife,
		Query: "djan",
	})
	if err != nil || list.GetTotal() != 1 || list.GetRisks()[0].GetPath() != "app/app-1.0.tar.gz" ||
		list.GetRisks()[0].GetRepository() != "eol-libs" {
		t.Errorf("listing = %v, %v", list, err)
	}

	if _, err := scans.ListOperationalRisks(ctx, &relicv1.ListOperationalRisksRequest{
		Kind: "ancient",
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown kind: %v", err)
	}

	if _, err := scans.PutPolicy(ctx, &relicv1.PutPolicyRequest{
		Policy: &relicv1.Policy{
			Name: "no-end-of-life",
			Kind: relicv1.PolicyKind_POLICY_KIND_SECURITY,
			Rules: []*relicv1.PolicyRule{
				{
					Name:          "past-end-of-life",
					EndOfLife:     true,
					MinSeverity:   relicv1.Severity_SEVERITY_HIGH,
					BlockDownload: true,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := scans.PutWatch(ctx, &relicv1.PutWatchRequest{
		Watch: &relicv1.Watch{
			Name:   "eol",
			Active: true,
			Scopes: []*relicv1.WatchScope{
				{
					Kind: relicv1.WatchScopeKind_WATCH_SCOPE_KIND_REPOSITORY,
					Names: []string{
						"eol-libs",
					},
				},
			},
			Policies: []string{
				"no-end-of-life",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/eol-libs/app/app-1.0.tar.gz",
	}); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "end of life") {
		t.Errorf("download of an end-of-life artifact = %d %s, want blocked", resp.StatusCode, body)
	}

	checkDjango := func(version string) *relicv1.CheckPackageResponse {
		t.Helper()

		r, err := scans.CheckPackage(ctx, &relicv1.CheckPackageRequest{
			Purl:       "pkg:pypi/django@" + version,
			Repository: "eol-libs",
		})
		if err != nil {
			t.Fatal(err)
		}

		return r
	}

	if r := checkDjango("4.2.11"); r.GetAllowed() || len(r.GetRefusals()) != 1 ||
		r.GetRefusals()[0].GetRule() != "past-end-of-life" || len(r.GetRisks()) != 1 ||
		r.GetRisks()[0].GetKind() != endoflife.KindEndOfLife {
		t.Errorf("check of an end-of-life version = %v", r)
	}

	if r := checkDjango("5.2.7"); !r.GetAllowed() || len(r.GetRisks()) != 0 {
		t.Errorf("check of a maintained version = %v", r)
	}

	for _, purl := range []string{
		"pkg:pypi/django@4.2.11",
		"pkg:generic/alpine@3.17.4",
	} {
		if _, err := scans.CreateIgnoreRule(ctx, &relicv1.CreateIgnoreRuleRequest{
			Rule: &relicv1.IgnoreRule{
				Notes: "upgrade planned",
				Packages: []string{
					purl,
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if resp, body := do(t, srv, call{
		method: "GET",
		path:   "/artifactory/eol-libs/app/app-1.0.tar.gz",
	}); resp.StatusCode != http.StatusOK || body != content {
		t.Errorf("download with both components ignored = %d %s", resp.StatusCode, body)
	}

	if r := checkDjango("4.2.11"); !r.GetAllowed() || len(r.GetRisks()) != 1 {
		t.Errorf("check of an ignored end-of-life version = %v", r)
	}
}
