package malicious

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// DefaultFeed is OSV's bucket of per-ecosystem archives
// (<feed>/<ecosystem>/all.zip).
const DefaultFeed = "https://osv-vulnerabilities.storage.googleapis.com"

const maxArchive = 2 << 30

// Refresher reads the ecosystems' archives into the table, keeping the
// MAL- records only.
type Refresher struct {
	db     *database.DB
	feed   string
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
}

// NewRefresher returns a Refresher of the feed; client nil uses a default
// one.
func NewRefresher(db *database.DB, feed string, client *http.Client, log *slog.Logger) *Refresher {
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Minute,
		}
	}

	return &Refresher{
		db:     db,
		feed:   strings.TrimSuffix(feed, "/"),
		client: client,
		log:    log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Refresh reads each ecosystem's archive that changed since the last run.
func (r *Refresher) Refresh(ctx context.Context) (changed bool, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "malicious.Refresh")
	defer func() {
		telemetry.End(span, err)
	}()

	var errs []error

	for _, eco := range Ecosystems {
		c, err := r.refreshEcosystem(ctx, eco)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", eco.OSV, err))
		}

		changed = changed || c
	}

	return changed, errors.Join(errs...)
}

func (r *Refresher) refreshEcosystem(ctx context.Context, eco Ecosystem) (bool, error) {
	prev, err := r.db.GetMaliciousFeed(ctx, eco.OSV)
	if err != nil && !errors.Is(database.NotFound(err), database.ErrNotFound) {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.feed+"/"+eco.OSV+"/all.zip", nil)
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

	now := r.now()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return false, r.db.SaveMaliciousFeed(ctx, database.SaveMaliciousFeedParams{
			Ecosystem: eco.OSV,
			Etag:      prev.Etag,
			Fetched:   now,
			Changed:   prev.Changed,
		})
	case http.StatusOK:
	default:
		return false, fmt.Errorf("feed answered %s", resp.Status)
	}

	tmp, err := os.CreateTemp("", "relic-osv-*.zip")
	if err != nil {
		return false, err
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	size, err := io.Copy(tmp, io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return false, fmt.Errorf("download: %w", err)
	}

	if size > maxArchive {
		return false, errors.New("the archive is larger than 2 GiB")
	}

	n, err := r.load(ctx, eco, tmp, size)
	if err != nil {
		return false, err
	}

	r.log.InfoContext(ctx, "malicious packages: feed read", "ecosystem", eco.OSV, "advisories", n)

	return true, r.db.SaveMaliciousFeed(ctx, database.SaveMaliciousFeedParams{
		Ecosystem: eco.OSV,
		Etag:      resp.Header.Get("ETag"),
		Fetched:   now,
		Changed:   r.now(),
	})
}

const advisoriesPerTransaction = 500

type advisory struct {
	id        string
	withdrawn bool
	packages  []Package
}

func (r *Refresher) load(ctx context.Context, eco Ecosystem, archive io.ReaderAt, size int64) (int, error) {
	zr, err := zip.NewReader(archive, size)
	if err != nil {
		return 0, fmt.Errorf("read archive: %w", err)
	}

	stored := 0

	err = r.readBatches(ctx, zr, eco, advisoriesPerTransaction, func(batch []advisory) error {
		if err := r.store(ctx, batch); err != nil {
			return err
		}

		for _, a := range batch {
			if !a.withdrawn {
				stored++
			}
		}

		return nil
	})

	return stored, err
}

func (r *Refresher) readBatches(
	ctx context.Context,
	zr *zip.Reader,
	eco Ecosystem,
	size int,
	store func([]advisory) error,
) error {
	batch := make([]advisory, 0, size)

	for _, f := range zr.File {
		name := path.Base(f.Name)
		if !strings.HasPrefix(name, "MAL-") || !strings.HasSuffix(name, ".json") || f.UncompressedSize64 > 1<<20 {
			continue
		}

		b, err := readEntry(f)
		if err != nil {
			return err
		}

		pkgs, id, withdrawn, err := parse(b, eco)
		if err != nil {
			r.log.WarnContext(ctx, "malicious packages: unreadable record", "file", name, "error", err)

			continue
		}

		batch = append(batch, advisory{
			id:        id,
			withdrawn: withdrawn,
			packages:  pkgs,
		})

		if len(batch) == size {
			if err := store(batch); err != nil {
				return err
			}

			batch = batch[:0]
		}
	}

	if len(batch) == 0 {
		return nil
	}

	return store(batch)
}

func (r *Refresher) store(ctx context.Context, batch []advisory) error {
	return r.db.WithTx(ctx, func(q database.Querier) error {
		for _, a := range batch {
			if a.withdrawn {
				if err := q.DeleteMaliciousAdvisory(ctx, a.id); err != nil {
					return err
				}

				continue
			}

			for _, p := range a.packages {
				ranges, err := json.Marshal(p.Ranges)
				if err != nil {
					return err
				}

				if err := q.UpsertMaliciousPackage(ctx, database.UpsertMaliciousPackageParams{
					ID:          p.ID,
					Ecosystem:   p.Ecosystem,
					Name:        p.Name,
					AllVersions: p.AllVersions,
					Versions:    nonNil(p.Versions),
					Ranges:      ranges,
					Summary:     p.Summary,
					Modified:    p.Modified,
				}); err != nil {
					return fmt.Errorf("store %s: %w", p.ID, err)
				}
			}
		}

		return nil
	})
}

func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	return io.ReadAll(io.LimitReader(rc, 1<<20))
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
