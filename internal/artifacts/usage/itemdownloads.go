package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// FlushInterval is how often counts are written.
const FlushInterval = 5 * time.Second

const maxPendingItems = 100_000

type pending struct {
	count int64
	last  time.Time
	by    string
}

// ItemDownloads counts the downloads of each item.
type ItemDownloads struct {
	db  *database.DB
	log *slog.Logger
	now func() time.Time

	mu         sync.Mutex
	pending    map[uuid.UUID]pending
	maxPending int
	// usage also counts each download by repository and user.
	usage func(repository, user string)
}

// SetUsage makes every download also counted by fn, by repository and user.
func (r *ItemDownloads) SetUsage(fn func(repository, user string)) { r.usage = fn }

// NewItemDownloads returns an ItemDownloads writing to db.
func NewItemDownloads(db *database.DB, log *slog.Logger) *ItemDownloads {
	return &ItemDownloads{
		db:         db,
		log:        log,
		now:        time.Now,
		pending:    map[uuid.UUID]pending{},
		maxPending: maxPendingItems,
	}
}

// Download counts one download of item, stored in repository, by user.
func (r *ItemDownloads) Download(item uuid.UUID, repository, user string) {
	if r == nil {
		return
	}

	if r.usage != nil {
		r.usage(repository, user)
	}

	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.pending[item]
	if !ok && len(r.pending) >= r.maxPending {
		telemetry.ItemDownloadsDropped(1)
		return
	}

	p.count++
	if !now.Before(p.last) {
		p.last, p.by = now, user
	}

	r.pending[item] = p
}

// Flush writes the counts gathered so far.
func (r *ItemDownloads) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	batch := r.pending
	r.pending = map[uuid.UUID]pending{}
	r.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}

	params := database.RecordDownloadsParams{}
	for id, p := range batch {
		params.ItemIds = append(params.ItemIds, id)
		params.Counts = append(params.Counts, p.count)
		params.Lasts = append(params.Lasts, p.last)
		params.Bys = append(params.Bys, p.by)
	}

	if err := r.db.RecordDownloads(ctx, params); err != nil {
		r.keep(batch)
		return err
	}

	return nil
}

func (r *ItemDownloads) keep(batch map[uuid.UUID]pending) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var dropped int64

	for id, p := range batch {
		q, ok := r.pending[id]
		if !ok && len(r.pending) >= r.maxPending {
			dropped += p.count
			continue
		}

		q.count += p.count
		if !p.last.Before(q.last) {
			q.last, q.by = p.last, p.by
		}

		r.pending[id] = q
	}

	if dropped > 0 {
		telemetry.ItemDownloadsDropped(dropped)
	}
}

// Run flushes every FlushInterval until ctx ends, then flushes once more.
func (r *ItemDownloads) Run(ctx context.Context) {
	t := time.NewTicker(FlushInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()

			if err := r.Flush(flushCtx); err != nil {
				r.log.WarnContext(ctx, "final download stats flush failed", "error", err)
			}

			return
		case <-t.C:
			if err := r.Flush(ctx); err != nil {
				r.log.WarnContext(ctx, "download stats flush failed", "error", err)
			}
		}
	}
}

// Totals are an item's recorded downloads.
type Totals struct {
	Count            int64
	LastDownloadedAt time.Time
	LastDownloadedBy string
}

// Of returns the downloads recorded for item; zero Totals when there are
// none, or without a recorder.
func (r *ItemDownloads) Of(ctx context.Context, item uuid.UUID) (Totals, error) {
	if r == nil {
		return Totals{}, nil
	}

	d, err := r.db.GetDownloadStats(ctx, item)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Totals{}, nil
	}

	if err != nil {
		return Totals{}, fmt.Errorf("download stats: %w", err)
	}

	return Totals{
		Count:            d.Downloads,
		LastDownloadedAt: d.LastDownloaded,
		LastDownloadedBy: d.LastDownloadedBy,
	}, nil
}
