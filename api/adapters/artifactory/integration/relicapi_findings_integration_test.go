//go:build integration

package integration

import (
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
)

type seededFinding struct {
	id       string
	severity string
	pkg      string
}

func deployWithFindings(
	t *testing.T,
	srv *httptest.Server,
	db *database.DB,
	path, content string,
	findings []seededFinding,
) {
	t.Helper()

	ctx := t.Context()
	do(t, srv, call{
		method: "PUT",
		path:   "/artifactory/libs/" + path,
		body:   content,
	})

	claimed, err := db.ClaimUnscanned(
		ctx,
		database.ClaimUnscannedParams{
			Now:         time.Now(),
			NamePattern: `.*`,
			MaxRows:     10,
		},
	)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim %s = %v %v", path, claimed, err)
	}

	for _, f := range findings {
		if err := db.InsertFinding(ctx, database.InsertFindingParams{
			Sha256:         claimed[0].Sha256,
			VulnID:         f.id,
			Purl:           "pkg:npm/" + f.pkg + "@1.0.0",
			PackageName:    f.pkg,
			PackageVersion: "1.0.0",
			Severity:       f.severity,
			Cves:           []string{},
			FixedVersions:  []string{},
			Urls:           []string{},
			Cvss:           []byte(`[]`),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func findingsTotal(t *testing.T, scans relicv1connect.ScanServiceClient, req *relicv1.ListFindingsRequest) int64 {
	t.Helper()

	resp, err := scans.ListFindings(t.Context(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}

	return resp.Msg.GetTotal()
}

func TestRelicAPIFindingsQueryIsALiteralSubstring(t *testing.T) {
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
	deployWithFindings(t, srv, db, "app.tgz", "app", []seededFinding{
		{
			id:       "GHSA-aaaa",
			severity: "high",
			pkg:      "snake_case",
		},
		{
			id:       "GHSA-bbbb",
			severity: "high",
			pkg:      "plain",
		},
		{
			id:       "GHSA-cccc",
			severity: "low",
			pkg:      "Ünïcode",
		},
	})

	scans := relicv1connect.NewScanServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	for _, tt := range []struct {
		query string
		want  int64
	}{
		{
			query: "",
			want:  3,
		},
		{
			query: "_",
			want:  1,
		},
		{
			query: "%",
			want:  0,
		},
		{
			query: "ghsa-b",
			want:  1,
		},
		{
			query: "ünï",
			want:  1,
		},
	} {
		if got := findingsTotal(t, scans, &relicv1.ListFindingsRequest{
			Query:    tt.query,
			PageSize: 10,
		}); got != tt.want {
			t.Errorf("findings matching %q = %d, want %d", tt.query, got, tt.want)
		}
	}
}
