package artifactory

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"relic/api/adapters/artifactory/buildjson"
	"relic/api/adapters/artifactory/wire"
	"relic/api/packages"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/buildinfo"
)

const maxBuildInfoBytes = 64 << 20

func project(r *http.Request) string {
	return queryValue(rawQuery(r.URL.RawQuery), "project")
}

func (h *Handler) publishBuild(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBuildInfoBytes))
	if err != nil {
		wire.WriteBodyError(w, err, "build info too large")
		return
	}

	var head struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.Name == "" {
		wire.WriteError(w, http.StatusBadRequest, "invalid build info")
		return
	}

	if !h.requireBuild(w, r, project(r), head.Name, auth.ActionWrite) {
		return
	}

	if err := h.builds.Publish(r.Context(), project(r), raw, auth.FromContext(r.Context()).Name()); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type buildsList struct {
	URI    string         `json:"uri"`
	Builds []buildSummary `json:"builds"`
}

type buildSummary struct {
	URI         string `json:"uri"`
	LastStarted string `json:"lastStarted"`
}

type buildRuns struct {
	URI           string     `json:"uri"`
	BuildsNumbers []buildRun `json:"buildsNumbers"`
}

type buildRun struct {
	URI     string `json:"uri"`
	Started string `json:"started"`
}

type publishedBuild struct {
	URI       string          `json:"uri"`
	BuildInfo json.RawMessage `json:"buildInfo"`
}

// Names may contain unescaped '/'.
func (h *Handler) getBuild(w http.ResponseWriter, r *http.Request) {
	ctx, proj := r.Context(), project(r)
	base := baseURL(r) + "/api/build"

	rest := strings.Trim(r.PathValue("rest"), "/")
	if rest == "" {
		all, err := h.builds.List(ctx, proj)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		p := auth.FromContext(ctx)

		perms, err := h.security.Permissions(ctx)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		out := buildsList{
			URI:    base,
			Builds: []buildSummary{},
		}

		for _, b := range all {
			if perms.CanBuild(p.Subject(), h.builds.Repository(proj), b.Name, auth.ActionRead) {
				out.Builds = append(
					out.Builds,
					buildSummary{
						URI:         "/" + escapePath(b.Name),
						LastStarted: b.LastStarted.Format(buildjson.TimeFormat),
					},
				)
			}
		}

		wire.WriteJSON(w, http.StatusOK, out)

		return
	}

	name, number, err := h.builds.Split(ctx, proj, rest)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.requireBuild(w, r, proj, name, auth.ActionRead) {
		return
	}

	if number == "" {
		runs, err := h.builds.Runs(ctx, proj, name)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		out := buildRuns{
			URI:           base + "/" + escapePath(name),
			BuildsNumbers: make([]buildRun, len(runs)),
		}
		for i, run := range runs {
			out.BuildsNumbers[i] = buildRun{
				URI:     "/" + run.Number,
				Started: run.Started.Format(buildjson.TimeFormat),
			}
		}

		wire.WriteJSON(w, http.StatusOK, out)

		return
	}

	run, err := h.builds.Run(ctx, proj, name, number)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	info, err := buildjson.View(run)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	wire.WriteJSON(
		w,
		http.StatusOK,
		publishedBuild{
			URI:       base + "/" + escapePath(name) + "/" + escapePath(number),
			BuildInfo: info,
		},
	)
}

func (h *Handler) promoteBuild(w http.ResponseWriter, r *http.Request) {
	var req buildjson.Promotion
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid promotion request: "+err.Error())
		return
	}

	if !h.requireBuild(w, r, project(r), r.PathValue("name"), auth.ActionWrite) {
		return
	}

	if req.TargetRepo != "" && !h.packages.RequireRepo(w, r, packages.RepoCheck{
		Repo:   req.TargetRepo,
		Path:   "",
		Action: auth.ActionWrite,
	}) {
		return
	}

	msgs, err := h.builds.Promote(
		r.Context(),
		project(r),
		r.PathValue("name"),
		r.PathValue("number"),
		req.Native(),
		auth.FromContext(r.Context()).Name(),
		h.itemCheck(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string][]buildjson.Message{
		"messages": buildjson.Messages(msgs),
	})
}

func (h *Handler) discardBuilds(w http.ResponseWriter, r *http.Request) {
	var body buildjson.Retention
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid retention request: "+err.Error())
		return
	}

	if !h.requireBuild(w, r, project(r), r.PathValue("name"), auth.ActionDelete) {
		return
	}

	req, err := body.Native()
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if err := h.builds.Discard(r.Context(), project(r), r.PathValue("name"), req, h.itemCheck(r)); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type deleteBuildsRequest struct {
	BuildName       string   `json:"buildName"`
	Project         string   `json:"project"`
	BuildNumbers    []string `json:"buildNumbers"`
	DeleteArtifacts bool     `json:"deleteArtifacts"`
	DeleteAll       bool     `json:"deleteAll"`
}

func (h *Handler) deleteBuilds(w http.ResponseWriter, r *http.Request) {
	var req deleteBuildsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.BuildName == "" {
		wire.WriteError(w, http.StatusBadRequest, "invalid delete request")
		return
	}

	if !h.requireBuild(w, r, req.Project, req.BuildName, auth.ActionDelete) {
		return
	}

	err := h.builds.Delete(
		r.Context(),
		req.Project,
		req.BuildName,
		req.BuildNumbers,
		req.DeleteAll,
		req.DeleteArtifacts,
		h.itemCheck(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type buildArtifact struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Name string `json:"name"`
}

func (h *Handler) buildArtifacts(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(r.PathValue("rest"), "/")

	i := strings.LastIndex(rest, "/")
	if i < 0 {
		wire.WriteError(w, http.StatusBadRequest, "expected <name>/<number>/<buildRepo>")
		return
	}

	proj := strings.TrimSuffix(rest[i+1:], "-build-info")
	if proj == "artifactory" {
		proj = ""
	}

	name, number, err := h.builds.Split(r.Context(), proj, rest[:i])
	if err == nil && number == "" {
		err = buildinfo.ErrNotFound
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.requireBuild(w, r, proj, name, auth.ActionRead) {
		return
	}

	items, err := h.builds.Artifacts(r.Context(), proj, name, number, h.itemCheck(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := make([]buildArtifact, len(items))
	for i, it := range items {
		out[i] = buildArtifact{
			Repo: it.Repository,
			Path: it.Parent,
			Name: it.Name,
		}
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func isBuildError(err error) (int, bool) {
	switch {
	case errors.Is(err, buildinfo.ErrNotFound):
		return http.StatusNotFound, true
	case errors.Is(err, buildinfo.ErrInvalid):
		return http.StatusBadRequest, true
	}

	return 0, false
}

func (h *Handler) requireBuild(w http.ResponseWriter, r *http.Request, proj, name string, a auth.Action) bool {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return true
	}

	perms, err := h.security.Permissions(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return false
	}

	if !perms.CanBuild(p.Subject(), h.builds.Repository(proj), name, a) {
		packages.Forbidden(w, r, string(a), "build "+name)
		return false
	}

	return true
}

// A permission lookup error denies.
func (h *Handler) itemCheck(r *http.Request) buildinfo.ItemCheck {
	return func(it repository.Item, a auth.Action) bool {
		ok, err := h.packages.CanRepo(r, it.Repository, it.Path(), a)
		return err == nil && ok
	}
}
