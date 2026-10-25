package relicapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/scan/policy"
)

const maxVexDocument = 16 << 20

func (s *scanServer) ExportVex(
	ctx context.Context,
	req *relicv1.ExportVexRequest,
) (_ *relicv1.ExportVexResponse, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "scan.ExportVex")

	defer func() {
		telemetry.End(span, err)
	}()

	rules, err := s.d.ScanPolicies.IgnoreRules(ctx, true)
	if err != nil {
		return nil, err
	}

	doc, skipped := policy.ExportVEX(
		rules,
		"urn:uuid:"+uuid.NewV7().String(),
		"Relic ("+auth.FromContext(ctx).Name()+")",
		time.Now(),
	)

	if req.GetIncludeUnreachable() {
		found, err := s.d.ScanResults.UnreachableFindings(ctx)
		if err != nil {
			return nil, err
		}

		doc.Statements = append(doc.Statements, policy.UnreachableStatements(found)...)
	}

	var b []byte

	switch req.GetFormat() {
	case relicv1.VexFormat_VEX_FORMAT_CYCLONEDX:
		b, err = policy.CycloneDXVEX(doc)
	default:
		b, err = json.MarshalIndent(doc, "", "  ")
	}

	if err != nil {
		return nil, err
	}

	telemetry.VEXExported(req.GetFormat() == relicv1.VexFormat_VEX_FORMAT_CYCLONEDX)

	return &relicv1.ExportVexResponse{
		Document: string(b),
		Skipped:  int32(min(skipped, 1<<30)), //nolint:gosec // bounded
	}, nil
}

func (s *scanServer) ImportVex(
	ctx context.Context,
	req *relicv1.ImportVexRequest,
) (_ *relicv1.ImportVexResponse, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "scan.ImportVex")
	defer func() {
		telemetry.End(span, err)
	}()

	if len(req.GetDocument()) > maxVexDocument {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the document is larger than 16 MiB")
	}

	doc, err := policy.ParseAnyVEX([]byte(req.GetDocument()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}

	rules, skipped := policy.ImportVEX(doc)
	out := &relicv1.ImportVexResponse{
		Skipped: int32(min(skipped, 1<<30)), //nolint:gosec // bounded
	}
	user := auth.FromContext(ctx).Name()

	created, existing, err := s.d.ScanPolicies.ImportIgnoreRules(ctx, rules, user)
	if err != nil {
		return nil, fmt.Errorf("import ignore rules: %w", err)
	}

	out.Existing = int32(min(existing, 1<<30)) //nolint:gosec // bounded

	for _, r := range created {
		out.Rules = append(out.Rules, ignoreRuleMessage(r))
	}

	s.d.Log.InfoContext(
		ctx,
		"vex imported",
		"document",
		doc.ID,
		"rules",
		len(out.GetRules()),
		"skipped",
		skipped,
		"existing",
		existing,
		"user",
		user,
	)

	return out, nil
}
