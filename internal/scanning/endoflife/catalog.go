package endoflife

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Kinds of operational risk.
const (
	KindEndOfLife    = "end_of_life"
	KindDeprecated   = "deprecated"
	KindUnmaintained = "unmaintained"
)

// Severities of operational risks.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// warnWithin is how soon an announced end of life is reported.
const warnWithin = 90 * 24 * time.Hour

// Package is a component to assess.
type Package struct {
	PURL    string
	Name    string
	Version string
}

// Distro is the operating system of an image, as os-release names it.
type Distro struct {
	ID      string
	Version string
}

// Risk is an operational risk a component carries.
type Risk struct {
	Kind     string
	Severity string
	PURL     string
	Name     string
	Version  string
	Product  string
	Cycle    string
	// EOLFrom is the ISO date of the end of life, "" when there is none.
	EOLFrom string
	Detail  string
}

// Status is what end-of-life data the server holds.
type Status struct {
	Source   string
	Products int
	Fetched  time.Time
	Changed  time.Time
}

// distroProducts maps os-release IDs to endoflife.date products where the
// names differ.
var distroProducts = map[string]string{
	"alpine":        "alpine-linux",
	"rocky":         "rocky-linux",
	"amzn":          "amazon-linux",
	"ol":            "oracle-linux",
	"opensuse-leap": "opensuse",
}

// Catalog assesses components against the stored end-of-life data and,
// when set, deps.dev.
type Catalog struct {
	db   *database.DB
	deps *DepsDev
	now  func() time.Time
}

// NewCatalog returns a Catalog; deps nil leaves deprecation and maintenance
// unchecked.
func NewCatalog(db *database.DB, deps *DepsDev) *Catalog {
	return &Catalog{
		db:   db,
		deps: deps,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Changed is when the end-of-life data last changed; the zero time when
// there is none.
func (c *Catalog) Changed(ctx context.Context) (time.Time, error) {
	f, err := c.db.LatestEndOfLifeFeed(ctx)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return time.Time{}, nil
	}

	return f.Changed, err
}

// Status reports the data the server holds.
func (c *Catalog) Status(ctx context.Context) (Status, error) {
	f, err := c.db.LatestEndOfLifeFeed(ctx)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Status{}, nil
	}

	if err != nil {
		return Status{}, err
	}

	return Status{
		Source:   f.Source,
		Products: int(f.Products),
		Fetched:  f.Fetched,
		Changed:  f.Changed,
	}, nil
}

// Assess returns the operational risks of an artifact's components and
// operating system.
func (c *Catalog) Assess(ctx context.Context, pkgs []Package, distro Distro) (risks []Risk, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "endoflife.Assess")
	defer func() {
		telemetry.End(span, err)
	}()

	byKey := map[string][]Package{}
	keys := make([]string, 0, len(pkgs))

	for _, p := range pkgs {
		key, ok := Key(p.PURL)
		if !ok || p.Version == "" {
			continue
		}

		if _, seen := byKey[key]; !seen {
			keys = append(keys, key)
		}

		byKey[key] = append(byKey[key], p)
	}

	if len(keys) > 0 {
		rows, err := c.db.ListEndOfLifeCycles(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("load end-of-life data: %w", err)
		}

		cycles := map[string][]database.ListEndOfLifeCyclesRow{}
		for _, r := range rows {
			cycles[r.Purl] = append(cycles[r.Purl], r)
		}

		for key, ps := range byKey {
			for _, p := range ps {
				risks = append(risks, c.packageRisks(p, cycles[key])...)
			}
		}
	}

	if distro.ID != "" && distro.Version != "" {
		r, err := c.distroRisk(ctx, distro)
		if err != nil {
			return nil, err
		}

		risks = append(risks, r...)
	}

	if c.deps != nil {
		risks = append(risks, c.deps.Risks(ctx, pkgs)...)
	}

	for _, r := range risks {
		telemetry.OperationalRiskFound(r.Kind)
	}

	return risks, nil
}

func (c *Catalog) packageRisks(p Package, rows []database.ListEndOfLifeCyclesRow) []Risk {
	var (
		best    database.ListEndOfLifeCyclesRow
		matched bool
	)

	for _, r := range rows {
		if inCycle(p.Version, r.Cycle) && (!matched || len(r.Cycle) > len(best.Cycle)) {
			best, matched = r, true
		}
	}

	if !matched {
		return nil
	}

	r, ok := c.cycleRisk(best.Label, best.Cycle, best.EolFrom, best.IsEol)
	if !ok {
		return nil
	}

	r.PURL, r.Name, r.Version, r.Product = p.PURL, p.Name, p.Version, best.Product

	return []Risk{
		r,
	}
}

func (c *Catalog) distroRisk(ctx context.Context, d Distro) ([]Risk, error) {
	product := distroProducts[d.ID]
	if product == "" {
		product = d.ID
	}

	rows, err := c.db.ListEndOfLifeProductCycles(ctx, []string{
		product,
	})
	if err != nil {
		return nil, fmt.Errorf("load end-of-life data: %w", err)
	}

	var (
		best    database.ListEndOfLifeProductCyclesRow
		matched bool
	)

	for _, r := range rows {
		if inCycle(d.Version, r.Cycle) && (!matched || len(r.Cycle) > len(best.Cycle)) {
			best, matched = r, true
		}
	}

	if !matched {
		return nil, nil
	}

	r, ok := c.cycleRisk(best.Label, best.Cycle, best.EolFrom, best.IsEol)
	if !ok {
		return nil, nil
	}

	r.PURL = "pkg:generic/" + d.ID + "@" + d.Version
	r.Name, r.Version, r.Product = d.ID, d.Version, best.Product

	return []Risk{
		r,
	}, nil
}

func (c *Catalog) cycleRisk(label, cycle, eolFrom string, isEOL bool) (Risk, bool) {
	now := c.now()
	r := Risk{
		Kind:    KindEndOfLife,
		Cycle:   cycle,
		EOLFrom: eolFrom,
	}

	until, err := time.Parse(time.DateOnly, eolFrom)
	dated := err == nil

	switch {
	case dated && !until.After(now):
		r.Severity = SeverityHigh
		r.Detail = fmt.Sprintf("%s %s reached end of life on %s", label, cycle, eolFrom)
	case isEOL:
		r.Severity = SeverityHigh
		r.Detail = fmt.Sprintf("%s %s is past end of life", label, cycle)
	case dated && until.Sub(now) <= warnWithin:
		r.Severity = SeverityLow
		r.Detail = fmt.Sprintf("%s %s reaches end of life on %s", label, cycle, eolFrom)
	default:
		return Risk{}, false
	}

	return r, true
}

// inCycle reports whether version belongs to the release cycle: it is the
// cycle, or starts with it followed by something other than a letter or a
// digit (1.27.1 is in 1.27, 1.270 is not).
func inCycle(version, cycle string) bool {
	v := normalVersion(version)
	if cycle == "" || !strings.HasPrefix(v, cycle) {
		return false
	}

	if len(v) == len(cycle) {
		return true
	}

	next := rune(v[len(cycle)])

	return !unicode.IsLetter(next) && !unicode.IsDigit(next)
}

func normalVersion(v string) string {
	if i := strings.Index(v, ":"); i >= 0 && i < len(v)-1 && strings.Trim(v[:i], "0123456789") == "" {
		v = v[i+1:]
	}

	return strings.TrimPrefix(v, "v")
}
