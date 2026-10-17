package lifecycle

import (
	"encoding/json"
	"fmt"
	"strings"

	"relic/api/adapters/artifactory/aql"
	"relic/api/adapters/artifactory/layout"
	rb "relic/internal/delivery/release"
)

type createBody struct {
	Name       string       `json:"release_bundle_name"`
	Version    string       `json:"release_bundle_version"`
	SourceType string       `json:"source_type,omitempty"`
	Source     *sourceBody  `json:"source,omitempty"`
	Sources    []sourceBody `json:"sources,omitempty"`
	AddSources []sourceBody `json:"add_sources,omitempty"`
	Draft      bool         `json:"draft,omitempty"`
}

type sourceBody struct {
	SourceType     string            `json:"source_type,omitempty"`
	AQL            string            `json:"aql,omitempty"`
	Artifacts      []artifactSource  `json:"artifacts,omitempty"`
	Builds         []buildSource     `json:"builds,omitempty"`
	ReleaseBundles []bundleSource    `json:"release_bundles,omitempty"`
	Packages       []json.RawMessage `json:"packages,omitempty"`
}

type artifactSource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

type buildSource struct {
	Name         string `json:"build_name"`
	Number       string `json:"build_number"`
	Repository   string `json:"build_repository,omitempty"`
	Dependencies bool   `json:"include_dependencies,omitempty"`
}

type bundleSource struct {
	Name       string `json:"release_bundle_name"`
	Version    string `json:"release_bundle_version"`
	Project    string `json:"project,omitempty"`
	Repository string `json:"repository_key,omitempty"`
}

const (
	typeAQL       = "aql"
	typeArtifacts = "artifacts"
	typeBuilds    = "builds"
	typeBundles   = "release_bundles"
	typePackages  = "packages"
)

func (b createBody) spec() (rb.Spec, error) {
	bodies := b.Sources
	switch {
	case len(b.AddSources) > 0:
		bodies = b.AddSources
	case len(bodies) == 0 && b.Source != nil:
		s := *b.Source
		s.SourceType = b.SourceType
		bodies = []sourceBody{
			s,
		}
	}

	out := rb.Spec{
		Name:    b.Name,
		Version: b.Version,
		Draft:   b.Draft,
	}

	for _, s := range bodies {
		src, err := s.source()
		if err != nil {
			return rb.Spec{}, err
		}

		out.Sources = append(out.Sources, src)
	}

	return out, nil
}

func (s sourceBody) source() (rb.Source, error) {
	switch s.SourceType {
	case typeAQL:
		q, err := aql.Parse(s.AQL)
		if err != nil {
			return rb.Source{}, fmt.Errorf("%w: aql: %w", rb.ErrInvalid, err)
		}

		if q.Domain != "items" {
			return rb.Source{}, fmt.Errorf("%w: aql: only items queries select release artifacts", rb.ErrInvalid)
		}

		iq, err := aql.ItemQuery(q)
		if err != nil {
			return rb.Source{}, fmt.Errorf("%w: aql: %w", rb.ErrInvalid, err)
		}

		return rb.Source{
			Kind:  rb.SourceQuery,
			Query: iq,
		}, nil
	case typeArtifacts:
		src := rb.Source{
			Kind: rb.SourceArtifacts,
		}
		for _, a := range s.Artifacts {
			src.Artifacts = append(src.Artifacts, rb.ArtifactRef(a))
		}

		return src, nil
	case typeBuilds:
		src := rb.Source{
			Kind: rb.SourceBuilds,
		}
		for _, b := range s.Builds {
			src.Builds = append(src.Builds, rb.BuildRef{
				Name:         b.Name,
				Number:       b.Number,
				Project:      buildProject(b.Repository),
				Dependencies: b.Dependencies,
			})
		}

		return src, nil
	case typeBundles:
		src := rb.Source{
			Kind: rb.SourceReleases,
		}
		for _, b := range s.ReleaseBundles {
			src.Releases = append(src.Releases, rb.Ref{
				Name:    b.Name,
				Version: b.Version,
				Project: bundleProject(b),
			})
		}

		return src, nil
	case typePackages:
		return rb.Source{}, fmt.Errorf("%w: package sources are not supported", rb.ErrInvalid)
	}

	return rb.Source{}, fmt.Errorf("%w: unknown source type %q", rb.ErrInvalid, s.SourceType)
}

func buildProject(repository string) string { return layout.BuildInfo.Project(repository) }

func bundleProject(b bundleSource) string {
	switch {
	case b.Repository == layout.ReleaseBundles.Repository(""):
		return rb.DefaultProject
	case b.Repository != "":
		project, _ := strings.CutSuffix(b.Repository, "-release-bundles-v2")
		return project
	}

	return b.Project
}

type promoteBody struct {
	Environment string   `json:"environment"`
	Included    []string `json:"included_repository_keys,omitempty"`
	Excluded    []string `json:"excluded_repository_keys,omitempty"`
}

func (p promoteBody) spec(operation string) rb.PromotionSpec {
	return rb.PromotionSpec{
		Environment: p.Environment,
		Include:     p.Included,
		Exclude:     p.Excluded,
		Move:        operation == "move",
	}
}

func status(s rb.Status) string { return strings.ToUpper(string(s)) }
