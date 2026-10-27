// Package scan is Relic Scan's scanner layer.
package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Severity is how severe a vulnerability is.
type Severity string

// Severities, most severe first.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityUnknown  Severity = "unknown"
)

// Severities lists every severity, most severe first.
var Severities = []Severity{
	SeverityCritical,
	SeverityHigh,
	SeverityMedium,
	SeverityLow,
	SeverityUnknown,
}

// Known reports whether s is one of Severities.
func (s Severity) Known() bool { return slices.Contains(Severities, s) }

// Rank orders severities: unknown 0, low 1 … critical 4; -1 for a value
// that is not a severity.
func (s Severity) Rank() int {
	switch s {
	case SeverityUnknown:
		return 0
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	default:
		return -1
	}
}

// ErrTooLarge is returned when a scanner's output exceeds the limit.
var ErrTooLarge = errors.New("scanner output too large")

// Cataloger produces a Syft JSON SBOM for a source: a directory, an archive
// file, or an OCI layout directory (source is a Syft scheme such as
// "dir:/x" or "oci-dir:/x").
type Cataloger interface {
	Catalog(ctx context.Context, source string) ([]byte, error)
}

// Matcher matches an SBOM file (Syft JSON or CycloneDX) against the
// vulnerability database.
type Matcher interface {
	Match(ctx context.Context, sbomPath string) (Report, error)
}

// CVSS is one CVSS score of a vulnerability.
type CVSS struct {
	Version string  `json:"version"`
	Vector  string  `json:"vector,omitempty"`
	Score   float64 `json:"score"`
}

// Package is the matched package.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
	PURL    string `json:"purl"`
}

// Finding is one vulnerability affecting one package.
type Finding struct {
	ID            string            `json:"id"`      // GHSA-…, CVE-…, …
	Aliases       []string          `json:"aliases"` // related IDs, CVEs first
	Severity      Severity          `json:"severity"`
	Summary       string            `json:"summary"`
	FixedVersions []string          `json:"fixed_versions"`
	CVSS          []CVSS            `json:"cvss"`
	AliasCVSS     map[string][]CVSS `json:"alias_cvss,omitempty"`
	URLs          []string          `json:"urls"`
	Package       Package           `json:"package"`
}

// CVEs returns the finding's CVE IDs: its own ID if it is one, then aliases.
func (f Finding) CVEs() []string {
	var out []string
	for _, id := range append([]string{
		f.ID,
	}, f.Aliases...) {
		if strings.HasPrefix(id, "CVE-") && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}

	return out
}

// Report is the result of matching one SBOM.
type Report struct {
	Findings []Finding
	DBBuilt  time.Time // vulnerability database build time
	Scanner  string    // e.g. "grype 0.119.0"
	// Components are the packages a graph scan matched, with the licenses
	// known for them.
	Components []Component `json:",omitempty"`
}

type grypeReport struct {
	Matches []struct {
		Vulnerability          grypeVuln   `json:"vulnerability"`
		RelatedVulnerabilities []grypeVuln `json:"relatedVulnerabilities"`
		Artifact               Package     `json:"artifact"`
	} `json:"matches"`
	Descriptor struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		DB      struct {
			Status struct {
				Built time.Time `json:"built"`
			} `json:"status"`
		} `json:"db"`
	} `json:"descriptor"`
}

type grypeVuln struct {
	ID          string   `json:"id"`
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	URLs        []string `json:"urls"`
	DataSource  string   `json:"dataSource"`
	CVSS        []struct {
		Version string `json:"version"`
		Vector  string `json:"vector"`
		Metrics struct {
			BaseScore float64 `json:"baseScore"`
		} `json:"metrics"`
	} `json:"cvss"`
	Fix struct {
		Versions []string `json:"versions"`
		State    string   `json:"state"`
	} `json:"fix"`
}

// ParseGrype converts Grype's JSON report into a Report.
func ParseGrype(data []byte) (Report, error) {
	return decodeGrype(bytes.NewReader(data))
}

func decodeGrype(r io.Reader) (Report, error) {
	var gr grypeReport
	if err := json.NewDecoder(r).Decode(&gr); err != nil {
		return Report{}, fmt.Errorf("parse grype report: %w", err)
	}

	rep := Report{
		DBBuilt: gr.Descriptor.DB.Status.Built,
		Scanner: strings.TrimSpace(gr.Descriptor.Name + " " + gr.Descriptor.Version),
	}
	for _, m := range gr.Matches {
		v := m.Vulnerability

		f := Finding{
			ID:            v.ID,
			Severity:      normalizeSeverity(v.Severity),
			Summary:       v.Description,
			FixedVersions: v.Fix.Versions,
			URLs:          v.URLs,
			Package:       m.Artifact,
		}
		if f.Summary == "" || f.Severity == SeverityUnknown {
			for _, r := range m.RelatedVulnerabilities {
				if f.Summary == "" {
					f.Summary = r.Description
				}

				if f.Severity == SeverityUnknown {
					f.Severity = normalizeSeverity(r.Severity)
				}
			}
		}

		for _, c := range v.CVSS {
			f.CVSS = append(f.CVSS, CVSS{
				Version: c.Version,
				Vector:  c.Vector,
				Score:   c.Metrics.BaseScore,
			})
		}

		for _, r := range m.RelatedVulnerabilities {
			if r.ID != v.ID && !slices.Contains(f.Aliases, r.ID) {
				f.Aliases = append(f.Aliases, r.ID)
			}

			if r.ID != v.ID && len(r.CVSS) > 0 {
				if f.AliasCVSS == nil {
					f.AliasCVSS = map[string][]CVSS{}
				}

				for _, c := range r.CVSS {
					f.AliasCVSS[r.ID] = append(f.AliasCVSS[r.ID], CVSS{
						Version: c.Version,
						Vector:  c.Vector,
						Score:   c.Metrics.BaseScore,
					})
				}
			}

			if len(f.CVSS) == 0 {
				for _, c := range r.CVSS {
					f.CVSS = append(f.CVSS, CVSS{
						Version: c.Version,
						Vector:  c.Vector,
						Score:   c.Metrics.BaseScore,
					})
				}
			}
		}

		slices.SortStableFunc(f.Aliases, func(a, b string) int {
			return boolCmp(strings.HasPrefix(b, "CVE-"), strings.HasPrefix(a, "CVE-"))
		})
		rep.Findings = append(rep.Findings, f)
	}

	return rep, nil
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

func normalizeSeverity(s string) Severity {
	switch strings.ToLower(s) {
	case "critical":
		return SeverityCritical
	case "high":
		return SeverityHigh
	case "medium":
		return SeverityMedium
	case "low", "negligible":
		return SeverityLow
	default:
		return SeverityUnknown
	}
}

// Exec runs a scanner binary with a timeout and bounded output.
type Exec struct {
	Path      string
	Env       []string // added to a minimal environment
	Timeout   time.Duration
	MaxOutput int64
	// Slots bounds the scanners that run at once and sets each one's
	// GOMEMLIMIT; nil runs them unbounded.
	Slots *ScannerSlots
}

var defaultExecTimeout = 10 * time.Minute

func (e Exec) run(ctx context.Context, args ...string) ([]byte, error) {
	return e.read(e.output(ctx, false, args...))
}

func (e Exec) runAlone(ctx context.Context, args ...string) ([]byte, error) {
	return e.read(e.output(ctx, true, args...))
}

func (Exec) read(out *os.File, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}

	defer removeOutput(out)

	info, err := out.Stat()
	if err != nil {
		return nil, fmt.Errorf("scanner output: %w", err)
	}

	b := make([]byte, info.Size())
	if _, err := io.ReadFull(out, b); err != nil {
		return nil, fmt.Errorf("read scanner output: %w", err)
	}

	return b, nil
}

func removeOutput(out *os.File) {
	_ = out.Close()
	_ = os.Remove(out.Name())
}

func (e Exec) output(ctx context.Context, databaseChange bool, args ...string) (*os.File, error) {
	memory, release, err := e.Slots.acquire(ctx, databaseChange)
	if err != nil {
		return nil, err
	}
	defer release()

	timeout := e.Timeout
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	//nolint:gosec // the binary is configured by the operator; args are Relic's own
	cmd := exec.CommandContext(ctx, e.Path, args...)
	// A minimal environment: scanners must not inherit Relic's secrets.
	cmd.Env = append([]string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + os.TempDir(),
	}, e.Env...)
	if memory > 0 {
		cmd.Env = append(cmd.Env, "GOMEMLIMIT="+strconv.FormatInt(memory, 10))
	}

	limit := e.MaxOutput
	if limit <= 0 {
		limit = 256 << 20
	}

	spool, err := os.CreateTemp("", "relic-scanner-*.out")
	if err != nil {
		return nil, fmt.Errorf("scanner output: %w", err)
	}

	stdout := limitedFile{
		file:  spool,
		limit: limit,
	}

	var stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &limitedWriter{
		w: &stderr,
		n: 64 << 10,
	}
	killGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()

	switch {
	case stdout.overflow:
		err = ErrTooLarge
	case err != nil:
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			err = fmt.Errorf("%s: %w: %s", filepath.Base(e.Path), err, detail)
		} else {
			err = fmt.Errorf("%s: %w", filepath.Base(e.Path), err)
		}
	default:
		if _, seekErr := spool.Seek(0, io.SeekStart); seekErr != nil {
			err = fmt.Errorf("scanner output: %w", seekErr)
		}
	}

	if err != nil {
		removeOutput(spool)

		return nil, err
	}

	return spool, nil
}

// Syft is the subprocess Cataloger.
type Syft struct {
	Exec
}

// Catalog implements Cataloger.
func (s Syft) Catalog(ctx context.Context, source string) ([]byte, error) {
	args := []string{
		"scan",
		source,
		"-o",
		"syft-json",
		"-q",
	}

	// Syft reads an npm package's package.json only in images by default; an
	// extracted package tarball holds nothing else to catalog.
	if strings.HasPrefix(source, "dir:") {
		args = append(args, "--select-catalogers", "+javascript-package-cataloger")
	}

	e := s.Exec
	e.Env = append(slices.Clone(s.Env), "SYFT_CHECK_FOR_APP_UPDATE=false")

	return e.run(ctx, args...)
}

// ErrInvalidDB is an installed vulnerability database that fails Grype's
// checksum, such as one a crash left half written.
var ErrInvalidDB = errors.New("scan: the vulnerability database is invalid")

// Grype is the subprocess Matcher.
type Grype struct {
	Exec
	DatabaseDir string
	// UpdateURL is where UpdateDB looks for databases (Grype's listing, or
	// a mirror of it); empty for Grype's own.
	UpdateURL string
	// Offline never downloads a database.
	Offline bool
}

func (g Grype) env() []string {
	env := append(
		slices.Clone(g.Env),
		"GRYPE_DB_CACHE_DIR="+g.DatabaseDir,
		"GRYPE_DB_AUTO_UPDATE=false",
		"GRYPE_CHECK_FOR_APP_UPDATE=false",
	)
	if g.UpdateURL != "" {
		env = append(env, "GRYPE_DB_UPDATE_URL="+g.UpdateURL)
	}

	return env
}

// Match implements Matcher.
func (g Grype) Match(ctx context.Context, sbomPath string) (Report, error) {
	e := g.Exec
	e.Env = g.env()

	out, err := e.output(ctx, false, "sbom:"+sbomPath, "-o", "json")
	if err != nil {
		if _, dbErr := g.DBBuilt(ctx); errors.Is(dbErr, ErrInvalidDB) {
			return Report{}, dbErr
		}

		return Report{}, err
	}
	defer removeOutput(out)

	return decodeGrype(out)
}

const databaseTimeout = 30 * time.Minute

const abandonedDownloads = "grype-db-download*"

var databaseChanges sync.Mutex

// HasDB reports whether a vulnerability database is installed: an
// import.json beside it, in a directory named after its schema version.
func (g Grype) HasDB() bool {
	found, err := filepath.Glob(filepath.Join(g.DatabaseDir, "*", "import.json"))
	if err != nil {
		return false
	}

	return slices.ContainsFunc(found, func(path string) bool {
		schema := filepath.Base(filepath.Dir(path))
		if schema == "" || strings.Trim(schema, "0123456789") != "" {
			return false
		}

		st, err := os.Stat(path)

		return err == nil && st.Size() > 0
	})
}

func (g Grype) databaseExec() Exec {
	e := g.Exec
	e.Env = g.env()

	if e.Timeout <= 0 {
		e.Timeout = databaseTimeout
	}

	return e
}

func (g Grype) changeDB(ctx context.Context, args ...string) error {
	databaseChanges.Lock()
	defer databaseChanges.Unlock()

	if err := g.removeAbandonedDownloads(); err != nil {
		return err
	}

	_, err := g.databaseExec().runAlone(ctx, args...)

	return errors.Join(err, g.removeAbandonedDownloads())
}

func (g Grype) removeAbandonedDownloads() error {
	found, err := filepath.Glob(filepath.Join(g.DatabaseDir, abandonedDownloads))
	if err != nil {
		return fmt.Errorf("find abandoned vulnerability database downloads: %w", err)
	}

	var errs []error

	for _, dir := range found {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove an abandoned vulnerability database download: %w", err))
		}
	}

	return errors.Join(errs...)
}

// UpdateDB downloads the latest vulnerability database; offline, it does
// nothing.
func (g Grype) UpdateDB(ctx context.Context) error {
	if g.Offline {
		return nil
	}

	return g.changeDB(ctx, "db", "update")
}

// ReplaceInvalidDB deletes an installed database that fails Grype's
// checksum and downloads a new one, which `grype db update` alone never
// does while the invalid one is the latest; it reports whether it did.
// Offline, it returns the ErrInvalidDB error instead.
func (g Grype) ReplaceInvalidDB(ctx context.Context) (bool, error) {
	if !g.HasDB() {
		return false, nil
	}

	_, err := g.DBBuilt(ctx)
	if !errors.Is(err, ErrInvalidDB) {
		return false, nil
	}

	if g.Offline {
		return false, fmt.Errorf("%w; import a new one", err)
	}

	if err := g.changeDB(ctx, "db", "delete"); err != nil {
		return false, fmt.Errorf("delete the invalid vulnerability database: %w", err)
	}

	if err := g.changeDB(ctx, "db", "update"); err != nil {
		return true, fmt.Errorf("download a vulnerability database to replace the invalid one: %w", err)
	}

	return true, nil
}

// DBBuilt reports when the installed database was built; an installed
// database that fails its checksum is ErrInvalidDB.
func (g Grype) DBBuilt(ctx context.Context) (time.Time, error) {
	e := g.Exec
	e.Env = g.env()

	out, err := e.run(ctx, "db", "status", "-o", "json")
	if err != nil {
		return time.Time{}, err
	}

	var st struct {
		Built time.Time `json:"built"`
		Valid *bool     `json:"valid"`
		Error string    `json:"error"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return time.Time{}, fmt.Errorf("grype db status: unexpected output: %w", err)
	}

	if st.Valid != nil && !*st.Valid {
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidDB, st.Error)
	}

	if st.Built.IsZero() {
		return time.Time{}, errors.New("grype db status: no build time in its output")
	}

	return st.Built, nil
}

// ImportDB loads a database archive (air-gapped mode).
func (g Grype) ImportDB(ctx context.Context, archive string) error {
	return g.changeDB(ctx, "db", "import", archive)
}

// Embedding *os.File would let io.Copy bypass Write through its ReadFrom.
type limitedFile struct {
	file     *os.File
	written  int64
	limit    int64
	overflow bool
}

func (f *limitedFile) Write(p []byte) (int, error) {
	if f.written+int64(len(p)) > f.limit {
		f.overflow = true
		return 0, ErrTooLarge
	}

	n, err := f.file.Write(p)
	f.written += int64(n)

	return n, err
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}

	q := p[:min(len(p), l.n)]
	l.n -= len(q)
	_, err := l.w.Write(q)

	return len(p), err
}
