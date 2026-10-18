package relicapi

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/scanning/scan/policy"
)

type waiverServer struct {
	d Dependencies
}

var waiverStatuses = map[policy.WaiverStatus]relicv1.WaiverStatus{
	policy.WaiverPending:  relicv1.WaiverStatus_WAIVER_STATUS_PENDING,
	policy.WaiverApproved: relicv1.WaiverStatus_WAIVER_STATUS_APPROVED,
	policy.WaiverRejected: relicv1.WaiverStatus_WAIVER_STATUS_REJECTED,
}

func waiverMessage(w policy.Waiver) *relicv1.Waiver {
	return &relicv1.Waiver{
		Id:           w.ID,
		RequestedBy:  w.RequestedBy,
		Reason:       w.Reason,
		Rule:         ignoreRuleMessage(w.Rule),
		Status:       waiverStatuses[w.Status],
		DecidedBy:    w.DecidedBy,
		DecisionNote: w.DecisionNote,
		Decided:      ts(w.Decided),
		Expires:      ts(w.Expires),
		IgnoreRuleId: w.IgnoreRuleID,
		Created:      timestamppb.New(w.Created),
	}
}

func (s *waiverServer) RequestWaiver(
	ctx context.Context,
	req *relicv1.RequestWaiverRequest,
) (*relicv1.RequestWaiverResponse, error) {
	w, err := s.d.ScanPolicies.RequestWaiver(
		ctx,
		ignoreRuleOf(req.GetRule()),
		req.GetReason(),
		auth.FromContext(ctx).Name(),
	)
	if err != nil {
		return nil, err
	}

	return &relicv1.RequestWaiverResponse{
		Waiver: waiverMessage(w),
	}, nil
}

func (s *waiverServer) ListWaivers(
	ctx context.Context,
	req *relicv1.ListWaiversRequest,
) (*relicv1.ListWaiversResponse, error) {
	p := auth.FromContext(ctx)

	requestedBy := ""
	if req.GetMine() || (!p.Admin && !p.Viewer) {
		requestedBy = p.Name()
	}

	status := policy.WaiverStatus("")

	for st, m := range waiverStatuses {
		if m == req.GetStatus() {
			status = st
		}
	}

	n, size := page(req.GetPage(), req.GetPageSize())

	list, total, err := s.d.ScanPolicies.Waivers(ctx, policy.WaiverQuery{
		RequestedBy: requestedBy,
		Status:      status,
		Skip:        (n - 1) * size,
		Max:         size,
	})
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListWaiversResponse{
		Waivers: make([]*relicv1.Waiver, 0, len(list)),
		Total:   total,
	}
	for _, w := range list {
		out.Waivers = append(out.Waivers, waiverMessage(w))
	}

	return out, nil
}

func (s *waiverServer) WithdrawWaiver(
	ctx context.Context,
	req *relicv1.WithdrawWaiverRequest,
) (*relicv1.WithdrawWaiverResponse, error) {
	if err := s.d.ScanPolicies.WithdrawWaiver(ctx, req.GetId(), auth.FromContext(ctx).Name()); err != nil {
		return nil, err
	}

	return &relicv1.WithdrawWaiverResponse{}, nil
}

func (s *waiverServer) ApproveWaiver(
	ctx context.Context,
	req *relicv1.ApproveWaiverRequest,
) (*relicv1.ApproveWaiverResponse, error) {
	m := req

	w, err := s.d.ScanPolicies.ApproveWaiver(
		ctx,
		m.GetId(),
		int(m.GetDays()),
		m.GetNote(),
		auth.FromContext(ctx).Name(),
	)
	if err != nil {
		return nil, err
	}

	return &relicv1.ApproveWaiverResponse{
		Waiver: waiverMessage(w),
	}, nil
}

func (s *waiverServer) RejectWaiver(
	ctx context.Context,
	req *relicv1.RejectWaiverRequest,
) (*relicv1.RejectWaiverResponse, error) {
	w, err := s.d.ScanPolicies.RejectWaiver(ctx, req.GetId(), req.GetNote(), auth.FromContext(ctx).Name())
	if err != nil {
		return nil, err
	}

	return &relicv1.RejectWaiverResponse{
		Waiver: waiverMessage(w),
	}, nil
}
