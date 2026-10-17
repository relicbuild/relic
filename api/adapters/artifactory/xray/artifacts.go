package xray

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/api/adapters/artifactory/aql"
	"relic/api/adapters/artifactory/layout"
	"relic/api/adapters/artifactory/wire"
	"relic/api/adapters/artifactory/xray/xraymap"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httpserver"
	"relic/internal/scanning/scan"
)

// SetRepos gives the artifact endpoints the repository service they read
// items through.
func (h *Handler) SetRepos(r *repository.Service) { h.repos = r }

// Scan statuses of an artifact.
const (
	statusNotSupported = "NOT_SUPPORTED"
	statusPending      = "PENDING"
	statusScanning     = "SCANNING"
	statusDone         = "DONE"
	statusPartial      = "PARTIAL"
	statusFailed       = "FAILED"
)

const notIndexed = "Artifact does not exist or is not indexed"

var scannable = regexp.MustCompile(`(?i)` + scan.ScannablePattern)

const listManifestName = "list.manifest.json"

func (h *Handler) registerArtifacts(rt *httpserver.Router) {
	rt.HandleFunc("POST /xray/api/v1/scanArtifact", auth.Authenticated, h.scanArtifact)
	rt.HandleFunc("POST /xray/api/v1/artifact/status", auth.Authenticated, h.artifactStatus)
	rt.HandleFunc("POST /xray/api/v1/summary/artifact", auth.Authenticated, h.artifactSummary)
	rt.HandleFunc("POST /xray/api/v2/summary/artifact", auth.Authenticated, h.artifactSummary)
	rt.HandleFunc("GET /xray/api/v1/artifacts", auth.Authenticated, h.listArtifacts)
}

func (h *Handler) resolve(ctx context.Context, repoKey, p string) (repository.Item, bool, error) {
	remote, cached, err := layout.RemoteOfCache(ctx, h.repos, repoKey)
	if err != nil {
		return repository.Item{}, false, err
	}

	if cached {
		repoKey = remote
	}

	item, err := h.repos.Stat(ctx, repoKey, strings.Trim(p, "/"))
	if err != nil {
		return repository.Item{}, false, err
	}

	ok, err := h.canRead(ctx, item.Repository, item.Path())

	return item, ok, err
}

func (h *Handler) canRead(ctx context.Context, repoKey, p string) (bool, error) {
	readable, err := h.readableIn(ctx, repoKey)
	if err != nil {
		return false, err
	}

	return readable(p), nil
}

func (h *Handler) readableIn(ctx context.Context, repoKey string) (func(p string) bool, error) {
	pr := auth.FromContext(ctx)
	if pr.Admin {
		return func(string) bool {
			return true
		}, nil
	}

	unreadable := func(string) bool {
		return false
	}

	if h.Permissions == nil {
		return unreadable, nil
	}

	perms, err := h.Permissions(ctx)
	if err != nil {
		return nil, err
	}

	rp, err := h.repos.GetRepository(ctx, repoKey)
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		return unreadable, nil
	}

	if err != nil {
		return nil, err
	}

	target := auth.Repository{
		Name:  rp.Name,
		Class: string(rp.Class),
	}

	return func(p string) bool {
		if p == "" {
			return perms.CanReadRepository(pr.Subject(), target)
		}

		return perms.CanInRepository(pr.Subject(), target, p, auth.ActionRead)
	}, nil
}

func (h *Handler) contents(ctx context.Context, item repository.Item) ([]string, error) {
	if item.IsFolder() {
		return nil, nil
	}

	if item.Name != listManifestName {
		if scannable.MatchString(item.Name) {
			return []string{
				item.Checksums.SHA256,
			}, nil
		}

		return nil, nil
	}

	rc, err := h.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.NewDecoder(io.LimitReader(rc, 4<<20)).Decode(&index); err != nil {
		return nil, nil //nolint:nilerr // not an image index: nothing Relic Scan catalogs
	}

	image := path.Dir(item.Parent)

	var folders []string

	for _, m := range index.Manifests {
		if hexDigest, ok := strings.CutPrefix(m.Digest, "sha256:"); ok {
			folders = append(folders, path.Join(image, "sha256__"+hexDigest))
		}
	}

	manifests, err := h.results.ImageManifests(ctx, item.Repository, folders)
	if err != nil {
		return nil, fmt.Errorf("image manifests of %s: %w", item.Path(), err)
	}

	out := make([]string, 0, len(manifests))
	for _, f := range folders {
		if sha, ok := manifests[f]; ok {
			out = append(out, sha)
		}
	}

	return out, nil
}

type scanStatus struct {
	Status string `json:"status"`
	Time   string `json:"time"`
}

type scanStatuses struct {
	Overall scanStatus `json:"overall"`
	Details struct {
		Sca                scanStatus `json:"sca"`
		ContextualAnalysis scanStatus `json:"contextual_analysis"`
		Exposures          scanStatus `json:"exposures"`
	} `json:"details"`
}

func (h *Handler) statusOf(ctx context.Context, contents []string) (scanStatuses, error) {
	status, updated := statusNotSupported, time.Time{}

	if len(contents) > 0 {
		counts := map[string]int{}

		for _, sha := range contents {
			a, err := h.results.Artifact(ctx, sha)
			switch {
			case errors.Is(err, scan.ErrNoScan):
				counts[statusPending]++
				continue
			case err != nil:
				return scanStatuses{}, fmt.Errorf("scan of %s: %w", sha, err)
			}

			if a.Updated.After(updated) {
				updated = a.Updated
			}

			switch a.Status {
			case scan.StatusDone:
				counts[statusDone]++
			case scan.StatusPartial:
				counts[statusPartial]++
			case scan.StatusFailed:
				counts[statusFailed]++
			case scan.StatusSkipped:
				counts[statusNotSupported]++
			default:
				counts[statusScanning]++
			}
		}

		for _, s := range []string{
			statusScanning,
			statusPending,
			statusFailed,
			statusPartial,
			statusDone,
			statusNotSupported,
		} {
			if counts[s] > 0 {
				status = s
				break
			}
		}
	}

	if updated.IsZero() {
		updated = time.Now()
	}

	at := updated.UTC().Format(time.RFC3339)

	var out scanStatuses

	out.Overall = scanStatus{
		Status: status,
		Time:   at,
	}
	out.Details.Sca = out.Overall
	out.Details.ContextualAnalysis = scanStatus{
		Status: statusNotSupported,
		Time:   at,
	}
	out.Details.Exposures = scanStatus{
		Status: statusNotSupported,
		Time:   at,
	}

	return out, nil
}

func (h *Handler) scanArtifact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ComponentID string `json:"componentID"`
		Path        string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "expected {\"componentID\":…,\"path\":\"<repo>/<path>\"}")
		return
	}

	repoKey, p, _ := strings.Cut(strings.TrimPrefix(req.Path, "/"), "/")

	item, ok := h.readable(w, r, repoKey, p)
	if !ok {
		return
	}

	contents, err := h.contents(r.Context(), item)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	if len(contents) > 0 {
		forgotten, err := h.results.ForgetFailed(r.Context(), contents)
		if err != nil {
			h.internalError(w, r, err)
			return
		}

		if len(forgotten) > 0 {
			h.log.InfoContext(
				r.Context(),
				"xray: failed scans queued again",
				"repo",
				repoKey,
				"path",
				p,
				"count",
				len(forgotten),
			)
		}
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"info": "The artifact is being scanned.",
	})
}

func (h *Handler) readable(w http.ResponseWriter, r *http.Request, repoKey, p string) (repository.Item, bool) {
	item, ok, err := h.resolve(r.Context(), repoKey, p)
	switch {
	case errors.Is(err, repository.ErrNotFound),
		errors.Is(err, repository.ErrRepositoryNotFound),
		errors.Is(err, repository.ErrInvalidPath):
		writeError(w, http.StatusNotFound, notIndexed)
		return repository.Item{}, false
	case err != nil:
		h.log.ErrorContext(r.Context(), "xray: resolve artifact", "repo", repoKey, "path", p, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return repository.Item{}, false
	case !ok:
		writeError(w, http.StatusForbidden, "no permission to read "+repoKey+"/"+p)
		return repository.Item{}, false
	}

	return item, true
}

func (h *Handler) artifactStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Repo == "" {
		writeError(w, http.StatusBadRequest, "expected {\"repo\":…,\"path\":…}")
		return
	}

	item, ok := h.readable(w, r, req.Repo, req.Path)
	if !ok {
		return
	}

	contents, err := h.contents(r.Context(), item)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	st, err := h.statusOf(r.Context(), contents)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, st)
}

func (h *Handler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.ErrorContext(r.Context(), "xray artifact API", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

type summaryGeneral struct {
	ComponentID string `json:"component_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Path        string `json:"path,omitempty"`
	PkgType     string `json:"pkg_type,omitempty"`
	Sha256      string `json:"sha256,omitempty"`
}

type summaryCve struct {
	Cve    string `json:"cve,omitempty"`
	CvssV2 string `json:"cvss_v2,omitempty"`
	CvssV3 string `json:"cvss_v3,omitempty"`
}

type summaryComponent struct {
	ComponentID   string   `json:"component_id,omitempty"`
	FixedVersions []string `json:"fixed_versions,omitempty"`
}

type summaryIssue struct {
	IssueID     string             `json:"issue_id"`
	Summary     string             `json:"summary"`
	Description string             `json:"description"`
	IssueType   string             `json:"issue_type"`
	Severity    string             `json:"severity"`
	Provider    string             `json:"provider"`
	Cves        []summaryCve       `json:"cves,omitempty"`
	Created     string             `json:"created,omitempty"`
	ImpactPath  []string           `json:"impact_path,omitempty"`
	Components  []summaryComponent `json:"components,omitempty"`
}

type summaryLicense struct {
	Name       string   `json:"name"`
	FullName   string   `json:"full_name"`
	Components []string `json:"components"`
}

type summaryArtifact struct {
	General  summaryGeneral   `json:"general"`
	Issues   []summaryIssue   `json:"issues"`
	Licenses []summaryLicense `json:"licenses"`
}

type summaryError struct {
	Error      string `json:"error"`
	Identifier string `json:"identifier"`
}

func (h *Handler) artifactSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Checksums []string `json:"checksums"`
		Paths     []string `json:"paths"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).
		Decode(&req); err != nil ||
		len(req.Checksums)+len(req.Paths) > 1000 {
		writeError(w, http.StatusBadRequest, "expected {\"checksums\":[…]} or {\"paths\":[…]}")
		return
	}

	resp := struct {
		Artifacts []summaryArtifact `json:"artifacts"`
		Errors    []summaryError    `json:"errors"`
	}{
		Artifacts: []summaryArtifact{},
		Errors:    []summaryError{},
	}
	fail := func(id string) {
		resp.Errors = append(resp.Errors, summaryError{
			Error:      notIndexed,
			Identifier: id,
		})
	}
	ctx := r.Context()

	for _, sum := range req.Checksums {
		sha := strings.ToLower(sum)

		item, ok, err := h.readableContent(ctx, sha)
		if err != nil {
			h.internalError(w, r, err)
			return
		}

		if !ok {
			fail(sum)
			continue
		}

		a, ok, err := h.summarize(ctx, sha, item)
		if err != nil {
			h.internalError(w, r, err)
			return
		}

		if !ok {
			fail(sum)
			continue
		}

		resp.Artifacts = append(resp.Artifacts, a)
	}

	for _, p := range req.Paths {
		rest, ok := strings.CutPrefix(p, "default/")

		repoKey, itemPath, _ := strings.Cut(rest, "/")
		if !ok || itemPath == "" {
			fail(p)
			continue
		}

		item, readable, err := h.resolve(ctx, repoKey, itemPath)
		if err != nil && !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, repository.ErrRepositoryNotFound) &&
			!errors.Is(err, repository.ErrInvalidPath) {
			h.internalError(w, r, err)
			return
		}

		if err != nil || !readable || item.IsFolder() || !scannable.MatchString(item.Name) {
			fail(p)
			continue
		}

		a, ok, err := h.summarize(ctx, item.Checksums.SHA256, item)
		if err != nil {
			h.internalError(w, r, err)
			return
		}

		if !ok {
			fail(p)
			continue
		}

		resp.Artifacts = append(resp.Artifacts, a)
	}

	wire.WriteJSON(w, http.StatusOK, resp)
}

// Content the caller cannot read is reported as unknown.
func (h *Handler) readableContent(ctx context.Context, sha string) (repository.Item, bool, error) {
	items, err := h.repos.FilesWithContent(ctx, repository.Checksums{
		SHA256: sha,
	})
	if err != nil {
		return repository.Item{}, false, err
	}

	for _, it := range items {
		ok, err := h.canRead(ctx, it.Repository, it.Path())
		if err != nil {
			return repository.Item{}, false, err
		}

		if ok {
			return it, true, nil
		}
	}

	return repository.Item{}, false, nil
}

func (h *Handler) summarize(ctx context.Context, sha string, item repository.Item) (summaryArtifact, bool, error) {
	a, err := h.results.Artifact(ctx, sha)
	if errors.Is(err, scan.ErrNoScan) {
		return summaryArtifact{}, false, nil
	}

	if err != nil {
		return summaryArtifact{}, false, fmt.Errorf("scan of %s: %w", sha, err)
	}

	if a.Status != scan.StatusDone && a.Status != scan.StatusPartial {
		return summaryArtifact{}, false, nil
	}

	findings, err := h.results.Findings(ctx, sha)
	if err != nil {
		return summaryArtifact{}, false, err
	}

	full := layout.ShownName(ctx, h.repos, item.Repository) + "/" + item.Path()
	created := a.Updated.UTC().Format(time.RFC3339)
	out := summaryArtifact{
		General: summaryGeneral{
			Name:    item.Name,
			Path:    "default/" + full,
			PkgType: pkgType(item.Name),
			Sha256:  sha,
		},
		Issues:   []summaryIssue{},
		Licenses: sbomLicenses(a.SBOM),
	}
	byID := map[string]int{}

	for _, f := range findings {
		component, err := xraymap.FromPURL(f.Package.PURL)
		if err != nil {
			component = f.Package.PURL
		}

		i, ok := byID[f.ID]
		if !ok {
			i = len(out.Issues)
			byID[f.ID] = i
			out.Issues = append(out.Issues, summaryIssue{
				IssueID:     f.ID,
				Summary:     f.Summary,
				Description: f.Summary,
				IssueType:   "security",
				Severity:    xraymap.Severity(string(f.Severity)),
				Provider:    "Relic",
				Created:     created,
				Cves:        summaryCves(f),
			})
		}

		is := &out.Issues[i]
		is.Components = append(is.Components, summaryComponent{
			ComponentID:   component,
			FixedVersions: f.FixedVersions,
		})
		is.ImpactPath = append(is.ImpactPath, "default/"+full+"/"+component)
	}

	return out, true, nil
}

func summaryCves(f scan.Finding) []summaryCve {
	var v2, v3 string

	for _, c := range f.CVSS {
		s := strconv.FormatFloat(c.Score, 'f', -1, 64) + "/" + c.Vector
		if strings.HasPrefix(c.Version, "2") {
			v2 = s
		} else if strings.HasPrefix(c.Version, "3") && v3 == "" {
			v3 = s
		}
	}

	var out []summaryCve
	for _, id := range f.CVEs() {
		out = append(out, summaryCve{
			Cve:    id,
			CvssV2: v2,
			CvssV3: v3,
		})
	}

	if len(out) == 0 && (v2 != "" || v3 != "") {
		out = append(out, summaryCve{
			CvssV2: v2,
			CvssV3: v3,
		})
	}

	return out
}

func sbomLicenses(gz []byte) []summaryLicense {
	out := []summaryLicense{}
	if len(gz) == 0 {
		return out
	}

	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return out
	}

	var doc struct {
		Artifacts []struct {
			PURL     string            `json:"purl"`
			Licenses []json.RawMessage `json:"licenses"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(io.LimitReader(zr, 256<<20)).Decode(&doc); err != nil {
		return out
	}

	byName := map[string]int{}

	for _, a := range doc.Artifacts {
		component, err := xraymap.FromPURL(a.PURL)
		if err != nil {
			component = a.PURL
		}

		for _, raw := range a.Licenses {
			name := licenseName(raw)
			if name == "" {
				continue
			}

			i, ok := byName[name]
			if !ok {
				i = len(out)
				byName[name] = i
				out = append(out, summaryLicense{
					Name:     name,
					FullName: name,
				})
			}

			if component != "" && !slices.Contains(out[i].Components, component) {
				out[i].Components = append(out[i].Components, component)
			}
		}
	}

	return out
}

func licenseName(raw json.RawMessage) string {
	var (
		obj struct {
			Value string `json:"value"`
		}
		name string
	)
	if json.Unmarshal(raw, &obj) == nil && obj.Value != "" {
		name = obj.Value
	} else if json.Unmarshal(raw, &name) != nil {
		return ""
	}

	if strings.HasPrefix(name, "sha256:") || strings.HasPrefix(name, "LicenseRef-sha256") {
		return ""
	}

	return name
}

func pkgType(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "manifest.json":
		return "Docker"
	case strings.HasSuffix(lower, ".tgz"):
		return "Npm"
	case strings.HasSuffix(lower, ".whl"), strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".egg"):
		return "Pypi"
	case strings.HasSuffix(lower, ".nupkg"):
		return "NuGet"
	case strings.HasSuffix(lower, ".crate"):
		return "Cargo"
	case strings.HasSuffix(lower, ".jar"), strings.HasSuffix(lower, ".war"), strings.HasSuffix(lower, ".ear"):
		return "Maven"
	default:
		return "Generic"
	}
}

type listedArtifact struct {
	Name         string           `json:"name"`
	RepoPath     string           `json:"repo_path"`
	PackageID    string           `json:"package_id"`
	Version      string           `json:"version"`
	SecIssues    map[string]int64 `json:"sec_issues"`
	ScansStatus  scanStatuses     `json:"scans_status"`
	Size         string           `json:"size"`
	Violations   int              `json:"violations"`
	Created      string           `json:"created"`
	DeployedBy   string           `json:"deployed_by"`
	RepoFullPath string           `json:"repo_full_path"`
}

const maxListed = 10000

func (h *Handler) listArtifacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repoKey := q.Get("repo")
	rows, _ := strconv.Atoi(q.Get("num_of_rows"))
	page, _ := strconv.Atoi(q.Get("page_num"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	if repoKey == "" || rows <= 0 || rows > 1000 {
		writeError(w, http.StatusBadRequest, "repo and num_of_rows (1-1000) are required")
		return
	}

	page = max(page, 1)
	ctx := r.Context()

	key := repoKey

	remote, cached, err := layout.RemoteOfCache(ctx, h.repos, repoKey)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	if cached {
		key = remote
	}

	readable, err := h.readableIn(ctx, key)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	if !readable("") {
		writeError(w, http.StatusForbidden, "no permission to read "+repoKey)
		return
	}

	dir := "$desc"
	if strings.EqualFold(q.Get("direction"), "asc") {
		dir = "$asc"
	}

	criteria := map[string]any{
		"repo": key,
		"type": "file",
	}
	if s := q.Get("search"); s != "" {
		criteria["$or"] = []map[string]any{
			{
				"path": map[string]string{
					"$match": "*" + s + "*",
				},
			},
			{
				"name": map[string]string{
					"$match": "*" + s + "*",
				},
			},
		}
	}

	crit, err := json.Marshal(criteria)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	query, err := aql.Parse(
		fmt.Sprintf(
			`items.find(%s).include("repo","path","name","size","created","created_by","sha256").sort({%q:["created"]}).limit(%d)`,
			crit,
			dir,
			maxListed,
		),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid search")
		return
	}

	plan, err := aql.PlanItems(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid search")
		return
	}

	skip, want := (page-1)*rows, rows
	if offset > 0 {
		skip = offset
	}

	first := skip

	var picked []aql.ItemRow

	err = aql.Items(ctx, h.aql, plan, func(it aql.ItemRow) error {
		if len(picked) >= want || it.SHA256 == nil || !scannable.MatchString(it.Name) {
			return nil
		}

		if !readable(strings.TrimPrefix(path.Join(it.Path, it.Name), "./")) {
			return nil
		}

		if skip > 0 {
			skip--
			return nil
		}

		picked = append(picked, it)

		return nil
	})
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	shas := make([]string, 0, len(picked))
	for _, it := range picked {
		shas = append(shas, *it.SHA256)
	}

	counts, err := h.results.SeverityCounts(ctx, shas)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	bySha := map[string]map[string]int64{}

	for sha, bySeverity := range counts {
		m := map[string]int64{}
		for sev, n := range bySeverity {
			m[strings.ToLower(xraymap.Severity(string(sev)))] += n
			m["total"] += n
		}

		bySha[sha] = m
	}

	shown := layout.ShownName(ctx, h.repos, key)

	data := make([]listedArtifact, 0, len(picked))
	for _, it := range picked {
		p := strings.TrimPrefix(path.Join(it.Path, it.Name), "./")

		st, err := h.statusOf(ctx, []string{
			*it.SHA256,
		})
		if err != nil {
			h.internalError(w, r, err)
			return
		}

		issues := map[string]int64{
			"critical": 0,
			"high":     0,
			"medium":   0,
			"low":      0,
			"unknown":  0,
			"total":    0,
		}
		maps.Copy(issues, bySha[*it.SHA256])

		row := listedArtifact{
			Name:         it.Name,
			RepoPath:     "/" + p,
			SecIssues:    issues,
			ScansStatus:  st,
			Size:         humanSize(it.Size),
			Created:      it.Created.UTC().Format(time.RFC3339),
			DeployedBy:   it.CreatedBy,
			RepoFullPath: shown + "/" + p,
		}
		if it.Name == "manifest.json" && it.Path != "." {
			row.Name = it.Path
			row.PackageID = "docker://" + path.Dir(it.Path)
			row.Version = path.Base(it.Path)
		}

		data = append(data, row)
	}

	next := first + len(data)
	if len(data) < rows {
		next = -1
	}

	wire.WriteJSON(w, http.StatusOK, map[string]any{
		"data":   data,
		"offset": next,
	})
}

func humanSize(n int64) string {
	units := []string{
		"B",
		"KB",
		"MB",
		"GB",
		"TB",
	}

	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}

	if i == 0 {
		return strconv.FormatInt(n, 10) + " B"
	}

	return strconv.FormatFloat(f, 'f', 2, 64) + " " + units[i]
}
