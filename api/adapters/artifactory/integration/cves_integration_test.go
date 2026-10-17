//go:build integration

package integration

import (
	"compress/gzip"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
	"relic/internal/scanning/cvecatalog"
)

func TestCVEIntelligence(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	do(
		t,
		srv,
		call{
			method: "PUT",
			path:   "/artifactory/api/repositories/libs",
			body:   `{"rclass":"local","packageType":"generic"}`,
		},
	)

	for _, p := range []string{
		"app-1.jar",
		"app-2.jar",
	} {
		if resp, b := do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/libs/" + p,
				body:   "content of " + p,
			},
		); resp.StatusCode != 201 {
			t.Fatalf("deploy %s = %d %s", p, resp.StatusCode, b)
		}
	}

	claimed, err := db.ClaimUnscanned(
		t.Context(),
		database.ClaimUnscannedParams{
			Now:         time.Now(),
			NamePattern: `\.jar$`,
			MaxRows:     10,
		},
	)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim = %v %v", claimed, err)
	}

	now := time.Now()
	for i, c := range claimed {
		if err := db.FinishScanArtifact(
			t.Context(),
			database.FinishScanArtifactParams{
				Sha256: c.Sha256,
				Status: "done",
				Now:    now,
			},
		); err != nil {
			t.Fatal(err)
		}

		findings := []database.InsertFindingParams{
			{
				VulnID: "GHSA-jfh8-c2jp-5v3q",
				Cves: []string{
					"CVE-2021-44228",
				},
				Severity: "critical",
			},
		}
		if i == 0 {
			findings = []database.InsertFindingParams{
				{
					VulnID: "CVE-2021-44228",
					Cves: []string{
						"CVE-2021-44228",
					},
					Severity: "critical",
				},
				{
					VulnID:   "CVE-2024-0001",
					Cves:     []string{},
					Severity: "low",
				},
			}
		}

		for _, f := range findings {
			f.Sha256, f.Purl, f.PackageName, f.PackageVersion = c.Sha256, "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1", "log4j-core", "2.14.1"

			f.FixedVersions, f.Urls, f.Cvss = []string{
				"2.17.1",
			}, []string{}, []byte(`[]`)
			if f.VulnID == "CVE-2024-0001" {
				f.FixedVersions = []string{}
			}

			if err := db.InsertFinding(t.Context(), f); err != nil {
				t.Fatal(err)
			}
		}
	}

	var epssHits, kevHits, recordHits atomic.Int64

	feeds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/epss.csv.gz":
			epssHits.Add(1)

			zw := gzip.NewWriter(w)
			_, _ = io.WriteString(
				zw,
				"#model_version:v1,score_date:2026-09-25T00:00:00Z\ncve,epss,percentile\nCVE-2021-44228,0.94358,0.99962\nCVE-2024-0001,0.0004,0.05\nCVE-1999-0001,0.1,0.5\n",
			)
			_ = zw.Close()
		case r.URL.Path == "/kev.json":
			kevHits.Add(1)

			_, _ = io.WriteString(
				w,
				`{"catalogVersion":"2026.09.25","vulnerabilities":[{"cveID":"CVE-2021-44228","dateAdded":"2021-12-10","dueDate":"2021-12-24","knownRansomwareCampaignUse":"Known","requiredAction":"Apply updates"}]}`,
			)
		case r.URL.Path == "/cve/CVE-2021-44228":
			recordHits.Add(1)

			_, _ = io.WriteString(
				w,
				`{"cveMetadata":{"cveId":"CVE-2021-44228","state":"PUBLISHED","datePublished":"2021-12-10T00:00:00Z"},"containers":{
				"cna":{"providerMetadata":{"shortName":"apache"},"title":"Log4Shell","descriptions":[{"lang":"en","value":"JNDI lookups"}],
				"problemTypes":[{"descriptions":[{"cweId":"CWE-502"}]}],"references":[{"url":"https://logging.apache.org"}]},
				"adp":[{"providerMetadata":{"shortName":"CISA-ADP"},"metrics":[{"cvssV3_1":{"baseScore":10,"vectorString":"CVSS:3.1/AV:N/AC:L"}},
				{"other":{"type":"ssvc","content":{"options":[{"Exploitation":"active"},{"Automatable":"yes"},{"Technical Impact":"total"}]}}}]}]}}`,
			)
		case strings.HasPrefix(r.URL.Path, "/cve/"):
			recordHits.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(feeds.Close)

	refresher := cvecatalog.NewRefresher(
		db,
		cvecatalog.Feeds{
			EPSS:    feeds.URL + "/epss.csv.gz",
			KEV:     feeds.URL + "/kev.json",
			Records: feeds.URL + "/cve",
		},
		feeds.Client(),
		slog.New(slog.DiscardHandler),
	)
	if err := refresher.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	t.Run("the list carries the facts", func(t *testing.T) {
		page, err := scans.ListCves(ctx, connect.NewRequest(&relicv1.ListCvesRequest{}))
		if err != nil || page.Msg.GetTotal() != 2 || len(page.Msg.GetCves()) != 2 {
			t.Fatalf("= %v %v", page, err)
		}

		top := page.Msg.GetCves()[0]
		if top.GetId() != "CVE-2021-44228" || top.GetTitle() != "Log4Shell" ||
			top.GetSeverity() != relicv1.Severity_SEVERITY_CRITICAL ||
			!top.GetKev() ||
			!top.GetRansomware() ||
			top.Epss == nil ||
			top.GetEpss() != 0.94358 ||
			top.CvssScore == nil ||
			top.GetCvssScore() != 10 ||
			top.GetExploitation() != "active" ||
			top.GetArtifacts() != 2 ||
			top.GetPackages() != 1 {
			t.Errorf("top = %v", top)
		}

		if low := page.Msg.GetCves()[1]; low.GetId() != "CVE-2024-0001" || low.GetKev() ||
			low.GetSeverity() != relicv1.Severity_SEVERITY_LOW ||
			low.GetArtifacts() != 1 ||
			low.GetTitle() != "" {
			t.Errorf("second = %v", low)
		}

		if f := page.Msg.GetFeeds(); len(f) != 2 || f[0].GetVersion() != "2026-09-25" ||
			f[1].GetVersion() != "2026.09.25" {
			t.Errorf("feeds = %v", f)
		}
	})
	t.Run("filters and sort", func(t *testing.T) {
		tests := []struct {
			name    string
			req     *relicv1.ListCvesRequest
			total   int64
			firstID string
		}{
			{
				"known exploited",
				&relicv1.ListCvesRequest{
					KevOnly: true,
				},
				1,
				"CVE-2021-44228",
			},
			{
				"EPSS floor",
				&relicv1.ListCvesRequest{
					MinEpss: 0.5,
				},
				1,
				"CVE-2021-44228",
			},
			{
				"severity",
				&relicv1.ListCvesRequest{
					Severity: relicv1.Severity_SEVERITY_LOW,
				},
				1,
				"CVE-2024-0001",
			},
			{
				"title search",
				&relicv1.ListCvesRequest{
					Query: "log4shell",
				},
				1,
				"CVE-2021-44228",
			},
			{
				"by id",
				&relicv1.ListCvesRequest{
					Sort:     "id",
					PageSize: 1,
				},
				2,
				"CVE-2024-0001",
			},
			{
				"most affected",
				&relicv1.ListCvesRequest{
					Sort: "affected",
				},
				2,
				"CVE-2021-44228",
			},
			{
				"no match",
				&relicv1.ListCvesRequest{
					Severity: relicv1.Severity_SEVERITY_HIGH,
				},
				0,
				"",
			},
			{
				"id search",
				&relicv1.ListCvesRequest{
					Query: "CVE-2024",
				},
				1,
				"CVE-2024-0001",
			},
			{
				"page past the end",
				&relicv1.ListCvesRequest{
					Page: math.MaxInt32,
				},
				2,
				"",
			},
		}
		for _, tc := range tests {
			page, err := scans.ListCves(ctx, connect.NewRequest(tc.req))

			first := ""
			if err == nil && len(page.Msg.GetCves()) > 0 {
				first = page.Msg.GetCves()[0].GetId()
			}

			if err != nil || page.Msg.GetTotal() != tc.total || first != tc.firstID {
				t.Errorf("%s: %v %v, want total %d first %q", tc.name, page, err, tc.total, tc.firstID)
			}
		}
	})
	t.Run("one CVE", func(t *testing.T) {
		got, err := scans.GetCve(ctx, connect.NewRequest(&relicv1.GetCveRequest{
			Id: "cve-2021-44228",
		}))
		if err != nil {
			t.Fatal(err)
		}

		d := got.Msg.GetCve()
		if d.GetDescription() != "JNDI lookups" || d.GetAutomatable() != "yes" || len(d.GetCwes()) != 1 ||
			len(d.GetReferences()) != 1 ||
			d.GetKevAdded() == nil ||
			d.GetKevAction() != "Apply updates" ||
			len(d.GetAffected()) != 2 {
			t.Errorf("detail = %v", d)
		}

		if _, err := scans.GetCve(
			ctx,
			connect.NewRequest(&relicv1.GetCveRequest{
				Id: "CVE-2000-1",
			}),
		); code(err) != connect.CodeNotFound {
			t.Errorf("unknown CVE: %v", err)
		}
	})
	t.Run("findings show EPSS and KEV", func(t *testing.T) {
		f, err := scans.ListFindings(ctx, connect.NewRequest(&relicv1.ListFindingsRequest{
			Query: "GHSA",
		}))
		if err != nil || len(f.Msg.GetFindings()) == 0 || f.Msg.GetFindings()[0].GetEpss() != 0.94358 ||
			!f.Msg.GetFindings()[0].GetKev() {
			t.Errorf("findings = %v %v", f, err)
		}
	})
	t.Run("findings filters", func(t *testing.T) {
		tests := []struct {
			name string
			req  *relicv1.ListFindingsRequest
			want int64
		}{
			{
				"all",
				&relicv1.ListFindingsRequest{},
				3,
			},
			{
				"known exploited only",
				&relicv1.ListFindingsRequest{
					KevOnly: true,
				},
				2,
			},
			{
				"fixable only",
				&relicv1.ListFindingsRequest{
					FixableOnly: true,
				},
				2,
			},
			{
				"repository",
				&relicv1.ListFindingsRequest{
					Repository: "libs",
				},
				3,
			},
			{
				"other repository",
				&relicv1.ListFindingsRequest{
					Repository: "nope",
				},
				0,
			},
			{
				"package type",
				&relicv1.ListFindingsRequest{
					PurlType: "maven",
				},
				3,
			},
			{
				"other package type",
				&relicv1.ListFindingsRequest{
					PurlType: "npm",
				},
				0,
			},
			{
				"combined",
				&relicv1.ListFindingsRequest{
					Severity:    relicv1.Severity_SEVERITY_LOW,
					FixableOnly: true,
				},
				0,
			},
		}
		for _, tc := range tests {
			f, err := scans.ListFindings(ctx, connect.NewRequest(tc.req))
			if err != nil || f.Msg.GetTotal() != tc.want || int64(len(f.Msg.GetFindings())) != tc.want {
				t.Errorf(
					"%s: total %d, %d rows, %v; want %d",
					tc.name,
					f.Msg.GetTotal(),
					len(f.Msg.GetFindings()),
					err,
					tc.want,
				)
			}
		}
	})
	t.Run("feeds are read only when due", func(t *testing.T) {
		e, k, rec := epssHits.Load(), kevHits.Load(), recordHits.Load()
		if e != 1 || k != 1 || rec != 2 {
			t.Fatalf("first run hits: epss %d kev %d records %d", e, k, rec)
		}

		if err := refresher.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}

		if epssHits.Load() != e || kevHits.Load() != k || recordHits.Load() != rec {
			t.Errorf(
				"second run went to the feeds again: epss %d kev %d records %d",
				epssHits.Load(),
				kevHits.Load(),
				recordHits.Load(),
			)
		}
	})
	t.Run("administrators only", func(t *testing.T) {
		//nolint:contextcheck // do sends with the subtest's context, through t
		do(
			t,
			srv,
			call{
				method: "PUT",
				path:   "/artifactory/api/security/users/viewer",
				body:   `{"email":"v@example.com","password":"Viewer-pass-1"}`,
			},
		)

		viewer := relicv1connect.NewScanServiceClient(relicHTTP("viewer", "Viewer-pass-1"), srv.URL+"/api")
		if _, err := viewer.ListCves(
			ctx,
			connect.NewRequest(&relicv1.ListCvesRequest{}),
		); code(err) != connect.CodePermissionDenied {
			t.Errorf("non-admin: %v", err)
		}
	})
}
