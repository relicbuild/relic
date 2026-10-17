// Package huggingface implements Hugging Face model and dataset
// repositories: the Hub API
// subset huggingface_hub uses for snapshot_download and upload_folder.
package huggingface

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // commit ids are 40-hex like git's; not a security use
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
)

// Format is the repository format this package serves.
const Format = repository.FormatHuggingFace

// Domain errors, mapped to X-Error-Code by the API layer.
var (
	ErrRepoNotFound     = errors.New("repository not found")
	ErrRevisionNotFound = errors.New("revision not found")
	ErrEntryNotFound    = errors.New("entry not found")
	ErrInvalid          = errors.New("invalid request")
)

// LFSThreshold is the size above which preupload asks for LFS.
const LFSThreshold = 10 << 20

// revTimeFormat sorts lexically in time order (fixed width).
const revTimeFormat = "2006-01-02T15:04:05.000Z"

const lfsStaging = ".hf-lfs"

const infoCacheSuffix = ".hf.json"

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}(/[A-Za-z0-9][A-Za-z0-9._-]{0,95})?$`)
	revPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)
	oidPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Kind is "models" or "datasets" (the <t> in URLs and paths).
type Kind string

// ParseKind accepts the API's plural form.
func ParseKind(s string) (Kind, bool) {
	switch s {
	case "models", "datasets":
		return Kind(s), true
	}

	return "", false
}

// Service serves Hugging Face repositories.
type Service struct {
	repos *repository.Service
	log   *slog.Logger
	now   func() time.Time
}

// NewService returns a Service.
func NewService(repos *repository.Service, log *slog.Logger) *Service {
	repos.RegisterMetadata(func(name string) bool {
		return strings.HasSuffix(name, infoCacheSuffix)
	})

	return &Service{
		repos: repos,
		log:   log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// ValidID reports whether id is a Hub repo id ("name" or "org/name").
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Commit is a stored commit of a revision.
type Commit struct {
	Kind   Kind
	ID     string
	Rev    string
	Folder string
	Oid    string
	Time   time.Time
}

// Dir is the folder holding the commit's files.
func (c Commit) Dir() string { return c.Folder + "/" + c.Oid }

// Latest returns the newest commit of rev in a local repository.
func (s *Service) Latest(ctx context.Context, repoKey string, kind Kind, id, rev string) (Commit, error) {
	commits, err := s.commits(ctx, repoKey, kind, id)
	if err != nil {
		return Commit{}, err
	}

	var best *Commit
	for i, c := range commits {
		if c.Rev == rev && (best == nil || c.Folder > best.Folder) {
			best = &commits[i]
		}
	}

	if best == nil {
		return Commit{}, ErrRevisionNotFound
	}

	return *best, nil
}

// Revision finds a commit of a local repository by rev: a commit SHA, or
// a branch or tag (its newest commit).
func (s *Service) Revision(ctx context.Context, repoKey string, kind Kind, id, rev string) (Commit, error) {
	if IsCommitSHA(rev) {
		return s.BySHA(ctx, repoKey, kind, id, rev)
	}

	return s.Latest(ctx, repoKey, kind, id, rev)
}

// BySHA finds the commit with oid in a local repository.
func (s *Service) BySHA(ctx context.Context, repoKey string, kind Kind, id, oid string) (Commit, error) {
	commits, err := s.commits(ctx, repoKey, kind, id)
	if err != nil {
		return Commit{}, err
	}

	for _, c := range commits {
		if c.Oid == oid {
			return c, nil
		}
	}

	return Commit{}, ErrRevisionNotFound
}

func (s *Service) commits(ctx context.Context, repoKey string, kind Kind, id string) ([]Commit, error) {
	base := string(kind) + "/" + id

	folder, err := s.repos.Stat(ctx, repoKey, base)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrRepoNotFound
	}

	if err != nil {
		return nil, err
	}

	revs, err := s.repos.Children(ctx, folder)
	if err != nil {
		return nil, err
	}

	var out []Commit

	for _, r := range revs {
		i := strings.LastIndexByte(r.Name, '_')
		if !r.IsFolder() || i <= 0 {
			continue
		}

		t, err := time.Parse(revTimeFormat, r.Name[i+1:])
		if err != nil {
			continue
		}

		kids, err := s.repos.Children(ctx, r)
		if err != nil {
			return nil, err
		}

		for _, k := range kids {
			if k.IsFolder() && shaPattern.MatchString(k.Name) {
				out = append(
					out,
					Commit{
						Kind:   kind,
						ID:     id,
						Rev:    r.Name[:i],
						Folder: base + "/" + r.Name,
						Oid:    k.Name,
						Time:   t,
					},
				)
			}
		}
	}

	return out, nil
}

// File is a file in a commit.
type File struct {
	Path string
	Item repository.Item
}

// Files lists a commit's files, by path.
func (s *Service) Files(ctx context.Context, repoKey string, c Commit) ([]File, error) {
	dir, err := s.repos.Stat(ctx, repoKey, c.Dir())
	if err != nil {
		return nil, err
	}

	items, err := s.repos.Subtree(ctx, dir)
	if err != nil {
		return nil, err
	}

	var out []File

	for _, it := range items {
		if it.IsFolder() {
			continue
		}

		out = append(out, File{
			Path: strings.TrimPrefix(it.Path(), c.Dir()+"/"),
			Item: it,
		})
	}

	slices.SortFunc(out, func(a, b File) int {
		return strings.Compare(a.Path, b.Path)
	})

	return out, nil
}

// Info is the revision answer (model or dataset info).
func Info(c Commit, files []File) map[string]any {
	siblings := make([]map[string]string, 0, len(files))
	for _, f := range files {
		siblings = append(siblings, map[string]string{
			"rfilename": f.Path,
		})
	}

	info := map[string]any{
		"id":           c.ID,
		"sha":          c.Oid,
		"siblings":     siblings,
		"private":      false,
		"lastModified": c.Time.Format(revTimeFormat),
	}
	if c.Kind == "models" {
		info["modelId"] = c.ID
	}

	return info
}

// StagedLFS reports whether an LFS blob is already staged in repoKey.
func (s *Service) StagedLFS(ctx context.Context, repoKey, oid string) bool {
	_, err := s.repos.Stat(ctx, repoKey, lfsStaging+"/"+oid)
	return err == nil
}

// StageLFS stores an uploaded LFS blob, checking its sha256 (the oid).
func (s *Service) StageLFS(ctx context.Context, repoKey, oid string, body io.Reader, user string) error {
	if !oidPattern.MatchString(oid) {
		return fmt.Errorf("%w: bad oid", ErrInvalid)
	}

	_, err := s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: repoKey,
			Path:       lfsStaging + "/" + oid,
			Body:       body,
			Expected: repository.Checksums{
				SHA256: oid,
			},
			User: user,
		},
	)

	return err
}

// Operation is one commit operation from the NDJSON body.
type Operation struct {
	Key   string `json:"key"`
	Value struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Oid      string `json:"oid"`
		Size     int64  `json:"size"`
		Summary  string `json:"summary"`
	} `json:"value"`
}

// ParseCommit reads a commit's NDJSON operations.
func ParseCommit(body []byte) ([]Operation, error) {
	var ops []Operation

	for line := range bytes.SplitSeq(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var op Operation
		if err := json.Unmarshal(line, &op); err != nil {
			return nil, fmt.Errorf("%w: commit line: %w", ErrInvalid, err)
		}

		ops = append(ops, op)
	}

	return ops, nil
}

// CreateCommit applies ops on top of rev's latest commit, as a new commit.
func (s *Service) CreateCommit(
	ctx context.Context,
	repoKey string,
	kind Kind,
	id, rev string,
	ops []Operation,
	user string,
) (Commit, error) {
	var c Commit

	err := s.repos.WithLock(ctx, commitLock(repoKey, kind, id, rev), func() error {
		var err error

		c, err = s.createCommit(ctx, repoKey, kind, id, rev, ops, user)
		if err != nil && c.Folder != "" {
			if derr := s.repos.Delete(ctx, repoKey, c.Folder); derr != nil && !errors.Is(derr, repository.ErrNotFound) {
				s.log.WarnContext(ctx, "clean up failed commit", "repo", repoKey, "folder", c.Folder, "error", derr)
			}

			c = Commit{}
		}

		return err
	})

	return c, err
}

func commitLock(repoKey string, kind Kind, id, rev string) string {
	return "huggingface-commit:" + repoKey + "/" + string(kind) + "/" + id + "/" + rev
}

func (s *Service) createCommit(
	ctx context.Context,
	repoKey string,
	kind Kind,
	id, rev string,
	ops []Operation,
	user string,
) (Commit, error) {
	if !ValidID(id) || !revPattern.MatchString(rev) {
		return Commit{}, fmt.Errorf("%w: invalid repo id or revision", ErrInvalid)
	}

	prev, prevErr := s.Latest(ctx, repoKey, kind, id, rev)
	if prevErr != nil && !errors.Is(prevErr, ErrRevisionNotFound) && !errors.Is(prevErr, ErrRepoNotFound) {
		return Commit{}, prevErr
	}

	now := s.now().Truncate(time.Millisecond)
	if prevErr == nil && !now.After(prev.Time) {
		now = prev.Time.Add(time.Millisecond)
	}

	h := sha1.New() //nolint:gosec // an id, not a security use

	_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n%d\n", kind, id, rev, now.UnixNano())
	for _, op := range ops {
		_, _ = fmt.Fprintf(h, "%s %s %s %s\n", op.Key, op.Value.Path, op.Value.Oid, op.Value.Content)
	}

	c := Commit{
		Kind:   kind,
		ID:     id,
		Rev:    rev,
		Folder: string(kind) + "/" + id + "/" + rev + "_" + now.Format(revTimeFormat),
		Oid:    hex.EncodeToString(h.Sum(nil)),
		Time:   now,
	}

	if prevErr == nil {
		src, err := s.repos.Stat(ctx, repoKey, prev.Dir())
		if err != nil {
			return c, err
		}

		if err := s.repos.Copy(
			ctx,
			repository.CopyInput{
				Source:                src,
				DestinationRepository: repoKey,
				DestinationPath:       c.Dir(),
				User:                  user,
			},
		); err != nil {
			return c, fmt.Errorf("copy previous commit: %w", err)
		}
	} else if _, err := s.repos.CreateFolder(ctx, repoKey, c.Dir(), user); err != nil {
		return c, err
	}

	var staged []string

	for _, op := range ops {
		p := op.Value.Path
		if op.Key != "header" {
			clean, err := repository.CleanPath(p)
			if err != nil || clean != p || p == "" {
				return c, fmt.Errorf("%w: invalid path %q", ErrInvalid, p)
			}
		}

		target := c.Dir() + "/" + p

		switch op.Key {
		case "header":
		case "file":
			if op.Value.Encoding != "base64" {
				return c, fmt.Errorf("%w: unsupported encoding %q", ErrInvalid, op.Value.Encoding)
			}

			content, err := base64.StdEncoding.DecodeString(op.Value.Content)
			if err != nil {
				return c, fmt.Errorf("%w: %s: %w", ErrInvalid, p, err)
			}

			if _, err := s.repos.Deploy(
				ctx,
				repository.DeployInput{
					Repository: repoKey,
					Path:       target,
					Body:       bytes.NewReader(content),
					User:       user,
				},
			); err != nil {
				return c, err
			}
		case "lfsFile":
			if !oidPattern.MatchString(op.Value.Oid) {
				return c, fmt.Errorf("%w: bad oid for %s", ErrInvalid, p)
			}
			// Only content uploaded to (or already in) this repository may be
			// linked: a known sha256 alone must not reach other repositories'
			// files.
			if !s.StagedLFS(ctx, repoKey, op.Value.Oid) {
				inRepo, err := s.repos.FindBySHA256(ctx, repoKey, "", op.Value.Oid)
				if err != nil {
					return c, err
				}

				if len(inRepo) == 0 {
					return c, fmt.Errorf("%w: LFS object for %s was never uploaded", ErrInvalid, p)
				}
			}

			_, err := s.repos.DeployByChecksum(
				ctx,
				repository.DeployInput{
					Repository: repoKey,
					Path:       target,
					Expected: repository.Checksums{
						SHA256: op.Value.Oid,
					},
					User: user,
				},
			)
			if errors.Is(err, repository.ErrChecksumUnknown) {
				return c, fmt.Errorf("%w: LFS object for %s was never uploaded", ErrInvalid, p)
			}

			if err != nil {
				return c, err
			}

			staged = append(staged, op.Value.Oid)
		case "deletedFile":
			if err := s.repos.Delete(ctx, repoKey, target); err != nil && !errors.Is(err, repository.ErrNotFound) {
				return c, err
			}
		default:
			return c, fmt.Errorf("%w: unsupported operation %q", ErrInvalid, op.Key)
		}
	}

	for _, oid := range staged {
		if err := s.repos.Delete(ctx, repoKey, lfsStaging+"/"+oid); err != nil &&
			!errors.Is(err, repository.ErrNotFound) {
			s.log.WarnContext(ctx, "remove staged LFS object", "repo", repoKey, "oid", oid, "error", err)
		}
	}

	return c, nil
}

// RemoteInfo fetches revision info from a remote's upstream (cached as
// metadata).
func (s *Service) RemoteInfo(
	ctx context.Context,
	r repository.Repository,
	kind Kind,
	id, rev string,
) (map[string]any, error) {
	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")

	item, err := s.repos.ResolveURL(
		ctx,
		r,
		upstreamRevisionURL(base, kind, id, rev),
		".hf/"+string(kind)+"/"+id+"/"+rev+infoCacheSuffix,
	)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrRepoNotFound
	}

	if err != nil {
		return nil, err
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	var info map[string]any
	if err := json.NewDecoder(rc).Decode(&info); err != nil {
		return nil, fmt.Errorf("%w: upstream revision info: %w", repository.ErrUpstream, err)
	}

	return info, nil
}

// RemoteTree fetches the recursive file listing of an upstream commit
// (cached as metadata), without Xet hashes: clients fetch the files through
// Relic, which has no Xet endpoints.
func (s *Service) RemoteTree(ctx context.Context, r repository.Repository, kind Kind, id, sha string) ([]any, error) {
	if !shaPattern.MatchString(sha) {
		return nil, ErrRevisionNotFound
	}

	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")

	item, err := s.repos.ResolveURL(
		ctx,
		r,
		base+"/api/"+string(kind)+"/"+id+"/tree/"+sha+"?recursive=true",
		".hf/"+string(kind)+"/"+id+"/"+sha+".tree"+infoCacheSuffix,
	)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrRepoNotFound
	}

	if err != nil {
		return nil, err
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	var tree []any
	if err := json.NewDecoder(rc).Decode(&tree); err != nil {
		return nil, fmt.Errorf("%w: upstream tree: %w", repository.ErrUpstream, err)
	}

	for _, entry := range tree {
		if fields, ok := entry.(map[string]any); ok {
			delete(fields, "xetHash")
		}
	}

	return tree, nil
}

// IsCommitSHA reports whether rev is a commit SHA rather than a branch or
// tag name.
func IsCommitSHA(rev string) bool { return shaPattern.MatchString(rev) }

// RemoteSHA resolves a branch or tag of an upstream repository to its
// commit SHA, from the upstream's revision info.
func (s *Service) RemoteSHA(ctx context.Context, r repository.Repository, kind Kind, id, rev string) (string, error) {
	info, err := s.RemoteInfo(ctx, r, kind, id, rev)
	if err != nil {
		return "", err
	}

	sha, _ := info["sha"].(string)
	if !IsCommitSHA(sha) {
		return "", ErrRevisionNotFound
	}

	return sha, nil
}

// RemoteFile resolves a file of an upstream commit, cached at
// <t>/<id>/<sha>/<file>.
func (s *Service) RemoteFile(
	ctx context.Context,
	r repository.Repository,
	kind Kind,
	id, sha, file string,
) (repository.Item, error) {
	if !shaPattern.MatchString(sha) {
		return repository.Item{}, ErrRevisionNotFound
	}

	base := strings.TrimSuffix(s.repos.UpstreamOf(r).URL, "/")

	return s.repos.ResolveURL(ctx, r, upstreamFileURL(base, kind, id, sha, file), string(kind)+"/"+id+"/"+sha+"/"+file)
}

func upstreamRevisionURL(base string, kind Kind, id, rev string) string {
	return base + "/api/" + string(kind) + "/" + escapedSegments(id) + "/revision/" + url.PathEscape(rev)
}

func upstreamFileURL(base string, kind Kind, id, sha, file string) string {
	prefix := ""
	if kind == "datasets" {
		prefix = "datasets/"
	}

	return base + "/" + prefix + escapedSegments(id) + "/resolve/" + sha + "/" + escapedSegments(file)
}

func escapedSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/")
}
