//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/platform/database"
)

func TestUsageReport(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()

	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/api/repositories/usage-libs",
		body:   `{"rclass":"local","packageType":"generic"}`,
	})
	do(t, srv, call{
		method: http.MethodPut,
		path:   "/artifactory/usage-libs/a.bin",
		body:   "12345",
	})

	today := time.Now().UTC().Format(time.DateOnly)
	if err := db.SnapshotRepositoryUsage(ctx, today); err != nil {
		t.Fatal(err)
	}

	if err := db.AddRepositoryUsage(ctx, database.AddRepositoryUsageParams{
		Day:       today,
		RepoKey:   "usage-libs",
		Downloads: 4,
		Deploys:   1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.AddUserUsage(ctx, database.AddUserUsageParams{
		Day:       today,
		Username:  "ci",
		Downloads: 4,
	}); err != nil {
		t.Fatal(err)
	}

	system := relicv1connect.NewSystemServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	res, err := system.GetUsageReport(ctx, connect.NewRequest(&relicv1.GetUsageReportRequest{
		Days: 7,
	}))
	if err != nil {
		t.Fatal(err)
	}

	var libs *relicv1.RepositoryUsage

	for _, r := range res.Msg.GetRepositories() {
		if r.GetRepository() == "usage-libs" {
			libs = r
		}
	}

	if libs == nil || libs.GetFiles() != 1 || libs.GetBytes() != 5 || libs.GetDownloads() != 4 {
		t.Errorf("usage-libs = %v", libs)
	}

	if top := res.Msg.GetTopDownloaders(); len(top) != 1 || top[0].GetUser() != "ci" {
		t.Errorf("top downloaders = %v", top)
	}

	if _, err := relicv1connect.NewSystemServiceClient(relicHTTP("", ""), srv.URL+"/api").GetUsageReport(
		ctx,
		connect.NewRequest(&relicv1.GetUsageReportRequest{}),
	); code(err) != connect.CodeUnauthenticated {
		t.Errorf("an anonymous report: %v", err)
	}
}
