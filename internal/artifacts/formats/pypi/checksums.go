package pypi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
)

var (
	nameSeparators = regexp.MustCompile(`[-_.]+`)
	versionStart   = regexp.MustCompile(`-\d`)
)

// projectOf is the most likely normalized project name of a distribution
// file.
func projectOf(file string) string {
	return projectCandidates(file)[0]
}

func projectCandidates(file string) []string {
	normalized := func(name string) string {
		return strings.ToLower(nameSeparators.ReplaceAllString(name, "-"))
	}

	if strings.HasSuffix(file, ".whl") {
		name, _, _ := strings.Cut(file, "-")

		return []string{
			normalized(name),
		}
	}

	var out []string
	for _, loc := range versionStart.FindAllStringIndex(file, -1) {
		out = append(out, normalized(file[:loc[0]]))
	}

	if len(out) == 0 {
		out = append(out, normalized(withoutArchiveExtension(file)))
	}

	return out
}

func withoutArchiveExtension(file string) string {
	for _, ext := range archiveExtensions {
		if name, ok := strings.CutSuffix(file, ext); ok {
			return name
		}
	}

	return file
}

var archiveExtensions = []string{
	".tar.gz",
	".tar.bz2",
	".tar.xz",
	".zip",
	".tgz",
	".egg",
}

// simpleJSON is the JSON simple index (PEP 691), which gives files' upload
// times (PEP 700).
const simpleJSON = "application/vnd.pypi.simple.v1+json"

// uploadTimes are when the files of a project were uploaded upstream, from
// its JSON simple index; empty when the index gives none.
func (s *Service) uploadTimes(ctx context.Context, r repository.Repository, norm string) (map[string]time.Time, error) {
	body, err := s.repos.FetchUpstream(ctx, r, registryURL(r, s.repos)+"/simple/"+norm+"/", simpleJSON, 32<<20)
	if errors.Is(err, repository.ErrNotFound) {
		return map[string]time.Time{}, nil
	}

	if err != nil {
		return nil, err
	}

	var doc struct {
		Files []struct {
			Filename   string `json:"filename"`
			UploadTime string `json:"upload-time"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return map[string]time.Time{}, nil //nolint:nilerr // an index without JSON gives no times
	}

	out := make(map[string]time.Time, len(doc.Files))
	for _, f := range doc.Files {
		if t, err := time.Parse(time.RFC3339, f.UploadTime); err == nil {
			out[f.Filename] = t
		}
	}

	return out, nil
}

// versionOf is the version of a distribution file: the field after the
// project name.
func versionOf(project, file string) string {
	if strings.HasSuffix(file, ".whl") {
		parts := strings.Split(file, "-")
		if len(parts) > 1 {
			return parts[1]
		}

		return ""
	}

	base := withoutArchiveExtension(file)
	for i, c := range base {
		if c == '-' && Normalize(base[:i]) == Normalize(project) {
			return base[i+1:]
		}
	}

	loc := versionStart.FindStringIndex(file)
	if loc == nil {
		return ""
	}

	return withoutArchiveExtension(file[loc[0]+1:])
}

// checkDistribution refuses a distribution uploaded within the cooldown, and
// finds its #sha256= in the cached simple page of its project.
func (s *Service) checkDistribution(
	ctx context.Context,
	r repository.Repository,
	_, p string,
	_ repository.Checksums,
) (repository.Checksums, error) {
	file := path.Base(p)
	if !IsDist(file) {
		return repository.Checksums{}, nil
	}

	project, page, err := s.cachedProject(ctx, r, file)
	if err != nil {
		return repository.Checksums{}, err
	}

	if cooldown := r.Upstream().Cooldown(); cooldown > 0 {
		times, err := s.uploadTimes(ctx, r, project)
		if err != nil {
			return repository.Checksums{}, err
		}

		if uploaded, ok := times[file]; ok && s.now().Sub(uploaded) < cooldown {
			return repository.Checksums{}, repository.ErrCoolingDown
		}
	}

	if page == nil {
		return repository.Checksums{}, nil
	}

	rc, err := s.repos.Open(ctx, *page, nil)
	if err != nil {
		return repository.Checksums{}, err
	}

	defer func() {
		_ = rc.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(rc, 32<<20))
	if err != nil {
		return repository.Checksums{}, err
	}

	base, err := url.Parse(registryURL(r, s.repos) + "/simple/" + project + "/")
	if err != nil {
		return repository.Checksums{}, fmt.Errorf(
			"%w: index URL of %s: %w",
			repository.ErrInvalidRepository,
			r.Name,
			err,
		)
	}

	for _, l := range parseSimple(string(body), base, nil) {
		if l.File == file && len(l.SHA256) == 64 {
			return repository.Checksums{
				SHA256: strings.ToLower(l.SHA256),
			}, nil
		}
	}

	return repository.Checksums{}, nil
}

func (s *Service) cachedProject(
	ctx context.Context,
	r repository.Repository,
	file string,
) (string, *repository.Item, error) {
	candidates := projectCandidates(file)
	for _, project := range candidates {
		page, err := s.repos.Stat(ctx, r.Name, ".pypi/"+project+".pypi.html")
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if err != nil {
			return "", nil, err
		}

		return project, &page, nil
	}

	return candidates[0], nil, nil
}
