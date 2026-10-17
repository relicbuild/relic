package endoflife

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// DefaultURL is endoflife.date's full product dump.
const DefaultURL = "https://endoflife.date/api/v1/products/full"

// Sources of end-of-life data.
const (
	SourceFeed   = "feed"
	SourceImport = "import"
)

// Refresher keeps the end-of-life data: from a URL on a schedule, or from
// a file an operator imports.
type Refresher struct {
	db     *database.DB
	url    string
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
}

// NewRefresher returns a Refresher of url ("" when only imports feed it);
// client nil uses a default one.
func NewRefresher(db *database.DB, url string, client *http.Client, log *slog.Logger) *Refresher {
	if client == nil {
		client = &http.Client{
			Timeout: 5 * time.Minute,
		}
	}

	return &Refresher{
		db:     db,
		url:    url,
		client: client,
		log:    log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Refresh reads the URL's data when it changed since the last read.
func (r *Refresher) Refresh(ctx context.Context) (changed bool, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "endoflife.Refresh")

	defer func() {
		telemetry.EndOfLifeRefreshed(refreshResult(changed, err))
		telemetry.End(span, err)
	}()

	if r.url == "" {
		return false, nil
	}

	prev, err := r.db.GetEndOfLifeFeed(ctx, SourceFeed)
	if err != nil && !errors.Is(database.NotFound(err), database.ErrNotFound) {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return false, err
	}

	if prev.Etag != "" {
		req.Header.Set("If-None-Match", prev.Etag)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return false, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusNotModified:
		prev.Fetched = r.now()

		return false, r.saveFeed(ctx, prev)
	case http.StatusOK:
	default:
		return false, fmt.Errorf("end-of-life data: the source answered %s", resp.Status)
	}

	products, err := ParseDump(resp.Body)
	if err != nil {
		return false, err
	}

	if err := r.store(ctx, products, SourceFeed, resp.Header.Get("ETag")); err != nil {
		return false, err
	}

	r.log.InfoContext(ctx, "end-of-life data: read", "products", len(products))

	return true, nil
}

// Import replaces the end-of-life data with a dump an operator supplies.
func (r *Refresher) Import(ctx context.Context, body io.Reader) (n int, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "endoflife.Import")

	defer func() {
		telemetry.EndOfLifeRefreshed(refreshResult(true, err))
		telemetry.End(span, err)
	}()

	products, err := ParseDump(body)
	if err != nil {
		return 0, err
	}

	if err := r.store(ctx, products, SourceImport, ""); err != nil {
		return 0, err
	}

	r.log.InfoContext(ctx, "end-of-life data: imported", "products", len(products))

	return len(products), nil
}

const endOfLifeDataLock = "end-of-life-data"

func (r *Refresher) store(ctx context.Context, products []Product, source, etag string) error {
	now := r.now()

	return r.db.WithLock(ctx, endOfLifeDataLock, func() error {
		return r.db.WithTx(ctx, func(q database.Querier) error {
			if err := q.DeleteEndOfLifeData(ctx); err != nil {
				return err
			}

			for _, p := range products {
				if err := storeProduct(ctx, q, p); err != nil {
					return fmt.Errorf("store %s: %w", p.Name, err)
				}
			}

			return q.SaveEndOfLifeFeed(ctx, database.SaveEndOfLifeFeedParams{
				Source:   source,
				Etag:     etag,
				Fetched:  now,
				Changed:  now,
				Products: int32(min(len(products), 1<<30)), //nolint:gosec // bounded
			})
		})
	})
}

func storeProduct(ctx context.Context, q database.Querier, p Product) error {
	if err := q.InsertEndOfLifeProduct(ctx, database.InsertEndOfLifeProductParams{
		Product: p.Name,
		Label:   p.Label,
	}); err != nil {
		return err
	}

	for _, purl := range p.PURLs {
		if err := q.InsertEndOfLifeIdentifier(ctx, database.InsertEndOfLifeIdentifierParams{
			Purl:    purl,
			Product: p.Name,
		}); err != nil {
			return err
		}
	}

	for _, c := range p.Cycles {
		if err := q.InsertEndOfLifeCycle(ctx, database.InsertEndOfLifeCycleParams{
			Product:      p.Name,
			Cycle:        c.Name,
			EolFrom:      c.EOLFrom,
			IsEol:        c.IsEOL,
			IsMaintained: c.IsMaintained,
			Latest:       c.Latest,
		}); err != nil {
			return err
		}
	}

	return nil
}

func (r *Refresher) saveFeed(ctx context.Context, f database.EndOfLifeFeed) error {
	return r.db.SaveEndOfLifeFeed(ctx, database.SaveEndOfLifeFeedParams(f))
}

func refreshResult(changed bool, err error) string {
	switch {
	case err != nil:
		return "error"
	case changed:
		return "changed"
	default:
		return "unchanged"
	}
}
