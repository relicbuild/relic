package relicapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/replication"
)

type replicationServer struct {
	d Dependencies
}

func replicationMessage(r replication.Replication) *relicv1.Replication {
	return &relicv1.Replication{
		Repository:     r.Repository,
		TargetUrl:      r.TargetURL,
		Username:       r.Username,
		HasPassword:    r.HasPassword,
		Schedule:       r.Schedule,
		Enabled:        r.Enabled,
		SyncDeletes:    r.SyncDeletes,
		SyncProperties: r.SyncProperties,
		PathPrefix:     r.PathPrefix,
		LastRun:        ts(r.LastRun),
		LastStatus:     fromDomain[relicv1.ReplicationStatus](r.LastStatus),
		LastError:      r.LastError,
		OnChange:       r.OnChange,
		TimeoutSeconds: int32(min(r.TimeoutSeconds, math.MaxInt32)), //nolint:gosec // saturated
		Direction:      fromDomain[relicv1.ReplicationDirection](r.Effective()),
	}
}

func (s *replicationServer) ListReplications(
	ctx context.Context,
	_ *connect.Request[relicv1.ListReplicationsRequest],
) (*connect.Response[relicv1.ListReplicationsResponse], error) {
	list, err := s.d.Replications.List(ctx)
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListReplicationsResponse{
		Replications: make([]*relicv1.Replication, 0, len(list)),
	}
	for _, r := range list {
		out.Replications = append(out.Replications, replicationMessage(r))
	}

	return connect.NewResponse(out), nil
}

func (s *replicationServer) GetReplication(
	ctx context.Context,
	req *connect.Request[relicv1.GetReplicationRequest],
) (*connect.Response[relicv1.GetReplicationResponse], error) {
	r, err := s.d.Replications.Get(ctx, req.Msg.GetRepository())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.GetReplicationResponse{
		Replication: replicationMessage(r),
	}), nil
}

func (s *replicationServer) PutReplication(
	ctx context.Context,
	req *connect.Request[relicv1.PutReplicationRequest],
) (*connect.Response[relicv1.PutReplicationResponse], error) {
	m := req.Msg.GetReplication()

	rp, err := s.d.Repositories.GetRepository(ctx, m.GetRepository())
	if err != nil {
		return nil, err
	}

	if rp.Class != repository.ClassLocal {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("only local repositories can be push-replicated"),
		)
	}

	u, err := url.Parse(m.GetTargetUrl())
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("the target URL must be an http or https URL"),
		)
	case u.User != nil:
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("put credentials in username and password, not the URL"),
		)
	}

	if err := replication.ValidSchedule(m.GetSchedule()); err != nil {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("invalid schedule %q: a standard 5-field cron expression is expected", m.GetSchedule()),
		)
	}

	cfg := replication.Config{
		Repository:     rp.Name,
		TargetURL:      m.GetTargetUrl(),
		Username:       m.GetUsername(),
		Password:       m.GetPassword(),
		Schedule:       m.GetSchedule(),
		Enabled:        m.GetEnabled(),
		SyncDeletes:    m.GetSyncDeletes(),
		SyncProperties: m.GetSyncProperties(),
		PathPrefix:     m.GetPathPrefix(),
		OnChange:       m.GetOnChange(),
		TimeoutSeconds: int(m.GetTimeoutSeconds()),
	}

	direction, ok := toDomain[replication.Direction](m.GetDirection())
	if !ok && m.GetDirection() != relicv1.ReplicationDirection_REPLICATION_DIRECTION_UNSPECIFIED {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			fmt.Errorf("unknown replication direction %d", m.GetDirection()),
		)
	}

	cfg.Direction = direction

	if old, err := s.d.Replications.Get(ctx, rp.Name); err == nil {
		cfg.Extensions = old.Extensions
	} else if !errors.Is(err, replication.ErrNotConfigured) {
		return nil, err
	}

	if err := s.d.Replications.Save(ctx, cfg); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.PutReplicationResponse{}), nil
}

func (s *replicationServer) DeleteReplication(
	ctx context.Context,
	req *connect.Request[relicv1.DeleteReplicationRequest],
) (*connect.Response[relicv1.DeleteReplicationResponse], error) {
	if err := s.d.Replications.Delete(ctx, req.Msg.GetRepository()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.DeleteReplicationResponse{}), nil
}

func (s *replicationServer) RunReplication(
	ctx context.Context,
	req *connect.Request[relicv1.RunReplicationRequest],
) (*connect.Response[relicv1.RunReplicationResponse], error) {
	if _, err := s.d.Replications.Get(ctx, req.Msg.GetRepository()); err != nil {
		return nil, err
	}

	if s.d.QueueReplication == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no job queue"))
	}

	if err := s.d.QueueReplication(ctx, req.Msg.GetRepository()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.RunReplicationResponse{}), nil
}
