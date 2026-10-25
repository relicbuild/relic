//go:build integration

package integration

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
	"relic/internal/access/audit"
)

func TestAuditLog(t *testing.T) {
	t.Parallel()
	srv, db := serverDB(t)
	ctx := t.Context()
	admin := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	if _, err := admin.PutUser(ctx, &relicv1.PutUserRequest{
		User: &relicv1.User{
			Name:     "carol",
			Email:    "carol@example.com",
			Password: "Carol-pw-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := admin.ListUsers(ctx, &relicv1.ListUsersRequest{}); err != nil {
		t.Fatal(err)
	}

	carol := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("carol", "Carol-pw-1"), srv.URL))
	if _, err := carol.DeleteUser(ctx, &relicv1.DeleteUserRequest{
		Name: "admin",
	}); code(err) != connect.CodePermissionDenied {
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

	if _, err := carol.CreateToken(ctx, &relicv1.CreateTokenRequest{
		Description: "g",
	}); err != nil {
		t.Fatal(err)
	}

	tokens, err := carol.ListTokens(ctx, &relicv1.ListTokensRequest{})
	if err != nil || len(tokens.GetTokens()) != 1 {
		t.Fatalf("carol's tokens = %v, %v", tokens, err)
	}

	tokenID := tokens.GetTokens()[0].GetId()
	if _, err := admin.RevokeToken(ctx, &relicv1.RevokeTokenRequest{
		Id: tokenID,
	}); err != nil {
		t.Fatal(err)
	}

	scim := func(method, path, body string) string {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}

		req.SetBasicAuth("admin", "password")
		req.Header.Set("Content-Type", "application/scim+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			_ = resp.Body.Close()
		}()

		var created struct {
			ID string `json:"id"`
		}

		_ = json.NewDecoder(resp.Body).Decode(&created)

		return created.ID
	}

	samID := scim(http.MethodPost, "/scim/v2/Users", `{"userName":"Sam.Smith","active":true}`)
	if samID == "" {
		t.Fatal("SCIM did not create Sam.Smith")
	}

	scim(http.MethodDelete, "/scim/v2/Users/"+samID, "")
	scim(http.MethodPost, "/scim/v2/Groups", `{"displayName":"scim-team"}`)

	login := relicv1connect.NewSessionServiceClient(apiClient(http.DefaultClient, srv.URL))
	req := &relicv1.LoginRequest{
		Username: "carol",
		Password: "wrong",
	}
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("X-Relic-UI", "1")

	if _, err := login.Login(ctx, req); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("login with a wrong password: %v", err)
	}

	log := relicv1connect.NewAuditServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	list, err := log.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Since: timestamppb.New(time.Now().Add(-time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := log.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Outcome: relicv1.AuditOutcome(99),
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an unknown outcome filter = %v, want InvalidArgument", err)
	}

	type event struct {
		actor, action, target string
		outcome               relicv1.AuditOutcome
	}

	got := make([]event, 0, len(list.GetEvents()))
	for _, e := range list.GetEvents() {
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
		{
			actor:   "carol",
			action:  "relic.v1.SecurityService/CreateToken",
			target:  tokenID,
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
		},
		{
			actor:   "admin",
			action:  "relic.v1.SecurityService/RevokeToken",
			target:  tokenID,
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
		},
		{
			actor:   "admin",
			action:  "POST /scim/v2/Users",
			target:  "Sam.Smith",
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
		},
		{
			actor:   "admin",
			action:  "DELETE /scim/v2/Users/{id}",
			target:  "Sam.Smith",
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
		},
		{
			actor:   "admin",
			action:  "POST /scim/v2/Groups",
			target:  "scim-team",
			outcome: relicv1.AuditOutcome_AUDIT_OUTCOME_SUCCESS,
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

	denied, err := log.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Actor:    "carol",
		Outcome:  relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED,
		PageSize: 1,
	})
	if err != nil || len(denied.GetEvents()) != 1 || denied.GetTotal() != 2 {
		t.Fatalf("first page of carol's denied events = %v, %v", denied, err)
	}

	next, err := log.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Actor:    "carol",
		Outcome:  relicv1.AuditOutcome_AUDIT_OUTCOME_DENIED,
		Page:     2,
		PageSize: 1,
	})
	if err != nil || len(next.GetEvents()) != 1 ||
		next.GetEvents()[0].GetId() == denied.GetEvents()[0].GetId() {
		t.Fatalf("second page of carol's denied events = %v, %v", next, err)
	}

	if _, err := log.ListAuditEvents(ctx, &relicv1.ListAuditEventsRequest{
		Page:     3_000_000,
		PageSize: 1000,
	}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a page whose offset overflows = %v, want InvalidArgument", err)
	}

	if _, err := carol.ListUsers(
		ctx,
		&relicv1.ListUsersRequest{},
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
