//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

func TestWaivers(t *testing.T) {
	t.Parallel()
	srv := server(t)
	ctx := t.Context()

	sec := relicv1connect.NewSecurityServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	for _, name := range []string{
		"wanda",
		"walt",
	} {
		if _, err := sec.PutUser(ctx, &relicv1.PutUserRequest{
			User: &relicv1.User{
				Name:     name,
				Email:    name + "@example.com",
				Password: "Waiver-pass-12345",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	admin := relicv1connect.NewWaiverServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))
	wanda := relicv1connect.NewWaiverServiceClient(apiClient(relicHTTP("wanda", "Waiver-pass-12345"), srv.URL))
	walt := relicv1connect.NewWaiverServiceClient(apiClient(relicHTTP("walt", "Waiver-pass-12345"), srv.URL))

	request := func(c relicv1connect.WaiverServiceClient, reason string, rule *relicv1.IgnoreRule) (*relicv1.Waiver, error) {
		resp, err := c.RequestWaiver(ctx, &relicv1.RequestWaiverRequest{
			Rule:   rule,
			Reason: reason,
		})
		if err != nil {
			return nil, err
		}

		return resp.GetWaiver(), nil
	}

	lodash := &relicv1.IgnoreRule{
		Packages: []string{
			"pkg:npm/lodash@4.17.15",
		},
		Vulnerabilities: []string{
			"CVE-2020-8203",
		},
	}

	if _, err := request(wanda, "", lodash); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a waiver without a reason: %v", err)
	}

	if _, err := request(wanda, "not reachable", &relicv1.IgnoreRule{}); code(err) != connect.CodeInvalidArgument ||
		connectMessage(err) != "a waiver needs at least one filter" {
		t.Errorf("a waiver without filters: %v", err)
	}

	if _, err := request(wanda, "not reachable", &relicv1.IgnoreRule{
		Packages: []string{
			"notapurl",
		},
	}); code(err) != connect.CodeInvalidArgument || connectMessage(err) != `"notapurl" is not a package URL` {
		t.Errorf("a waiver on something that is not a package URL: %v", err)
	}

	approved, err := request(wanda, "the vulnerable function is not reachable", lodash)
	if err != nil || approved.GetStatus() != relicv1.WaiverStatus_WAIVER_STATUS_PENDING ||
		approved.GetRequestedBy() != "wanda" {
		t.Fatalf("request = %v, %v", approved, err)
	}

	rejected, err := request(walt, "too noisy", &relicv1.IgnoreRule{
		Licenses: []string{
			"GPL-3.0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	withdrawn, err := request(walt, "mistake", lodash)
	if err != nil {
		t.Fatal(err)
	}

	list := func(c relicv1connect.WaiverServiceClient, status relicv1.WaiverStatus) []*relicv1.Waiver {
		resp, err := c.ListWaivers(ctx, &relicv1.ListWaiversRequest{
			Status: status,
		})
		if err != nil {
			t.Fatal(err)
		}

		return resp.GetWaivers()
	}

	if got := list(
		wanda,
		relicv1.WaiverStatus_WAIVER_STATUS_UNSPECIFIED,
	); len(got) != 1 ||
		got[0].GetId() != approved.GetId() {
		t.Errorf("wanda sees %v, want only her waiver", got)
	}

	if got := list(admin, relicv1.WaiverStatus_WAIVER_STATUS_PENDING); len(got) != 3 {
		t.Errorf("administrators see %d pending waivers, want 3", len(got))
	}

	if _, err := wanda.ApproveWaiver(ctx, &relicv1.ApproveWaiverRequest{
		Id:   approved.GetId(),
		Days: 30,
	}); code(err) != connect.CodePermissionDenied {
		t.Errorf("a user approving: %v", err)
	}

	if _, err := wanda.WithdrawWaiver(ctx, &relicv1.WithdrawWaiverRequest{
		Id: withdrawn.GetId(),
	}); code(err) != connect.CodeNotFound || connectMessage(err) != "no pending waiver "+withdrawn.GetId()+" of yours" {
		t.Errorf("withdrawing another user's waiver: %v", err)
	}

	if _, err := walt.WithdrawWaiver(ctx, &relicv1.WithdrawWaiverRequest{
		Id: withdrawn.GetId(),
	}); err != nil {
		t.Errorf("withdraw: %v", err)
	}

	if _, err := admin.ApproveWaiver(ctx, &relicv1.ApproveWaiverRequest{
		Id:   approved.GetId(),
		Days: 400,
	}); code(err) != connect.CodeInvalidArgument || connectMessage(err) != "a waiver lasts 1 to 365 days" {
		t.Errorf("approving for 400 days: %v", err)
	}

	resp, err := admin.ApproveWaiver(ctx, &relicv1.ApproveWaiverRequest{
		Id:   approved.GetId(),
		Days: 30,
		Note: "until the upgrade",
	})
	if err != nil {
		t.Fatal(err)
	}

	w := resp.GetWaiver()
	if w.GetStatus() != relicv1.WaiverStatus_WAIVER_STATUS_APPROVED || w.GetDecidedBy() != "admin" ||
		w.GetIgnoreRuleId() == "" {
		t.Fatalf("approved = %v", w)
	}

	if d := time.Until(w.GetExpires().AsTime()); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("approved waiver expires in %s, want 30 days", d)
	}

	scans := relicv1connect.NewScanServiceClient(apiClient(relicHTTP("admin", "password"), srv.URL))

	rules, err := scans.ListIgnoreRules(ctx, &relicv1.ListIgnoreRulesRequest{})
	if err != nil {
		t.Fatal(err)
	}

	var rule *relicv1.IgnoreRule

	for _, r := range rules.GetRules() {
		if r.GetId() == w.GetIgnoreRuleId() {
			rule = r
		}
	}

	if rule == nil || rule.GetExpires() == nil || len(rule.GetPackages()) != 1 ||
		rule.GetPackages()[0] != "pkg:npm/lodash@4.17.15" {
		t.Fatalf("the approval's ignore rule = %v", rule)
	}

	if _, err := admin.RejectWaiver(ctx, &relicv1.RejectWaiverRequest{
		Id: approved.GetId(),
	}); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("rejecting an approved waiver: %v", err)
	}

	rej, err := admin.RejectWaiver(ctx, &relicv1.RejectWaiverRequest{
		Id:   rejected.GetId(),
		Note: "fix the license instead",
	})
	if err != nil || rej.GetWaiver().GetStatus() != relicv1.WaiverStatus_WAIVER_STATUS_REJECTED ||
		rej.GetWaiver().GetIgnoreRuleId() != "" {
		t.Fatalf("reject = %v, %v", rej, err)
	}

	if got := list(admin, relicv1.WaiverStatus_WAIVER_STATUS_UNSPECIFIED); len(got) != 2 {
		t.Errorf("after a withdrawal administrators see %d waivers, want 2", len(got))
	}

	for i, want := range []string{
		rejected.GetId(),
		approved.GetId(),
	} {
		resp, err := admin.ListWaivers(ctx, &relicv1.ListWaiversRequest{
			Page:     int32(i + 1), //nolint:gosec // a loop index
			PageSize: 1,
		})
		if err != nil {
			t.Fatal(err)
		}

		if got := resp.GetWaivers(); len(got) != 1 || got[0].GetId() != want || resp.GetTotal() != 2 {
			t.Errorf("page %d of one waiver = %v, total %d; want %s of 2", i+1, got, resp.GetTotal(), want)
		}
	}
}

func connectMessage(err error) string {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Message()
	}

	return ""
}
