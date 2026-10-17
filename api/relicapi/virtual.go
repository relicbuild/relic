package relicapi

import (
	"context"
	"errors"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
)

func storingMembers(ctx context.Context, d Dependencies, r repository.Repository) ([]string, error) {
	var out []string

	seen := map[string]bool{
		r.Name: true,
	}

	var walk func(r repository.Repository, depth int) error

	walk = func(r repository.Repository, depth int) error {
		if depth >= repository.MaxVirtualDepth {
			return nil
		}

		for _, name := range r.Members() {
			if seen[name] {
				continue
			}

			seen[name] = true

			m, err := d.Repositories.GetRepository(ctx, name)
			if errors.Is(err, repository.ErrRepositoryNotFound) {
				continue
			}

			if err != nil {
				return err
			}

			if m.Class == repository.ClassVirtual {
				if err := walk(m, depth+1); err != nil {
					return err
				}

				continue
			}

			out = append(out, name)
		}

		return nil
	}

	return out, walk(r, 0)
}

func hiddenMember(err error) bool {
	return errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrRepositoryNotFound)
}

func virtualChildren(ctx context.Context, d Dependencies, r repository.Repository, p string) ([]*relicv1.Item, error) {
	members, err := storingMembers(ctx, d, r)
	if err != nil {
		return nil, err
	}

	taken := map[string]bool{}

	var out []*relicv1.Item

	for _, m := range members {
		if err := canList(ctx, d, m, p); hiddenMember(err) {
			continue
		} else if err != nil {
			return nil, err
		}

		folder, err := d.Repositories.Stat(ctx, m, p)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if !folder.IsFolder() {
			continue
		}

		kids, err := d.Repositories.Children(ctx, folder)
		if err != nil {
			return nil, err
		}

		check, err := repositoryAccess(ctx, d, m)
		if err != nil {
			return nil, err
		}

		var found []*relicv1.Item

		for _, k := range kids {
			if taken[k.Name] || check(k.Path(), auth.ActionRead) != nil {
				continue
			}

			taken[k.Name] = true
			found = append(found, itemMessage(k))
		}

		if err := addVulnerabilities(ctx, d, m, found); err != nil {
			return nil, err
		}

		out = append(out, found...)
	}

	return out, nil
}

func virtualHolder(ctx context.Context, d Dependencies, r repository.Repository, p string) (string, error) {
	members, err := storingMembers(ctx, d, r)
	if err != nil {
		return "", err
	}

	for _, m := range members {
		if err := can(ctx, d, m, p, auth.ActionRead); hiddenMember(err) {
			continue
		} else if err != nil {
			return "", err
		}

		_, err := d.Repositories.Stat(ctx, m, p)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if err != nil {
			return "", err
		}

		return m, nil
	}

	return "", repository.ErrNotFound
}
