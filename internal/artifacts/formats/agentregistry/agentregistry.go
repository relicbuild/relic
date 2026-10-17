// Package agentregistry indexes the agent registries: MCP server
// definitions (server.json files) in mcp repositories and agent skill
// bundles (zip files with a SKILL.md) in skills repositories, and their
// approvals; an entry is served only while its content is approved.
package agentregistry

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
)

// Errors.
var (
	ErrNotFound = errors.New("registry entry not found")
	ErrInvalid  = errors.New("invalid registry entry")
)

const byteOrderMark = "\uFEFF"

const (
	maxServerJSON  = 1 << 20
	maxSkillBundle = 64 << 20
	maxSkillFile   = 1 << 20
)

// Entry is one MCP server definition or skill bundle.
type Entry struct {
	Repository  string
	Path        string
	Kind        repository.Format
	Name        string
	Version     string
	Description string
	SHA256      string
	// Doc is the server.json, or the skill's SKILL.md front matter as JSON.
	Doc        json.RawMessage
	Updated    time.Time
	ApprovedBy string
	Approved   *time.Time
}

// IsApproved reports whether the entry's current content is approved.
func (e Entry) IsApproved() bool { return e.Approved != nil }

// Service indexes and approves registry entries.
type Service struct {
	db    *database.DB
	repos *repository.Service
	now   func() time.Time
}

// New returns a Service and registers its hooks with repos.
func New(db *database.DB, repos *repository.Service) *Service {
	s := &Service{
		db:    db,
		repos: repos,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}

	for _, f := range []repository.Format{
		repository.FormatMCP,
		repository.FormatSkills,
	} {
		repos.RegisterHook(f, s.index)
		repos.RegisterDeleteHook(f, s.remove)
	}

	return s
}

func (s *Service) index(ctx context.Context, r repository.Repository, item repository.Item) (err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "agentregistry.Index")
	defer func() {
		telemetry.End(span, err)
	}()

	var (
		name, version, description string
		doc                        []byte
	)

	switch {
	case r.Format == repository.FormatMCP && strings.HasSuffix(item.Name, ".json"):
		body, err := s.read(ctx, item, maxServerJSON)
		if err != nil {
			return err
		}

		name, version, description, err = serverFields(body)
		if err != nil {
			return err
		}

		doc = body
	case r.Format == repository.FormatSkills && strings.HasSuffix(item.Name, ".zip"):
		body, err := s.read(ctx, item, maxSkillBundle)
		if err != nil {
			return err
		}

		front, err := skillFrontMatter(body)
		if err != nil {
			return err
		}

		name, description = stringField(front, "name"), stringField(front, "description")
		version = stringField(front, "version")

		if metadata, ok := front["metadata"].(map[string]any); ok && version == "" {
			version = stringField(metadata, "version")
		}

		if doc, err = json.Marshal(front); err != nil {
			return err
		}
	default:
		return nil
	}

	if name == "" {
		return fmt.Errorf("%w: %s/%s names no server or skill", ErrInvalid, r.Name, item.Path())
	}

	return s.db.UpsertRegistryEntry(ctx, database.UpsertRegistryEntryParams{
		RepoKey:     r.Name,
		Path:        item.Path(),
		Kind:        string(r.Format),
		Name:        name,
		Version:     version,
		Description: description,
		Sha256:      item.Checksums.SHA256,
		Doc:         doc,
		Updated:     s.now(),
	})
}

func (s *Service) read(ctx context.Context, item repository.Item, limit int64) ([]byte, error) {
	if item.Size > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrInvalid, item.Path(), limit)
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	return io.ReadAll(io.LimitReader(rc, limit))
}

func serverFields(body []byte) (name, version, description string, err error) {
	var server struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &server); err != nil {
		return "", "", "", fmt.Errorf("%w: server.json: %w", ErrInvalid, err)
	}

	return server.Name, server.Version, server.Description, nil
}

// skillFrontMatter reads the YAML front matter of the bundle's SKILL.md, at
// its root or in its one top-level folder.
func skillFrontMatter(bundle []byte) (map[string]any, error) {
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		return nil, fmt.Errorf("%w: not a zip file: %w", ErrInvalid, err)
	}

	for _, f := range zr.File {
		dir, base := path.Split(f.Name)
		if base != "SKILL.md" || strings.Count(dir, "/") > 1 {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		text, err := io.ReadAll(io.LimitReader(rc, maxSkillFile))
		_ = rc.Close()

		if err != nil {
			return nil, err
		}

		return frontMatter(string(text))
	}

	return nil, fmt.Errorf("%w: the bundle has no SKILL.md", ErrInvalid)
}

func frontMatter(text string) (map[string]any, error) {
	text = strings.ReplaceAll(strings.TrimPrefix(text, byteOrderMark), "\r\n", "\n")

	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, fmt.Errorf("%w: SKILL.md has no front matter", ErrInvalid)
	}

	block, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return nil, fmt.Errorf("%w: SKILL.md's front matter is not closed", ErrInvalid)
	}

	out := map[string]any{}
	if err := yaml.Unmarshal([]byte(block), &out); err != nil {
		return nil, fmt.Errorf("%w: SKILL.md front matter: %w", ErrInvalid, err)
	}

	return out, nil
}

func stringField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (s *Service) remove(ctx context.Context, r repository.Repository, item repository.Item) error {
	p := item.Path()
	if item.Name == repository.RootPath {
		p = ""
	}

	prefix := likeEscape(p) + "/%"

	return s.db.WithTx(ctx, func(q database.Querier) error {
		if err := q.DeleteRegistryApprovals(ctx, database.DeleteRegistryApprovalsParams{
			RepoKey: r.Name,
			Path:    p,
			Prefix:  prefix,
		}); err != nil {
			return err
		}

		return q.DeleteRegistryEntries(ctx, database.DeleteRegistryEntriesParams{
			RepoKey: r.Name,
			Path:    p,
			Prefix:  prefix,
		})
	})
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Entries lists a repository's entries; approvedOnly leaves out those whose
// current content is not approved.
func (s *Service) Entries(ctx context.Context, repoKey string, approvedOnly bool) ([]Entry, error) {
	rows, err := s.db.ListRegistryEntries(ctx, repoKey)
	if err != nil {
		return nil, fmt.Errorf("list registry entries: %w", err)
	}

	out := make([]Entry, 0, len(rows))

	for _, r := range rows {
		e := Entry{
			Repository:  r.RepoKey,
			Path:        r.Path,
			Kind:        repository.Format(r.Kind),
			Name:        r.Name,
			Version:     r.Version,
			Description: r.Description,
			SHA256:      r.Sha256,
			Doc:         r.Doc,
			Updated:     r.Updated,
			ApprovedBy:  r.ApprovedBy,
			Approved:    r.Approved,
		}
		if approvedOnly && !e.IsApproved() {
			continue
		}

		out = append(out, e)
	}

	return out, nil
}

// Unapproved reports whether the item at itemPath, holding content
// sha256Hex, is a registry entry whose content no one approved: such an
// entry is not served.
func (s *Service) Unapproved(ctx context.Context, repoKey, itemPath, sha256Hex string) (bool, error) {
	approved, err := s.db.RegistryEntryApproved(ctx, database.RegistryEntryApprovedParams{
		Sha256:  sha256Hex,
		RepoKey: repoKey,
		Path:    itemPath,
	})
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("registry approval of %s/%s: %w", repoKey, itemPath, err)
	}

	return !approved, nil
}

// Approve approves an entry's current content; a non-empty reviewed SHA-256
// must match it.
func (s *Service) Approve(ctx context.Context, repoKey, entryPath, reviewed, user string) error {
	e, err := s.db.GetRegistryEntry(ctx, database.GetRegistryEntryParams{
		RepoKey: repoKey,
		Path:    entryPath,
	})
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return fmt.Errorf("%w: %s/%s", ErrNotFound, repoKey, entryPath)
	}

	if err != nil {
		return fmt.Errorf("get registry entry: %w", err)
	}

	if reviewed != "" && reviewed != e.Sha256 {
		return fmt.Errorf("%w: %s/%s changed after it was reviewed", ErrInvalid, repoKey, entryPath)
	}

	telemetry.RegistryApproval(true)

	return s.db.PutApproval(ctx, database.PutApprovalParams{
		RepoKey:    repoKey,
		Path:       entryPath,
		Sha256:     e.Sha256,
		ApprovedBy: user,
		Approved:   s.now(),
	})
}

// Revoke withdraws an entry's approval.
func (s *Service) Revoke(ctx context.Context, repoKey, entryPath string) error {
	n, err := s.db.DeleteApproval(ctx, database.DeleteApprovalParams{
		RepoKey: repoKey,
		Path:    entryPath,
	})
	if err != nil {
		return fmt.Errorf("revoke approval: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("%w: %s/%s is not approved", ErrNotFound, repoKey, entryPath)
	}

	telemetry.RegistryApproval(false)

	return nil
}
