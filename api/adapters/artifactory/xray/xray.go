// Package xray serves Relic Scan's adapter API.
package xray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"relic/api/adapters/artifactory/wire"
	"relic/api/adapters/artifactory/xray/xraymap"
	"relic/internal/access/auth"
	"relic/internal/artifacts/itemquery"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/buildinfo"
	"relic/internal/platform/httpserver"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

// Version is the version this API reports.
const Version = xraymap.XrayVersion

const maxGraphBytes = 32 << 20

// Handler serves the scan API.
type Handler struct {
	// IndexerDir holds relic-indexer-<os>-<arch>[.exe] builds.
	IndexerDir string
	// Permissions loads permission targets for build checks; without it
	// only admins may scan builds.
	Permissions func(context.Context) (auth.Permissions, error)

	aql       itemquery.Querier
	results   *scan.Results
	builds    *buildinfo.Service
	scans     *scan.Service
	enqueue   func(ctx context.Context, id uuid.UUID) error
	admin     *policy.Admin
	repos     *repository.Service
	decisions *policy.Decisions
	log       *slog.Logger
}

// New returns a Handler.
func New(
	q itemquery.Querier,
	results *scan.Results,
	builds *buildinfo.Service,
	scans *scan.Service,
	admin *policy.Admin,
	enqueue func(context.Context, uuid.UUID) error,
	log *slog.Logger,
) *Handler {
	return &Handler{
		aql:     q,
		results: results,
		builds:  builds,
		scans:   scans,
		admin:   admin,
		enqueue: enqueue,
		log:     log,
	}
}

// Register mounts the API on rt.
func (h *Handler) Register(rt *httpserver.Router) {
	if h.repos != nil {
		h.registerArtifacts(rt)
	}

	rt.HandleFunc("GET /xray/api/v1/system/ping", auth.Public, func(w http.ResponseWriter, _ *http.Request) {
		wire.WriteJSON(w, http.StatusOK, map[string]string{
			"status": "pong",
		})
	})
	rt.HandleFunc("GET /xray/api/v1/system/version", auth.Authenticated, func(w http.ResponseWriter, _ *http.Request) {
		wire.WriteJSON(w, http.StatusOK, map[string]string{
			"xray_version":  Version,
			"xray_revision": "relic",
		})
	})
	rt.HandleFunc(
		"GET /xray/api/v1/entitlements/feature/{feature}",
		auth.Authenticated,
		func(w http.ResponseWriter, r *http.Request) {
			wire.WriteJSON(w, http.StatusOK, map[string]any{
				"feature_id": r.PathValue("feature"),
				"entitled":   false,
			})
		},
	)
	rt.HandleFunc("/xray/api/v1/xsc/", auth.Authenticated, func(w http.ResponseWriter, _ *http.Request) {
		wire.WriteError(w, http.StatusNotFound, "no such endpoint")
	})
	rt.HandleFunc("GET /xray/api/v1/indexer-resources/download/{os}/{arch}", auth.Authenticated, h.downloadIndexer)
	rt.HandleFunc("POST /xray/api/v1/scan/graph", auth.Authenticated, h.postGraph)
	rt.HandleFunc("GET /xray/api/v1/scan/graph/{id}", auth.Authenticated, h.getGraph)
	rt.HandleFunc("GET /xray/api/v2/policies", auth.Admin, h.listPolicies)
	rt.HandleFunc("POST /xray/api/v2/policies", auth.Admin, h.putPolicy)
	rt.HandleFunc("GET /xray/api/v2/policies/{name}", auth.Admin, h.getPolicy)
	rt.HandleFunc("PUT /xray/api/v2/policies/{name}", auth.Admin, h.putPolicy)
	rt.HandleFunc("DELETE /xray/api/v2/policies/{name}", auth.Admin, h.deletePolicy)
	rt.HandleFunc("GET /xray/api/v1/ignore_rules", auth.Admin, h.listIgnoreRules)
	rt.HandleFunc("POST /xray/api/v1/ignore_rules", auth.Admin, h.createIgnoreRule)
	rt.HandleFunc("GET /xray/api/v1/ignore_rules/{id}", auth.Admin, h.getIgnoreRule)
	rt.HandleFunc("DELETE /xray/api/v1/ignore_rules/{id}", auth.Admin, h.deleteIgnoreRule)
	rt.HandleFunc("GET /xray/api/v2/watches", auth.Admin, h.listWatches)
	rt.HandleFunc("POST /xray/api/v2/watches", auth.Admin, h.putWatch)
	rt.HandleFunc("GET /xray/api/v2/watches/{name}", auth.Admin, h.getWatch)
	rt.HandleFunc("PUT /xray/api/v2/watches/{name}", auth.Admin, h.putWatch)
	rt.HandleFunc("DELETE /xray/api/v2/watches/{name}", auth.Admin, h.deleteWatch)
	rt.HandleFunc("POST /xray/api/v2/ci/build", auth.Authenticated, h.triggerBuild)
	rt.HandleFunc("POST /xray/api/v2/ci/build/scanResult", auth.Authenticated, h.buildScanResult)
}

var platformRe = regexp.MustCompile(`^[a-z0-9]+$`)

func (h *Handler) downloadIndexer(w http.ResponseWriter, r *http.Request) {
	goos, goarch := r.PathValue("os"), r.PathValue("arch")
	if !platformRe.MatchString(goos) || !platformRe.MatchString(goarch) || h.IndexerDir == "" {
		writeError(w, http.StatusNotFound, "no indexer for "+goos+"/"+goarch)
		return
	}

	name := "relic-indexer-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}

	root, err := os.OpenRoot(h.IndexerDir)
	if err != nil {
		writeError(w, http.StatusNotFound, "no indexer for "+goos+"/"+goarch)
		return
	}

	defer func() {
		_ = root.Close()
	}()

	f, err := root.Open(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "no indexer for "+goos+"/"+goarch)
		return
	}

	defer func() {
		_ = f.Close()
	}()

	info, err := f.Stat()
	if err != nil {
		h.internal(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="indexer"`)
	http.ServeContent(w, r, "indexer", info.ModTime(), f)
}

// Node is a component graph node of a scan request.
type Node struct {
	ComponentID string   `json:"component_id"`
	Path        string   `json:"path,omitempty"`
	Licenses    []string `json:"licenses,omitempty"`
	Nodes       []Node   `json:"nodes,omitempty"`
}

type graphRequest struct {
	PURLs     []string            `json:"-"`
	Licenses  map[string][]string `json:"-"`
	Build     *buildRef           `json:"-"`
	Root      string              `json:"root"`
	IDsByPURL map[string][]string `json:"ids_by_purl"`
	Parents   map[string][]string `json:"parents"`
	Paths     map[string]string   `json:"paths,omitempty"`
	Context   bool                `json:"context"`
	Binary    bool                `json:"binary"`
	Watches   []string            `json:"watches,omitempty"`
	Repo      string              `json:"repo,omitempty"`
	Artifacts []string            `json:"artifacts,omitempty"`
}

const graphExtension = "xray"

func graphOf(g scan.GraphScan) (graphRequest, error) {
	var req graphRequest
	if raw := g.Request.Extensions[graphExtension]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return graphRequest{}, err
		}
	}

	req.PURLs = g.Request.PURLs
	if b := g.Build; b != nil {
		req.Build = &buildRef{
			Name:    b.Name,
			Number:  b.Number,
			Project: b.Project,
		}
	}

	if req.Root == "" && req.Build != nil {
		req = buildGraph(req.Build, g.Request)
	}

	return req, nil
}

func buildGraph(b *buildRef, r scan.GraphRequest) graphRequest {
	g := graphRequest{
		Root:      "build://" + b.Name + ":" + b.Number,
		IDsByPURL: map[string][]string{},
		Parents:   map[string][]string{},
		Binary:    true,
		Build:     b,
		Artifacts: r.Artifacts,
	}

	for _, p := range r.PURLs {
		if id, err := xraymap.FromPURL(p); err == nil {
			g.addWithPURL(id, p, g.Root)
		}
	}

	return g
}

func (h *Handler) postGraph(w http.ResponseWriter, r *http.Request) {
	var root Node
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGraphBytes)).
		Decode(&root); err != nil ||
		root.ComponentID == "" {
		writeError(w, http.StatusBadRequest, "invalid component graph")
		return
	}

	q := rawQuery(r)
	for _, name := range q["watch"] {
		if _, err := h.admin.Watch(r.Context(), name); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("watch '%s' does not exist", name))
			return
		}
	}

	repoPath, _, _ := strings.Cut(first(q["repo_path"]), "/")
	req := graphRequest{
		Root:      root.ComponentID,
		IDsByPURL: map[string][]string{},
		Parents:   map[string][]string{},
		Context: len(q["project"]) > 0 || len(q["repo_path"]) > 0 ||
			len(q["watch"]) > 0,
		Binary:  first(q["scan_type"]) == "binary",
		Watches: q["watch"],
		Repo:    repoPath,
	}

	var walk func(n Node, parent string, depth int)

	walk = func(n Node, parent string, depth int) {
		if depth > 64 {
			return
		}

		req.addComponent(n.ComponentID, parent)
		req.addLicenses(n.ComponentID, n.Licenses)

		if n.Path != "" {
			if req.Paths == nil {
				req.Paths = map[string]string{}
			}

			req.Paths[n.ComponentID] = n.Path
		}

		for _, c := range n.Nodes {
			walk(c, n.ComponentID, depth+1)
		}
	}
	walk(root, "", 0)

	id, err := h.queueID(r, req)
	if err != nil {
		h.log.ErrorContext(r.Context(), "queue graph scan", "error", err)
		writeError(w, http.StatusInternalServerError, "could not queue the scan")

		return
	}

	wire.WriteJSON(w, http.StatusCreated, map[string]string{
		"scan_id": id.String(),
	})
}

func (h *Handler) queue(r *http.Request, g graphRequest) error {
	_, err := h.queueID(r, g)
	return err
}

func (h *Handler) queueID(r *http.Request, g graphRequest) (uuid.UUID, error) {
	ext, err := json.Marshal(g)
	if err != nil {
		return uuid.UUID{}, err
	}

	req := scan.GraphRequest{
		PURLs:    g.PURLs,
		Licenses: g.Licenses,
		Extensions: map[string]json.RawMessage{
			graphExtension: ext,
		},
	}

	var build *scan.GraphBuild
	if b := g.Build; b != nil {
		build = &scan.GraphBuild{
			Name:    b.Name,
			Number:  b.Number,
			Project: b.Project,
		}
	}

	id, err := h.scans.QueueGraph(r.Context(), auth.FromContext(r.Context()).Name(), req, build)
	if err != nil {
		return uuid.UUID{}, err
	}

	return id, h.enqueue(r.Context(), id)
}

func (h *Handler) getGraph(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "scan not found")
		return
	}

	g, err := h.results.GraphScan(r.Context(), id)
	if errors.Is(err, scan.ErrNoScan) {
		writeError(w, http.StatusNotFound, "scan not found")
		return
	}

	if err != nil {
		h.log.ErrorContext(r.Context(), "load graph scan", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	if g.Username != auth.FromContext(r.Context()).Name() && !auth.FromContext(r.Context()).Admin {
		writeError(w, http.StatusNotFound, "scan not found")
		return
	}

	switch g.Status {
	case scan.StatusQueued:
		wire.WriteJSON(w, http.StatusAccepted, map[string]string{
			"scan_id": id.String(),
			"status":  "in_progress",
		})

		return
	case scan.StatusFailed:
		wire.WriteJSON(
			w,
			http.StatusOK,
			map[string]string{
				"scan_id": id.String(),
				"status":  "failed",
				"error":   g.Error,
			},
		)

		return
	}

	req, rerr := graphOf(g)

	var rep scan.Report
	if err := errors.Join(rerr, json.Unmarshal(g.Result, &rep)); err != nil {
		writeError(w, http.StatusInternalServerError, "corrupt scan record")
		return
	}

	q := rawQuery(r)

	res := buildResult(id.String(), req, rep, true, first(q["include_licenses"]) == "true")
	if req.Context {
		var kind policy.ScopeKind
		if req.Repo != "" {
			kind = policy.ScopeRepository
		}

		watches, pols, err := h.watchesFor(r.Context(), req.Watches, kind, req.Repo)
		if err != nil {
			h.internal(w, r, err)
			return
		}

		rules, err := h.loadIgnoreRules(r.Context())
		if err != nil {
			h.internal(w, r, err)
			return
		}

		viol, _ := violations(watches, pols, rules, res.Vulnerabilities, nil)
		res.Violations = viol
	}

	if first(q["include_vulnerabilities"]) != "true" {
		res.Vulnerabilities = nil
	}

	wire.WriteJSON(w, http.StatusOK, res)
}

// Result is the graph scan result.
type Result struct {
	ScanID          string          `json:"scan_id"`
	ComponentID     string          `json:"component_id"`
	PackageType     string          `json:"package_type"`
	Status          string          `json:"status"`
	XrayDataURL     string          `json:"xray_data_url"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities,omitempty"`
	Violations      []Violation     `json:"violations,omitempty"`
	Licenses        []License       `json:"licenses,omitempty"`
}

// License is one license with the components that carry it.
type License struct {
	LicenseKey string                      `json:"license_key"`
	Name       string                      `json:"name"`
	Custom     bool                        `json:"custom"`
	References []string                    `json:"references"`
	Components map[string]LicenseComponent `json:"components"`
}

// LicenseComponent is a component carrying a license.
type LicenseComponent struct {
	ImpactPaths [][]ImpactNode `json:"impact_paths"`
}

const unknownLicense = "Unknown"

// Vulnerability is one issue with the components it affects.
type Vulnerability struct {
	IssueID    string               `json:"issue_id"`
	Summary    string               `json:"summary"`
	Severity   string               `json:"severity"`
	CVEs       []CVE                `json:"cves,omitempty"`
	Components map[string]Component `json:"components"`
	References []string             `json:"references"`
	Provider   string               `json:"provider"`

	vulnID string
}

// CVE is a CVE with its CVSS scores, as strings.
type CVE struct {
	CVE          string `json:"cve"`
	CVSSV2Score  string `json:"cvss_v2_score,omitempty"`
	CVSSV2Vector string `json:"cvss_v2_vector,omitempty"`
	CVSSV3Score  string `json:"cvss_v3_score,omitempty"`
	CVSSV3Vector string `json:"cvss_v3_vector,omitempty"`
}

// Component is an affected component.
type Component struct {
	FixedVersions []string       `json:"fixed_versions"`
	ImpactPaths   [][]ImpactNode `json:"impact_paths"`
}

// ImpactNode is one step of an impact path.
type ImpactNode struct {
	ComponentID string `json:"component_id"`
	FullPath    string `json:"full_path,omitempty"`
}

// IssueID is Relic's stable issue ID for a vulnerability ID.
func IssueID(vulnID string) string {
	sum := sha256.Sum256([]byte(vulnID))
	return "RELIC-" + strings.ToUpper(hex.EncodeToString(sum[:6]))
}

func buildResult(id string, req graphRequest, rep scan.Report, vulns, licenses bool) Result {
	res := Result{
		ScanID:      id,
		ComponentID: req.Root,
		PackageType: packageType(req.Root),
		Status:      "completed",
	}
	if licenses {
		res.Licenses = wireLicenses(req, rep.Components)
	}

	if !vulns {
		return res
	}

	byID := map[string]*Vulnerability{}

	var order []string

	for _, f := range rep.Findings {
		ids := req.IDsByPURL[f.Package.PURL]
		if len(ids) == 0 {
			continue
		}

		v, ok := byID[f.ID]
		if !ok {
			v = &Vulnerability{
				IssueID:    IssueID(f.ID),
				Summary:    f.Summary,
				Severity:   xraymap.Severity(string(f.Severity)),
				CVEs:       cves(f),
				Components: map[string]Component{},
				References: nonNil(f.URLs),
				Provider:   "Relic Scan (" + f.ID + ")",
				vulnID:     f.ID,
			}
			byID[f.ID] = v
			order = append(order, f.ID)
		}

		for _, cid := range ids {
			v.Components[cid] = Component{
				FixedVersions: fixedVersions(f.FixedVersions),
				ImpactPaths:   impactPaths(req, cid),
			}
		}
	}

	slices.Sort(order)

	res.Vulnerabilities = []Vulnerability{}
	for _, vid := range order {
		res.Vulnerabilities = append(res.Vulnerabilities, *byID[vid])
	}

	return res
}

func wireLicenses(req graphRequest, comps []scan.Component) []License {
	byKey := map[string]*License{}

	for _, c := range comps {
		ids := req.IDsByPURL[c.PURL]
		if len(ids) == 0 {
			continue
		}

		keys := c.LicenseIDs
		if len(keys) == 0 {
			keys = []string{
				unknownLicense,
			}
		}

		for _, key := range keys {
			l, ok := byKey[key]
			if !ok {
				l = &License{
					LicenseKey: key,
					Name:       key,
					References: []string{},
					Components: map[string]LicenseComponent{},
				}
				byKey[key] = l
			}

			for _, cid := range ids {
				l.Components[cid] = LicenseComponent{
					ImpactPaths: impactPaths(req, cid),
				}
			}
		}
	}

	out := make([]License, 0, len(byKey))
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		out = append(out, *byKey[key])
	}

	return out
}

func cves(f scan.Finding) []CVE {
	shared := cveScores(f.CVSS)

	var out []CVE

	for _, id := range f.CVEs() {
		c := shared
		if own, ok := f.AliasCVSS[id]; ok {
			c = cveScores(own)
		}

		c.CVE = id
		out = append(out, c)
	}

	if len(out) == 0 && shared.CVSSV3Score != "" {
		out = append(out, CVE{
			CVSSV3Score:  shared.CVSSV3Score,
			CVSSV3Vector: shared.CVSSV3Vector,
		})
	}

	return out
}

func cveScores(scores []scan.CVSS) CVE {
	var c CVE

	for _, s := range scores {
		score := strconv.FormatFloat(s.Score, 'f', 1, 64)
		switch {
		case strings.HasPrefix(s.Version, "3") && c.CVSSV3Score == "":
			c.CVSSV3Score, c.CVSSV3Vector = score, s.Vector
		case strings.HasPrefix(s.Version, "2") && c.CVSSV2Score == "":
			c.CVSSV2Score, c.CVSSV2Vector = score, s.Vector
		}
	}

	return c
}

func fixedVersions(vs []string) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, "["+v+"]")
	}

	return out
}

func impactPaths(req graphRequest, id string) [][]ImpactNode {
	const (
		maxPaths = 20
		maxSteps = 10_000
	)

	var (
		out   [][]ImpactNode
		steps int
		walk  func(node string, path []ImpactNode, depth int)
	)

	walk = func(node string, path []ImpactNode, depth int) {
		if len(out) >= maxPaths || depth > 64 || steps >= maxSteps {
			return
		}

		steps++

		path = append([]ImpactNode{
			{
				ComponentID: node,
				FullPath:    req.Paths[node],
			},
		}, path...)

		parents := req.Parents[node]
		if len(parents) == 0 {
			if node != req.Root {
				path = append([]ImpactNode{
					{
						ComponentID: req.Root,
						FullPath:    req.Paths[req.Root],
					},
				}, path...)
			}

			out = append(out, path)

			return
		}

		for _, p := range parents {
			if !slices.ContainsFunc(path, func(n ImpactNode) bool {
				return n.ComponentID == p
			}) {
				walk(p, slices.Clone(path), depth+1)
			}
		}
	}
	walk(id, nil, 0)

	return out
}

func packageType(root string) string {
	scheme, _, ok := strings.Cut(root, "://")
	if !ok {
		return "generic"
	}

	switch scheme {
	case "gav":
		return "maven"
	case "rubygems":
		return "gem"
	}

	return scheme
}

func rawQuery(r *http.Request) map[string][]string {
	out := map[string][]string{}

	for pair := range strings.SplitSeq(r.URL.RawQuery, "&") {
		k, v, _ := strings.Cut(pair, "=")
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}

		out[k] = append(out[k], v)
	}

	return out
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}

	return v[0]
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

func writeError(w http.ResponseWriter, status int, msg string) {
	wire.WriteJSON(w, status, map[string]string{
		"error": msg,
	})
}
