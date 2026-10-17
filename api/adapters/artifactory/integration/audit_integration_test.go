//go:build integration

package integration

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/audit"
)

func TestAuditLog(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()
	admin := relicv1connect.NewSecurityServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	if _, err := admin.PutUser(ctx, connect.NewRequest(&relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "carol",
			Email:    "carol@example.com",
			Password: "Carol-pw-1",
		},
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := admin.ListUsers(ctx, connect.NewRequest(&relicv1.ListUsersRequest{})); err != nil {
		t.Fatal(err)
	}

	carol := relicv1connect.NewSecurityServiceClient(relicHTTP("carol", "Carol-pw-1"), srv.URL+"/api")
	if _, err := carol.DeleteUser(ctx, connect.NewRequest(&relicv1.DeleteUserRequest{
		Name: "admin",
	})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("carol deletes admin: %v", err)
	}

	put, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		srv.URL+"/artifactory/api/security/groups/auditors",
		strings.NewReader(`{"description":"audit"}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	put.SetBasicAuth("admin", "password")
	put.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	login := relicv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL+"/api")
	req := connect.NewRequest(&relicv1.LoginRequest{
		Username: "carol",
		Password: "wrong",
	})
	req.Header().Set("X-Relic-UI", "1")

	if _, err := login.Login(ctx, req); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("login with a wrong password: %v", err)
	}

	log := relicv1connect.NewAuditServiceClient(relicHTTP("admin", "password"), srv.URL+"/api")

	list, err := log.ListAuditEvents(ctx, connect.NewRequest(&relicv1.ListAuditEventsRequest{
		Since: timestamppb.New(time.Now().Add(-time.Hour)),
	}))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := log.ListAuditEvents(ctx, connect.NewRequest(&relicv1.ListAuditEventsRequest{
		Outcome: relicv1.AuditOutcome(99),
	})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown outcome filter = %v, want InvalidArgument", err)
	}

	type event struct {
		actor, action, target string
		outcome               relicv1.AuditOutcome
	}

	got := make([]event, 0, len(list.Msg.GetEvents()))
	for _, e := range list.Msg.GetEvents() {
		got = append(got, event{
			actor:   e.GetActor(),
			action:  e.GetAction(),
			target:  e.GetTarget(),
			outcome: e.GetOutcome(),
		})

		if strings.Contains(e.GetAction(), "ListUsers") {
			t.Errorf("a read was audited: %v", e)
		}
	}

	for _, want := range []event{
		{
			actor:   "admin",
			action:  "relic.v1.SecurityService/PutUser",
			target:  "carol",
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
		},
		{
			actor:   "carol",
			action:  "relic.v1.SecurityService/DeleteUser",
			target:  "admin",
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED,
		},
		{
			actor:   "admin",
			action:  "PUT /artifactory/api/security/groups/{name}",
			target:  "/artifactory/api/security/groups/auditors",
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
		},
		{
			actor:   "carol",
			action:  "relic.v1.SessionService/Login",
			target:  "carol",
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED,
		},
	} {
		found := false
		for _, g := range got {
			found = found || g == want
		}

		if !found {
			t.Errorf("no audit event %+v in %+v", want, got)
		}
	}

	denied, err := log.ListAuditEvents(ctx, connect.NewRequest(&relicv1.ListAuditEventsRequest{
		Actor:    "carol",
		Outcome:  relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED,
		PageSize: 1,
	}))
	if err != nil || len(denied.Msg.GetEvents()) != 1 || denied.Msg.GetTotal() != 2 {
		t.Fatalf("first page of carol's denied events = %v, %v", denied, err)
	}

	next, err := log.ListAuditEvents(ctx, connect.NewRequest(&relicv1.ListAuditEventsRequest{
		Actor:    "carol",
		Outcome:  relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED,
		Page:     2,
		PageSize: 1,
	}))
	if err != nil || len(next.Msg.GetEvents()) != 1 ||
		next.Msg.GetEvents()[0].GetId() == denied.Msg.GetEvents()[0].GetId() {
		t.Fatalf("second page of carol's denied events = %v, %v", next, err)
	}

	if _, err := log.ListAuditEvents(ctx, connect.NewRequest(&relicv1.ListAuditEventsRequest{
		Page:     3_000_000,
		PageSize: 1000,
	})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a page whose offset overflows = %v, want InvalidArgument", err)
	}

	if _, err := carol.ListUsers(
		ctx,
		connect.NewRequest(&relicv1.ListUsersRequest{}),
	); code(
		err,
	) != connect.CodePermissionDenied {
		t.Errorf("carol lists users: %v", err)
	}

	purged, err := audit.New(db, slog.New(slog.DiscardHandler)).Purge(ctx, -time.Minute)
	if err != nil || purged < 4 {
		t.Errorf("purge = %d, %v", purged, err)
	}
}
