package cvecatalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"relic/internal/platform/database"
)

// ErrUnknown is returned for a CVE no finding names.
var ErrUnknown = errors.New("cve: no finding names this CVE")

// Facts are what the feeds say about one CVE.
type Facts struct {
	ID             string
	EPSS           *float64
	EPSSPercentile *float64
	KEVAdded       *time.Time
}

// Summary is a CVE in a list, with how much it affects.
type Summary struct {
	ID               string
	Title            string
	Published        *time.Time
	CVSSScore        *float64
	CVSSVersion      string
	EPSS             *float64
	EPSSPercentile   *float64
	KEVAdded         *time.Time
	KEVRansomware    bool
	SSVCExploitation string
	Artifacts        int64
	Packages         int64
	SeverityRank     int32 // of the most severe finding; see Filter.SeverityRank
}

// Details is everything known about one CVE.
type Details struct {
	ID, Title, Description, State string
	Published, Updated            *time.Time
	CWEs, References              []string
	CVSSScore                     *float64
	CVSSVector                    string
	CVSSVersion                   string
	CVSSSource                    string
	SSVCExploitation              string
	SSVCAutomatable               string
	SSVCImpact                    string
	EPSS, EPSSPercentile          *float64
	EPSSDate                      *time.Time
	KEVAdded, KEVDue              *time.Time
	KEVRansomware                 bool
	KEVAction                     string
	RecordChecked                 *time.Time
}

// Affected is a finding naming a CVE, with one item holding the content.
type Affected struct {
	VulnID         string
	Severity       string
	PackageName    string
	PackageVersion string
	PURL           string
	FixedVersions  []string
	Sha256         string
	Repository     string
	Path           string
	Name           string
}

// Feed says when a feed was last read.
type Feed struct {
	Name    string
	Version string
	Fetched time.Time
}

// Filter selects CVEs.
type Filter struct {
	Query   string
	KEVOnly bool
	MinEPSS float64
	// SeverityRank is 0 Critical … 4 Unknown; -1 for any.
	SeverityRank int32
	// Sort is epss (default), cvss, published, affected or id.
	Sort string
	Skip int
	Max  int
}

// Catalog reads the CVE facts the refresher keeps.
type Catalog struct {
	db *database.DB
}

// NewCatalog returns a Catalog reading db.
func NewCatalog(db *database.DB) *Catalog {
	return &Catalog{
		db: db,
	}
}

// Page returns a page of the CVEs findings name, and how many match.
func (c *Catalog) Page(ctx context.Context, f Filter) ([]Summary, int64, error) {
	//nolint:gosec // Skip is clamped, Max bounded by callers
	rows, err := c.db.ListCVEPage(ctx, database.ListCVEPageParams{
		Query:        f.Query,
		KevOnly:      f.KEVOnly,
		MinEpss:      f.MinEPSS,
		SeverityRank: f.SeverityRank,
		Sort:         f.Sort,
		Skip:         int32(min(f.Skip, math.MaxInt32)),
		MaxRows:      int32(f.Max),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list CVEs: %w", err)
	}

	total, err := c.db.CountCVEs(
		ctx,
		database.CountCVEsParams{
			Query:        f.Query,
			KevOnly:      f.KEVOnly,
			MinEpss:      f.MinEPSS,
			SeverityRank: f.SeverityRank,
		},
	)
	if err != nil {
		return nil, 0, fmt.Errorf("count CVEs: %w", err)
	}

	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		out = append(out, Summary{
			ID:               r.ID,
			Title:            r.Title,
			Published:        r.Published,
			CVSSScore:        r.CvssScore,
			CVSSVersion:      r.CvssVersion,
			EPSS:             r.Epss,
			EPSSPercentile:   r.EpssPercentile,
			KEVAdded:         r.KevAdded,
			KEVRansomware:    r.KevRansomware,
			SSVCExploitation: r.SsvcExploitation,
			Artifacts:        r.Artifacts,
			Packages:         r.Packages,
			SeverityRank:     r.SeverityRank,
		})
	}

	return out, total, nil
}

// Get returns one CVE; ErrUnknown when no finding names it.
func (c *Catalog) Get(ctx context.Context, id string) (Details, error) {
	r, err := c.db.GetCVE(ctx, id)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Details{}, ErrUnknown
	}

	if err != nil {
		return Details{}, fmt.Errorf("get %s: %w", id, err)
	}

	return Details{
		ID:               r.ID,
		Title:            r.Title,
		Description:      r.Description,
		State:            r.State,
		Published:        r.Published,
		Updated:          r.Updated,
		CWEs:             r.Cwes,
		References:       r.Refs,
		CVSSScore:        r.CvssScore,
		CVSSVector:       r.CvssVector,
		CVSSVersion:      r.CvssVersion,
		CVSSSource:       r.CvssSource,
		SSVCExploitation: r.SsvcExploitation,
		SSVCAutomatable:  r.SsvcAutomatable,
		SSVCImpact:       r.SsvcImpact,
		EPSS:             r.Epss,
		EPSSPercentile:   r.EpssPercentile,
		EPSSDate:         r.EpssDate,
		KEVAdded:         r.KevAdded,
		KEVDue:           r.KevDue,
		KEVRansomware:    r.KevRansomware,
		KEVAction:        r.KevAction,
		RecordChecked:    r.RecordChecked,
	}, nil
}

// Affected returns the findings that name id.
func (c *Catalog) Affected(ctx context.Context, id string) ([]Affected, error) {
	rows, err := c.db.CVEFindings(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("findings of %s: %w", id, err)
	}

	out := make([]Affected, 0, len(rows))
	for _, f := range rows {
		out = append(out, Affected{
			VulnID:         f.VulnID,
			Severity:       f.Severity,
			PackageName:    f.PackageName,
			PackageVersion: f.PackageVersion,
			PURL:           f.Purl,
			FixedVersions:  f.FixedVersions,
			Sha256:         f.Sha256,
			Repository:     f.RepoKey,
			Path:           f.Path,
			Name:           f.Name,
		})
	}

	return out, nil
}

// FactsFor returns the feeds' facts of the given CVEs, by ID.
func (c *Catalog) FactsFor(ctx context.Context, ids []string) (map[string]Facts, error) {
	rows, err := c.db.CVEFactsFor(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("CVE facts: %w", err)
	}

	out := make(map[string]Facts, len(rows))
	for _, r := range rows {
		out[r.ID] = Facts{
			ID:             r.ID,
			EPSS:           r.Epss,
			EPSSPercentile: r.EpssPercentile,
			KEVAdded:       r.KevAdded,
		}
	}

	return out, nil
}

// Feeds returns when the EPSS and KEV feeds were last read; a feed never
// read is left out.
func (c *Catalog) Feeds(ctx context.Context) ([]Feed, error) {
	out := []Feed{}

	for _, name := range []string{
		"epss",
		"kev",
	} {
		f, err := c.db.GetCVEFeed(ctx, name)
		switch {
		case err == nil:
			out = append(out, Feed{
				Name:    f.Name,
				Version: f.Version,
				Fetched: f.Fetched,
			})
		case !errors.Is(database.NotFound(err), database.ErrNotFound):
			return nil, fmt.Errorf("feed %s: %w", name, err)
		}
	}

	return out, nil
}
