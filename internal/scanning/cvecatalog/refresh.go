package cvecatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"relic/internal/platform/database"
)

// Feeds locates the public feeds.
type Feeds struct {
	// EPSS is FIRST's daily scores file (gzipped CSV).
	EPSS string
	// KEV is CISA's Known Exploited Vulnerabilities catalog (JSON).
	KEV string
	// Records is a CVE record service: <Records>/<CVE-ID> answers CVE JSON 5.
	Records string
}

const (
	epssEvery      = 20 * time.Hour
	kevEvery       = 6 * time.Hour
	recordEvery    = 7 * 24 * time.Hour
	recordsPerRun  = 400
	recordWorkers  = 4
	requestTimeout = 2 * time.Minute
)

// Refresher updates the cves table from the feeds.
type Refresher struct {
	db     *database.DB
	feeds  Feeds
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
}

// NewRefresher returns a Refresher; client nil uses a default one.
func NewRefresher(db *database.DB, feeds Feeds, client *http.Client, log *slog.Logger) *Refresher {
	if client == nil {
		client = &http.Client{
			Timeout: requestTimeout,
		}
	}

	return &Refresher{
		db:     db,
		feeds:  feeds,
		client: client,
		log:    log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Refresh registers the CVEs findings name, then reads each feed that is
// due.
func (r *Refresher) Refresh(ctx context.Context) error {
	added, err := r.db.RegisterFindingCVEs(ctx)
	if err != nil {
		return fmt.Errorf("register finding CVEs: %w", err)
	}

	if added > 0 {
		r.log.InfoContext(ctx, "cve: new CVEs from findings", "count", added)
	}

	return errors.Join(r.refreshEPSS(ctx), r.refreshKEV(ctx), r.refreshRecords(ctx))
}

func (r *Refresher) due(ctx context.Context, feed string, every time.Duration) bool {
	f, err := r.db.GetCVEFeed(ctx, feed)
	return err != nil || r.now().Sub(f.Fetched) >= every
}

func (r *Refresher) get(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "relic-cve")

	resp, err := r.client.Do(req) //nolint:gosec // an operator-configured feed URL
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFeed, redact(rawURL), err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s: %s", ErrFeed, redact(rawURL), resp.Status)
	}

	return resp.Body, nil
}

func (r *Refresher) refreshEPSS(ctx context.Context) error {
	if r.feeds.EPSS == "" || !r.due(ctx, "epss", epssEvery) {
		return nil
	}

	ids, err := r.db.KnownCVEIDs(ctx)
	if err != nil {
		return fmt.Errorf("known CVEs for EPSS: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}

	body, err := r.get(ctx, r.feeds.EPSS)
	if err != nil {
		return err
	}

	defer func() {
		_ = body.Close()
	}()

	scores, date, err := ReadEPSS(body, func(id string) bool {
		return known[id]
	})
	if err != nil {
		return err
	}

	p := database.SetCVEEPSSParams{
		ScoreDate: &date,
	}
	for id, s := range scores {
		p.Ids, p.Scores, p.Percentiles = append(p.Ids, id), append(p.Scores, s.Score), append(
			p.Percentiles,
			s.Percentile,
		)
	}

	if err := r.db.SetCVEEPSS(ctx, p); err != nil {
		return fmt.Errorf("store EPSS: %w", err)
	}

	r.log.InfoContext(
		ctx,
		"cve: EPSS updated",
		"scored",
		len(scores),
		"known",
		len(ids),
		"date",
		date.Format(time.DateOnly),
	)

	return r.db.SaveCVEFeed(
		ctx,
		database.SaveCVEFeedParams{
			Name:    "epss",
			Version: date.Format(time.DateOnly),
			Fetched: r.now(),
		},
	)
}

func (r *Refresher) refreshKEV(ctx context.Context) error {
	if r.feeds.KEV == "" || !r.due(ctx, "kev", kevEvery) {
		return nil
	}

	body, err := r.get(ctx, r.feeds.KEV)
	if err != nil {
		return err
	}

	defer func() {
		_ = body.Close()
	}()

	entries, version, err := ReadKEV(body)
	if err != nil {
		return err
	}

	p := database.SetCVEKEVParams{}
	for _, e := range entries {
		p.Ids = append(p.Ids, e.ID)
		p.Added = append(p.Added, e.Added)
		p.Due = append(p.Due, e.Due)
		p.Ransomware = append(p.Ransomware, e.Ransomware)
		p.Actions = append(p.Actions, e.Action)
	}

	if err := r.db.SetCVEKEV(ctx, p); err != nil {
		return fmt.Errorf("store KEV: %w", err)
	}

	r.log.InfoContext(ctx, "cve: KEV catalog read", "entries", len(entries), "version", version)

	return r.db.SaveCVEFeed(ctx, database.SaveCVEFeedParams{
		Name:    "kev",
		Version: version,
		Fetched: r.now(),
	})
}

func (r *Refresher) refreshRecords(ctx context.Context) error {
	if r.feeds.Records == "" {
		return nil
	}

	ids, err := r.db.CVEsDueForRecord(
		ctx,
		database.CVEsDueForRecordParams{
			CheckedBefore: new(r.now().Add(-recordEvery)),
			MaxRows:       recordsPerRun,
		},
	)
	if err != nil || len(ids) == 0 {
		return err
	}

	base := strings.TrimSuffix(r.feeds.Records, "/")

	var (
		mu       sync.Mutex
		failures []error
		missing  []string
		stored   int
	)

	jobs := make(chan string)

	var wg sync.WaitGroup
	for range recordWorkers {
		wg.Go(func() {
			for id := range jobs {
				err := r.readRecord(ctx, base, id)

				mu.Lock()
				switch {
				case err == nil:
					stored++
				case errors.Is(err, errNoRecord):
					missing = append(missing, id)
				default:
					failures = append(failures, err)
				}
				mu.Unlock()
			}
		})
	}

	for _, id := range ids {
		select {
		case jobs <- id:
		case <-ctx.Done():
		}
	}

	close(jobs)
	wg.Wait()

	if len(missing) > 0 {
		if err := r.db.MarkCVERecordChecked(
			ctx,
			database.MarkCVERecordCheckedParams{
				Checked: new(r.now()),
				Ids:     missing,
			},
		); err != nil {
			failures = append(failures, err)
		}
	}

	r.log.InfoContext(
		ctx,
		"cve: records read",
		"stored",
		stored,
		"missing",
		len(missing),
		"failed",
		len(failures),
		"due",
		len(ids),
	)

	if len(failures) > 0 {
		return fmt.Errorf(
			"%d of %d CVE record lookups failed: %w",
			len(failures),
			len(ids),
			errors.Join(failures[:min(len(failures), 3)]...),
		)
	}

	return ctx.Err()
}

var errNoRecord = errors.New("no CVE record")

func (r *Refresher) readRecord(ctx context.Context, base, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "relic-cve")

	resp, err := r.client.Do(req) //nolint:gosec // an operator-configured feed URL
	if err != nil {
		return fmt.Errorf("%w: record %s: %w", ErrFeed, id, err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNotFound {
		return errNoRecord
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: record %s: %s", ErrFeed, id, resp.Status)
	}

	rec, err := ReadRecord(resp.Body)
	if err != nil {
		return fmt.Errorf("record %s: %w", id, err)
	}

	return r.db.SaveCVERecord(ctx, database.SaveCVERecordParams{
		ID:               id,
		Title:            rec.Title,
		Description:      rec.Description,
		State:            rec.State,
		Published:        rec.Published,
		Updated:          rec.Updated,
		Cwes:             rec.CWEs,
		Refs:             rec.References,
		CvssScore:        rec.CVSS.Score,
		CvssVector:       rec.CVSS.Vector,
		CvssVersion:      rec.CVSS.Version,
		CvssSource:       rec.CVSS.Source,
		SsvcExploitation: rec.Exploitation,
		SsvcAutomatable:  rec.Automatable,
		SsvcImpact:       rec.TechnicalImpact,
		Checked:          new(r.now()),
	})
}

func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "feed"
	}

	u.User = nil

	return u.String()
}
