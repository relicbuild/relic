package replication

import (
	"context"
	"errors"
	"log/slog"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/events"
)

// Changes follows deploys and deletes: it keeps a federated repository's
// tombstones, and queues a run of a replication that synchronizes on change
// or is federated. It implements events.Publisher.
type Changes struct {
	pusher  *Pusher
	enqueue func(ctx context.Context, repository string) error
	log     *slog.Logger
}

// NewChanges returns Changes; enqueue may be nil where no queue is at hand.
func NewChanges(p *Pusher, enqueue func(context.Context, string) error, log *slog.Logger) *Changes {
	return &Changes{
		pusher:  p,
		enqueue: enqueue,
		log:     log,
	}
}

func (c *Changes) config(ctx context.Context, repo string) (Config, bool, error) {
	cfg, _, err := c.pusher.Load(ctx, repo)
	if errors.Is(err, ErrNotConfigured) {
		return Config{}, false, nil
	}

	if err != nil {
		return Config{}, false, err
	}

	return cfg, cfg.Enabled && cfg.TargetURL != "", nil
}

// Publish implements events.Publisher.
func (c *Changes) Publish(ctx context.Context, e events.Event) {
	if e.Type != events.ArtifactDeployed && e.Type != events.ArtifactDeleted {
		return
	}

	repo, _ := e.Data["repository"].(string)
	path, _ := e.Data["path"].(string)

	cfg, ok, err := c.config(ctx, repo)
	if err != nil {
		c.log.WarnContext(ctx, "replication settings", "repo", repo, "path", path, "error", err)

		if e.Type == events.ArtifactDeleted {
			if err := c.pusher.RecordDelete(ctx, repo, path, e.Time); err != nil {
				c.log.WarnContext(ctx, "replication tombstone", "repo", repo, "path", path, "error", err)
			}
		}

		return
	}

	if !ok {
		return
	}

	federated := cfg.Effective() == DirectionFederated

	if federated {
		var err error
		if e.Type == events.ArtifactDeleted {
			err = c.pusher.RecordDelete(ctx, repo, path, e.Time)
		} else {
			err = c.pusher.ForgetDelete(ctx, repo, path)
		}

		if err != nil {
			c.log.WarnContext(ctx, "replication tombstone", "repo", repo, "path", path, "error", err)
		}
	}

	if c.enqueue == nil || e.Actor == repository.SystemUser || (!cfg.OnChange && !federated) ||
		cfg.Effective() == DirectionPull {
		return
	}

	if err := c.enqueue(ctx, repo); err != nil {
		c.log.WarnContext(ctx, "queue replication", "repo", repo, "error", err)
	}
}
