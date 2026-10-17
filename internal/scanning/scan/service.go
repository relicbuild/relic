package scan

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/endoflife"

	"github.com/ulikunitz/xz"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ScannablePattern selects the file names the sweep catalogs (a PostgreSQL
// regular expression, case-insensitive).
const ScannablePattern = `(\.(jar|war|ear|tgz|tar\.gz|whl|zip|nupkg|crate|egg|tar\.bz2|tar\.xz|txz|pkl|pickle|joblib|dill|pt|pth|bin|ckpt|npy)|^manifest\.json)$`

const retryWaitingAfter = 10 * time.Minute

const imageManifestName = "manifest.json"

// Scan statuses.
const (
	StatusQueued  = "queued"
	StatusDone    = "done"
	StatusPartial = "partial"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// Limits bound the work spent on one artifact.
type Limits struct {
	MaxArtifactBytes int64 // larger artifacts are skipped
	MaxExtractBytes  int64 // total bytes extracted from an archive
	MaxExtractFiles  int
}

// DefaultLimits are used for zero fields.
var DefaultLimits = Limits{
	MaxArtifactBytes: 2 << 30,
	MaxExtractBytes:  4 << 30,
	MaxExtractFiles:  200_000,
}

// ErrLimit marks an artifact that exceeded a limit; it is recorded as
// partial or skipped, with the reason.
var ErrLimit = errors.New("scan limit exceeded")

// Service runs the scan pipeline against Relic's stores.
type Service struct {
	db        *database.DB
	blobs     objectstore.Store
	catalog   Cataloger
	match     Matcher
	limits    Limits
	log       *slog.Logger
	now       func() time.Time
	malicious MaliciousMatcher
	risks     RiskAssessor
	analyzers []Analyzer
	// reachability runs govulncheck on Go executables; nil runs nothing.
	reachability *GoReachability
	// secrets validates the secrets found; nil validates none.
	secrets *SecretValidator
	events  events.Publisher
}

// SetEvents publishes finished scans to p.
func (s *Service) SetEvents(p events.Publisher) { s.events = p }

// NewService returns a Service.
func NewService(
	db *database.DB,
	blobs objectstore.Store,
	c Cataloger,
	m Matcher,
	limits Limits,
	log *slog.Logger,
) *Service {
	if limits.MaxArtifactBytes <= 0 {
		limits.MaxArtifactBytes = DefaultLimits.MaxArtifactBytes
	}

	if limits.MaxExtractBytes <= 0 {
		limits.MaxExtractBytes = DefaultLimits.MaxExtractBytes
	}

	if limits.MaxExtractFiles <= 0 {
		limits.MaxExtractFiles = DefaultLimits.MaxExtractFiles
	}

	return &Service{
		db:      db,
		blobs:   blobs,
		catalog: c,
		match:   m,
		limits:  limits,
		log:     log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// Claim records queued scans for up to max unscanned contents and returns
// them.
func (s *Service) Claim(ctx context.Context, maxRows int32) ([]database.ClaimUnscannedRow, error) {
	now := s.now()

	rows, err := s.db.ClaimUnscanned(ctx, database.ClaimUnscannedParams{
		Now:         now,
		NamePattern: ScannablePattern,
		RetryBefore: now.Add(-retryWaitingAfter),
		MaxRows:     maxRows,
	})
	if err != nil {
		return nil, fmt.Errorf("claim unscanned content: %w", err)
	}

	return rows, nil
}

// ScanArtifact catalogs and matches one content digest.
func (s *Service) ScanArtifact(ctx context.Context, sha256Hex, fileName string) (err error) {
	ctx, span := telemetry.Tracer().
		Start(ctx, "scan.Artifact", trace.WithAttributes(attribute.String("relic.sha256", sha256Hex)))

	defer func() {
		if err != nil {
			telemetry.ScanFinished("error")
		}

		telemetry.End(span, err)
	}()

	claimed, err := s.db.GetScanArtifact(ctx, sha256Hex)
	if err != nil {
		return fmt.Errorf("load scan %s: %w", sha256Hex, database.NotFound(err))
	}

	since := claimed.Updated

	dir, err := os.MkdirTemp("", "relic-scan-")
	if err != nil {
		return fmt.Errorf("scan temp dir: %w", err)
	}

	defer func() {
		_ = os.RemoveAll(dir)
	}()

	info, err := s.blobs.Head(ctx, objectstore.Key(sha256Hex))
	if errors.Is(err, objectstore.ErrNotFound) {
		return s.finish(ctx, sha256Hex, since, StatusSkipped, "content no longer stored", nil, nil)
	}

	if err != nil {
		return err
	}

	if info.Size > s.limits.MaxArtifactBytes {
		return s.finish(
			ctx,
			sha256Hex,
			since,
			StatusSkipped,
			fmt.Sprintf("larger than %d bytes", s.limits.MaxArtifactBytes),
			nil,
			nil,
		)
	}

	name := filepath.Base(fileName)
	if name == "." || name == "/" || name == "" {
		name = "artifact"
	}

	var source, reason string
	if name == imageManifestName {
		source, err = s.prepareImage(ctx, sha256Hex, filepath.Join(dir, "oci"))
	} else {
		file := filepath.Join(dir, "in", name)
		if err := s.download(ctx, sha256Hex, file); err != nil {
			return err
		}

		source, reason, err = s.prepare(file, filepath.Join(dir, "src"))
	}

	if errors.Is(err, errNotImage) {
		return s.finish(ctx, sha256Hex, since, StatusSkipped, err.Error(), nil, nil)
	}

	if errors.Is(err, errLayersMissing) {
		return s.finish(ctx, sha256Hex, since, StatusQueued, err.Error(), nil, nil)
	}

	if err != nil {
		return s.finish(ctx, sha256Hex, since, StatusFailed, err.Error(), nil, nil)
	}

	var sbom []byte

	err = telemetry.ScanStage(ctx, "catalog", func(ctx context.Context) error {
		var err error

		sbom, err = s.catalog.Catalog(ctx, source)

		return err
	})
	if err != nil {
		return s.finish(ctx, sha256Hex, since, StatusFailed, "catalog: "+err.Error(), nil, nil)
	}

	sbom = withArchivePackage(sbom, name, source)

	rep, err := s.matchSBOM(ctx, dir, sbom)
	if err != nil {
		return err
	}

	issues, failed := s.analyze(ctx, source)
	if failed != "" {
		reason = strings.TrimPrefix(reason+"; "+failed, "; ")
	}

	s.validateSecrets(ctx, sha256Hex, issues)

	var goAnalysis GoAnalysis

	if s.reachability != nil {
		err := telemetry.ScanStage(ctx, "reachability", func(ctx context.Context) error {
			var err error

			goAnalysis, err = s.reachability.Analyze(ctx, source, sbom, dir)

			return err
		})
		if err != nil {
			s.log.WarnContext(ctx, "scan: reachability analysis failed", "error", err)

			reason = strings.TrimPrefix(reason+"; reachability analysis failed", "; ")
		}
	}

	status := StatusDone
	if reason != "" {
		status = StatusPartial
	}

	if err := s.finish(ctx, sha256Hex, since, status, reason, sbom, &rep); err != nil {
		return err
	}

	if s.reachability != nil {
		var checked *time.Time

		if goAnalysis.DatabasePresent {
			now := s.now()
			checked = &now
		}

		if err := s.db.WithTx(ctx, func(q database.Querier) error {
			if err := storeGoExtracts(ctx, q, sha256Hex, goAnalysis.Extracts); err != nil {
				return err
			}

			if err := storeReachability(ctx, q, sha256Hex, goAnalysis.Vulnerabilities); err != nil {
				return err
			}

			return q.SetReachabilityChecked(ctx, database.SetReachabilityCheckedParams{
				Sha256:  sha256Hex,
				Checked: checked,
			})
		}); err != nil {
			return err
		}
	}

	return s.saveIssues(ctx, sha256Hex, issues)
}

func (s *Service) analyze(ctx context.Context, source string) (issues []Issue, failed string) {
	var failures []string

	for _, a := range s.analyzers {
		var found []Issue

		err := telemetry.ScanStage(ctx, a.Name(), func(ctx context.Context) error {
			var err error

			found, err = a.Analyze(ctx, source)

			return err
		})
		if err != nil {
			s.log.WarnContext(ctx, "scan: analyzer failed", "analyzer", a.Name(), "error", err)
			failures = append(failures, a.Name()+" analysis failed")

			continue
		}

		issues = append(issues, found...)
	}

	return issues, strings.Join(failures, "; ")
}

func (s *Service) saveIssues(ctx context.Context, sha256Hex string, issues []Issue) error {
	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := q.DeleteIssues(ctx, sha256Hex); err != nil {
			return err
		}

		for _, is := range issues {
			if err := q.InsertIssue(ctx, database.InsertIssueParams{
				Sha256:     sha256Hex,
				Analyzer:   is.Analyzer,
				Rule:       truncate(is.Rule, 200),
				Severity:   string(is.Severity),
				Title:      truncate(is.Title, 1000),
				File:       truncate(is.File, 1000),
				Line:       int32(min(max(is.Line, 0), 1<<30)), //nolint:gosec // bounded
				Resource:   truncate(is.Resource, 500),
				Validation: is.Validation,
				Validated:  is.Validated,
			}); err != nil {
				return fmt.Errorf("record issue: %w", err)
			}
		}

		return nil
	})
}

// SetSecretValidator makes scans validate the secrets they find; nil
// validates none.
func (s *Service) SetSecretValidator(v *SecretValidator) { s.secrets = v }

// SetAnalyzers sets the analyzers scans run after cataloging.
func (s *Service) SetAnalyzers(a ...Analyzer) { s.analyzers = a }

// Issues returns the analyzers' issues of scanned content.
func (r *Results) Issues(ctx context.Context, sha256Hex string) ([]Issue, error) {
	rows, err := r.db.ListIssues(ctx, sha256Hex)
	if err != nil {
		return nil, fmt.Errorf("issues of %s: %w", sha256Hex, err)
	}

	out := make([]Issue, 0, len(rows))
	for _, i := range rows {
		out = append(out, Issue{
			Analyzer:   i.Analyzer,
			Rule:       i.Rule,
			Severity:   Severity(i.Severity),
			Title:      i.Title,
			File:       i.File,
			Line:       int(i.Line),
			Resource:   i.Resource,
			Validation: i.Validation,
			Validated:  i.Validated,
		})
	}

	return out, nil
}

// Rematch re-matches a stored SBOM against the current database.
func (s *Service) Rematch(ctx context.Context, sha256Hex string) error {
	a, err := s.db.GetScanArtifact(ctx, sha256Hex)
	if err != nil {
		return fmt.Errorf("load scan %s: %w", sha256Hex, database.NotFound(err))
	}

	if a.Sbom == nil {
		return nil
	}

	sbom, err := gunzip(a.Sbom)
	if err != nil {
		return fmt.Errorf("read stored SBOM %s: %w", sha256Hex, err)
	}

	dir, err := os.MkdirTemp("", "relic-rematch-")
	if err != nil {
		return err
	}

	defer func() {
		_ = os.RemoveAll(dir)
	}()

	rep, err := s.matchSBOM(ctx, dir, sbom)
	if err != nil {
		return err
	}

	err = s.save(ctx, sha256Hex, a.Updated, a.Status, a.Reason, sbom, &rep)
	if errors.Is(err, errSuperseded) {
		return nil
	}

	return err
}

func (s *Service) matchSBOM(ctx context.Context, dir string, sbom []byte) (Report, error) {
	path := filepath.Join(dir, "sbom.json")
	if err := os.WriteFile(path, sbom, 0o600); err != nil {
		return Report{}, err
	}

	var rep Report

	err := telemetry.ScanStage(ctx, "match", func(ctx context.Context) error {
		var err error

		rep, err = s.match.Match(ctx, path)

		return err
	})

	return rep, err
}

func (s *Service) download(ctx context.Context, sha256Hex, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	rc, _, err := s.blobs.Get(ctx, objectstore.Key(sha256Hex), nil)
	if err != nil {
		return fmt.Errorf("open content %s: %w", sha256Hex, err)
	}

	defer func() {
		_ = rc.Close()
	}()

	//nolint:gosec // path is in Relic's own temp dir
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return fmt.Errorf("download content %s: %w", sha256Hex, err)
	}

	return f.Close()
}

func (s *Service) prepare(file, dest string) (source, partial string, err error) {
	lower := strings.ToLower(file)

	var extract func(string, string) error

	switch {
	case strings.HasSuffix(lower, ".jar"), strings.HasSuffix(lower, ".war"), strings.HasSuffix(lower, ".ear"):
		return "file:" + file, "", nil
	case strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".crate"):
		extract = s.untarGz
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		extract = s.untarXz
	case strings.HasSuffix(lower, ".tar.bz2"):
		extract = s.untarBz2
	case strings.HasSuffix(lower, ".zip"),
		strings.HasSuffix(lower, ".whl"),
		strings.HasSuffix(lower, ".nupkg"),
		strings.HasSuffix(lower, ".egg"):
		extract = s.unzip
	default:
		return "file:" + file, "", nil
	}

	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", "", err
	}

	err = extract(file, dest)
	switch {
	case errors.Is(err, ErrLimit):
		return "dir:" + dest, err.Error(), nil
	case err != nil:
		return "", "", fmt.Errorf("extract: %w", err)
	}

	return "dir:" + dest, "", nil
}

var errNotImage = errors.New("not an image manifest")

// errLayersMissing marks an image manifest whose config or layers are not
// cached yet. Its message starts with "waiting", which ClaimUnscanned keys on.
var errLayersMissing = errors.New("waiting for image blobs to be cached")

func (s *Service) prepareImage(ctx context.Context, manifestHex, dir string) (string, error) {
	rc, _, err := s.blobs.Get(ctx, objectstore.Key(manifestHex), nil)
	if err != nil {
		return "", fmt.Errorf("open manifest: %w", err)
	}

	body, err := io.ReadAll(io.LimitReader(rc, 4<<20))
	_ = rc.Close()

	if err != nil {
		return "", err
	}

	var m struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        *struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string   `json:"digest"`
			URLs   []string `json:"urls"`
		} `json:"layers"`
	}
	if json.Unmarshal(body, &m) != nil || m.SchemaVersion != 2 || m.Config == nil || len(m.Layers) == 0 {
		return "", errNotImage
	}

	blobsDir := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobsDir, 0o700); err != nil {
		return "", err
	}

	digests := []string{
		m.Config.Digest,
	}
	for _, l := range m.Layers {
		if len(l.URLs) > 0 {
			return "", fmt.Errorf("%w: foreign layers are not stored", errNotImage)
		}

		digests = append(digests, l.Digest)
	}

	for _, d := range digests {
		h, ok := strings.CutPrefix(d, "sha256:")
		if !ok || !isHex64(h) {
			return "", errNotImage
		}

		if _, err := s.blobs.Head(ctx, objectstore.Key(h)); errors.Is(err, objectstore.ErrNotFound) {
			return "", fmt.Errorf("%w: %s", errLayersMissing, d)
		} else if err != nil {
			return "", fmt.Errorf("image blob %s: %w", d, err)
		}
	}

	for _, d := range digests {
		h, _ := strings.CutPrefix(d, "sha256:")
		if err := s.download(ctx, h, filepath.Join(blobsDir, h)); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("image blob %s: %w", d, err)
		}
	}

	if err := os.WriteFile(filepath.Join(blobsDir, manifestHex), body, 0o600); err != nil {
		return "", err
	}

	mediaType := m.MediaType
	if mediaType == "" {
		mediaType = "application/vnd.oci.image.manifest.v1+json"
	}

	index := fmt.Sprintf(
		`{"schemaVersion":2,"manifests":[{"mediaType":%q,"digest":"sha256:%s","size":%d}]}`,
		mediaType,
		manifestHex,
		len(body),
	)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o600); err != nil {
		return "", err
	}

	if err := os.WriteFile(
		filepath.Join(dir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`),
		0o600,
	); err != nil {
		return "", err
	}

	return "oci-dir:" + dir, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}

	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

func safeJoin(dest, name string) (string, bool) {
	name = filepath.FromSlash(name)
	if !filepath.IsLocal(name) {
		return "", false
	}

	return filepath.Join(dest, name), true
}

type extractBudget struct {
	bytes int64
	files int
}

func (s *Service) budget() *extractBudget {
	return &extractBudget{
		bytes: s.limits.MaxExtractBytes,
		files: s.limits.MaxExtractFiles,
	}
}

func (b *extractBudget) write(path string, r io.Reader) error {
	if b.files <= 0 {
		return fmt.Errorf("%w: more than the file limit", ErrLimit)
	}

	b.files--

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path checked by safeJoin
	if err != nil {
		return err
	}

	n, err := io.Copy(f, io.LimitReader(r, b.bytes+1))
	b.bytes -= n

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err == nil && b.bytes < 0 {
		err = fmt.Errorf("%w: more than the extracted size limit", ErrLimit)
	}

	return err
}

func (s *Service) untarGz(file, dest string) error {
	return s.untar(file, dest, func(r io.Reader) (io.Reader, error) {
		return gzip.NewReader(r)
	})
}

func (s *Service) untarXz(file, dest string) error {
	return s.untar(file, dest, func(r io.Reader) (io.Reader, error) {
		return xz.NewReader(r)
	})
}

func (s *Service) untarBz2(file, dest string) error {
	return s.untar(file, dest, func(r io.Reader) (io.Reader, error) {
		return bzip2.NewReader(r), nil
	})
}

func (s *Service) untar(file, dest string, decompress func(io.Reader) (io.Reader, error)) error {
	f, err := os.Open(file) //nolint:gosec // Relic's own temp file
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	zr, err := decompress(f)
	if err != nil {
		return err
	}

	tr := tar.NewReader(zr)
	b := s.budget()

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		path, ok := safeJoin(dest, hdr.Name)
		if !ok {
			continue
		}

		if err := b.write(path, tr); err != nil {
			return err
		}
	}
}

func (s *Service) unzip(file, dest string) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer func() {
		_ = zr.Close()
	}()

	b := s.budget()

	for _, zf := range zr.File {
		if !zf.Mode().IsRegular() {
			continue
		}

		path, ok := safeJoin(dest, zf.Name)
		if !ok {
			continue
		}

		rc, err := zf.Open()
		if err != nil {
			return err
		}

		err = b.write(path, rc)
		_ = rc.Close()

		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) finish(
	ctx context.Context,
	sha256Hex string,
	since time.Time,
	status, reason string,
	sbom []byte,
	rep *Report,
) error {
	err := s.save(ctx, sha256Hex, since, status, reason, sbom, rep)
	if errors.Is(err, errSuperseded) {
		s.log.InfoContext(ctx, "scan result dropped: the content's scan changed while it ran", "sha256", sha256Hex)

		return nil
	}

	if err != nil {
		return err
	}

	telemetry.ScanFinished(status)

	counts := map[string]int{}

	if rep != nil {
		for _, f := range rep.Findings {
			counts[string(f.Severity)]++
		}
	}

	events.Publish(ctx, s.events, events.Event{
		Type:    events.ScanFinished,
		Subject: "sha256:" + sha256Hex,
		Data: map[string]any{
			"sha256":   sha256Hex,
			"status":   status,
			"reason":   reason,
			"findings": counts,
		},
	})

	return nil
}

// errSuperseded is returned by save when the content's scan changed since
// the result was computed (an SBOM import, or a newer claim).
var errSuperseded = errors.New("the scan changed while it ran")

func (s *Service) save(
	ctx context.Context,
	sha256Hex string,
	since time.Time,
	status, reason string,
	sbom []byte,
	rep *Report,
) error {
	var (
		packed     []byte
		components int32
	)

	if sbom != nil {
		comps, _ := sbomComponents(sbom)
		components = int32(min(len(comps), 1<<30)) //nolint:gosec // bounded

		var err error
		if packed, err = gzipBytes(sbom); err != nil {
			return err
		}
	}

	var built *time.Time
	if rep != nil && !rep.DBBuilt.IsZero() {
		built = &rep.DBBuilt
	}

	var (
		found   []Finding
		checked *time.Time
	)

	if sbom != nil && rep != nil && s.malicious != nil {
		if purls, err := sbomPURLs(sbom); err != nil {
			s.log.WarnContext(
				ctx,
				"scan: SBOM unreadable, malicious packages not checked",
				"sha256",
				sha256Hex,
				"error",
				err,
			)
		} else {
			now := s.now()

			if found, err = s.maliciousFindings(ctx, purls); err != nil {
				return err
			}

			checked = &now
		}
	}

	var risks []endoflife.Risk

	assessed := sbom != nil && rep != nil && s.risks != nil
	if assessed {
		var err error
		if risks, err = s.assessRisks(ctx, sbom); err != nil {
			return err
		}
	}

	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := q.DeleteFindings(ctx, sha256Hex); err != nil {
			return fmt.Errorf("clear findings: %w", err)
		}

		if rep != nil {
			for _, f := range append(slices.Clone(rep.Findings), found...) {
				if err := insertFinding(ctx, q, sha256Hex, f); err != nil {
					return err
				}
			}
		}

		if checked != nil {
			if err := q.SetMaliciousChecked(ctx, database.SetMaliciousCheckedParams{
				Sha256:  sha256Hex,
				Checked: checked,
			}); err != nil {
				return err
			}
		}

		if assessed {
			if err := storeRisks(ctx, q, sha256Hex, risks, s.now()); err != nil {
				return err
			}
		}

		n, err := q.FinishUnchangedScanArtifact(ctx, database.FinishUnchangedScanArtifactParams{
			Sha256:     sha256Hex,
			Status:     status,
			Reason:     truncate(reason, 1000),
			Sbom:       packed,
			Components: components,
			DbBuilt:    built,
			Now:        s.now(),
			Expected:   since,
		})
		if err != nil {
			return fmt.Errorf("record scan: %w", err)
		}

		if n == 0 {
			return errSuperseded
		}

		return nil
	})
}

func insertFinding(ctx context.Context, q database.Querier, sha256Hex string, f Finding) error {
	cvss, err := json.Marshal(f.CVSS)
	if err != nil {
		return err
	}

	err = q.InsertFinding(ctx, database.InsertFindingParams{
		Sha256:         sha256Hex,
		VulnID:         f.ID,
		Purl:           f.Package.PURL,
		PackageName:    f.Package.Name,
		PackageVersion: f.Package.Version,
		Severity:       string(f.Severity),
		Summary:        truncate(f.Summary, 4000),
		Cves:           nonNil(f.CVEs()),
		FixedVersions:  nonNil(f.FixedVersions),
		Cvss:           cvss,
		Urls:           nonNil(f.URLs),
	})
	if err != nil {
		return fmt.Errorf("record finding: %w", err)
	}

	return nil
}

// GraphRequest is a stored graph scan request.
type GraphRequest struct {
	PURLs []string `json:"purls"`
	// Licenses are the license expressions the requester found for its
	// packages, by package URL.
	Licenses map[string][]string `json:"licenses,omitempty"`
	// Artifacts are content digests (SHA-256) whose own scans join the
	// result: a build run's artifacts.
	Artifacts []string `json:"artifacts,omitempty"`
	// Extensions keeps what only an API adapter reads (the adapter's
	// component tree), by adapter name.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// GraphBuild names the build run a graph scan is for.
type GraphBuild struct {
	Name, Number, Project string
}

// QueueGraph records a graph scan of the request's package URLs, for build
// when it is not nil.
func (s *Service) QueueGraph(ctx context.Context, user string, req GraphRequest, build *GraphBuild) (uuid.UUID, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return uuid.UUID{}, err
	}

	id := uuid.NewV4()

	p := database.InsertGraphScanParams{
		ID:       id,
		Username: user,
		Request:  body,
		Now:      s.now(),
	}
	if build != nil {
		p.BuildName, p.BuildNumber, p.Project = &build.Name, &build.Number, &build.Project
	}

	if err := s.db.InsertGraphScan(ctx, p); err != nil {
		return uuid.UUID{}, fmt.Errorf("record graph scan: %w", err)
	}

	return id, nil
}

// RunGraph matches a queued graph scan's components.
func (s *Service) RunGraph(ctx context.Context, id uuid.UUID, lastAttempt bool) error {
	g, err := s.db.GetGraphScan(ctx, id)
	if err != nil {
		return fmt.Errorf("load graph scan: %w", database.NotFound(err))
	}

	var req GraphRequest
	if err := json.Unmarshal(g.Request, &req); err != nil {
		return s.finishGraph(ctx, id, nil, err)
	}

	doc, err := CycloneDX(req.PURLs)
	if err != nil {
		return s.finishGraph(ctx, id, nil, err)
	}

	dir, err := os.MkdirTemp("", "relic-graph-")
	if err != nil {
		return err
	}

	defer func() {
		_ = os.RemoveAll(dir)
	}()

	rep, err := s.matchSBOM(ctx, dir, doc)
	if err == nil {
		var found []Finding

		found, err = s.maliciousFindings(ctx, req.PURLs)
		rep.Findings = append(rep.Findings, found...)
	}

	if err != nil {
		if lastAttempt {
			return errors.Join(err, s.finishGraph(ctx, id, nil, err))
		}

		return err
	}

	rep.Components = graphComponents(req)

	return s.finishGraph(ctx, id, &rep, nil)
}

// FailGraph records a queued graph scan that will never run as failed.
func (s *Service) FailGraph(ctx context.Context, id uuid.UUID, failure error) error {
	return s.finishGraph(ctx, id, nil, failure)
}

func (s *Service) finishGraph(ctx context.Context, id uuid.UUID, rep *Report, failure error) error {
	p := database.FinishGraphScanParams{
		ID:     id,
		Status: StatusDone,
		Now:    s.now(),
	}
	if failure != nil {
		p.Status, p.Error = StatusFailed, failure.Error()
	}

	if rep != nil {
		var err error
		if p.Result, err = json.Marshal(rep); err != nil {
			return err
		}
	}

	if err := s.db.FinishGraphScan(ctx, p); err != nil {
		return fmt.Errorf("record graph scan result: %w", err)
	}

	return nil
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}

	return io.ReadAll(io.LimitReader(zr, 1<<30))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return strings.ToValidUTF8(s[:n], "")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
