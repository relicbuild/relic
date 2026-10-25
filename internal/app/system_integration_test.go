//go:build integration

package app

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/app/backgroundjobs"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestRelicAPISystemStatus(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDB(t)

	log := slog.New(slog.DiscardHandler)
	if err := BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := NewTokens(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}

	queue, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHandler(db, objectstoretest.NewMemory(), tokens, queue, Options{}, log))
	t.Cleanup(srv.Close)

	createRepository(t, srv, "libs", "LOCAL", "GENERIC", "")
	upload(t, srv, "libs", "a.txt", "aaaa")
	upload(t, srv, "libs", "b.txt", "bb")
	ctx := t.Context()

	arts := relicv1connect.NewArtifactServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := arts.DeleteItem(
		ctx,
		&relicv1.DeleteItemRequest{
			Repository: "libs",
			Path:       "b.txt",
		},
	); err != nil {
		t.Fatal(err)
	}

	trash := relicv1connect.NewTrashServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	if _, err := trash.EmptyTrash(ctx, &relicv1.EmptyTrashRequest{}); err != nil {
		t.Fatal(err)
	}

	sys := relicv1connect.NewSystemServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	for range 2 {
		if _, err := sys.CollectGarbage(ctx, &relicv1.CollectGarbageRequest{}); err != nil {
			t.Fatalf("run gc: %v", err)
		}
	}

	st, err := sys.GetSystemStatus(ctx, &relicv1.GetSystemStatusRequest{})
	if err != nil {
		t.Fatalf("system: %v", err)
	}

	got := st
	if len(got.GetChecks()) != 2 || !got.GetChecks()[0].GetOk() || !got.GetChecks()[1].GetOk() {
		t.Errorf("checks = %v", got.GetChecks())
	}

	want := &relicv1.StorageUsage{
		Blobs:             2,
		Bytes:             6,
		UnreferencedBlobs: 1,
		UnreferencedBytes: 2,
	}
	if !proto.Equal(got.GetStorage(), want) {
		t.Errorf("storage = %v, want %v", got.GetStorage(), want)
	}

	if len(got.GetBackgroundJobs()) != 1 || got.GetBackgroundJobs()[0].GetKind() != "gc" ||
		got.GetBackgroundJobs()[0].GetStates()["available"] != 1 {
		t.Errorf("jobs = %v", got.GetBackgroundJobs())
	}

	long := strings.Repeat("the upstream refused the push; ", 20)

	seed := []string{
		`INSERT INTO river_job (kind, state, queue, attempt, max_attempts, args, attempted_at, attempted_by, finalized_at, errors)
		 VALUES ('replication', 'discarded', 'default', 2, 2, '{"repository":"libs"}', now(), ARRAY['relic-1_2026'], now(),
		 ARRAY[jsonb_build_object('attempt', 1, 'at', now(), 'error', 'first failure'), jsonb_build_object('attempt', 2, 'at', now(), 'error', $1::text)])`,
		`INSERT INTO river_job (kind, state, queue, attempt, max_attempts, args, attempted_at, attempted_by)
		 VALUES ('scan', 'running', 'scan', 1, 5, '{}', now(), ARRAY['relic-2_2026'])`,
		`INSERT INTO river_leader (elected_at, expires_at, leader_id, name) VALUES (now(), now() + interval '1 minute', 'relic-2_2026', 'default')`,
		`INSERT INTO river_queue (name, updated_at) VALUES ('scan', now())`,
	}
	if db.SQLite() {
		seed = []string{
			`INSERT INTO river_job (kind, state, queue, attempt, max_attempts, args, attempted_at, attempted_by, finalized_at, errors)
			 VALUES ('replication', 'discarded', 'default', 2, 2, jsonb('{"repository":"libs"}'), datetime('now', 'subsec'),
			 jsonb('["relic-1_2026"]'), datetime('now', 'subsec'),
			 jsonb_array(json_object('attempt', 1, 'at', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'error', 'first failure'),
			             json_object('attempt', 2, 'at', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'error', $1)))`,
			`INSERT INTO river_job (kind, state, queue, attempt, max_attempts, args, attempted_at, attempted_by)
			 VALUES ('scan', 'running', 'scan', 1, 5, jsonb('{}'), datetime('now', 'subsec'), jsonb('["relic-2_2026"]'))`,
			`INSERT INTO river_leader (elected_at, expires_at, leader_id, name)
			 VALUES (datetime('now', 'subsec'), datetime('now', '+1 minute', 'subsec'), 'relic-2_2026', 'default')`,
			`INSERT INTO river_queue (name, updated_at) VALUES ('scan', datetime('now', 'subsec'))`,
		}
	}

	for _, q := range seed {
		args := []any{}
		if strings.Contains(q, "$1") {
			args = append(args, long)
		}

		rows, err := db.Query(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}

		rows.Close()

		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}

	st, err = sys.GetSystemStatus(ctx, &relicv1.GetSystemStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}

	workers := make([]string, 0, len(st.GetWorkers()))
	for _, w := range st.GetWorkers() {
		workers = append(workers, fmt.Sprintf("%s running=%d leader=%v", w.GetId(), w.GetRunning(), w.GetLeader()))
	}

	if want := []string{
		"relic-1_2026 running=0 leader=false",
		"relic-2_2026 running=1 leader=true",
	}; !slices.Equal(workers, want) {
		t.Errorf("workers = %v, want %v", workers, want)
	}

	if qs := st.GetQueues(); len(qs) != 1 || qs[0].GetName() != "scan" || qs[0].GetRunning() != 1 {
		t.Errorf("queues = %v", qs)
	}

	jobs, err := sys.ListBackgroundJobs(
		ctx,
		&relicv1.ListBackgroundJobsRequest{
			Kind: "replication",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if j := jobs.GetJobs(); len(j) != 1 || j[0].GetState() != "discarded" || j[0].GetWorker() != "relic-1_2026" ||
		!sameJSON(j[0].GetArgs(), `{"repository": "libs"}`) ||
		len(j[0].GetErrors()) != 2 ||
		j[0].GetErrors()[1].GetError() != long ||
		j[0].GetErrors()[1].GetAttempt() != 2 {
		t.Errorf("replication jobs = %v", j)
	}

	if all, err := sys.ListBackgroundJobs(
		ctx,
		&relicv1.ListBackgroundJobsRequest{
			State: "running",
		},
	); err != nil || len(all.GetJobs()) != 1 ||
		all.GetJobs()[0].GetKind() != "scan" {
		t.Errorf("running jobs = %v %v", all, err)
	}

	plain := server(t)

	plainSys := relicv1connect.NewSystemServiceClient(apiClient(relicHTTP("admin", "password"), plain.URL))
	if _, err := plainSys.CollectGarbage(
		ctx,
		&relicv1.CollectGarbageRequest{},
	); code(err) != connect.CodeUnavailable {
		t.Errorf("run gc without a queue: %v, want unavailable", err)
	}

	anon := relicv1connect.NewSystemServiceClient(apiClient(relicHTTP("", ""), srv.URL))
	if _, err := anon.GetSystemStatus(
		ctx,
		&relicv1.GetSystemStatusRequest{},
	); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous system: %v, want unauthenticated", err)
	}
}

func TestGraphScansNeedAWorkerThatScans(t *testing.T) {
	t.Parallel()
	db := databasetest.NewDB(t)

	log := slog.New(slog.DiscardHandler)
	if err := BootstrapAdmin(t.Context(), db, "admin", "password", log); err != nil {
		t.Fatal(err)
	}

	tokens, err := NewTokens(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}

	queue, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHandler(db, objectstoretest.NewMemory(), tokens, queue, Options{}, log))
	t.Cleanup(srv.Close)

	ctx := t.Context()
	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	req := &relicv1.ScanPackagesRequest{
		Purls: []string{
			"pkg:npm/lodash@4.17.15",
		},
	}

	stale := `INSERT INTO river_queue (name, updated_at) VALUES ('scan_graph', now() - interval '1 hour')`
	fresh := `UPDATE river_queue SET updated_at = now() WHERE name = 'scan_graph'`

	if db.SQLite() {
		stale = `INSERT INTO river_queue (name, updated_at) VALUES ('scan_graph', datetime('now', '-1 hour', 'subsec'))`
		fresh = `UPDATE river_queue SET updated_at = datetime('now', 'subsec') WHERE name = 'scan_graph'`
	}

	for _, step := range []struct {
		name      string
		statement string
		wantCode  connect.Code
	}{
		{
			name:     "no worker ever ran graph scans",
			wantCode: connect.CodeUnavailable,
		},
		{
			name:      "the last worker stopped an hour ago",
			statement: stale,
			wantCode:  connect.CodeUnavailable,
		},
		{
			name:      "a worker reported the queue just now",
			statement: fresh,
		},
	} {
		if step.statement != "" {
			rows, err := db.Query(ctx, step.statement)
			if err != nil {
				t.Fatal(err)
			}

			rows.Close()

			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		}

		_, err := scans.ScanPackages(ctx, req)
		if step.wantCode == 0 && err != nil || step.wantCode != 0 && code(err) != step.wantCode {
			t.Errorf("%s: ScanPackages = %v, want code %v", step.name, err, step.wantCode)
		}
	}
}

func TestRelicAPISaysWhenRelicScanIsOff(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)

	createRepository(t, srv, "scan-off", "LOCAL", "GENERIC", "")
	upload(t, srv, "scan-off", "app/app-1.0.jar", "not really a jar")

	ctx := t.Context()
	client := apiClient(relicHTTP("admin", "password"), srv.URL)
	scans := relicv1connect.NewScanServiceClient(client)
	sys := relicv1connect.NewSystemServiceClient(client)

	report := func(step string) (relicv1.ScanStatus, bool, bool) {
		t.Helper()

		item, err := scans.GetItemScan(ctx, &relicv1.GetItemScanRequest{
			Repository: "scan-off",
			Path:       "app/app-1.0.jar",
		})
		if err != nil {
			t.Fatalf("%s: GetItemScan: %v", step, err)
		}

		st, err := sys.GetSystemStatus(ctx, &relicv1.GetSystemStatusRequest{})
		if err != nil {
			t.Fatalf("%s: GetSystemStatus: %v", step, err)
		}

		summary, err := sys.GetDashboardSummary(ctx, &relicv1.GetDashboardSummaryRequest{})
		if err != nil {
			t.Fatalf("%s: GetDashboardSummary: %v", step, err)
		}

		return item.GetScan().GetStatus(), st.GetScanner().GetAvailable(), summary.GetScannerAvailable()
	}

	status, available, summarized := report("no worker")
	if status != relicv1.ScanStatus_SCAN_STATUS_UNAVAILABLE || available || summarized {
		t.Errorf("without a worker that scans: item %v, system available %v, summary available %v; "+
			"want unavailable and off", status, available, summarized)
	}

	fresh := `INSERT INTO river_queue (name, updated_at) VALUES ('scan', now())`
	if db.SQLite() {
		fresh = `INSERT INTO river_queue (name, updated_at) VALUES ('scan', datetime('now', 'subsec'))`
	}

	rows, err := db.Query(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	status, available, summarized = report("a worker that scans")
	if status != relicv1.ScanStatus_SCAN_STATUS_QUEUED || !available || !summarized {
		t.Errorf("with a worker that scans: item %v, system available %v, summary available %v; "+
			"want queued and on", status, available, summarized)
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}

	return reflect.DeepEqual(x, y)
}
