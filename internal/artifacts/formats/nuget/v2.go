package nuget

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"relic/internal/artifacts/repository"
)

// V2Entry is one package version as the NuGet V2 (OData) feed describes it.
type V2Entry struct {
	ID, Version, Description, Authors string
	Dependencies                      string
	Prerelease                        bool
	Size                              int64
	Published                         time.Time
}

const v2Concurrency = 8

// V2Entries lists every version of id in r with its dependencies (V2
// clients resolve from them).
func (s *Service) V2Entries(ctx context.Context, r repository.Repository, id string) ([]V2Entry, error) {
	lower := strings.ToLower(id)

	type source struct {
		remote repository.Repository
		item   *repository.Item
	}

	found := map[string]source{}

	var order []string

	for _, m := range s.members(ctx, r, 0) {
		if m.Class == "remote" {
			versions, err := s.remoteVersions(ctx, m, lower)
			if errors.Is(err, ErrNotFound) || errors.Is(err, repository.ErrUpstream) {
				continue
			}

			if err != nil {
				return nil, err
			}

			for _, v := range versions {
				if _, ok := found[v]; !ok {
					found[v] = source{
						remote: m,
					}
					order = append(order, v)
				}
			}

			continue
		}

		items, err := s.localPackages(ctx, m.Name, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		for v, it := range items {
			if _, ok := found[v]; !ok {
				found[v] = source{
					item: &it,
				}
				order = append(order, v)
			}
		}
	}

	if len(order) == 0 {
		return nil, ErrNotFound
	}

	slices.SortFunc(order, compareVersions)
	entries := make([]V2Entry, len(order))

	var g errgroup.Group
	g.SetLimit(v2Concurrency)

	for i, v := range order {
		g.Go(func() error {
			src := found[v]
			e := V2Entry{
				ID:         id,
				Version:    v,
				Prerelease: strings.Contains(v, "-"),
			}

			var (
				n   Nuspec
				err error
			)
			if src.item != nil {
				_, n, err = s.Nuspec(ctx, *src.item)
				e.Size, e.Published = src.item.Size, src.item.Created
			} else {
				n, err = s.remoteNuspec(ctx, src.remote, lower, v)
			}

			switch {
			case errors.Is(err, ErrInvalid), errors.Is(err, repository.ErrNotFound),
				errors.Is(err, repository.ErrUpstream):
				s.log.WarnContext(ctx, "V2 entry without metadata", "id", id, "version", v, "error", err)
			case err != nil:
				return fmt.Errorf("metadata of %s %s: %w", id, v, err)
			default:
				e.ID = n.Metadata.ID
				e.Description = n.Metadata.Description
				e.Authors = n.Metadata.Authors
				e.Dependencies = v2Dependencies(n)
			}

			entries[i] = e

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return entries, nil
}

func (s *Service) remoteNuspec(ctx context.Context, r repository.Repository, lower, version string) (Nuspec, error) {
	item, err := s.repos.ResolveURL(
		ctx,
		r,
		s.remoteBase(r)+"/"+lower+"/"+version+"/"+lower+".nuspec",
		".nuget/"+lower+"/"+version+".nuspec",
	)
	if err != nil {
		return Nuspec{}, err
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return Nuspec{}, err
	}

	defer func() {
		_ = rc.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(rc, maxNuspec))
	if err != nil {
		return Nuspec{}, err
	}

	var n Nuspec
	if err := xml.Unmarshal(raw, &n); err != nil {
		return Nuspec{}, fmt.Errorf("%w: unreadable .nuspec of %s %s", ErrInvalid, lower, version)
	}

	return n, nil
}

func v2Dependencies(n Nuspec) string {
	var parts []string
	for _, d := range n.Metadata.Dependencies.Flat {
		parts = append(parts, d.ID+":"+d.Version+":")
	}

	for _, g := range n.Metadata.Dependencies.Groups {
		tfm := ShortFramework(g.TargetFramework)
		if len(g.Dependencies) == 0 {
			parts = append(parts, "::"+tfm)
		}

		for _, d := range g.Dependencies {
			parts = append(parts, d.ID+":"+d.Version+":"+tfm)
		}
	}

	return strings.Join(parts, "|")
}

// ShortFramework turns a nuspec target framework into NuGet's short folder
// name: ".NETStandard2.0" → "netstandard2.0", ".NETFramework4.6.2" →
// "net462", ".NETCoreApp3.1" → "netcoreapp3.1"; short names pass through.
func ShortFramework(tfm string) string {
	t := strings.ToLower(strings.TrimSpace(tfm))
	for long, short := range map[string]string{
		".netstandard": "netstandard",
		".netcoreapp":  "netcoreapp",
	} {
		if v, ok := strings.CutPrefix(t, long); ok {
			return short + v
		}
	}

	if v, ok := strings.CutPrefix(t, ".netframework"); ok {
		return "net" + strings.ReplaceAll(v, ".", "")
	}

	return t
}
