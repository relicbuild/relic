package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"relic/internal/platform/database"
	"relic/internal/scanning/endoflife"
)

// RiskAssessor finds the operational risks of components: past or near
// end of life, deprecated, unmaintained.
type RiskAssessor interface {
	Assess(ctx context.Context, pkgs []endoflife.Package, distro endoflife.Distro) ([]endoflife.Risk, error)
	// Changed is when the data the assessment reads last changed.
	Changed(ctx context.Context) (time.Time, error)
}

// OperationalRisk is a risk a scanned artifact's component carries.
type OperationalRisk struct {
	Kind           string
	Severity       Severity
	PURL           string
	PackageName    string
	PackageVersion string
	Product        string
	Cycle          string
	// EOLFrom is the ISO date of the end of life, "" when there is none.
	EOLFrom string
	Detail  string
}

// ListedRisk is an operational risk with one repository item holding its
// content.
type ListedRisk struct {
	OperationalRisk
	Sha256     string
	Repository string
	Path       string
	Name       string
}

// RisksQuery selects a page of operational risks across scanned content.
type RisksQuery struct {
	Kind       string // end_of_life, deprecated or unmaintained; empty for all
	Query      string // matched against package and product names
	Repository string // content in this repository; empty for all
	Skip       int
	Max        int
}

// SetOperationalRisk makes scans report operational risks; nil reports
// none.
func (s *Service) SetOperationalRisk(a RiskAssessor) { s.risks = a }

func (s *Service) assessRisks(ctx context.Context, sbom []byte) ([]endoflife.Risk, error) {
	comps, err := sbomComponents(sbom)
	if err != nil {
		s.log.WarnContext(
			ctx,
			"operational risk: SBOM components unreadable, assessing the distribution only",
			"error",
			err,
		)
	}

	pkgs := make([]endoflife.Package, 0, len(comps))
	for _, c := range comps {
		if c.PURL != "" {
			pkgs = append(pkgs, endoflife.Package{
				PURL:    c.PURL,
				Name:    c.Name,
				Version: c.Version,
			})
		}
	}

	return s.risks.Assess(ctx, pkgs, sbomDistro(sbom))
}

func storeRisks(
	ctx context.Context,
	q database.Querier,
	sha256Hex string,
	risks []endoflife.Risk,
	now time.Time,
) error {
	if err := q.DeleteOperationalRisks(ctx, sha256Hex); err != nil {
		return fmt.Errorf("clear operational risks: %w", err)
	}

	for _, r := range risks {
		if err := q.InsertOperationalRisk(ctx, database.InsertOperationalRiskParams{
			Sha256:         sha256Hex,
			Purl:           truncate(r.PURL, 2000),
			Kind:           r.Kind,
			Severity:       r.Severity,
			PackageName:    truncate(r.Name, 500),
			PackageVersion: truncate(r.Version, 200),
			Product:        r.Product,
			Cycle:          r.Cycle,
			EolFrom:        r.EOLFrom,
			Detail:         truncate(r.Detail, 1000),
		}); err != nil {
			return fmt.Errorf("record operational risk: %w", err)
		}
	}

	return q.SetRisksChecked(ctx, database.SetRisksCheckedParams{
		Sha256:  sha256Hex,
		Checked: &now,
	})
}

const risksBatch = 200

// CheckOperationalRisks assesses again the stored scans checked before the
// data last changed, a batch at a time, and returns how many it checked. A
// scan that cannot be assessed is logged and left for the next check.
func (s *Service) CheckOperationalRisks(ctx context.Context) (int, error) {
	if s.risks == nil {
		return 0, nil
	}

	changed, err := s.risks.Changed(ctx)
	if err != nil {
		return 0, err
	}

	rows, err := s.db.ListRiskUncheckedScans(ctx, database.ListRiskUncheckedScansParams{
		Changed: &changed,
		MaxRows: risksBatch,
	})
	if err != nil {
		return 0, err
	}

	checked := 0

	for _, r := range rows {
		if err := s.checkOperationalRisks(ctx, r.Sha256, r.Sbom); err != nil {
			s.log.WarnContext(ctx, "operational risk: scan not assessed", "sha256", r.Sha256, "error", err)

			continue
		}

		checked++
	}

	return checked, ctx.Err()
}

func (s *Service) checkOperationalRisks(ctx context.Context, sha256Hex string, packed []byte) error {
	sbom, err := gunzip(packed)
	if err != nil {
		return fmt.Errorf("read stored SBOM: %w", err)
	}

	risks, err := s.assessRisks(ctx, sbom)
	if err != nil {
		return err
	}

	return s.db.WithTx(ctx, func(q database.Querier) error {
		return storeRisks(ctx, q, sha256Hex, risks, s.now())
	})
}

// OperationalRisks returns the operational risks of scanned content.
func (r *Results) OperationalRisks(ctx context.Context, sha256Hex string) ([]OperationalRisk, error) {
	rows, err := r.db.ListOperationalRisks(ctx, sha256Hex)
	if err != nil {
		return nil, fmt.Errorf("operational risks of %s: %w", sha256Hex, err)
	}

	out := make([]OperationalRisk, 0, len(rows))
	for _, row := range rows {
		out = append(out, OperationalRisk{
			Kind:           row.Kind,
			Severity:       Severity(row.Severity),
			PURL:           row.Purl,
			PackageName:    row.PackageName,
			PackageVersion: row.PackageVersion,
			Product:        row.Product,
			Cycle:          row.Cycle,
			EOLFrom:        row.EolFrom,
			Detail:         row.Detail,
		})
	}

	return out, nil
}

// RisksPage returns a page of operational risks across scanned content
// still in a repository, most severe first, and how many there are.
func (r *Results) RisksPage(ctx context.Context, q RisksQuery) ([]ListedRisk, int64, error) {
	rows, err := r.db.ListOperationalRisksPage(ctx, database.ListOperationalRisksPageParams{
		Kind:       q.Kind,
		Query:      q.Query,
		Repository: q.Repository,
		Skip:       int32(min(q.Skip, math.MaxInt32)), //nolint:gosec // clamped
		MaxRows:    int32(q.Max),                      //nolint:gosec // bounded by callers
	})
	if err != nil {
		return nil, 0, fmt.Errorf("operational risks page: %w", err)
	}

	total, err := r.db.CountOperationalRisks(ctx, database.CountOperationalRisksParams{
		Kind:       q.Kind,
		Query:      q.Query,
		Repository: q.Repository,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("count operational risks: %w", err)
	}

	out := make([]ListedRisk, 0, len(rows))
	for _, row := range rows {
		out = append(out, ListedRisk{
			OperationalRisk: OperationalRisk{
				Kind:           row.Kind,
				Severity:       Severity(row.Severity),
				PURL:           row.Purl,
				PackageName:    row.PackageName,
				PackageVersion: row.PackageVersion,
				Product:        row.Product,
				Cycle:          row.Cycle,
				EOLFrom:        row.EolFrom,
				Detail:         row.Detail,
			},
			Sha256:     row.Sha256,
			Repository: row.RepoKey,
			Path:       row.Path,
			Name:       row.Name,
		})
	}

	return out, total, nil
}

// sbomDistro is the operating system an SBOM names: Syft's distro, or a
// CycloneDX operating-system component.
func sbomDistro(raw []byte) endoflife.Distro {
	var doc struct {
		Distro *struct {
			ID        string `json:"id"`
			VersionID string `json:"versionID"`
		} `json:"distro"`
		Metadata *struct {
			Component *cdxComponent `json:"component"`
		} `json:"metadata"`
		Components []cdxComponent `json:"components"`
	}

	if json.Unmarshal(raw, &doc) != nil {
		return endoflife.Distro{}
	}

	if doc.Distro != nil && doc.Distro.ID != "" {
		return endoflife.Distro{
			ID:      doc.Distro.ID,
			Version: doc.Distro.VersionID,
		}
	}

	for _, c := range doc.Components {
		if c.Type == "operating-system" && c.Name != "" {
			return endoflife.Distro{
				ID:      c.Name,
				Version: c.Version,
			}
		}
	}

	return endoflife.Distro{}
}
