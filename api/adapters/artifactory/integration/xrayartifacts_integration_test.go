//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"relic/internal/platform/database"
)

func TestXrayArtifactAPIs(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/images",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)

	child := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:c"},"layers":[]}`
	sum := sha256.Sum256([]byte(child))
	childHex := hex.EncodeToString(sum[:])

	list := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:` + childHex + `","platform":{"os":"linux","architecture":"amd64"}}]}`
	for p, body := range map[string]string{
		"app/1.0/list.manifest.json":                 list,
		"app/sha256__" + childHex + "/manifest.json": child,
		"lib/2.0/manifest.json":                      `{"schemaVersion":2,"layers":[{"digest":"sha256:unscanned"}]}`,
		"docs/readme.txt":                            "not scannable",
	} {
		if resp, b := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/images/" + p,
				body:   body,
			},
		); resp.StatusCode != 201 {
			t.Fatalf("deploy %s = %d %s", p, resp.StatusCode, b)
		}
	}

	var sbom bytes.Buffer

	zw := gzip.NewWriter(&sbom)

	_, _ = zw.Write(
		[]byte(
			`{"artifacts":[{"name":"openssl","version":"3.0","type":"deb","purl":"pkg:deb/debian/openssl@3.0","licenses":[{"value":"Apache-2.0"}]}]}`,
		),
	)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	built := time.Now()
	if _, err := db.ClaimUnscanned(
		t.Context(),
		database.ClaimUnscannedParams{
			Now:         built,
			NamePattern: `^manifest\.json$`,
			MaxRows:     10,
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := db.FinishScanArtifact(
		t.Context(),
		database.FinishScanArtifactParams{
			Sha256:     childHex,
			Status:     "done",
			Sbom:       sbom.Bytes(),
			Components: 1,
			DbBuilt:    &built,
			Now:        built,
		},
	); err != nil {
		t.Fatal(err)
	}

	for _, f := range []struct {
		id, sev string
	}{
		{
			"CVE-2026-1",
			"Critical",
		},
		{
			"CVE-2026-2",
			"High",
		},
		{
			"CVE-2026-3",
			"High",
		},
	} {
		if err := db.InsertFinding(t.Context(), database.InsertFindingParams{
			Sha256:         childHex,
			VulnID:         f.id,
			Purl:           "pkg:deb/debian/openssl@3.0",
			PackageName:    "openssl",
			PackageVersion: "3.0",
			Severity:       f.sev,
			Summary:        "bad " + f.id,
			Cves: []string{
				f.id,
			},
			FixedVersions: []string{
				"3.1",
			},
			Urls: []string{},
			Cvss: []byte(`[{"version":"3.1","vector":"CVSS:3.1/AV:N","score":9.8}]`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	post := func(t *testing.T, path, body string, headers map[string]string) (int, string) {
		t.Helper()
		resp, b := do(
			t,
			srv,
			call{
				method:  "POST",
				path:    "/xray/api/" + path,
				body:    body,
				noAuth:  headers != nil,
				headers: headers,
			},
		)

		return resp.StatusCode, b
	}

	type status struct {
		Overall struct {
			Status, Time string
		} `json:"overall"`
		Details struct {
			Sca struct {
				Status string
			} `json:"sca"`
		} `json:"details"`
	}

	statusOf := func(t *testing.T, repoKey, path string) string {
		t.Helper()
		code, body := post(t, "v1/artifact/status", `{"repo":"`+repoKey+`","path":"`+path+`"}`, nil)

		var s status
		if code != 200 || json.Unmarshal([]byte(body), &s) != nil || s.Overall.Status != s.Details.Sca.Status {
			t.Fatalf("status of %s = %d %s", path, code, body)
		}

		return s.Overall.Status
	}

	t.Run("scanArtifact", func(t *testing.T) {
		if code, body := post(
			t,
			"v1/scanArtifact",
			`{"componentID":"docker://app:1.0","path":"images/app/1.0/list.manifest.json"}`,
			nil,
		); code != 200 {
			t.Errorf("= %d %s", code, body)
		}

		if code, _ := post(
			t,
			"v1/scanArtifact",
			`{"componentID":"docker://app:9","path":"images/app/9/manifest.json"}`,
			nil,
		); code != 404 {
			t.Errorf("missing path = %d", code)
		}
	})
	t.Run("artifact status", func(t *testing.T) {
		for path, want := range map[string]string{
			"app/1.0/list.manifest.json":                 "DONE",
			"app/sha256__" + childHex + "/manifest.json": "DONE",
			"lib/2.0/manifest.json":                      "SCANNING",
			"docs/readme.txt":                            "NOT_SUPPORTED",
		} {
			if got := statusOf(t, "images", path); got != want {
				t.Errorf("%s = %s, want %s", path, got, want)
			}
		}
	})

	type summary struct {
		Artifacts []struct {
			General struct {
				Sha256, Path string
			} `json:"general"`
			Issues []struct {
				IssueID  string `json:"issue_id"`
				Severity string `json:"severity"`
				Summary  string `json:"summary"`
				Cves     []struct {
					Cve    string `json:"cve"`
					CvssV3 string `json:"cvss_v3"`
				} `json:"cves"`
				Components []struct {
					ComponentID   string   `json:"component_id"`
					FixedVersions []string `json:"fixed_versions"`
				} `json:"components"`
			} `json:"issues"`
			Licenses []struct {
				Name string
			} `json:"licenses"`
		} `json:"artifacts"`
		Errors []struct {
			Identifier, Error string
		} `json:"errors"`
	}

	summaryOf := func(t *testing.T, body string, headers map[string]string) summary {
		t.Helper()
		code, b := post(t, "v2/summary/artifact", body, headers)

		var s summary
		if code != 200 || json.Unmarshal([]byte(b), &s) != nil {
			t.Fatalf("summary %s = %d %s", body, code, b)
		}

		return s
	}
	checkIssues := func(t *testing.T, s summary) {
		t.Helper()

		if len(s.Artifacts) != 1 || len(s.Errors) != 0 || s.Artifacts[0].General.Sha256 != childHex ||
			len(s.Artifacts[0].Issues) != 3 {
			t.Fatalf("summary = %+v", s)
		}

		is := s.Artifacts[0].Issues[0]
		if is.IssueID != "CVE-2026-1" || is.Severity != "Critical" || is.Summary == "" || len(is.Cves) != 1 ||
			is.Cves[0].Cve != "CVE-2026-1" ||
			!strings.HasPrefix(is.Cves[0].CvssV3, "9.8/CVSS:3.1/") ||
			len(is.Components) != 1 ||
			is.Components[0].FixedVersions[0] != "3.1" {
			t.Errorf("issue = %+v", is)
		}

		if len(s.Artifacts[0].Licenses) != 1 || s.Artifacts[0].Licenses[0].Name != "Apache-2.0" {
			t.Errorf("licenses = %+v", s.Artifacts[0].Licenses)
		}
	}
	t.Run("summary by checksum", func(t *testing.T) {
		checkIssues(t, summaryOf(t, `{"checksums":["`+childHex+`"]}`, nil))

		if s := summaryOf(t, `{"checksums":["`+strings.Repeat("0", 64)+`"]}`, nil); len(s.Artifacts) != 0 ||
			len(s.Errors) != 1 {
			t.Errorf("unknown checksum = %+v", s)
		}
	})
	t.Run("summary by path", func(t *testing.T) {
		checkIssues(t, summaryOf(t, `{"paths":["default/images/app/sha256__`+childHex+`/manifest.json"]}`, nil))

		for _, p := range []string{
			"default/images/lib/2.0/manifest.json",
			"images/app/sha256__" + childHex + "/manifest.json",
		} {
			if s := summaryOf(t, `{"paths":["`+p+`"]}`, nil); len(s.Artifacts) != 0 || len(s.Errors) != 1 {
				t.Errorf("%s = %+v", p, s)
			}
		}
	})
	t.Run("scans list", func(t *testing.T) {
		resp, body := do(
			t,
			srv,
			call{
				method: "GET",
				path:   "/xray/api/v1/artifacts?repo=images&num_of_rows=10&page_num=1&order_by=created&direction=desc&search=app",
			},
		)

		var got struct {
			Data []struct {
				RepoPath     string         `json:"repo_path"`
				RepoFullPath string         `json:"repo_full_path"`
				SecIssues    map[string]int `json:"sec_issues"`
				ScansStatus  status         `json:"scans_status"`
			} `json:"data"`
			Offset int `json:"offset"`
		}
		if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got.Data) != 1 ||
			got.Offset != -1 {
			t.Fatalf("= %d %s", resp.StatusCode, body)
		}

		row := got.Data[0]
		if row.RepoPath != "/app/sha256__"+childHex+"/manifest.json" || row.RepoFullPath != "images"+row.RepoPath ||
			row.SecIssues["critical"] != 1 ||
			row.SecIssues["high"] != 2 ||
			row.SecIssues["total"] != 3 ||
			row.ScansStatus.Overall.Status != "DONE" {
			t.Errorf("row = %+v", row)
		}
	})
	t.Run("scans list pages by offset", func(t *testing.T) {
		for q, want := range map[string]string{
			"num_of_rows=1":          `"offset": 1`,
			"num_of_rows=1&offset=1": `"offset": 2`,
			"num_of_rows=2&offset=1": `"offset": -1`,
			"num_of_rows=1&offset=5": `"offset": -1`,
		} {
			if resp, body := do(
				t,
				srv,
				call{
					method: "GET",
					path:   "/xray/api/v1/artifacts?repo=images&" + q,
				},
			); resp.StatusCode != 200 ||
				!strings.Contains(body, want) {
				t.Errorf("%s = %d, want %s:\n%s", q, resp.StatusCode, want, body)
			}
		}
	})
	t.Run("scanArtifact queues a failed scan again", func(t *testing.T) {
		if err := db.FinishScanArtifact(
			t.Context(),
			database.FinishScanArtifactParams{
				Sha256: childHex,
				Status: "failed",
				Reason: "blob not found",
				Now:    time.Now(),
			},
		); err != nil {
			t.Fatal(err)
		}

		if got := statusOf(t, "images", "app/1.0/list.manifest.json"); got != "FAILED" {
			t.Fatalf("before = %s", got)
		}

		if code, body := post(
			t,
			"v1/scanArtifact",
			`{"componentID":"docker://app:1.0","path":"images/app/1.0/list.manifest.json"}`,
			nil,
		); code != 200 {
			t.Fatalf("= %d %s", code, body)
		}

		if got := statusOf(t, "images", "app/1.0/list.manifest.json"); got != "PENDING" {
			t.Errorf("after = %s, want PENDING", got)
		}

		claimed, err := db.ClaimUnscanned(
			t.Context(),
			database.ClaimUnscannedParams{
				Now:         time.Now(),
				RetryBefore: time.Now().Add(-time.Hour),
				NamePattern: `^manifest\.json$`,
				MaxRows:     10,
			},
		)
		if err != nil || len(claimed) != 1 || claimed[0].Sha256 != childHex {
			t.Errorf("the sweep claimed %+v %v, want the failed manifest", claimed, err)
		}
	})
	t.Run("a caller without read sees nothing", func(t *testing.T) {
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/outsider",
				body:   `{"email":"o@example.com","password":"Outsider-pass-1"}`,
			},
		)

		as := map[string]string{
			"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("outsider:Outsider-pass-1")),
		}
		if code, _ := post(
			t,
			"v1/artifact/status",
			`{"repo":"images","path":"app/1.0/list.manifest.json"}`,
			as,
		); code != 403 {
			t.Errorf("status = %d", code)
		}

		if s := summaryOf(t, `{"checksums":["`+childHex+`"]}`, as); len(s.Artifacts) != 0 {
			t.Errorf("summary leaked %+v", s)
		}

		if resp, _ := do(
			t,
			srv,
			call{
				method:  "GET",
				path:    "/xray/api/v1/artifacts?repo=images&num_of_rows=10&page_num=1",
				noAuth:  true,
				headers: as,
			},
		); resp.StatusCode != 403 {
			t.Errorf("list = %d", resp.StatusCode)
		}
	})
}
