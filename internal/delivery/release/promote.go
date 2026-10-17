package release

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/telemetry"
)

// PromotionSpec promotes a release version to an environment.
type PromotionSpec struct {
	Environment string
	// Include limits the target repositories to these; empty for all.
	Include []string
	// Exclude leaves these repositories out.
	Exclude []string
	// Move asks to remove the files from the previous environment.
	Move bool
}

// Promotion is a recorded promotion of a release version.
type Promotion struct {
	Status        Status
	Repository    string // the release's storing repository
	Name          string
	Version       string
	Environment   string
	Include       []string
	Exclude       []string
	Messages      []string
	CreatedBy     string
	Created       time.Time
	CreatedMillis int64
}

const promotionRecordAttempts = 10

// Promote copies a completed release version's artifacts into the local
// repositories assigned to the environment (their "environments" setting),
// narrowed by the include and exclude lists, and records the promotion.
func (s *Service) Promote(
	ctx context.Context,
	project, name, version string,
	req PromotionSpec,
	user string,
	access Access,
) (_ Promotion, err error) {
	ctx, done := telemetry.Operation(ctx, "release.Promote", func(err error) {
		telemetry.Promoted("release", err)
	})
	defer func() {
		done(err)
	}()

	if req.Environment == "" {
		return Promotion{}, fmt.Errorf("%w: environment is required", ErrInvalid)
	}

	b, err := s.Get(ctx, project, name, version)
	if err != nil {
		return Promotion{}, err
	}

	if b.Draft {
		return Promotion{}, fmt.Errorf("%w: a draft cannot be promoted", ErrInvalid)
	}

	if b.Status != StatusCompleted {
		return Promotion{}, fmt.Errorf("%w: release version %s is %s, not completed", ErrInvalid, version, b.Status)
	}

	stored, err := s.storedArtifacts(ctx, b)
	if err != nil {
		return Promotion{}, err
	}

	targets, err := s.environmentRepos(ctx, req)
	if err != nil {
		return Promotion{}, err
	}

	var msgs []string
	if req.Move {
		msgs = append(msgs, "promotion type move is performed as copy")
	}

	type placement struct {
		artifact Artifact
		target   string
	}

	var placements []placement

	sourceTypes := map[string]repository.Format{}
	for _, a := range b.Artifacts {
		target, ok := s.pickTarget(ctx, targets, a.Repository, sourceTypes)
		if !ok {
			msgs = append(msgs, fmt.Sprintf("no repository in environment %s accepts %s", req.Environment, a.Path))
			continue
		}

		if !access.canWrite(target, a.Path) {
			return Promotion{}, fmt.Errorf("%w: cannot write %s/%s", ErrForbidden, target, a.Path)
		}

		placements = append(placements, placement{
			artifact: a,
			target:   target,
		})
	}

	if len(placements) == 0 && len(b.Artifacts) > 0 {
		return Promotion{}, fmt.Errorf(
			"%w: no repository in environment %s accepts any of the version's artifacts",
			ErrInvalid,
			req.Environment,
		)
	}

	if _, err := s.checkGates(ctx, b, req.Environment, user); err != nil {
		return Promotion{}, err
	}

	var copies []repository.CopyInput

	for _, p := range placements {
		if in, needed := s.artifactCopy(ctx, p.artifact, stored[p.artifact.Path], p.target, user, access); needed {
			copies = append(copies, in)
		}
	}

	if len(copies) > 0 {
		if err := s.repos.CopyAll(ctx, copies); err != nil {
			return Promotion{}, fmt.Errorf("promote to %s: %w", req.Environment, err)
		}
	}

	now := s.now()
	p := Promotion{
		Status:        StatusCompleted,
		Repository:    b.Repository,
		Name:          b.Name,
		Version:       b.Version,
		Environment:   req.Environment,
		Include:       nonNil(req.Include),
		Exclude:       nonNil(req.Exclude),
		Messages:      nonNil(msgs),
		CreatedBy:     user,
		Created:       now,
		CreatedMillis: now.UnixMilli(),
	}

	encoded, err := json.Marshal(p.Messages)
	if err != nil {
		return Promotion{}, fmt.Errorf("encode promotion messages: %w", err)
	}

	for attempt := 1; ; attempt++ {
		err = s.db.InsertReleasePromotion(ctx, database.InsertReleasePromotionParams{
			ID:                   uuid.NewV7(),
			ReleaseID:            b.ID,
			Environment:          p.Environment,
			Status:               string(p.Status),
			IncludedRepositories: p.Include,
			ExcludedRepositories: p.Exclude,
			Messages:             encoded,
			CreatedBy:            user,
			Created:              now,
			CreatedMillis:        p.CreatedMillis,
		})
		if !database.IsUniqueViolation(err) || attempt == promotionRecordAttempts {
			break
		}

		p.CreatedMillis++
	}

	if err != nil {
		return Promotion{}, fmt.Errorf("record promotion: %w", err)
	}

	events.Publish(ctx, s.events, events.Event{
		Type:    events.ReleasePromoted,
		Subject: b.Folder(),
		Actor:   user,
		Data: map[string]any{
			"project":     b.Project,
			"name":        b.Name,
			"version":     b.Version,
			"environment": p.Environment,
			"artifacts":   len(b.Artifacts),
		},
	})

	return p, nil
}

func (s *Service) environmentRepos(ctx context.Context, req PromotionSpec) ([]repository.Repository, error) {
	locals, err := s.repos.ListRepositories(ctx, repository.ClassLocal, "")
	if err != nil {
		return nil, err
	}

	var (
		out      []repository.Repository
		assigned int
	)

	for _, r := range locals {
		if r.Format == repository.FormatRelease || !slices.ContainsFunc(r.Environments(), func(e string) bool {
			return strings.EqualFold(e, req.Environment)
		}) {
			continue
		}

		assigned++

		if len(req.Include) > 0 && !slices.Contains(req.Include, r.Name) || slices.Contains(req.Exclude, r.Name) {
			continue
		}

		out = append(out, r)
	}

	switch {
	case assigned == 0:
		return nil, fmt.Errorf("%w: no local repository is assigned to environment %s", ErrInvalid, req.Environment)
	case len(out) == 0:
		return nil, fmt.Errorf(
			"%w: the include and exclude repositories leave none of environment %s's %d",
			ErrInvalid,
			req.Environment,
			assigned,
		)
	}

	return out, nil
}

func (s *Service) pickTarget(
	ctx context.Context,
	targets []repository.Repository,
	sourceRepo string,
	types map[string]repository.Format,
) (string, bool) {
	if len(targets) == 1 {
		return targets[0].Name, true
	}

	pt, ok := types[sourceRepo]
	if !ok {
		pt = repository.FormatGeneric
		if r, err := s.repos.GetRepository(ctx, sourceRepo); err == nil {
			pt = r.Format
		}

		types[sourceRepo] = pt
	}

	for _, want := range []repository.Format{
		pt,
		repository.FormatGeneric,
	} {
		for _, t := range targets {
			if t.Format == want {
				return t.Name, true
			}
		}
	}

	return "", false
}

// artifactCopy is the copy that promotes a to target; needed is false when
// target already holds it.
func (s *Service) artifactCopy(
	ctx context.Context,
	a Artifact,
	src repository.Item,
	target, user string,
	access Access,
) (_ repository.CopyInput, needed bool) {
	if cur, err := s.repos.Stat(ctx, target, a.Path); err == nil && cur.Checksums.SHA256 == a.SHA256 {
		return repository.CopyInput{}, false
	}

	return repository.CopyInput{
		Source:                src,
		DestinationRepository: target,
		DestinationPath:       a.Path,
		User:                  user,
		RefuseOverwrite: func(_ context.Context, existing repository.Item) error {
			if access.canDelete(existing.Repository, existing.Path()) {
				return nil
			}

			return fmt.Errorf(
				"%w: overwriting %s/%s needs delete permission",
				ErrForbidden,
				existing.Repository,
				existing.Path(),
			)
		},
	}, true
}

// Promotions lists a version's promotions, newest first.
func (s *Service) Promotions(ctx context.Context, project, name, version string) ([]Promotion, error) {
	b, err := s.Get(ctx, project, name, version)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListReleasePromotions(ctx, b.ID)
	if err != nil {
		return nil, fmt.Errorf("list promotions: %w", err)
	}

	out := make([]Promotion, 0, len(rows))
	for _, r := range rows {
		p := Promotion{
			Status:        Status(r.Status),
			Repository:    b.Repository,
			Name:          b.Name,
			Version:       b.Version,
			Environment:   r.Environment,
			Include:       r.IncludedRepositories,
			Exclude:       r.ExcludedRepositories,
			CreatedBy:     r.CreatedBy,
			Created:       r.Created,
			CreatedMillis: r.CreatedMillis,
		}
		if err := json.Unmarshal(r.Messages, &p.Messages); err != nil {
			return nil, fmt.Errorf("decode promotion messages: %w", err)
		}

		out = append(out, p)
	}

	return out, nil
}

// DeletePromotion removes a promotion record.
func (s *Service) DeletePromotion(ctx context.Context, project, name, version string, createdMillis int64) error {
	b, err := s.Get(ctx, project, name, version)
	if err != nil {
		return err
	}

	n, err := s.db.DeleteReleasePromotion(
		ctx,
		database.DeleteReleasePromotionParams{
			ReleaseID:     b.ID,
			CreatedMillis: createdMillis,
		},
	)
	if err != nil {
		return fmt.Errorf("delete promotion: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("%w: no promotion created at %d", ErrNotFound, createdMillis)
	}

	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}
