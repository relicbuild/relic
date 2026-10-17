package relicapi

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/scanning/scan/policy"
)

type policyDecisionServer struct {
	d Dependencies
}

const (
	defaultDecisionLimit = 500
	defaultPreviewLimit  = 1000
	maxPreviewLimit      = 10_000
)

var decisionKinds = map[policy.DecisionKind]relicv1.PolicyDecisionKind{
	policy.DecisionDownload: relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_DOWNLOAD,
	policy.DecisionBuild:    relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_BUILD,
	policy.DecisionRelease:  relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_RELEASE,
}

func (s *policyDecisionServer) ListPolicyDecisions(
	ctx context.Context,
	req *connect.Request[relicv1.ListPolicyDecisionsRequest],
) (*connect.Response[relicv1.ListPolicyDecisionsResponse], error) {
	kind := policy.DecisionKind("")

	for k, m := range decisionKinds {
		if m == req.Msg.GetKind() {
			kind = k
		}
	}

	if kind == "" && req.Msg.GetKind() != relicv1.PolicyDecisionKind_POLICY_DECISION_KIND_UNSPECIFIED {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("unknown policy decision kind %d", req.Msg.GetKind()),
		)
	}

	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = defaultDecisionLimit
	}

	list, err := s.d.PolicyDecisions.List(ctx, kind, limit)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListPolicyDecisionsResponse{
		Decisions: make([]*relicv1.PolicyDecision, 0, len(list)),
	}
	for _, d := range list {
		out.Decisions = append(out.Decisions, &relicv1.PolicyDecision{
			Kind:      decisionKinds[d.Kind],
			Subject:   d.Subject,
			Policy:    d.Policy,
			Watch:     d.Watch,
			Rule:      d.Rule,
			Reason:    d.Reason,
			Actor:     d.Actor,
			Count:     d.Count,
			FirstSeen: timestamppb.New(d.FirstSeen),
			LastSeen:  timestamppb.New(d.LastSeen),
		})
	}

	return connect.NewResponse(out), nil
}

func (s *policyDecisionServer) PreviewPolicy(
	ctx context.Context,
	req *connect.Request[relicv1.PreviewPolicyRequest],
) (*connect.Response[relicv1.PreviewPolicyResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = defaultPreviewLimit
	}

	hits, truncated, err := s.d.ScanPolicies.Preview(
		ctx,
		policyOf(req.Msg.GetPolicy()),
		req.Msg.GetRepositories(),
		min(limit, maxPreviewLimit),
	)
	if err != nil {
		return nil, err
	}

	out := &relicv1.PreviewPolicyResponse{
		Hits:      make([]*relicv1.PolicyPreviewHit, 0, len(hits)),
		Truncated: truncated,
	}
	for _, h := range hits {
		out.Hits = append(out.Hits, &relicv1.PolicyPreviewHit{
			Repository:    h.Repository,
			Path:          h.Path,
			Sha256:        h.SHA256,
			Rule:          h.Rule,
			Issue:         h.Issue,
			FailBuild:     h.FailBuild,
			BlockDownload: h.BlockDownload,
		})
	}

	return connect.NewResponse(out), nil
}
