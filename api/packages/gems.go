package packages

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/gems"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

const maxGemBytes = 512 << 20

func (h *Handler) gemsAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	repoKey, rest, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != gems.Format {
		httperror.Write(w, r, http.StatusNotFound, "repository not found")
		return
	}

	switch {
	case r.Method == http.MethodPost && rest == "api/v1/gems":
		h.gemPush(w, r, rp)
		return
	case r.Method == http.MethodDelete && rest == "api/v1/gems/yank":
		h.gemYank(w, r, rp)
		return
	case r.Method == http.MethodGet && (rest == "api/v1/api_key" || rest == "api/v1/api_key.yaml"):
		h.gemAPIKey(w, r, rest)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httperror.Write(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"",
		auth.ActionRead,
	}) {
		return
	}

	switch {
	case rest == "":
		writeBody(w, r, "text/plain; charset=utf-8", []byte("RubyGems repository "+rp.Name+"\n"))
	case strings.HasPrefix(rest, "gems/") && strings.HasSuffix(rest, ".gem"):
		item, err := h.gems.Gem(r.Context(), rp, strings.TrimPrefix(rest, "gems/"))
		if h.gemsFail(w, r, err) || !h.allowDownload(w, r, item) {
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

		if r.Method != http.MethodHead {
			h.dockerStream(w, r, item)
		}
	case rest == "versions":
		locals, err := h.gems.Locals(r.Context(), rp)
		if h.gemsFail(w, r, err) {
			return
		}

		infos := map[string][]byte{}
		for _, spec := range locals {
			if _, ok := infos[spec.Name]; !ok {
				infos[spec.Name], _ = h.gems.Info(r.Context(), rp, spec.Name, locals)
			}
		}

		body, err := h.gems.Versions(r.Context(), rp, locals, infos)
		if h.gemsFail(w, r, err) {
			return
		}

		writeBody(w, r, "text/plain; charset=utf-8", body)
	case strings.HasPrefix(rest, "info/"):
		name := strings.TrimPrefix(rest, "info/")

		locals, err := h.gems.Locals(r.Context(), rp)
		if h.gemsFail(w, r, err) {
			return
		}

		info, err := h.gems.Info(r.Context(), rp, name, locals)
		if h.gemsFail(w, r, err) {
			return
		}

		writeBody(w, r, "text/plain; charset=utf-8", info)
	case rest == "specs.4.8.gz" || rest == "latest_specs.4.8.gz" || rest == "prerelease_specs.4.8.gz":
		locals, err := h.gems.Locals(r.Context(), rp)
		if h.gemsFail(w, r, err) {
			return
		}

		upstream, ok, err := h.gems.RemoteSpecsIndex(r.Context(), rp, rest)
		if h.gemsFail(w, r, err) {
			return
		}

		body, err := gems.SpecsIndex(selectSpecs(locals, rest))
		if ok {
			body, err = gems.SpecsIndexAfter(upstream, selectSpecs(locals, rest))
		}

		if h.gemsFail(w, r, err) {
			return
		}

		writeBody(w, r, "application/octet-stream", body)
	case strings.HasPrefix(rest, "quick/Marshal.4.8/") && strings.HasSuffix(rest, ".gemspec.rz"):
		h.gemQuickSpec(w, r, rp, strings.TrimPrefix(rest, "quick/Marshal.4.8/"))
	case rest == "api/v1/dependencies":
		locals, err := h.gems.Locals(r.Context(), rp)
		if h.gemsFail(w, r, err) {
			return
		}

		names := strings.Split(r.URL.Query().Get("gems"), ",")
		writeBody(w, r, "application/octet-stream", gems.Dependencies(locals, names))
	default:
		http.NotFound(w, r)
	}
}

func selectSpecs(specs []gems.Spec, index string) []gems.Spec {
	var out []gems.Spec

	switch index {
	case "prerelease_specs.4.8.gz":
		for _, s := range specs {
			if s.Prerelease() {
				out = append(out, s)
			}
		}
	case "latest_specs.4.8.gz":
		latest := map[string]gems.Spec{}

		var order []string

		for _, s := range specs {
			if s.Prerelease() {
				continue
			}

			key := s.Name + "\x00" + s.Platform

			cur, ok := latest[key]
			if !ok {
				order = append(order, key)
			}

			if !ok || versionLess(cur.Version, s.Version) {
				latest[key] = s
			}
		}

		for _, k := range order {
			out = append(out, latest[k])
		}
	default:
		for _, s := range specs {
			if !s.Prerelease() {
				out = append(out, s)
			}
		}
	}

	return out
}

func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])

		bi, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil && ai != bi:
			return ai < bi
		case (aerr != nil || berr != nil) && as[i] != bs[i]:
			return as[i] < bs[i]
		}
	}

	return len(as) < len(bs)
}

func (h *Handler) gemQuickSpec(w http.ResponseWriter, r *http.Request, rp repository.Repository, file string) {
	full := strings.TrimSuffix(file, ".gemspec.rz")

	locals, err := h.gems.Locals(r.Context(), rp)
	if h.gemsFail(w, r, err) {
		return
	}

	for _, s := range locals {
		if s.FullName() == full {
			body, err := gems.QuickSpec(s)
			if h.gemsFail(w, r, err) {
				return
			}

			writeBody(w, r, "application/octet-stream", body)

			return
		}
	}

	body, err := h.gems.RemoteQuickSpec(r.Context(), rp, file)
	if h.gemsFail(w, r, err) {
		return
	}

	writeBody(w, r, "application/octet-stream", body)
}

func (h *Handler) gemPush(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		target,
		"gems",
		auth.ActionWrite,
	}) {
		return
	}

	release := reserveBuffer(w, r, maxGemBytes, 1)
	if release == nil {
		return
	}
	defer release()

	body, err := io.ReadAll(io.LimitReader(r.Body, maxGemBytes+1))
	if err != nil || len(body) > maxGemBytes {
		httperror.Write(w, r, http.StatusBadRequest, "gem unreadable or too large")
		return
	}

	spec, err := h.gems.Push(r.Context(), target, body, auth.FromContext(r.Context()).Name())
	switch {
	case errors.Is(err, gems.ErrInvalid):
		httperror.Write(w, r, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, gems.ErrExists):
		httperror.Write(w, r, http.StatusConflict, "Repushing of gem versions is not allowed.")
	case err != nil:
		h.writeError(w, r, err)
	default:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "Successfully registered gem: "+spec.FullName()) //nolint:gosec // plain text
	}
}

func writeBody(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))

	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:gosec // index files served with their own content type
	}
}

func (h *Handler) gemsFail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, gems.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		http.NotFound(w, r)
	default:
		h.writeError(w, r, err)
	}

	return true
}

func (h *Handler) gemAPIKey(w http.ResponseWriter, r *http.Request, rest string) {
	p := auth.FromContext(r.Context())
	if p.Anonymous() {
		auth.Unauthorized(w, r)
		return
	}

	tok, err := h.tokens.Issue(r.Context(), p.LimitIssue(auth.IssueParams{
		Username:    p.Username,
		Scope:       auth.UserScope,
		ExpiresIn:   gemKeyTTL,
		Description: "gem api key",
	}, time.Now()))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	key := "Bearer " + tok.AccessToken
	if rest == "api/v1/api_key.yaml" {
		writeBody(w, r, "application/x-yaml", []byte("---\n:rubygems_api_key: "+key+"\n"))
		return
	}

	writeBody(w, r, "text/plain; charset=utf-8", []byte(key))
}

const gemKeyTTL = 365 * 24 * time.Hour

func (h *Handler) gemYank(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	// gem sends the form in the DELETE body, which ParseForm ignores.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		httperror.Write(w, r, http.StatusBadRequest, "invalid yank request")
		return
	}

	form, err := url.ParseQuery(string(raw))
	if err != nil {
		httperror.Write(w, r, http.StatusBadRequest, "invalid yank request")
		return
	}

	for k, v := range r.URL.Query() {
		if form.Get(k) == "" {
			form[k] = v
		}
	}

	name, version, platform := form.Get("gem_name"), form.Get("version"), form.Get("platform")
	if name == "" || version == "" || strings.ContainsAny(name+version+platform, "/\\") {
		httperror.Write(w, r, http.StatusBadRequest, "gem_name and version are required")
		return
	}

	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	file := name + "-" + version
	if platform != "" && platform != "ruby" {
		file += "-" + platform
	}

	p := "gems/" + file + ".gem"
	if !h.RequireRepo(w, r, RepoCheck{
		target,
		p,
		auth.ActionDelete,
	}) {
		return
	}

	if err := h.repos.DeleteToTrash(r.Context(), target, p, auth.FromContext(r.Context()).Name()); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeBody(w, r, "text/plain; charset=utf-8", []byte("Successfully deleted gem: "+file))
}

// GeneratedFile returns an index file other repository managers store in
// a repository and Relic computes on request.
func (h *Handler) GeneratedFile(r *http.Request, repoKey, p string) (repository.Item, []byte, bool) {
	name, ok := strings.CutPrefix(p, "info/")
	if !ok || h.gems == nil || name == "" || strings.Contains(name, "/") {
		return repository.Item{}, nil, false
	}

	ctx := r.Context()

	rp, err := h.repos.GetRepository(ctx, repoKey)
	if err != nil || rp.Class != "local" || rp.Format != gems.Format {
		return repository.Item{}, nil, false
	}

	locals, err := h.gems.Locals(ctx, rp)
	if err != nil {
		return repository.Item{}, nil, false
	}

	body, err := h.gems.Info(ctx, rp, name, locals)
	if err != nil {
		return repository.Item{}, nil, false
	}

	now := time.Now()

	return repository.Item{
		Repository: repoKey,
		Parent:     "info",
		Name:       name,
		Type:       repository.TypeFile,
		Size:       int64(len(body)),
		Checksums:  repository.ChecksumsOf(body),
		Created:    now,
		Modified:   now,
		Updated:    now,
	}, body, true
}
