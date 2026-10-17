package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Reachability states of a finding.
const (
	Reachable    = "reachable"
	NotReachable = "not_reachable"
	// ReachabilityUnknown is every finding govulncheck did not analyze:
	// other ecosystems, Go module sources, binaries without build
	// information.
	ReachabilityUnknown = "unknown"
)

const maxGoBinaries = 25

// GoReachability runs govulncheck on the Go executables a scan finds, to
// tell which vulnerable functions their code calls.
type GoReachability struct {
	Exec Exec
	// DatabaseDir holds the Go vulnerability database (vuln.go.dev's
	// layout).
	DatabaseDir string
}

// GoVulnerability is what govulncheck found for one OSV entry and module.
type GoVulnerability struct {
	OSV     string
	Aliases []string
	Module  string
	// Reachable is set when a call path reaches a vulnerable function;
	// Trace names it.
	Reachable bool
	Trace     string
}

// SetReachability makes scans run govulncheck on Go executables; nil runs
// nothing.
func (s *Service) SetReachability(r *GoReachability) { s.reachability = r }

// GoExtract is govulncheck's extract of a Go executable (its modules and
// symbols), gzipped: enough to analyze the executable again.
type GoExtract struct {
	Executable string
	Data       []byte
}

// GoAnalysis is what Analyze found: the reachable vulnerabilities, the
// extracts to analyze again later, and whether the Go vulnerability
// database was there to analyze them with.
type GoAnalysis struct {
	Vulnerabilities []GoVulnerability
	Extracts        []GoExtract
	DatabasePresent bool
}

// Analyze extracts the Go executables Syft found in source and, when the Go
// vulnerability database is there, analyzes them; the extracts are kept to
// analyze them again when the database changes.
func (g *GoReachability) Analyze(
	ctx context.Context,
	source string,
	sbom []byte,
	scratch string,
) (GoAnalysis, error) {
	paths := goBinaryPaths(sbom)
	if len(paths) == 0 {
		return GoAnalysis{
			DatabasePresent: g.HasDatabase(),
		}, nil
	}

	files, names, err := localBinaries(source, paths, scratch)
	if err != nil {
		return GoAnalysis{}, err
	}

	extracts := make([]GoExtract, 0, len(files))

	var failures []error

	for i, f := range files {
		out, err := g.Exec.run(ctx, "-mode=extract", f)
		if err != nil {
			failures = append(failures, fmt.Errorf("extract %s: %w", names[i], err))

			continue
		}

		packed, err := gzipBytes(out)
		if err != nil {
			failures = append(failures, fmt.Errorf("pack the extract of %s: %w", names[i], err))

			continue
		}

		extracts = append(extracts, GoExtract{
			Executable: names[i],
			Data:       packed,
		})
	}

	if !g.HasDatabase() {
		return GoAnalysis{
			Extracts: extracts,
		}, errors.Join(failures...)
	}

	vulns, err := g.AnalyzeExtracts(ctx, extracts, scratch)

	return GoAnalysis{
		Vulnerabilities: vulns,
		Extracts:        extracts,
		DatabasePresent: true,
	}, errors.Join(append(failures, err)...)
}

// HasDatabase reports whether the Go vulnerability database is there.
func (g *GoReachability) HasDatabase() bool {
	return GoVulnerabilityDatabase{
		Dir: g.DatabaseDir,
	}.Present()
}

// AnalyzeExtracts runs govulncheck on stored extracts.
func (g *GoReachability) AnalyzeExtracts(
	ctx context.Context,
	extracts []GoExtract,
	scratch string,
) ([]GoVulnerability, error) {
	found := map[string]*GoVulnerability{}

	for i, e := range extracts {
		data, err := gunzip(e.Data)
		if err != nil {
			return nil, fmt.Errorf("read the extract of %s: %w", e.Executable, err)
		}

		file := filepath.Join(scratch, "go-extract-"+strconv.Itoa(i)+".json")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			return nil, err
		}

		out, err := g.Exec.run(ctx, "-mode=binary", "-format=json", "-db=file://"+g.DatabaseDir, file)
		if err != nil {
			return nil, err
		}

		if err := readGovulncheck(out, found); err != nil {
			return nil, err
		}
	}

	vulns := make([]GoVulnerability, 0, len(found))
	for _, v := range found {
		vulns = append(vulns, *v)
	}

	slices.SortFunc(vulns, func(a, b GoVulnerability) int {
		return strings.Compare(a.OSV+" "+a.Module, b.OSV+" "+b.Module)
	})

	return vulns, nil
}

// goBinaryPaths are the files Syft read Go build information from.
func goBinaryPaths(sbom []byte) []string {
	var doc struct {
		Artifacts []struct {
			Type         string     `json:"type"`
			MetadataType string     `json:"metadataType"`
			Locations    []Location `json:"locations"`
		} `json:"artifacts"`
	}

	if json.Unmarshal(sbom, &doc) != nil {
		return nil
	}

	var out []string

	for _, a := range doc.Artifacts {
		if a.Type != "go-module" || a.MetadataType != "go-module-buildinfo-entry" {
			continue
		}

		for _, l := range a.Locations {
			if l.Path != "" && !slices.Contains(out, l.Path) {
				out = append(out, l.Path)
			}
		}
	}

	slices.Sort(out)

	return out[:min(len(out), maxGoBinaries)]
}

// localBinaries returns the files of paths in source (in the directory an
// archive was unpacked to, the uploaded file itself, or copied out of an
// image's layers into scratch) and, for each, the executable's name: its
// path in the image or archive, or the uploaded file's name.
func localBinaries(source string, paths []string, scratch string) (files, names []string, err error) {
	kind, root, _ := strings.Cut(source, ":")

	switch kind {
	case "dir":
		for _, p := range paths {
			f, err := joinWithin(root, p)
			if err != nil {
				continue
			}

			if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
				files, names = append(files, f), append(names, p)
			}
		}
	case "file":
		files, names = append(files, root), append(names, filepath.Base(root))
	case "oci-dir":
		copies, err := imageFiles(root, paths, filepath.Join(scratch, "go-binaries"))
		if err != nil {
			return nil, nil, fmt.Errorf("copy Go executables out of the image: %w", err)
		}

		for _, p := range paths {
			if f, ok := copies[p]; ok {
				files, names = append(files, f), append(names, p)
			}
		}
	}

	return files, names, nil
}

func joinWithin(root, p string) (string, error) {
	f := filepath.Join(root, filepath.FromSlash(p))

	rel, err := filepath.Rel(root, f)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("outside the content")
	}

	return f, nil
}

type govulncheckMessage struct {
	OSV *struct {
		ID      string   `json:"id"`
		Aliases []string `json:"aliases"`
	} `json:"osv"`
	Finding *struct {
		OSV   string `json:"osv"`
		Trace []struct {
			Module   string `json:"module"`
			Package  string `json:"package"`
			Function string `json:"function"`
			Receiver string `json:"receiver"`
		} `json:"trace"`
	} `json:"finding"`
}

// readGovulncheck adds what one govulncheck JSON stream reports to found,
// keyed by OSV entry and module.
func readGovulncheck(out []byte, found map[string]*GoVulnerability) error {
	aliases := map[string][]string{}
	dec := json.NewDecoder(strings.NewReader(string(out)))

	for {
		var m govulncheckMessage

		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("read govulncheck's output: %w", err)
		}

		switch {
		case m.OSV != nil:
			aliases[m.OSV.ID] = m.OSV.Aliases
		case m.Finding != nil && len(m.Finding.Trace) > 0:
			top := m.Finding.Trace[0]
			key := m.Finding.OSV + " " + top.Module

			v, ok := found[key]
			if !ok {
				v = &GoVulnerability{
					OSV:    m.Finding.OSV,
					Module: top.Module,
				}
				found[key] = v
			}

			if top.Function != "" && !v.Reachable {
				v.Reachable = true

				frames := make([]string, 0, len(m.Finding.Trace))
				for _, f := range m.Finding.Trace {
					frames = append(frames, frameName(f.Package, f.Receiver, f.Function))
				}

				v.Trace = truncate(strings.Join(frames, " ← "), 2000)
			}
		}
	}

	for _, v := range found {
		if a, ok := aliases[v.OSV]; ok && len(v.Aliases) == 0 {
			v.Aliases = a
		}
	}

	return nil
}

func frameName(pkg, receiver, function string) string {
	switch {
	case function == "":
		return pkg
	case receiver != "":
		return pkg + ".(" + receiver + ")." + function
	default:
		return pkg + "." + function
	}
}

func storeGoExtracts(ctx context.Context, q database.Querier, sha256Hex string, extracts []GoExtract) error {
	if err := q.DeleteGoExtracts(ctx, sha256Hex); err != nil {
		return fmt.Errorf("clear Go extracts: %w", err)
	}

	for _, e := range extracts {
		if err := q.InsertGoExtract(ctx, database.InsertGoExtractParams{
			Sha256:     sha256Hex,
			Executable: truncate(e.Executable, 1000),
			Extract:    e.Data,
		}); err != nil {
			return fmt.Errorf("record the Go extract of %s: %w", e.Executable, err)
		}
	}

	return nil
}

const reachabilityBatch = 100

// CheckReachability analyzes again, from their stored extracts, the Go
// executables of scans analyzed before the Go vulnerability database last
// changed, a batch at a time, and returns how many scans it checked; a scan
// whose analysis fails keeps its old results until the database changes.
func (s *Service) CheckReachability(ctx context.Context, changed time.Time) (int, error) {
	if s.reachability == nil || !s.reachability.HasDatabase() {
		return 0, nil
	}

	stale, err := s.db.ListReachabilityStaleScans(ctx, database.ListReachabilityStaleScansParams{
		Changed: &changed,
		MaxRows: reachabilityBatch,
	})
	if err != nil {
		return 0, err
	}

	var failures []error

	for _, sha := range stale {
		err := s.checkReachability(ctx, sha)
		if err == nil {
			continue
		}

		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		failures = append(failures, fmt.Errorf("reachability of %s: %w", sha, err))

		now := s.now()
		if err := s.db.SetReachabilityChecked(ctx, database.SetReachabilityCheckedParams{
			Sha256:  sha,
			Checked: &now,
		}); err != nil {
			return 0, errors.Join(append(failures, fmt.Errorf("record the failed check of %s: %w", sha, err))...)
		}
	}

	return len(stale), errors.Join(failures...)
}

func (s *Service) checkReachability(ctx context.Context, sha256Hex string) error {
	rows, err := s.db.ListGoExtracts(ctx, sha256Hex)
	if err != nil {
		return err
	}

	extracts := make([]GoExtract, 0, len(rows))
	for _, r := range rows {
		extracts = append(extracts, GoExtract{
			Executable: r.Executable,
			Data:       r.Extract,
		})
	}

	scratch, err := os.MkdirTemp("", "relic-reachability-")
	if err != nil {
		return err
	}

	defer func() {
		_ = os.RemoveAll(scratch)
	}()

	var vulns []GoVulnerability

	if err := telemetry.ScanStage(ctx, "reachability", func(ctx context.Context) error {
		var err error

		vulns, err = s.reachability.AnalyzeExtracts(ctx, extracts, scratch)

		return err
	}); err != nil {
		return err
	}

	now := s.now()

	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := storeReachability(ctx, q, sha256Hex, vulns); err != nil {
			return err
		}

		return q.SetReachabilityChecked(ctx, database.SetReachabilityCheckedParams{
			Sha256:  sha256Hex,
			Checked: &now,
		})
	})
}

func storeReachability(ctx context.Context, q database.Querier, sha256Hex string, vulns []GoVulnerability) error {
	if err := q.DeleteReachability(ctx, sha256Hex); err != nil {
		return fmt.Errorf("clear reachability: %w", err)
	}

	for _, v := range vulns {
		if err := q.InsertReachability(ctx, database.InsertReachabilityParams{
			Sha256:    sha256Hex,
			OsvID:     v.OSV,
			Module:    v.Module,
			Aliases:   nonNil(v.Aliases),
			Reachable: v.Reachable,
			Trace:     v.Trace,
		}); err != nil {
			return fmt.Errorf("record reachability: %w", err)
		}
	}

	return nil
}

// ReachabilityIndex tells the reachability of stored findings, from what
// govulncheck found in the same content.
type ReachabilityIndex map[string][]database.ScanReachability

// Reachability loads what govulncheck found in these contents.
func (r *Results) Reachability(ctx context.Context, sha256s []string) (ReachabilityIndex, error) {
	rows, err := r.db.ListReachability(ctx, nonNil(sha256s))
	if err != nil {
		return nil, fmt.Errorf("reachability: %w", err)
	}

	out := ReachabilityIndex{}
	for _, row := range rows {
		out[row.Sha256] = append(out[row.Sha256], row)
	}

	return out, nil
}

// State is the reachability of a finding (its vulnerability ID, CVEs and
// package URL) in content sha256Hex, and the call path when it is
// reachable: unknown unless govulncheck reported the vulnerability for the
// finding's Go module.
func (x ReachabilityIndex) State(sha256Hex, vulnID string, cves []string, purl string) (state, trace string) {
	module, ok := goModule(purl)
	if !ok {
		return ReachabilityUnknown, ""
	}

	state = ReachabilityUnknown

	for _, row := range x[sha256Hex] {
		if row.Module != module || !namesVulnerability(row, vulnID, cves) {
			continue
		}

		if row.Reachable {
			return Reachable, row.Trace
		}

		state = NotReachable
	}

	return state, ""
}

func namesVulnerability(row database.ScanReachability, vulnID string, cves []string) bool {
	if row.OsvID == vulnID || slices.Contains(row.Aliases, vulnID) {
		return true
	}

	for _, c := range cves {
		if slices.Contains(row.Aliases, c) {
			return true
		}
	}

	return false
}

// goModule is the Go module path of a golang package URL: stdlib for the
// standard library, as govulncheck names it.
func goModule(purl string) (string, bool) {
	rest, ok := strings.CutPrefix(purl, "pkg:golang/")
	if !ok {
		return "", false
	}

	if i := strings.IndexAny(rest, "@?#"); i >= 0 {
		rest = rest[:i]
	}

	return rest, rest != ""
}

// UnreachableFinding is a vulnerability govulncheck found no call path to in
// a scanned file.
type UnreachableFinding struct {
	Sha256   string
	FileName string
	VulnID   string
	CVEs     []string
	PURL     string
}

const maxUnreachableContents = 10000

// UnreachableFindings lists the findings govulncheck found no call path
// to, across scanned contents.
func (r *Results) UnreachableFindings(ctx context.Context) ([]UnreachableFinding, error) {
	contents, err := r.db.ListNotReachableContents(ctx, maxUnreachableContents)
	if err != nil {
		return nil, fmt.Errorf("unreachable findings: %w", err)
	}

	shas := make([]string, 0, len(contents))
	names := make(map[string]string, len(contents))

	for _, c := range contents {
		shas = append(shas, c.Sha256)
		names[c.Sha256] = c.FileName
	}

	index, err := r.Reachability(ctx, shas)
	if err != nil {
		return nil, err
	}

	findings, err := r.db.ListFindingsForContents(ctx, nonNil(shas))
	if err != nil {
		return nil, fmt.Errorf("unreachable findings: %w", err)
	}

	var out []UnreachableFinding

	for _, f := range findings {
		if state, _ := index.State(f.Sha256, f.VulnID, f.Cves, f.Purl); state != NotReachable {
			continue
		}

		out = append(out, UnreachableFinding{
			Sha256:   f.Sha256,
			FileName: names[f.Sha256],
			VulnID:   f.VulnID,
			CVEs:     f.Cves,
			PURL:     f.Purl,
		})
	}

	return out, nil
}
