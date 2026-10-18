//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestRelicAPIBuildScan(t *testing.T) {
	t.Parallel()

	srv := xrayServer(t)
	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	anon := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("", ""), srv.URL))
	run := &relicv1.ScanBuildRunRequest{
		Name:   "app",
		Number: "7",
	}

	if _, err := scans.ScanBuildRun(ctx, run); code(err) != connect.CodeNotFound {
		t.Errorf("scan of an unpublished run: %v", err)
	}

	if resp, got := do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/build",
		body: `{"version":"1.0.1","name":"app","number":"7","started":"2026-09-23T10:00:00.000+0000",
			"modules":[{"type":"npm","id":"app:1.0.0","dependencies":[{"id":"lodash:4.17.15"},{"id":"left-pad:1.3.0"}]}]}`,
	}); resp.StatusCode != 204 {
		t.Fatalf("publish = %d %s", resp.StatusCode, got)
	}

	for _, c := range []call{
		{
			method: "POST",
			path:   "/xray/api/v2/policies",
			body:   `{"name":"sec","type":"security","rules":[{"name":"any","priority":1,"criteria":{"min_severity":"Low"},"actions":{"fail_build":true}}]}`,
		},
		{
			method: "POST",
			path:   "/xray/api/v2/watches",
			body:   `{"general_data":{"name":"w1","active":true},"project_resources":{"resources":[{"type":"build","name":"app"}]},"assigned_policies":[{"name":"sec","type":"security"}]}`,
		},
	} {
		if resp, got := do(t, srv, c); resp.StatusCode != 201 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, got)
		}
	}

	if _, err := anon.ScanBuildRun(ctx, run); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous scan: %v", err)
	}

	if _, err := scans.ScanBuildRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	got, err := scans.GetBuildRunScan(ctx, &relicv1.GetBuildRunScanRequest{
		Name:   "app",
		Number: "7",
	})
	if err != nil {
		t.Fatal(err)
	}

	s := got.GetScan()
	if s.GetStatus() != relicv1.ScanStatus_SCAN_STATUS_DONE || len(s.GetFindings()) != 2 || s.GetComponents() != 2 {
		t.Fatalf("scan = %v", s)
	}

	if !got.GetFailBuild() || len(got.GetViolations()) != 2 ||
		got.GetViolations()[0].GetWatch() != "w1" || got.GetViolations()[0].GetPolicy() != "sec" {
		t.Errorf("violations = %v, fail build %v", got.GetViolations(), got.GetFailBuild())
	}

	resp, body := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/ci/build/scanResult?include_vulnerabilities=true",
		body:   `{"build_name":"app","build_number":"7"}`,
	})

	var res struct {
		Status          string
		FailBuild       bool `json:"fail_build"`
		Vulnerabilities []struct {
			Components map[string]struct {
				ImpactPaths [][]struct {
					ComponentID string `json:"component_id"`
				} `json:"impact_paths"`
			}
		}
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &res) != nil || res.Status != "completed" ||
		!res.FailBuild || len(res.Vulnerabilities) != 2 {
		t.Fatalf("jf scan result = %d %s", resp.StatusCode, body)
	}

	if c := res.Vulnerabilities[0].Components; len(c) != 1 ||
		!strings.Contains(body, `"component_id": "build://app:7"`) {
		t.Errorf("components = %+v", c)
	}

	decisions := relicv1connect.NewPolicyDecisionServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	recorded, err := decisions.ListPolicyDecisions(ctx, &relicv1.ListPolicyDecisionsRequest{
		Kind: relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_BUILD,
	})
	if err != nil {
		t.Fatal(err)
	}

	if d := recorded.GetDecisions(); len(d) != 1 || d[0].GetSubject() != "app/7" || d[0].GetPolicy() != "sec" ||
		d[0].GetWatch() != "w1" || d[0].GetRule() != "any" || d[0].GetCount() != 4 {
		t.Errorf("build decisions = %v", d)
	}

	for _, c := range []call{
		{
			method: "POST",
			path:   "/xray/api/v2/policies",
			body: `{"name":"notice","type":"security","rules":[{"name":"note","priority":1,"criteria":{"min_severity":"Low"},
				"actions":{"fail_build":false,"block_download":{"active":true,"unscanned":false}}}]}`,
		},
		{
			method: "POST",
			path:   "/xray/api/v2/watches",
			body: `{"general_data":{"name":"w2","active":true},"project_resources":{"resources":[{"type":"build","name":"app"}]},
				"assigned_policies":[{"name":"sec","type":"security"},{"name":"notice","type":"security"}]}`,
		},
	} {
		if resp, got := do(t, srv, c); resp.StatusCode != 201 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, got)
		}
	}

	if resp, body := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/ci/build/scanResult",
		body:   `{"build_name":"app","build_number":"7"}`,
	}); resp.StatusCode != 200 {
		t.Fatalf("jf scan result with a second watch = %d %s", resp.StatusCode, body)
	}

	recorded, err = decisions.ListPolicyDecisions(ctx, &relicv1.ListPolicyDecisionsRequest{
		Kind: relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_BUILD,
	})
	if err != nil {
		t.Fatal(err)
	}

	watches := map[string]bool{}
	for _, d := range recorded.GetDecisions() {
		watches[d.GetWatch()] = true
		if d.GetPolicy() == "notice" {
			t.Errorf("a policy without fail build was recorded as failing the build: %v", d)
		}
	}

	if !watches["w2"] {
		t.Errorf("build decisions %v lack the second watch", recorded.GetDecisions())
	}
}

func TestRelicAPIPromoteContainerImage(t *testing.T) {
	t.Parallel()

	srv := server(t)
	for _, repo := range []string{
		"img-dev",
		"img-prod",
	} {
		do(t, srv, call{
			method: "PUT",
			path:   "/artifactory/api/repositories/" + repo,
			body:   `{"rclass":"local","packageType":"docker"}`,
		})
	}

	config := pushBlob(t, srv, "img-dev/team/app", "{}")
	layer := pushBlob(t, srv, "img-dev/team/app", "layer")

	for _, tag := range []string{
		"1.0",
		"1.1",
	} {
		if resp, got := do(t, srv, call{
			method: "PUT",
			path:   "/v2/img-dev/team/app/manifests/" + tag,
			body:   manifestJSON(config, layer),
			headers: map[string]string{
				"Content-Type": "application/vnd.docker.distribution.manifest.v2+json",
			},
		}); resp.StatusCode != 201 {
			t.Fatalf("push %s = %d %s", tag, resp.StatusCode, got)
		}
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/security/users/dev",
		body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
	})

	ctx := t.Context()
	admin := relicv1connect.NewPackageServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	dev := relicv1connect.NewPackageServiceClient(apiClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL))
	manifest := func(path string) int {
		resp, _ := do(t, srv, call{
			method: "GET",
			path:   "/v2/" + path,
			headers: map[string]string{
				"Accept": "application/vnd.docker.distribution.manifest.v2+json",
			},
		})

		return resp.StatusCode
	}

	promote := &relicv1.PromoteContainerImageRequest{
		Repository:       "img-dev",
		Image:            "team/app",
		Tag:              "1.0",
		TargetRepository: "img-prod",
		TargetTag:        "stable",
		Copy:             true,
	}
	if _, err := dev.PromoteContainerImage(ctx, promote); code(err) != connect.CodeNotFound &&
		code(err) != connect.CodePermissionDenied {
		t.Errorf("promotion without permission: %v", err)
	}

	if _, err := admin.PromoteContainerImage(ctx, promote); err != nil {
		t.Fatal(err)
	}

	if manifest("img-prod/team/app/manifests/stable") != 200 || manifest("img-dev/team/app/manifests/1.0") != 200 {
		t.Error("copy: the target tag is missing or the source is gone")
	}

	if _, err := admin.PromoteContainerImage(ctx, &relicv1.PromoteContainerImageRequest{
		Repository:       "img-dev",
		Image:            "team/app",
		TargetRepository: "img-prod",
		TargetImage:      "release/app",
	}); err != nil {
		t.Fatal(err)
	}

	for _, tag := range []string{
		"1.0",
		"1.1",
	} {
		if manifest("img-prod/release/app/manifests/"+tag) != 200 ||
			manifest("img-dev/team/app/manifests/"+tag) != 404 {
			t.Errorf("move: tag %s did not move", tag)
		}
	}

	for name, req := range map[string]*relicv1.PromoteContainerImageRequest{
		"missing tag": {
			Repository:       "img-prod",
			Image:            "release/app",
			Tag:              "9.9",
			TargetRepository: "img-dev",
		},
		"invalid image": {
			Repository:       "img-prod",
			Image:            "Bad Image",
			TargetRepository: "img-dev",
		},
	} {
		if _, err := admin.PromoteContainerImage(ctx, req); err == nil {
			t.Errorf("%s: promoted", name)
		}
	}
}

func TestRelicAPIPackageScan(t *testing.T) {
	t.Parallel()

	srv := xrayServer(t)
	ctx := t.Context()
	admin := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/security/users/dev",
		body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
	})

	dev := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL))

	for _, c := range []call{
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-remote",
			body:   `{"rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"}`,
		},
		{
			method: "PUT",
			path:   "/artifactory/api/repositories/npm-private",
			body:   `{"rclass":"local","packageType":"npm"}`,
		},
		{
			method: "POST",
			path:   "/artifactory/api/v2/security/permissions/dev-npm-remote",
			body:   `{"name":"dev-npm-remote","repo":{"repositories":["npm-remote"],"actions":{"users":{"dev":["read"]}}}}`,
		},
		{
			method: "POST",
			path:   "/xray/api/v2/policies",
			body:   `{"name":"block","type":"security","rules":[{"name":"high","priority":1,"criteria":{"min_severity":"High"},"actions":{"block_download":{"active":true}}}]}`,
		},
		{
			method: "POST",
			path:   "/xray/api/v2/watches",
			body:   `{"general_data":{"name":"remote-watch","active":true},"project_resources":{"resources":[{"type":"repository","name":"npm-remote"}]},"assigned_policies":[{"name":"block","type":"security"}]}`,
		},
	} {
		if resp, got := do(t, srv, c); resp.StatusCode >= 300 {
			t.Fatalf("%s %s = %d %s", c.method, c.path, resp.StatusCode, got)
		}
	}

	lodash := []string{
		"pkg:npm/lodash@4.17.15",
	}
	for name, req := range map[string]*relicv1.ScanPackagesRequest{
		"none": {},
		"invalid": {
			Purls: []string{
				"lodash@4",
			},
		},
		"unknown watch": {
			Purls: lodash,
			Watches: []string{
				"remote-wach",
			},
		},
		"unknown repository": {
			Purls:      lodash,
			Repository: "npm-remot",
		},
	} {
		if _, err := dev.ScanPackages(ctx, req); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}

	for _, repo := range []string{
		"npm-missing",
		"npm-private",
	} {
		_, err := dev.ScanPackages(ctx, &relicv1.ScanPackagesRequest{
			Purls:      lodash,
			Repository: repo,
		})
		if want := `invalid_argument: repository "` + repo + `" does not exist`; err == nil || err.Error() != want {
			t.Errorf("scan scoped to %s = %v, want %s", repo, err, want)
		}
	}

	queued, err := dev.ScanPackages(ctx, &relicv1.ScanPackagesRequest{
		Purls: []string{
			"pkg:npm/lodash@4.17.15",
			"pkg:npm/left-pad@1.3.0",
		},
		Repository: "npm-remote",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := dev.GetPackagesScan(ctx, &relicv1.GetPackagesScanRequest{
		Id: queued.GetId(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if s := got.GetScan(); s.GetStatus() != relicv1.ScanStatus_SCAN_STATUS_DONE || len(s.GetFindings()) != 2 {
		t.Fatalf("scan = %v", s)
	}

	if !got.GetBlockDownload() || got.GetFailBuild() || len(got.GetViolations()) != 2 ||
		got.GetViolations()[0].GetWatch() != "remote-watch" {
		t.Errorf("violations = %v", got.GetViolations())
	}

	queued, err = dev.ScanPackages(ctx, &relicv1.ScanPackagesRequest{
		Purls: []string{
			"pkg:npm/lodash@4.17.15",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err = dev.GetPackagesScan(ctx, &relicv1.GetPackagesScanRequest{
		Id: queued.GetId(),
	})
	if err != nil || len(got.GetScan().GetFindings()) != 2 || len(got.GetViolations()) != 0 {
		t.Errorf("unscoped scan = %v %v", got, err)
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/security/users/other",
		body:   `{"email":"other@example.com","password":"Other-pass-12345"}`,
	})

	other := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("other", "Other-pass-12345"), srv.URL))
	if _, err := other.GetPackagesScan(ctx, &relicv1.GetPackagesScanRequest{
		Id: queued.GetId(),
	}); code(err) != connect.CodeNotFound {
		t.Errorf("another user's scan: %v", err)
	}

	if _, err := admin.GetPackagesScan(ctx, &relicv1.GetPackagesScanRequest{
		Id: queued.GetId(),
	}); err != nil {
		t.Errorf("administrator: %v", err)
	}
}

func TestRelicAPIVex(t *testing.T) {
	t.Parallel()

	srv := server(t)
	ctx := t.Context()
	admin := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	if _, err := admin.CreateIgnoreRule(ctx, &relicv1.CreateIgnoreRuleRequest{
		Rule: &relicv1.IgnoreRule{
			Notes: "not reachable",
			Cves: []string{
				"CVE-2021-23337",
			},
			Packages: []string{
				"pkg:npm/lodash@4.17.15",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	exported, err := admin.ExportVex(ctx, &relicv1.ExportVexRequest{})
	if err != nil || !strings.Contains(exported.GetDocument(), `"status": "not_affected"`) ||
		!strings.Contains(exported.GetDocument(), `"@id": "pkg:npm/lodash@4.17.15"`) {
		t.Fatalf("export = %v %v", exported, err)
	}

	imported, err := admin.ImportVex(ctx, &relicv1.ImportVexRequest{
		Document: exported.GetDocument(),
	})
	if err != nil || len(imported.GetRules()) != 1 ||
		!strings.Contains(imported.GetRules()[0].GetNotes(), "VEX urn:uuid:") {
		t.Fatalf("import = %v %v", imported, err)
	}

	list, err := admin.ListIgnoreRules(ctx, &relicv1.ListIgnoreRulesRequest{})
	if err != nil || len(list.GetRules()) != 2 {
		t.Errorf("rules = %v %v", list, err)
	}

	if _, err := admin.ImportVex(ctx, &relicv1.ImportVexRequest{
		Document: `{"bomFormat": "CycloneDX"}`,
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a CycloneDX document without vulnerabilities: %v", err)
	}

	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/security/users/dev",
		body:   `{"email":"dev@example.com","password":"Dev-pass-12345"}`,
	})

	dev := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("dev", "Dev-pass-12345"), srv.URL))
	if _, err := dev.ExportVex(
		ctx,
		&relicv1.ExportVexRequest{},
	); code(
		err,
	) != connect.CodePermissionDenied {
		t.Errorf("a non-administrator exported: %v", err)
	}
}

func TestRelicAPIPolicyCondition(t *testing.T) {
	t.Parallel()

	srv := xrayServer(t)
	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	put := func(condition string) error {
		_, err := scans.PutPolicy(ctx, &relicv1.PutPolicyRequest{
			Policy: &relicv1.Policy{
				Name: "cond",
				Kind: relicv1.PolicyKind_POLICY_KIND_SECURITY,
				Rules: []*relicv1.PolicyRule{
					{
						Name:      "not-lodash",
						Priority:  1,
						Condition: condition,
						FailBuild: true,
					},
				},
			},
		})

		return err
	}

	if err := put(`severity ==`); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an invalid condition: %v", err)
	}

	if err := put(`severity == "high" && !purl.startsWith("pkg:npm/lodash@")`); err != nil {
		t.Fatal(err)
	}

	list, err := scans.ListPolicies(ctx, &relicv1.ListPoliciesRequest{})
	if err != nil || len(list.GetPolicies()) != 1 ||
		list.GetPolicies()[0].GetRules()[0].GetCondition() != `severity == "high" && !purl.startsWith("pkg:npm/lodash@")` {
		t.Fatalf("policies = %v %v", list, err)
	}

	if _, err := scans.PutWatch(ctx, &relicv1.PutWatchRequest{
		Watch: &relicv1.Watch{
			Name:   "cond-watch",
			Active: true,
			Policies: []string{
				"cond",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	queued, err := scans.ScanPackages(ctx, &relicv1.ScanPackagesRequest{
		Purls: []string{
			"pkg:npm/lodash@4.17.15",
			"pkg:pypi/requests@2.19.0",
		},
		Watches: []string{
			"cond-watch",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := scans.GetPackagesScan(ctx, &relicv1.GetPackagesScanRequest{
		Id: queued.GetId(),
	})
	if err != nil {
		t.Fatal(err)
	}

	v := got.GetViolations()
	if len(v) != 1 || v[0].GetPurl() != "pkg:pypi/requests@2.19.0" || !got.GetFailBuild() {
		t.Errorf("violations = %v", v)
	}
}
