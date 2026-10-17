package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"relic/internal/platform/database"
)

// ErrNoScan is returned for content Relic Scan has no record of.
var ErrNoScan = errors.New("scan: no scan of this content")

// ArtifactScan is the scan record of one content (sha256).
type ArtifactScan struct {
	Sha256     string
	FileName   string
	Status     string
	Reason     string
	SBOM       []byte // gzipped Syft JSON
	Components int
	DBBuilt    *time.Time
	Updated    time.Time
}

// StoredFinding is a finding and the content it was found in.
type StoredFinding struct {
	Sha256 string
	Finding
}

// FindingsQuery selects a page of findings across all scanned content.
type FindingsQuery struct {
	Severity    Severity // exact; empty for all
	Query       string   // matched against IDs, packages and summaries
	Repository  string   // content in this repository; empty for all
	PurlType    string   // package URL type (npm, maven, …); empty for all
	FixableOnly bool     // only findings with a fixed version
	KEVOnly     bool     // only findings whose CVEs are known exploited
	// MaliciousOnly keeps malicious-package findings only.
	MaliciousOnly bool
	Skip          int
	Max           int
}

// ListedFinding is a finding with one repository item holding its content.
type ListedFinding struct {
	VulnID         string
	Severity       Severity
	PackageName    string
	PackageVersion string
	Summary        string
	CVEs           []string
	FixedVersions  []string
	Sha256         string
	PURL           string
	Repository     string
	Path           string
	Name           string
}

// GraphScan is an on-demand scan of a list of packages.
type GraphScan struct {
	ID       uuid.UUID
	Username string
	Status   string
	Request  GraphRequest
	// Build is the build run the scan is for; nil for other scans.
	Build   *GraphBuild
	Result  []byte
	Error   string
	Created time.Time
	Updated time.Time
}

// Results reads what Relic Scan has found.
type Results struct {
	db *database.DB
}

// NewResults returns a Results reading db.
func NewResults(db *database.DB) *Results {
	return &Results{
		db: db,
	}
}

// Artifact returns the scan record of content sha256; ErrNoScan if none.
func (r *Results) Artifact(ctx context.Context, sha256 string) (ArtifactScan, error) {
	a, err := r.db.GetScanArtifact(ctx, sha256)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return ArtifactScan{}, ErrNoScan
	}

	if err != nil {
		return ArtifactScan{}, fmt.Errorf("scan of %s: %w", sha256, err)
	}

	return ArtifactScan{
		Sha256:     a.Sha256,
		FileName:   a.FileName,
		Status:     a.Status,
		Reason:     a.Reason,
		SBOM:       a.Sbom,
		Components: int(a.Components),
		DBBuilt:    a.DbBuilt,
		Updated:    a.Updated,
	}, nil
}

// Findings returns the findings in content sha256.
func (r *Results) Findings(ctx context.Context, sha256 string) ([]Finding, error) {
	rows, err := r.db.ListFindings(ctx, sha256)
	if err != nil {
		return nil, fmt.Errorf("findings of %s: %w", sha256, err)
	}

	out := make([]Finding, 0, len(rows))
	for _, row := range rows {
		out = append(out, findingFromRow(row))
	}

	return out, nil
}

// FindingsFor returns the findings in any of the given contents.
func (r *Results) FindingsFor(ctx context.Context, sha256s []string) ([]StoredFinding, error) {
	rows, err := r.db.ListFindingsForContents(ctx, sha256s)
	if err != nil {
		return nil, fmt.Errorf("findings of %d contents: %w", len(sha256s), err)
	}

	out := make([]StoredFinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredFinding{
			Sha256:  row.Sha256,
			Finding: findingFromRow(row),
		})
	}

	return out, nil
}

func findingFromRow(f database.ScanFinding) Finding {
	out := Finding{
		ID:            f.VulnID,
		Aliases:       f.Cves,
		Severity:      Severity(f.Severity),
		Summary:       f.Summary,
		FixedVersions: f.FixedVersions,
		URLs:          f.Urls,
		Package: Package{
			Name:    f.PackageName,
			Version: f.PackageVersion,
			PURL:    f.Purl,
		},
	}
	_ = json.Unmarshal(f.Cvss, &out.CVSS)

	return out
}

// SeverityCounts returns, per content, how many findings it has of each
// severity.
func (r *Results) SeverityCounts(ctx context.Context, sha256s []string) (map[string]map[Severity]int64, error) {
	rows, err := r.db.SeverityCountsForContents(ctx, sha256s)
	if err != nil {
		return nil, fmt.Errorf("severity counts: %w", err)
	}

	out := map[string]map[Severity]int64{}
	for _, row := range rows {
		if out[row.Sha256] == nil {
			out[row.Sha256] = map[Severity]int64{}
		}

		out[row.Sha256][Severity(row.Severity)] += row.Count
	}

	return out, nil
}

// StatusCounts returns how many contents are in each scan status.
func (r *Results) StatusCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.ScanCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan counts: %w", err)
	}

	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Count
	}

	return out, nil
}

// FindingCounts returns how many findings there are of each severity.
func (r *Results) FindingCounts(ctx context.Context) (map[Severity]int64, error) {
	rows, err := r.db.FindingCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding counts: %w", err)
	}

	out := make(map[Severity]int64, len(rows))
	for _, row := range rows {
		out[Severity(row.Severity)] = row.Count
	}

	return out, nil
}

// LatestDBBuilt is the build time of the newest vulnerability database any
// scan used; zero when nothing was scanned.
func (r *Results) LatestDBBuilt(ctx context.Context) (time.Time, error) {
	t, err := r.db.LatestDBBuilt(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("latest vulnerability database: %w", err)
	}

	return t, nil
}

// ImageManifests returns the image manifest content (sha256) each of the
// given folders of repoKey holds.
func (r *Results) ImageManifests(ctx context.Context, repoKey string, folders []string) (map[string]string, error) {
	rows, err := r.db.ImageManifestsInFolders(
		ctx,
		database.ImageManifestsInFoldersParams{
			RepoKey: repoKey,
			Paths:   folders,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("image manifests in %s: %w", repoKey, err)
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Path] = row.Sha256
	}

	return out, nil
}

// ForgetFailed drops the failed scans of the given contents, so the sweep
// scans them again, and returns the contents dropped.
func (r *Results) ForgetFailed(ctx context.Context, sha256s []string) ([]string, error) {
	out, err := r.db.ForgetFailedScans(ctx, sha256s)
	if err != nil {
		return nil, fmt.Errorf("forget failed scans: %w", err)
	}

	return out, nil
}

// FindingsPage returns a page of findings across all scanned content that
// is still in a repository, most severe first, and how many there are.
func (r *Results) FindingsPage(ctx context.Context, q FindingsQuery) ([]ListedFinding, int64, error) {
	rows, err := r.db.ListFindingsPage(ctx, database.ListFindingsPageParams{
		Severity:      string(q.Severity),
		Query:         q.Query,
		Repository:    q.Repository,
		PurlType:      q.PurlType,
		FixableOnly:   q.FixableOnly,
		KevOnly:       q.KEVOnly,
		MaliciousOnly: q.MaliciousOnly,
		Skip:          int32(min(q.Skip, math.MaxInt32)), //nolint:gosec // clamped
		MaxRows:       int32(q.Max),                      //nolint:gosec // bounded by callers
	})
	if err != nil {
		return nil, 0, fmt.Errorf("findings page: %w", err)
	}

	total, err := r.db.CountFindings(ctx, database.CountFindingsParams{
		Severity:      string(q.Severity),
		Query:         q.Query,
		Repository:    q.Repository,
		PurlType:      q.PurlType,
		FixableOnly:   q.FixableOnly,
		KevOnly:       q.KEVOnly,
		MaliciousOnly: q.MaliciousOnly,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("count findings: %w", err)
	}

	out := make([]ListedFinding, 0, len(rows))
	for _, f := range rows {
		out = append(out, ListedFinding{
			VulnID:         f.VulnID,
			Severity:       Severity(f.Severity),
			PackageName:    f.PackageName,
			PackageVersion: f.PackageVersion,
			Summary:        f.Summary,
			CVEs:           f.Cves,
			FixedVersions:  f.FixedVersions,
			Sha256:         f.Sha256,
			PURL:           f.Purl,
			Repository:     f.RepoKey,
			Path:           f.Path,
			Name:           f.Name,
		})
	}

	return out, total, nil
}

// GraphScan returns one graph scan; ErrNoScan if there is none.
func (r *Results) GraphScan(ctx context.Context, id uuid.UUID) (GraphScan, error) {
	g, err := r.db.GetGraphScan(ctx, id)
	return graphScan(g, err)
}

// LatestBuildGraphScan returns the newest graph scan of a build run;
// ErrNoScan if there is none.
func (r *Results) LatestBuildGraphScan(ctx context.Context, b GraphBuild) (GraphScan, error) {
	g, err := r.db.GetLatestBuildGraphScan(
		ctx,
		database.GetLatestBuildGraphScanParams{
			BuildName:   b.Name,
			BuildNumber: b.Number,
			Project:     b.Project,
		},
	)

	return graphScan(g, err)
}

func graphScan(g database.ScanGraph, err error) (GraphScan, error) {
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return GraphScan{}, ErrNoScan
	}

	if err != nil {
		return GraphScan{}, fmt.Errorf("graph scan: %w", err)
	}

	out := GraphScan{
		ID:       g.ID,
		Username: g.Username,
		Status:   g.Status,
		Result:   g.Result,
		Error:    g.Error,
		Created:  g.Created,
		Updated:  g.Updated,
	}
	if err := json.Unmarshal(g.Request, &out.Request); err != nil {
		return GraphScan{}, fmt.Errorf("decode graph scan request %s: %w", g.ID, err)
	}

	if g.BuildName != nil {
		out.Build = &GraphBuild{
			Name: *g.BuildName,
		}
		if g.BuildNumber != nil {
			out.Build.Number = *g.BuildNumber
		}

		if g.Project != nil {
			out.Build.Project = *g.Project
		}
	}

	return out, nil
}
