package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"relic/internal/artifacts/itemquery"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/buildinfo"
	"relic/internal/delivery/evidence"
)

type entry struct {
	item repository.Item
	art  Artifact
}

func (s *Service) resolve(ctx context.Context, project string, sources []Source) ([]entry, error) {
	var out []entry

	seen := map[string]string{}
	add := func(it repository.Item, art Artifact) error {
		if sha, ok := seen[art.Path]; ok {
			if !strings.EqualFold(sha, art.SHA256) {
				return fmt.Errorf("%w: two sources provide %s with different content", ErrInvalid, art.Path)
			}

			return nil
		}

		seen[art.Path] = art.SHA256
		out = append(out, entry{
			item: it,
			art:  art,
		})

		return nil
	}
	fromItem := func(it repository.Item) error {
		if it.IsFolder() {
			return nil
		}

		return add(it, Artifact{
			Repository: it.Repository,
			Path:       it.Path(),
			SHA256:     it.Checksums.SHA256,
			Size:       it.Size,
		})
	}

	for _, src := range sources {
		var err error

		switch src.Kind {
		case SourceQuery:
			err = s.fromQuery(ctx, src.Query, fromItem)
		case SourceArtifacts:
			err = s.fromArtifacts(ctx, src.Artifacts, fromItem)
		case SourceBuilds:
			err = s.fromBuilds(ctx, src.Builds, fromItem)
		case SourceReleases:
			err = s.fromReleases(ctx, project, src.Releases, add)
		default:
			err = fmt.Errorf("%w: unknown source kind %q", ErrInvalid, src.Kind)
		}

		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (s *Service) fromQuery(ctx context.Context, q itemquery.Query, fn func(repository.Item) error) error {
	plan, err := itemquery.Compile(q)
	if err != nil {
		return fmt.Errorf("%w: query: %w", ErrInvalid, err)
	}

	var items []repository.Item

	err = itemquery.Run(ctx, s.db, plan, func(row itemquery.Row) error {
		if row.Type != repository.TypeFile {
			return nil
		}

		it, err := s.repos.Stat(ctx, row.Repository, repository.JoinPath(row.Path, row.Name))
		if err != nil {
			return err
		}

		items = append(items, it)

		return nil
	})
	if err != nil {
		return err
	}

	for _, it := range items {
		if err := fn(it); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) fromArtifacts(ctx context.Context, arts []ArtifactRef, fn func(repository.Item) error) error {
	for _, a := range arts {
		repoKey, path, ok := strings.Cut(strings.TrimPrefix(a.Path, "/"), "/")
		if !ok {
			return fmt.Errorf("%w: artifact path %q has no repository", ErrInvalid, a.Path)
		}

		it, err := s.repos.Stat(ctx, repoKey, path)
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrRepositoryNotFound) {
			return fmt.Errorf("%w: artifact %s not found", ErrInvalid, a.Path)
		}

		if err != nil {
			return err
		}

		if a.SHA256 != "" && !strings.EqualFold(a.SHA256, it.Checksums.SHA256) {
			return fmt.Errorf("%w: artifact %s does not match its sha256", ErrInvalid, a.Path)
		}

		if err := fn(it); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) fromBuilds(ctx context.Context, builds []BuildRef, fn func(repository.Item) error) error {
	for _, b := range builds {
		items, err := s.builds.Items(ctx, b.Project, b.Name, b.Number, b.Dependencies)
		if errors.Is(err, buildinfo.ErrNotFound) {
			return fmt.Errorf("%w: build %s/%s not found", ErrInvalid, b.Name, b.Number)
		}

		if err != nil {
			return err
		}

		for _, it := range items {
			if err := fn(it); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Service) fromReleases(
	ctx context.Context,
	project string,
	releases []Ref,
	add func(repository.Item, Artifact) error,
) error {
	for _, src := range releases {
		p := src.Project
		if p == "" {
			p = project
		}

		b, err := s.Get(ctx, p, src.Name, src.Version)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: release %s/%s not found", ErrInvalid, src.Name, src.Version)
		}

		if err != nil {
			return err
		}

		for _, a := range b.Artifacts {
			it, err := s.repos.Stat(ctx, b.Repository, b.artifactPath(a))
			if err != nil {
				return fmt.Errorf("release %s/%s: %w", b.Name, b.Version, err)
			}

			if !strings.EqualFold(a.SHA256, it.Checksums.SHA256) {
				return fmt.Errorf(
					"%w: release %s/%s: %s does not match its sha256",
					ErrInvalid,
					b.Name,
					b.Version,
					a.Path,
				)
			}

			if err := add(it, a); err != nil {
				return err
			}
		}
	}

	return nil
}

// Manifest statement types.
const (
	dssePayloadType = "application/vnd.in-toto+json"
	statementType   = "https://in-toto.io/Statement/v1"
	predicateType   = "https://relic.build/release-bundle/v1"
)

// SigningKey names the key pair that signs release manifests.
const SigningKey = "release-bundles"

func (s *Service) signedManifest(ctx context.Context, b Release) ([]byte, error) {
	statement, err := b.statement()
	if err != nil {
		return nil, err
	}

	key, _, err := s.keys.RSA(ctx, SigningKey)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}

	env, err := evidence.Sign(key, SigningKey, dssePayloadType, statement)
	if err != nil {
		return nil, err
	}

	return json.Marshal(env)
}

func (b Release) statement() ([]byte, error) {
	type subject struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	}

	subjects := make([]subject, 0, len(b.Artifacts))
	for _, a := range b.Artifacts {
		subjects = append(subjects, subject{
			Name: a.Path,
			Digest: map[string]string{
				"sha256": a.SHA256,
			},
		})
	}

	artifacts := b.Artifacts
	if artifacts == nil {
		artifacts = []Artifact{}
	}

	statement, err := json.Marshal(map[string]any{
		"_type":         statementType,
		"subject":       subjects,
		"predicateType": predicateType,
		"predicate": map[string]any{
			"name":       b.Name,
			"version":    b.Version,
			"project":    b.Project,
			"created":    b.Created.Format(timeFormat),
			"created_by": b.CreatedBy,
			"artifacts":  artifacts,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}

	return statement, nil
}

const timeFormat = "2006-01-02T15:04:05.000Z07:00"
