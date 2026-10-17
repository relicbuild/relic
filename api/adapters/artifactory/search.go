package artifactory

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"relic/api/adapters/artifactory/aql"
	"relic/api/adapters/artifactory/layout"
	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
)

const maxAQLBytes = 1 << 20

func (h *Handler) readFilter(r *http.Request) (func(aql.ItemRow) bool, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return nil, nil
	}

	perms, err := h.security.Permissions(r.Context())
	if err != nil {
		return nil, err
	}

	repos, err := h.repos.ListRepositories(r.Context(), "", "")
	if err != nil {
		return nil, err
	}

	class := make(map[string]string, len(repos))
	for _, rp := range repos {
		class[rp.Name] = string(rp.Class)
	}

	subject := p.Subject()

	return func(row aql.ItemRow) bool {
		itemPath := row.Name
		if row.Path != "." {
			itemPath = row.Path + "/" + row.Name
		}

		return perms.CanInRepository(
			subject,
			auth.Repository{
				Name:  row.Repository,
				Class: class[row.Repository],
			},
			itemPath,
			auth.ActionRead,
		)
	}, nil
}

const aqlTimeout = time.Minute

func (h *Handler) searchAQL(w http.ResponseWriter, r *http.Request) {
	src, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAQLBytes))
	if err != nil {
		wire.WriteBodyError(w, err, "AQL query too large")
		return
	}

	q, err := aql.Parse(string(src))
	if err != nil {
		wire.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	q.Criteria = aql.ExpandRepos(q.Criteria, h.virtualMembers(r.Context()))

	plan, err := aql.PlanItems(q)
	if err != nil {
		status := http.StatusBadRequest

		if errors.Is(err, aql.ErrUnsupported) {
			h.log.WarnContext(r.Context(), "unsupported AQL", "query", string(src), "error", err)
		}

		wire.WriteError(w, status, err.Error())

		return
	}

	filter, err := h.readFilter(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	filter = withoutTrash(filter, q.Criteria, h.repos.TrashRepository())
	plan.ShowRepo = h.cacheKeys(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), aqlTimeout)
	defer cancel()

	w.Header().Set("Content-Type", "application/json")

	out := &countingWriter{
		w: w,
	}
	if err := aql.WriteItems(ctx, h.aql, plan, out, filter); err != nil {
		// Part of the body may be out; the client sees invalid JSON.
		h.log.ErrorContext(r.Context(), "AQL execution failed", "error", err)

		if out.n == 0 {
			wire.WriteError(w, http.StatusInternalServerError, "AQL execution failed")
		}
	}
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)

	return n, err
}

func withoutTrash(keep func(aql.ItemRow) bool, criteria aql.Node, trash string) func(aql.ItemRow) bool {
	if namesRepository(criteria, trash) {
		return keep
	}

	return func(row aql.ItemRow) bool {
		return row.Repository != trash && (keep == nil || keep(row))
	}
}

func namesRepository(n aql.Node, key string) bool {
	names := func(c aql.Node) bool {
		return namesRepository(c, key)
	}

	switch n := n.(type) {
	case aql.And:
		return slices.ContainsFunc(n, names)
	case aql.Or:
		return slices.ContainsFunc(n, names)
	case aql.Compare:
		return n.Field == "repo" && (n.Op == aql.OpEq || n.Op == aql.OpMatch) && n.Value == key
	default:
		return false
	}
}

func (h *Handler) cacheKeys(ctx context.Context) func(string) string {
	remotes := map[string]bool{}

	list, err := h.repos.ListRepositories(ctx, "remote", "")
	if err != nil {
		h.log.WarnContext(ctx, "AQL results show remote repositories without their cache suffix", "error", err)
	}

	for _, rp := range list {
		remotes[rp.Name] = true
	}

	return func(key string) string {
		if remotes[key] {
			return key + layout.CacheSuffix
		}

		return key
	}
}

func (h *Handler) virtualMembers(ctx context.Context) func(string) []string {
	return func(key string) []string {
		rp, err := h.repos.GetRepository(ctx, key)
		if err != nil {
			if remote, ok := strings.CutSuffix(key, "-cache"); ok {
				if rp, err := h.repos.GetRepository(ctx, remote); err == nil && rp.Class == "remote" {
					return []string{
						remote,
					}
				}
			}

			return []string{
				key,
			}
		}

		if rp.Class != "virtual" {
			return []string{
				key,
			}
		}

		var keys []string
		for _, m := range h.packages.ReadRepos(ctx, rp) {
			keys = append(keys, m.Name)
		}

		return keys
	}
}
