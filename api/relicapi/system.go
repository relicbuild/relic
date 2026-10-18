package relicapi

import (
	"context"
	"errors"
	"math"
	"time"

	"relic/internal/artifacts/usage"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
)

const checkTimeout = 5 * time.Second

type systemServer struct {
	d Dependencies
}

func (s *systemServer) GetVersion(
	context.Context,
	*connect.Request[relicv1.GetVersionRequest],
) (*connect.Response[relicv1.GetVersionResponse], error) {
	return connect.NewResponse(&relicv1.GetVersionResponse{
		Version:  s.d.Version,
		Revision: s.d.Revision,
	}), nil
}

func (s *systemServer) GetSystemStatus(
	ctx context.Context,
	_ *connect.Request[relicv1.GetSystemStatusRequest],
) (*connect.Response[relicv1.GetSystemStatusResponse], error) {
	if s.d.SystemStatus == nil || s.d.ScanResults == nil {
		return nil, unimplemented("system status")
	}

	out := &relicv1.GetSystemStatusResponse{
		Scanner: &relicv1.ScannerStatus{},
	}
	for _, c := range s.d.HealthChecks {
		out.Checks = append(out.Checks, runCheck(ctx, c))
	}

	st, err := s.d.SystemStatus.Storage(ctx)
	if err != nil {
		return nil, err
	}

	out.Storage = &relicv1.StorageUsage{
		Blobs:             st.Blobs,
		Bytes:             st.Bytes,
		UnreferencedBlobs: st.UnreferencedBlobs,
		UnreferencedBytes: st.UnreferencedBytes,
	}

	jobs, err := s.d.SystemStatus.Jobs(ctx)
	if err != nil {
		return nil, err
	}

	for _, k := range jobs {
		out.BackgroundJobs = append(out.BackgroundJobs, &relicv1.BackgroundJobSummary{
			Kind:         k.Kind,
			States:       counts(k.States),
			LastFinished: ts(k.LastFinished),
			LastError:    k.LastError,
			LastErrorAt:  ts(k.LastErrorAt),
		})
	}

	workers, err := s.d.SystemStatus.Workers(ctx)
	if err != nil {
		return nil, err
	}

	for _, w := range workers {
		//nolint:gosec // bounded by the workers' concurrency
		out.Workers = append(
			out.Workers,
			&relicv1.JobWorker{
				Id:         w.ID,
				Running:    int32(w.Running),
				LastActive: tsValue(w.LastActive),
				Leader:     w.Leader,
			},
		)
	}

	queues, err := s.d.SystemStatus.Queues(ctx)
	if err != nil {
		return nil, err
	}

	for _, q := range queues {
		//nolint:gosec // job counts
		out.Queues = append(
			out.Queues,
			&relicv1.JobQueue{
				Name:    q.Name,
				Updated: tsValue(q.Updated),
				Paused:  q.Paused,
				Waiting: int32(q.Waiting),
				Running: int32(q.Running),
			},
		)
	}

	scans, err := s.d.ScanResults.StatusCounts(ctx)
	if err != nil {
		return nil, err
	}

	out.Scanner.Scans = counts(scans)
	if built, err := s.d.ScanResults.LatestDBBuilt(ctx); err == nil && !built.IsZero() {
		out.Scanner.VulnerabilityDatabaseBuilt = timestamppb.New(built)
	}

	return connect.NewResponse(out), nil
}

// ListBackgroundJobs returns recent jobs with their errors in full.
func (s *systemServer) ListBackgroundJobs(
	ctx context.Context,
	req *connect.Request[relicv1.ListBackgroundJobsRequest],
) (*connect.Response[relicv1.ListBackgroundJobsResponse], error) {
	if s.d.SystemStatus == nil {
		return nil, unimplemented("background jobs")
	}

	jobs, err := s.d.SystemStatus.ListJobs(ctx, req.Msg.GetKind(), req.Msg.GetState(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}

	out := &relicv1.ListBackgroundJobsResponse{
		Jobs: make([]*relicv1.BackgroundJob, 0, len(jobs)),
	}
	for _, j := range jobs {
		m := &relicv1.BackgroundJob{
			Id:          j.ID,
			Kind:        j.Kind,
			State:       j.State,
			Queue:       j.Queue,
			Attempt:     int32(j.Attempt),
			MaxAttempts: int32(j.MaxAttempts),
			Created:     tsValue(j.Created),
			Scheduled:   tsValue(j.Scheduled),
			Attempted:   ts(j.Attempted),
			Finalized:   ts(j.Finalized),
			Args:        string(j.Args),
		}
		if out := string(j.Output); out != "null" {
			m.Output = out
		}

		if j.Worker != nil {
			m.Worker = *j.Worker
		}

		for _, e := range j.Errors {
			//nolint:gosec // attempts are small
			m.Errors = append(
				m.Errors,
				&relicv1.BackgroundJobError{
					Attempt: int32(e.Attempt),
					At:      tsValue(e.At),
					Error:   e.Error,
				},
			)
		}

		out.Jobs = append(out.Jobs, m)
	}

	return connect.NewResponse(out), nil
}

func tsValue(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

func runCheck(ctx context.Context, c HealthCheck) *relicv1.HealthCheck {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	start := time.Now()
	err := c.Check(ctx)

	out := &relicv1.HealthCheck{
		Name:      c.Name,
		Ok:        err == nil,
		LatencyMs: time.Since(start).Milliseconds(),
	}
	if err != nil {
		out.Error = err.Error()
	}

	return out
}

func (s *systemServer) CheckStorage(
	ctx context.Context,
	_ *connect.Request[relicv1.CheckStorageRequest],
) (*connect.Response[relicv1.CheckStorageResponse], error) {
	if s.d.QueueStorageCheck == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no job queue"))
	}

	id, err := s.d.QueueStorageCheck(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.CheckStorageResponse{
		JobId: id,
	}), nil
}

func (s *systemServer) CollectGarbage(
	ctx context.Context,
	_ *connect.Request[relicv1.CollectGarbageRequest],
) (*connect.Response[relicv1.CollectGarbageResponse], error) {
	if s.d.QueueGarbageCollection == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no job queue"))
	}

	if err := s.d.QueueGarbageCollection(ctx); err != nil {
		return nil, err
	}

	return connect.NewResponse(&relicv1.CollectGarbageResponse{}), nil
}

func ts(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}

	return timestamppb.New(*t)
}

func unimplemented(what string) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New(what+" is not available on this server"))
}

func counts[K ~string](in map[K]int64) map[string]int32 {
	out := make(map[string]int32, len(in))
	for k, v := range in {
		out[string(k)] = int32(min(v, math.MaxInt32)) //nolint:gosec // saturated above
	}

	return out
}

func (s *systemServer) GetDashboardSummary(
	ctx context.Context,
	_ *connect.Request[relicv1.GetDashboardSummaryRequest],
) (*connect.Response[relicv1.GetDashboardSummaryResponse], error) {
	out := &relicv1.GetDashboardSummaryResponse{
		Repositories: map[string]int32{},
	}

	repos, err := s.d.Repositories.ListRepositories(ctx, "", "")
	if err != nil {
		return nil, err
	}

	rs := &repositoryServer{
		s.d,
	}
	for _, r := range repos {
		ok, err := rs.canRead(ctx, r)
		if err != nil {
			return nil, err
		}

		if ok {
			out.Repositories[string(r.Class)]++
		}
	}

	builds, err := s.d.Builds.List(ctx, "")
	if err != nil {
		return nil, err
	}

	readableBuilds := &buildServer{
		s.d,
	}
	for _, b := range builds {
		ok, err := readableBuilds.canRead(ctx, "", b.Name)
		if err != nil {
			return nil, err
		}

		if ok {
			out.Builds++
		}
	}

	if p := auth.FromContext(ctx); p.Admin || p.Viewer {
		scans, err := s.d.ScanResults.StatusCounts(ctx)
		if err != nil {
			return nil, err
		}

		findings, err := s.d.ScanResults.FindingCounts(ctx)
		if err != nil {
			return nil, err
		}

		out.Scans, out.Findings = counts(scans), counts(findings)
	}

	if built, err := s.d.ScanResults.LatestDBBuilt(ctx); err == nil && !built.IsZero() {
		out.VulnerabilityDatabaseBuilt = timestamppb.New(built)
	}

	return connect.NewResponse(out), nil
}

const (
	defaultUsageDays = 30
	maxUsageDays     = 730
)

func userUsageMessages(list []usage.UserUsage) []*relicv1.UserUsage {
	out := make([]*relicv1.UserUsage, 0, len(list))
	for _, u := range list {
		out = append(out, &relicv1.UserUsage{
			User:      u.User,
			Downloads: u.Downloads,
			Deploys:   u.Deploys,
		})
	}

	return out
}

func (s *systemServer) GetUsageReport(
	ctx context.Context,
	req *connect.Request[relicv1.GetUsageReportRequest],
) (*connect.Response[relicv1.GetUsageReportResponse], error) {
	days := int(req.Msg.GetDays())
	if days <= 0 {
		days = defaultUsageDays
	}

	rep, err := s.d.Usage.Report(ctx, min(days, maxUsageDays))
	if err != nil {
		return nil, err
	}

	out := &relicv1.GetUsageReportResponse{
		From:           rep.From,
		To:             rep.To,
		TopDownloaders: userUsageMessages(rep.TopDownloaders),
		TopDeployers:   userUsageMessages(rep.TopDeployers),
	}

	for _, r := range rep.Repositories {
		out.Repositories = append(out.Repositories, &relicv1.RepositoryUsage{
			Repository:  r.Repository,
			Project:     r.Project,
			Files:       r.Files,
			Bytes:       r.Bytes,
			GrowthBytes: r.Growth,
			Downloads:   r.Downloads,
			Deploys:     r.Deploys,
		})
	}

	for _, p := range rep.Projects {
		out.Projects = append(out.Projects, &relicv1.ProjectUsage{
			Project:     p.Project,
			Bytes:       p.Bytes,
			GrowthBytes: p.Growth,
			Downloads:   p.Downloads,
			Deploys:     p.Deploys,
		})
	}

	for _, d := range rep.Days {
		out.Days = append(out.Days, &relicv1.DayUsage{
			Day:       d.Day,
			Bytes:     d.Bytes,
			Downloads: d.Downloads,
			Deploys:   d.Deploys,
		})
	}

	return connect.NewResponse(out), nil
}
