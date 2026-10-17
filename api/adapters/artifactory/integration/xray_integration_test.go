//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relic/api/adapters/artifactory/access"

	"relic/internal/access/auth"
	"relic/internal/app"
	"relic/internal/app/backgroundjobs"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/scanning/scan"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type syncQueue struct {
	scans *scan.Service
}

func (q syncQueue) Insert(
	ctx context.Context,
	args river.JobArgs,
	_ *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	a, ok := args.(backgroundjobs.GraphScanArgs)
	if !ok {
		return nil, errors.New("unexpected job " + args.Kind())
	}

	return &rivertype.JobInsertResult{}, q.scans.RunGraph(ctx, a.ID, true)
}

func xrayServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := databasetest.NewDB(t)

	log := slog.New(slog.DiscardHandler)
	if err := app.BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := auth.NewTokens(t.Context(), db, nil, access.Format{})
	if err != nil {
		t.Fatal(err)
	}

	report, _ := filepath.Abs("../../../../internal/scanning/scan/testdata/grype.json")

	grype := filepath.Join(t.TempDir(), "grype")

	//nolint:gosec // G306: an executable test script
	if err := os.WriteFile(
		grype,
		[]byte("#!/bin/sh\ncat "+report+"\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}

	blobs := objectstoretest.NewMemory()
	svc := scan.NewService(db, blobs, nil, scan.Grype{
		Exec: scan.Exec{
			Path: grype,
		},
	}, scan.Limits{}, log)
	srv := httptest.NewServer(
		app.NewHandler(db, blobs, tokens, syncQueue{
			scans: svc,
		}, app.Options{
			IndexerDir: indexerDir(t),
		}, log),
	)
	t.Cleanup(srv.Close)

	return srv
}

func TestXrayGraphScan(t *testing.T) {
	t.Parallel()

	srv := xrayServer(t)
	if _, body := do(t, srv, call{
		method: "GET",
		path:   "/xray/api/v1/system/version",
	}); !strings.Contains(
		body,
		`"xray_version": "3.131.18"`,
	) {
		t.Errorf("version = %s", body)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/xray/api/v1/entitlements/feature/contextual_analysis",
		},
	); resp.StatusCode != 200 ||
		!strings.Contains(body, `"entitled": false`) {
		t.Errorf("entitlement = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/xray/api/v1/xsc/system/version",
	}); resp.StatusCode != 404 {
		t.Errorf("xsc = %d, want 404", resp.StatusCode)
	}

	graph := `{"component_id":"root","nodes":[{"component_id":"npm://lodash:4.17.15","licenses":["MIT"]},{"component_id":"pypi://requests:2.19.0"},{"component_id":"generic://root"}]}`
	resp, body := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v1/scan/graph?scan_type=dependency",
		body:   graph,
	})

	var started struct {
		ScanID string `json:"scan_id"`
	}
	if resp.StatusCode != 201 || json.Unmarshal([]byte(body), &started) != nil || started.ScanID == "" {
		t.Fatalf("post graph = %d %s", resp.StatusCode, body)
	}

	resp, body = do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/xray/api/v1/scan/graph/" + started.ScanID + "?include_vulnerabilities=true&include_licenses=true",
		},
	)

	var res struct {
		Status          string
		Vulnerabilities []struct {
			IssueID  string `json:"issue_id"`
			Severity string
			CVEs     []struct {
				CVE         string
				CVSSV3Score string `json:"cvss_v3_score"`
			} `json:"cves"`
			Components map[string]struct {
				FixedVersions []string `json:"fixed_versions"`
			}
		}
		Licenses []struct {
			LicenseKey string `json:"license_key"`
			Components map[string]struct {
				ImpactPaths [][]struct {
					ComponentID string `json:"component_id"`
				} `json:"impact_paths"`
			}
		}
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &res) != nil || res.Status != "completed" ||
		len(res.Vulnerabilities) != 3 {
		t.Fatalf("result = %d %s", resp.StatusCode, body)
	}

	licensed := map[string]string{}

	for _, l := range res.Licenses {
		for id, c := range l.Components {
			licensed[id] = l.LicenseKey
			if len(c.ImpactPaths) == 0 {
				t.Errorf("license %s of %s has no impact path", l.LicenseKey, id)
			}
		}
	}

	if len(licensed) != 2 || licensed["npm://lodash:4.17.15"] != "MIT" ||
		licensed["pypi://requests:2.19.0"] != "Unknown" {
		t.Errorf("licenses = %+v, want lodash MIT and requests Unknown", res.Licenses)
	}

	for _, v := range res.Vulnerabilities {
		if !strings.HasPrefix(v.IssueID, "RELIC-") || len(v.Components) != 1 {
			t.Errorf("vulnerability %+v", v)
		}

		for id, c := range v.Components {
			if id != "npm://lodash:4.17.15" && id != "pypi://requests:2.19.0" {
				t.Errorf("component key %q is not a posted id", id)
			}

			for _, fv := range c.FixedVersions {
				if !strings.HasPrefix(fv, "[") {
					t.Errorf("fixed version %q not in range notation", fv)
				}
			}
		}

		switch v.Severity {
		case "Critical", "High", "Medium", "Low", "Unknown":
		default:
			t.Errorf("severity %q", v.Severity)
		}
	}

	if _, body := do(t, srv, call{
		method: "GET",
		path:   "/xray/api/v1/scan/graph/" + started.ScanID,
	}); strings.Contains(
		body,
		"vulnerabilities",
	) ||
		strings.Contains(body, "licenses") {
		t.Errorf("vulnerabilities or licenses without asking: %s", body)
	}

	if resp, body := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/xray/api/v1/scan/graph?watch=w1&scan_type=dependency",
			body:   graph,
		},
	); resp.StatusCode != 400 ||
		!strings.Contains(body, `"error"`) {
		t.Errorf("unknown watch = %d %s", resp.StatusCode, body)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/xray/api/v1/scan/graph/00000000-0000-0000-0000-000000000000",
		},
	); resp.StatusCode != 404 {
		t.Errorf("unknown scan = %d", resp.StatusCode)
	}
}

func TestXrayBuildScan(t *testing.T) {
	t.Parallel()
	srv := xrayServer(t)
	body := `{"version":"1.0.1","name":"app","number":"7","started":"2026-09-23T10:00:00.000+0000",
		"modules":[{"type":"npm","id":"app:1.0.0","dependencies":[{"id":"lodash:4.17.15"},{"id":"left-pad:1.3.0"}]}]}`

	req := `{"build_name":"app","build_number":"7"}`
	if resp, got := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/xray/api/v2/ci/build",
			body:   req,
		},
	); resp.StatusCode != 404 ||
		!strings.Contains(got, "Build app number 7 was not found") {
		t.Errorf("unpublished build = %d %s", resp.StatusCode, got)
	}

	if resp, got := do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/build",
			body:   body,
		},
	); resp.StatusCode != 204 {
		t.Fatalf("publish = %d %s", resp.StatusCode, got)
	}

	resp, got := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/ci/build",
		body:   req,
	})
	if resp.StatusCode != 200 ||
		!strings.Contains(
			got,
			"No Xray “Fail build in case of a violation” policy rule has been defined on this build",
		) {
		t.Fatalf("trigger = %d %s", resp.StatusCode, got)
	}

	resp, got = do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/xray/api/v2/ci/build/scanResult?include_vulnerabilities=true",
			body:   req,
		},
	)

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
	if resp.StatusCode != 200 || json.Unmarshal([]byte(got), &res) != nil || res.Status != "completed" ||
		res.FailBuild ||
		len(res.Vulnerabilities) != 2 {
		t.Fatalf("scan result = %d %s", resp.StatusCode, got)
	}

	c, ok := res.Vulnerabilities[0].Components["npm://lodash:4.17.15"]
	if !ok || len(c.ImpactPaths) != 1 || c.ImpactPaths[0][0].ComponentID != "build://app:7" {
		t.Errorf("components = %+v", res.Vulnerabilities[0].Components)
	}
}

func TestXrayPoliciesAndViolations(t *testing.T) {
	t.Parallel()
	srv := xrayServer(t)

	pol := `{"name":"sec","type":"security","rules":[{"name":"any","priority":1,"criteria":{"min_severity":"Low"},"actions":{"fail_build":true}}]}`
	if resp, got := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/policies",
		body:   pol,
	}); resp.StatusCode != 201 {
		t.Fatalf("create policy = %d %s", resp.StatusCode, got)
	}

	if resp, _ := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/policies",
		body:   pol,
	}); resp.StatusCode != 409 {
		t.Errorf("duplicate policy = %d", resp.StatusCode)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/xray/api/v2/policies",
			body:   `{"name":"bad","type":"security","rules":[{"name":"r","criteria":{}}]}`,
		},
	); resp.StatusCode != 400 {
		t.Errorf("invalid policy = %d", resp.StatusCode)
	}

	for _, c := range []call{
		{
			method: "PUT",
			path:   "/xray/api/v2/policies/missing",
			body:   strings.Replace(pol, `"name":"sec"`, `"name":"missing"`, 1),
		},
		{
			method: "PUT",
			path:   "/xray/api/v2/watches/missing",
			body:   `{"general_data":{"name":"missing","active":true},"assigned_policies":[{"name":"sec","type":"security"}]}`,
		},
	} {
		if resp, got := do(t, srv, c); resp.StatusCode != 404 {
			t.Errorf("%s %s = %d %s, want 404", c.method, c.path, resp.StatusCode, got)
		}
	}

	if resp, _ := do(t, srv, call{
		method: "GET",
		path:   "/xray/api/v2/policies/missing",
	}); resp.StatusCode != 404 {
		t.Errorf("a policy updated before it existed was created: %d", resp.StatusCode)
	}

	watch := `{"general_data":{"name":"w1","active":true},"project_resources":{"resources":[{"type":"build","name":"app"},{"type":"repository","name":"libs"}]},"assigned_policies":[{"name":"sec","type":"security"}]}`
	if resp, got := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/xray/api/v2/watches",
			body:   watch,
		},
	); resp.StatusCode != 201 {
		t.Fatalf("create watch = %d %s", resp.StatusCode, got)
	}

	if resp, _ := do(t, srv, call{
		method: "DELETE",
		path:   "/xray/api/v2/policies/sec",
	}); resp.StatusCode != 400 {
		t.Errorf("deleting an assigned policy = %d, want 400", resp.StatusCode)
	}

	graph := `{"component_id":"root","nodes":[{"component_id":"npm://lodash:4.17.15"}]}`

	type result struct {
		FailBuild  bool `json:"fail_build"`
		Violations []struct {
			WatchName string `json:"watch_name"`
			FailBuild bool   `json:"fail_build"`
			Type      string
			Policies  []struct {
				Policy, Rule string
			}
		}
	}

	for _, ctx := range []string{
		"watch=w1",
		"repo_path=libs/",
	} {
		_, body := do(
			t,
			srv,
			call{
				method: "POST",
				path:   "/xray/api/v1/scan/graph?" + ctx + "&scan_type=dependency",
				body:   graph,
			},
		)

		var started struct {
			ScanID string `json:"scan_id"`
		}

		_ = json.Unmarshal([]byte(body), &started)
		_, body = do(t, srv, call{
			method: "GET",
			path:   "/xray/api/v1/scan/graph/" + started.ScanID,
		})

		var res result
		if json.Unmarshal([]byte(body), &res) != nil || len(res.Violations) != 2 {
			t.Fatalf("%s: violations = %s", ctx, body)
		}

		v := res.Violations[0]
		if v.WatchName != "w1" || !v.FailBuild || v.Type != "security" || len(v.Policies) != 1 ||
			v.Policies[0].Policy != "sec" {
			t.Errorf("%s: violation = %+v", ctx, v)
		}
	}

	body := `{"version":"1.0.1","name":"app","number":"1","started":"2026-09-23T10:00:00.000+0000","modules":[{"type":"npm","id":"app:1","dependencies":[{"id":"lodash:4.17.15"}]}]}`
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/api/build",
		body:   body,
	})

	req := `{"build_name":"app","build_number":"1"}`
	if _, got := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/ci/build",
		body:   req,
	}); !strings.Contains(
		got,
		"Scanning build app number 1.",
	) {
		t.Errorf("trigger with a fail-build watch = %s", got)
	}

	_, got := do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/ci/build/scanResult",
		body:   req,
	})

	var res result
	if json.Unmarshal([]byte(got), &res) != nil || !res.FailBuild || len(res.Violations) != 2 {
		t.Errorf("build scan result = %s", got)
	}

	rule := `{"notes":"accepted risk","ignore_filters":{"components":[{"name":"npm://lodash","version":"4.17.15"}],"watches":["w1"]}}`
	if resp, got := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/xray/api/v1/ignore_rules",
			body:   rule,
		},
	); resp.StatusCode != 201 ||
		!strings.Contains(got, "Ignore rule created, id: ") {
		t.Fatalf("create ignore rule = %d %s", resp.StatusCode, got)
	}

	if resp, _ := do(
		t,
		srv,
		call{
			method: "POST",
			path:   "/xray/api/v1/ignore_rules",
			body:   `{"ignore_filters":{}}`,
		},
	); resp.StatusCode != 400 {
		t.Errorf("empty ignore rule = %d", resp.StatusCode)
	}

	_, got = do(t, srv, call{
		method: "POST",
		path:   "/xray/api/v2/ci/build/scanResult",
		body:   req,
	})

	res = result{}
	if json.Unmarshal([]byte(got), &res) != nil || res.FailBuild || len(res.Violations) != 0 {
		t.Errorf("build scan result with ignore rule = %s", got)
	}

	if _, got := do(t, srv, call{
		method: "GET",
		path:   "/xray/api/v1/ignore_rules",
	}); !strings.Contains(
		got,
		`"total_count": 1`,
	) {
		t.Errorf("list ignore rules = %s", got)
	}
}

func indexerDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "relic-indexer-linux-amd64"),
		[]byte("indexer binary"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestXrayIndexerDownload(t *testing.T) {
	t.Parallel()

	srv := xrayServer(t)
	if resp, got := do(
		t,
		srv,
		call{
			method: "GET",
			path:   "/xray/api/v1/indexer-resources/download/linux/amd64",
		},
	); resp.StatusCode != 200 ||
		got != "indexer binary" {
		t.Errorf("download = %d %q", resp.StatusCode, got)
	}

	for _, p := range []string{
		"darwin/arm64",
		"..%2F..%2Fetc/passwd",
		"linux/amd64..%2Fx",
	} {
		if resp, _ := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/xray/api/v1/indexer-resources/download/" + p,
			},
		); resp.StatusCode != 404 {
			t.Errorf("%s = %d, want 404", p, resp.StatusCode)
		}
	}
}
