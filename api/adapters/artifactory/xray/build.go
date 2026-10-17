package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"relic/api/adapters/artifactory/wire"
	"relic/api/adapters/artifactory/xray/xraymap"
	"relic/internal/access/auth"
	"relic/internal/delivery/buildinfo"
	"relic/internal/scanning/scan"
)

// NoFailBuildPolicy is the answer for builds that no watch with a
// "fail build" rule covers.
const NoFailBuildPolicy = "No Xray “Fail build in case of a violation” policy rule has been defined on this build."

type buildRef struct {
	Name    string `json:"name"`
	Number  string `json:"number"`
	Project string `json:"project"`
}

type buildScanRequest struct {
	BuildName   string `json:"build_name"`
	BuildNumber string `json:"build_number"`
	Project     string `json:"project"`
	Rescan      bool   `json:"rescan"`
}

var modulePrefixes = map[string]string{
	"npm":    "npm",
	"maven":  "gav",
	"gradle": "gav",
	"go":     "go",
	"golang": "go",
	"python": "pypi",
	"pypi":   "pypi",
	"pip":    "pypi",
	"nuget":  "nuget",
	"dotnet": "nuget",
}

func (h *Handler) readBuildRequest(w http.ResponseWriter, r *http.Request) (buildScanRequest, bool) {
	var req buildScanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).
		Decode(&req); err != nil || req.BuildName == "" ||
		req.BuildNumber == "" {
		writeError(w, http.StatusBadRequest, "build_name and build_number are required")
		return req, false
	}

	if req.Project == "" {
		req.Project = first(rawQuery(r)["projectKey"])
	}

	return req, true
}

func (h *Handler) triggerBuild(w http.ResponseWriter, r *http.Request) {
	req, ok := h.readBuildRequest(w, r)
	if !ok || !h.requireBuildRead(w, r, req.Project, req.BuildName) {
		return
	}

	run, err := h.builds.Run(r.Context(), req.Project, req.BuildName, req.BuildNumber)
	if errors.Is(err, buildinfo.ErrNotFound) {
		wire.WriteJSON(w, http.StatusNotFound, map[string]string{
			"info": fmt.Sprintf("Build %s number %s was not found", req.BuildName, req.BuildNumber),
		})

		return
	}

	if err != nil {
		h.log.ErrorContext(r.Context(), "load build for scan", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	bi := run.Document
	root := "build://" + req.BuildName + ":" + req.BuildNumber
	g := graphRequest{
		Root:      root,
		IDsByPURL: map[string][]string{},
		Parents:   map[string][]string{},
		Binary:    true,
		Build: &buildRef{
			Name:    req.BuildName,
			Number:  req.BuildNumber,
			Project: req.Project,
		},
	}

	for _, m := range bi.Modules {
		prefix := modulePrefixes[strings.ToLower(m.Type)]
		for _, d := range m.Dependencies {
			if prefix == "" || d.ID == "" {
				continue
			}

			id := prefix + "://" + d.ID
			g.addComponent(id, root)
		}

		for _, a := range m.Artifacts {
			sha, err := h.artifactSHA256(r, a)
			if err != nil {
				h.internal(w, r, err)
				return
			}

			if sha != "" {
				g.Artifacts = append(g.Artifacts, sha)
			}
		}
	}

	if err := h.queue(r, g); err != nil {
		h.log.ErrorContext(r.Context(), "queue build scan", "error", err)
		writeError(w, http.StatusInternalServerError, "could not queue the scan")

		return
	}

	watches, pols, err := h.watchesFor(r.Context(), nil, "build", req.BuildName)
	if err != nil {
		h.internal(w, r, err)
		return
	}

	if !hasFailBuildRule(watches, pols) {
		wire.WriteJSON(w, http.StatusOK, map[string]string{
			"info": NoFailBuildPolicy,
		})

		return
	}

	wire.WriteJSON(
		w,
		http.StatusOK,
		map[string]string{
			"info": fmt.Sprintf("Scanning build %s number %s.", req.BuildName, req.BuildNumber),
		},
	)
}

func (h *Handler) artifactSHA256(r *http.Request, a buildinfo.Artifact) (string, error) {
	if a.SHA256 != "" {
		return strings.ToLower(a.SHA256), nil
	}

	if a.SHA1 == "" {
		return "", nil
	}

	return h.repos.SHA256OfSHA1(r.Context(), a.SHA1)
}

func (h *Handler) buildScanResult(w http.ResponseWriter, r *http.Request) {
	req, ok := h.readBuildRequest(w, r)
	if !ok || !h.requireBuildRead(w, r, req.Project, req.BuildName) {
		return
	}

	g, err := h.results.LatestBuildGraphScan(
		r.Context(),
		scan.GraphBuild{
			Name:    req.BuildName,
			Number:  req.BuildNumber,
			Project: req.Project,
		},
	)
	if errors.Is(err, scan.ErrNoScan) {
		writeError(
			w,
			http.StatusNotFound,
			fmt.Sprintf("no scan of build %s number %s was triggered", req.BuildName, req.BuildNumber),
		)

		return
	}

	if err != nil {
		h.log.ErrorContext(r.Context(), "load build scan", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	resp := map[string]any{
		"build_name":   req.BuildName,
		"build_number": req.BuildNumber,
		"fail_build":   false,
		"info":         "",
		"more_details_url": baseURL(r) + "/builds/" + url.PathEscape(req.BuildName) + "/" +
			url.PathEscape(req.BuildNumber),
		"violations": []any{},
	}

	switch g.Status {
	case scan.StatusQueued:
		wire.WriteJSON(w, http.StatusAccepted, map[string]string{
			"status": "in_progress",
		})

		return
	case scan.StatusFailed:
		resp["status"] = "failed"
		wire.WriteJSON(w, http.StatusOK, resp)

		return
	}

	gr, rerr := graphOf(g)

	var rep scan.Report
	if err := errors.Join(rerr, json.Unmarshal(g.Result, &rep)); err != nil {
		writeError(w, http.StatusInternalServerError, "corrupt scan record")
		return
	}

	stored, err := h.results.FindingsFor(r.Context(), gr.Artifacts)
	if err != nil {
		h.log.ErrorContext(r.Context(), "load artifact findings", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	for _, f := range stored {
		id, err := xraymap.FromPURL(f.Package.PURL)
		if err != nil {
			continue
		}

		gr.addWithPURL(id, f.Package.PURL, gr.Root)
		rep.Findings = append(rep.Findings, f.Finding)
	}

	resp["status"] = "completed"
	vulns := buildResult("", gr, rep, true, false).Vulnerabilities

	watches, pols, err := h.watchesFor(r.Context(), nil, "build", req.BuildName)
	if err != nil {
		h.internal(w, r, err)
		return
	}

	rules, err := h.loadIgnoreRules(r.Context())
	if err != nil {
		h.internal(w, r, err)
		return
	}

	found, failBuild := violations(
		watches,
		pols,
		rules,
		vulns,
		&buildRef{
			Name:   req.BuildName,
			Number: req.BuildNumber,
		},
	)
	resp["violations"], resp["fail_build"] = found, failBuild

	if failBuild {
		h.recordBuildDecisions(r.Context(), req.BuildName+"/"+req.BuildNumber, found)
	}

	if first(rawQuery(r)["include_vulnerabilities"]) == "true" {
		resp["vulnerabilities"] = vulns
	}

	wire.WriteJSON(w, http.StatusOK, resp)
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}

	return scheme + "://" + r.Host
}

func (g *graphRequest) addComponent(id, parent string) {
	p, err := xraymap.ToPURL(id)
	if err != nil {
		p = ""
	}

	g.addWithPURL(id, p, parent)
}

func (g *graphRequest) addWithPURL(id, p, parent string) {
	if parent != "" && !contains(g.Parents[id], parent) {
		g.Parents[id] = append(g.Parents[id], parent)
	}

	if p != "" && !contains(g.IDsByPURL[p], id) {
		if len(g.IDsByPURL[p]) == 0 {
			g.PURLs = append(g.PURLs, p)
		}

		g.IDsByPURL[p] = append(g.IDsByPURL[p], id)
	}
}

func (g *graphRequest) addLicenses(id string, licenses []string) {
	p, err := xraymap.ToPURL(id)
	if err != nil || len(licenses) == 0 {
		return
	}

	if g.Licenses == nil {
		g.Licenses = map[string][]string{}
	}

	for _, l := range licenses {
		if !contains(g.Licenses[p], l) {
			g.Licenses[p] = append(g.Licenses[p], l)
		}
	}
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}

func (h *Handler) requireBuildRead(w http.ResponseWriter, r *http.Request, proj, name string) bool {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return true
	}

	if h.Permissions != nil {
		perms, err := h.Permissions(r.Context())
		if err != nil {
			h.log.ErrorContext(r.Context(), "load permissions", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")

			return false
		}

		if perms.CanBuild(p.Subject(), h.builds.Repository(proj), name, auth.ActionRead) {
			return true
		}
	}

	writeError(w, http.StatusForbidden, "no permission to read build "+name)

	return false
}
