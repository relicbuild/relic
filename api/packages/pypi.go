package packages

import (
	"cmp"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

const maxUploadField = 1 << 20

const maxUploadFields = 256

func (h *Handler) pypiAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	repoKey, rest, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != pypi.Format {
		httperror.Write(w, r, http.StatusNotFound, "repository not found")
		return
	}

	switch {
	case r.Method == http.MethodPost && (rest == "" || rest == "legacy/" || rest == "legacy"):
		h.pypiUpload(w, r, rp)
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		httperror.Write(w, r, http.StatusMethodNotAllowed, "method not allowed")
	case rest == "simple" || rest == "simple/":
		h.pypiRoot(w, r, rp)
	case strings.HasPrefix(rest, "simple/"):
		h.pypiProject(w, r, rp, strings.Trim(strings.TrimPrefix(rest, "simple/"), "/"), "../../")
	case strings.HasPrefix(rest, "packages/"):
		h.pypiPackage(w, r, rp, strings.TrimPrefix(rest, "packages/"))
	case strings.HasPrefix(rest, "pypi/") && strings.HasSuffix(rest, "/json"):
		name, version, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(rest, "pypi/"), "/json"), "/")
		h.pypiJSON(w, r, rp, name, version)
	case strings.Count(strings.TrimSuffix(rest, "/"), "/") == 0 && rest != "":
		// Poetry configured with <mount>/<repo> without /simple asks for
		// <repo>/<name>/.
		h.pypiProject(w, r, rp, strings.TrimSuffix(rest, "/"), "../")
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) pypiRoot(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"",
		auth.ActionRead,
	}) {
		return
	}

	names, err := h.pypi.Projects(r.Context(), rp)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><head><title>Simple index</title></head><body>\n")

	for _, n := range names {
		fmt.Fprintf(&b, "<a href=\"%s/\">%s</a><br/>\n", html.EscapeString(n), html.EscapeString(n))
	}

	b.WriteString("</body></html>\n")
	writeHTML(w, b.String())
}

func (h *Handler) pypiProject(w http.ResponseWriter, r *http.Request, rp repository.Repository, name, up string) {
	if !pypi.ValidName(name) {
		http.NotFound(w, r)
		return
	}

	if norm := pypi.Normalize(name); norm != name {
		//nolint:gosec // relative; norm is [a-z0-9-] after ValidName
		http.Redirect(w, r, "../"+norm+"/", http.StatusMovedPermanently)

		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		name,
		auth.ActionRead,
	}) {
		return
	}

	links, err := h.pypi.Links(r.Context(), rp, name)
	if errors.Is(err, pypi.ErrNotFound) {
		http.NotFound(w, r)
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	var b strings.Builder
	fmt.Fprintf(
		&b,
		"<!DOCTYPE html>\n<html><head><title>Links for %s</title></head><body>\n<h1>Links for %s</h1>\n",
		html.EscapeString(name),
		html.EscapeString(name),
	)

	for _, l := range links {
		href := l.Href
		if strings.HasPrefix(href, "packages/") {
			href = up + href
		}

		if l.SHA256 != "" && !strings.Contains(href, "#") {
			href += "#sha256=" + l.SHA256
		}

		attrs := ""
		if l.RequiresPython != "" {
			attrs = fmt.Sprintf(" data-requires-python=\"%s\"", html.EscapeString(l.RequiresPython))
		}

		fmt.Fprintf(&b, "<a href=\"%s\"%s>%s</a><br/>\n", html.EscapeString(href), attrs, html.EscapeString(l.File))
	}

	b.WriteString("</body></html>\n")
	writeHTML(w, b.String())
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, body) //nolint:gosec // every interpolated value is HTML-escaped
}

func (h *Handler) pypiPackage(w http.ResponseWriter, r *http.Request, rp repository.Repository, p string) {
	clean, err := repository.CleanPath(p)
	if err != nil || clean == "" {
		http.NotFound(w, r)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		clean,
		auth.ActionRead,
	}) {
		return
	}

	item, err := h.pypi.Package(r.Context(), rp, clean)
	if errors.Is(err, pypi.ErrNotFound) {
		http.NotFound(w, r)
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.allowDownload(w, r, item) {
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))
	w.Header().Set("X-Checksum-Sha256", item.Checksums.SHA256)

	if r.Method == http.MethodHead {
		return
	}

	h.dockerStream(w, r, item)
}

func (h *Handler) pypiUpload(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		httperror.Write(w, r, http.StatusBadRequest, "expected a multipart upload")
		return
	}

	fields := map[string]string{}
	nfields := 0

	var (
		content  *os.File
		fileName string
	)

	defer func() {
		if content != nil {
			_ = content.Close()
			_ = os.Remove(content.Name())
		}
	}()

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			httperror.Write(w, r, http.StatusBadRequest, "malformed upload")
			return
		}

		if part.FormName() != "content" {
			if nfields++; nfields > maxUploadFields {
				httperror.Write(w, r, http.StatusBadRequest, "too many form fields")
				return
			}

			v, _ := io.ReadAll(io.LimitReader(part, maxUploadField))
			fields[part.FormName()] = string(v)

			continue
		}

		if content != nil {
			httperror.Write(w, r, http.StatusBadRequest, "more than one file in the upload")
			return
		}

		fileName = part.FileName()

		if !h.RequireRepo(w, r, RepoCheck{
			target,
			pypiFilePath(fields, fileName),
			auth.ActionWrite,
		}) {
			return
		}

		content, err = os.CreateTemp("", "relic-pypi-")
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		if _, err := io.Copy(content, part); err != nil {
			if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
				httperror.Write(w, r, http.StatusRequestEntityTooLarge, "upload too large")
				return
			}

			httperror.Write(w, r, http.StatusBadRequest, "malformed upload")

			return
		}
	}

	if content == nil {
		httperror.Write(w, r, http.StatusBadRequest, "missing content")
		return
	}

	if a := fields[":action"]; a != "" && a != "file_upload" {
		httperror.Write(w, r, http.StatusBadRequest, "unsupported action")
		return
	}

	if _, err := content.Seek(0, io.SeekStart); err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.pypiStore(w, r, target, fields, fileName, content) {
		return
	}

	w.WriteHeader(http.StatusOK)
}

func pypiFilePath(fields map[string]string, fileName string) string {
	name, version := fields["name"], fields["version"]
	if name == "" || version == "" {
		n, v, _ := pypi.ParseFilename(fileName)
		name, version = cmp.Or(name, n), cmp.Or(version, v)
	}

	return pypi.Normalize(name) + "/" + version + "/" + fileName
}

func (h *Handler) pypiStore(
	w http.ResponseWriter,
	r *http.Request,
	target string,
	f map[string]string,
	fileName string,
	body io.Reader,
) bool {
	u := pypi.Upload{
		Repository: target,
		User:       auth.FromContext(r.Context()).Name(),
		Name:       f["name"],
		Version:    f["version"],
		File:       fileName,
		Summary:    f["summary"],
		RequiresPy: f["requires_python"],
		SHA256:     f["sha256_digest"],
		MD5:        f["md5_digest"],
		Body:       body,
	}

	p := pypiFilePath(f, fileName)
	if !h.RequireRepo(w, r, RepoCheck{
		target,
		p,
		auth.ActionWrite,
	}) {
		return false
	}
	// An existing file is a 409, which twine --skip-existing treats as already uploaded.
	if _, err := h.repos.Stat(r.Context(), target, p); err == nil {
		canOverwrite, err := h.CanRepo(r, target, p, auth.ActionDelete)
		if err != nil {
			h.writeError(w, r, err)
			return false
		}

		if !canOverwrite {
			httperror.Write(w, r, http.StatusConflict, "File already exists: "+u.File)
			return false
		}
	}

	if _, err := h.pypi.Store(r.Context(), u); err != nil {
		if errors.Is(err, pypi.ErrInvalid) {
			httperror.Write(w, r, http.StatusBadRequest, err.Error())
		} else {
			h.writeError(w, r, err)
		}

		return false
	}

	return true
}

type pypiFile struct {
	Filename       string            `json:"filename"`
	URL            string            `json:"url"`
	Digests        map[string]string `json:"digests"`
	PackageType    string            `json:"packagetype"`
	RequiresPython *string           `json:"requires_python"`
}

type pypiJSONDoc struct {
	Info struct {
		Name           string  `json:"name"`
		Version        string  `json:"version"`
		RequiresPython *string `json:"requires_python"`
	} `json:"info"`
	URLs     []pypiFile            `json:"urls"`
	Releases map[string][]pypiFile `json:"releases,omitempty"`
}

func (h *Handler) pypiJSON(w http.ResponseWriter, r *http.Request, rp repository.Repository, name, version string) {
	if !pypi.ValidName(name) || strings.Contains(version, "/") {
		http.NotFound(w, r)
		return
	}

	norm := pypi.Normalize(name)
	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		norm,
		auth.ActionRead,
	}) {
		return
	}

	links, err := h.pypi.Links(r.Context(), rp, norm)
	if errors.Is(err, pypi.ErrNotFound) {
		httperror.Write(w, r, http.StatusNotFound, "Not Found")
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	up := "../../"
	if version != "" {
		up = "../../../"
	}

	releases := map[string][]pypiFile{}
	latest := ""

	for _, l := range links {
		_, v, ok := pypi.ParseFilename(l.File)
		if !ok {
			continue
		}

		href := l.Href
		if strings.HasPrefix(href, "packages/") {
			href = up + href
		}

		href, _, _ = strings.Cut(href, "#")

		f := pypiFile{
			Filename:    l.File,
			URL:         href,
			Digests:     map[string]string{},
			PackageType: "sdist",
		}
		if strings.HasSuffix(l.File, ".whl") {
			f.PackageType = "bdist_wheel"
		}

		if l.SHA256 != "" {
			f.Digests["sha256"] = l.SHA256
		}

		if l.RequiresPython != "" {
			f.RequiresPython = &l.RequiresPython
		}

		releases[v] = append(releases[v], f)
		if latest == "" || preferredLatest(v, latest) {
			latest = v
		}
	}

	var doc pypiJSONDoc

	doc.Info.Name = norm
	if version == "" {
		doc.Info.Version, doc.URLs, doc.Releases = latest, releases[latest], releases
	} else {
		files, ok := releases[version]
		if !ok {
			httperror.Write(w, r, http.StatusNotFound, "Not Found")
			return
		}

		doc.Info.Version, doc.URLs = version, files
	}

	if doc.URLs == nil {
		doc.URLs = []pypiFile{}
	}

	if len(doc.URLs) > 0 {
		doc.Info.RequiresPython = doc.URLs[0].RequiresPython
	}

	writeJSON(w, http.StatusOK, doc)
}

var pep440Pattern = regexp.MustCompile(
	`^v?(?:(\d+)!)?(\d+(?:\.\d+)*)` +
		`(?:[-_.]?(a|alpha|b|beta|c|rc|pre|preview)[-_.]?(\d*))?` +
		`(?:-(\d+)|[-_.]?(post|rev|r)[-_.]?(\d*))?` +
		`(?:[-_.]?(dev)[-_.]?(\d*))?` +
		`(?:\+[a-z0-9]+(?:[-_.][a-z0-9]+)*)?$`,
)

const pep440Final = 3

type pep440Version struct {
	epoch     int
	release   []int
	phase     int
	preNumber int
	post      int
	dev       int
}

func (v pep440Version) final() bool {
	return v.phase == pep440Final && v.dev == math.MaxInt
}

func parsePEP440(text string) (pep440Version, bool) {
	m := pep440Pattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(text)))
	if m == nil {
		return pep440Version{}, false
	}

	number := func(s string) int {
		n, _ := strconv.Atoi(s)
		return n
	}

	v := pep440Version{
		epoch: number(m[1]),
		phase: pep440Final,
		post:  -1,
		dev:   math.MaxInt,
	}

	for part := range strings.SplitSeq(m[2], ".") {
		v.release = append(v.release, number(part))
	}

	for len(v.release) > 1 && v.release[len(v.release)-1] == 0 {
		v.release = v.release[:len(v.release)-1]
	}

	switch m[3] {
	case "a", "alpha":
		v.phase = 0
	case "b", "beta":
		v.phase = 1
	case "c", "rc", "pre", "preview":
		v.phase = 2
	}

	v.preNumber = number(m[4])

	switch {
	case m[5] != "":
		v.post = number(m[5])
	case m[6] != "":
		v.post = number(m[7])
	}

	if m[8] != "" {
		v.dev = number(m[9])
		if v.phase == pep440Final && v.post < 0 {
			v.phase = -1
		}
	}

	return v, true
}

func comparePEP440(a, b pep440Version) int {
	return cmp.Or(
		cmp.Compare(a.epoch, b.epoch),
		slices.Compare(a.release, b.release),
		cmp.Compare(a.phase, b.phase),
		cmp.Compare(a.preNumber, b.preNumber),
		cmp.Compare(a.post, b.post),
		cmp.Compare(a.dev, b.dev),
	)
}

func preferredLatest(candidate, current string) bool {
	cv, cok := parsePEP440(candidate)
	lv, lok := parsePEP440(current)

	switch {
	case cok != lok:
		return cok
	case !cok:
		return candidate > current
	case cv.final() != lv.final():
		return cv.final()
	}

	return comparePEP440(cv, lv) > 0
}
