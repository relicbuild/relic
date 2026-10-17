package scan

import (
	"context"
	"fmt"
	"strings"

	"relic/internal/platform/database"
	"relic/internal/scanning/malicious"
)

// MaliciousMatcher tells which packages advisories name as malicious.
type MaliciousMatcher interface {
	Match(ctx context.Context, purls []string) ([]malicious.Match, error)
}

// SetMalicious makes scans report malicious packages; nil reports none.
func (s *Service) SetMalicious(m MaliciousMatcher) { s.malicious = m }

// IsMalicious reports whether a finding is a malicious-package advisory
// rather than a vulnerability.
func IsMalicious(id string) bool { return strings.HasPrefix(id, "MAL-") }

func (s *Service) maliciousFindings(ctx context.Context, purls []string) ([]Finding, error) {
	if s.malicious == nil || len(purls) == 0 {
		return nil, nil
	}

	matches, err := s.malicious.Match(ctx, purls)
	if err != nil {
		return nil, err
	}

	out := make([]Finding, 0, len(matches))
	for _, m := range matches {
		out = append(out, Finding{
			ID:       m.ID,
			Severity: SeverityCritical,
			Summary:  "Malicious package: " + m.Summary,
			URLs: []string{
				"https://osv.dev/vulnerability/" + m.ID,
			},
			Package: Package{
				Name:    m.Name,
				Version: m.Version,
				PURL:    m.PURL,
			},
		})
	}

	return out, nil
}

func sbomPURLs(sbom []byte) ([]string, error) {
	comps, err := sbomComponents(sbom)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(comps))
	for _, c := range comps {
		if c.PURL != "" {
			out = append(out, c.PURL)
		}
	}

	return out, nil
}

const maliciousBatch = 200

// CheckMalicious checks stored scans against advisories that changed since
// they were last checked: their malicious-package findings are replaced. A
// scan that cannot be checked is logged and left for the next check; the
// count is of the scans checked.
func (s *Service) CheckMalicious(ctx context.Context) (int, error) {
	if s.malicious == nil {
		return 0, nil
	}

	changed, err := s.db.LatestMaliciousChange(ctx)
	if err != nil {
		return 0, err
	}

	rows, err := s.db.ListUncheckedScans(ctx, database.ListUncheckedScansParams{
		Changed: &changed,
		MaxRows: maliciousBatch,
	})
	if err != nil {
		return 0, err
	}

	checked := 0

	for _, r := range rows {
		if ctx.Err() != nil {
			break
		}

		if err := s.checkMalicious(ctx, r.Sha256, r.Sbom); err != nil {
			s.log.WarnContext(ctx, "malicious packages: scan not checked", "sha256", r.Sha256, "error", err)

			continue
		}

		checked++
	}

	return checked, ctx.Err()
}

func (s *Service) checkMalicious(ctx context.Context, sha256Hex string, packed []byte) error {
	sbom, err := gunzip(packed)
	if err != nil {
		return fmt.Errorf("read stored SBOM: %w", err)
	}

	purls, err := sbomPURLs(sbom)
	if err != nil {
		return fmt.Errorf("read stored SBOM: %w", err)
	}

	found, err := s.maliciousFindings(ctx, purls)
	if err != nil {
		return err
	}

	now := s.now()

	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := q.DeleteMaliciousFindings(ctx, sha256Hex); err != nil {
			return err
		}

		for _, f := range found {
			if err := insertFinding(ctx, q, sha256Hex, f); err != nil {
				return err
			}
		}

		return q.SetMaliciousChecked(ctx, database.SetMaliciousCheckedParams{
			Sha256:  sha256Hex,
			Checked: &now,
		})
	})
}
