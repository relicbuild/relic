package relicapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/scanning/scan/blocking"
)

type quarantineServer struct {
	d Dependencies
}

var quarantineStates = map[blocking.QuarantineState]relicv1.QuarantineState{
	blocking.AwaitingScan: relicv1.QuarantineState_QUARANTINE_STATE_AWAITING_SCAN,
	blocking.ScanFailed:   relicv1.QuarantineState_QUARANTINE_STATE_SCAN_FAILED,
	blocking.ScanSkipped:  relicv1.QuarantineState_QUARANTINE_STATE_SCAN_SKIPPED,
	blocking.Malicious:    relicv1.QuarantineState_QUARANTINE_STATE_MALICIOUS,
	blocking.Rejected:     relicv1.QuarantineState_QUARANTINE_STATE_REJECTED,
}

const quarantineListLimit = 1000

func (s *quarantineServer) ListQuarantined(
	ctx context.Context,
	_ *connect.Request[relicv1.ListQuarantinedRequest],
) (*connect.Response[relicv1.ListQuarantinedResponse], error) {
	out := &relicv1.ListQuarantinedResponse{}
	if s.d.DownloadBlocker == nil {
		return connect.NewResponse(out), nil
	}

	held, err := s.d.DownloadBlocker.Quarantined(ctx, quarantineListLimit)
	if err != nil {
		return nil, err
	}

	for _, h := range held {
		out.Files = append(out.Files, &relicv1.QuarantinedFile{
			Repository: h.Repository,
			Path:       h.Path,
			Sha256:     h.SHA256,
			Cached:     timestamppb.New(h.Cached),
			State:      quarantineStates[h.State],
			Reason:     h.Reason,
			DecidedBy:  h.DecidedBy,
		})
	}

	return connect.NewResponse(out), nil
}

func (s *quarantineServer) decide(ctx context.Context, repo, p, reason string, release bool) error {
	if s.d.DownloadBlocker == nil {
		return unimplemented("quarantine")
	}

	it, err := s.d.Repositories.Stat(ctx, repo, cleanPath(p))
	if err != nil {
		return err
	}

	if it.IsFolder() {
		return connect.NewError(connect.CodeInvalidArgument, errors.New(repo+"/"+p+" is a folder"))
	}

	return s.d.DownloadBlocker.Decide(ctx, repo, it.Checksums.SHA256, release, reason, auth.FromContext(ctx).Name())
}

func (s *quarantineServer) ReleaseQuarantined(
	ctx context.Context,
	req *connect.Request[relicv1.ReleaseQuarantinedRequest],
) (*connect.Response[relicv1.ReleaseQuarantinedResponse], error) {
	m := req.Msg
	if err := s.decide(ctx, m.GetRepository(), m.GetPath(), m.GetReason(), true); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.ReleaseQuarantinedResponse{}), nil
}

func (s *quarantineServer) RejectQuarantined(
	ctx context.Context,
	req *connect.Request[relicv1.RejectQuarantinedRequest],
) (*connect.Response[relicv1.RejectQuarantinedResponse], error) {
	m := req.Msg
	if err := s.decide(ctx, m.GetRepository(), m.GetPath(), m.GetReason(), false); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.RejectQuarantinedResponse{}), nil
}
