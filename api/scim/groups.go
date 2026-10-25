package scim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"relic/internal/access/sso"
	"relic/internal/platform/httpserver"
)

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	name, err := equalsFilter(r.URL.Query().Get("filter"), "displayName")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}

	start, count := page(r)

	groups, total, err := h.dir.Groups(r.Context(), name, start-1, count)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	resources := make([]any, 0, len(groups))
	for _, g := range groups {
		resources = append(resources, wireGroup(r, g))
	}

	write(w, http.StatusOK, list(total, start, resources))
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	g, err := h.dir.Group(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	write(w, http.StatusOK, wireGroup(r, g))
}

func memberIDs(members []member) []string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.Value)
	}

	return ids
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	var in groupResource
	if !read(w, r, &in) {
		return
	}

	if strings.TrimSpace(in.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "displayName is required")
		return
	}

	httpserver.SetAuditTarget(r.Context(), strings.TrimSpace(in.DisplayName))

	g, err := h.dir.CreateGroup(r.Context(), strings.TrimSpace(in.DisplayName), memberIDs(in.Members))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	out := wireGroup(r, g)
	w.Header().Set("Location", out.Meta.Location)
	write(w, http.StatusCreated, out)
}

func renamed(id, displayName string) error {
	if name := strings.TrimSpace(displayName); name != "" && !strings.EqualFold(name, id) {
		return fmt.Errorf("%w: the name of group %s", sso.ErrImmutable, id)
	}

	return nil
}

func (h *Handler) replaceGroup(w http.ResponseWriter, r *http.Request) {
	var in groupResource
	if !read(w, r, &in) {
		return
	}

	id := r.PathValue("id")
	if err := renamed(id, in.DisplayName); err != nil {
		h.fail(w, r, err)
		return
	}

	if err := h.dir.ChangeMembers(r.Context(), id, memberIDs(in.Members), nil, true); err != nil {
		h.fail(w, r, err)
		return
	}

	h.getGroup(w, r)
}

var memberFilter = regexp.MustCompile(`(?i)^members\[\s*value\s+eq\s+"((?:[^"\\]|\\.)*)"\s*\]$`)

// membersChange is what a group patch does to the members it manages.
type membersChange struct {
	add, remove []string
	replace     bool
}

func readMembers(value json.RawMessage) ([]string, error) {
	var members []member
	if err := json.Unmarshal(value, &members); err != nil {
		return nil, fmt.Errorf("%w: members is not a list", sso.ErrInvalidValue)
	}

	return memberIDs(members), nil
}

func withoutMembers(ids []string, dropped ...string) []string {
	return slices.DeleteFunc(ids, func(id string) bool {
		return slices.Contains(dropped, id)
	})
}

func (c *membersChange) apply(id string, op patchOperation) error {
	path := strings.TrimSpace(op.Path)

	switch verb := strings.ToLower(op.Op); {
	case verb == "remove" && memberFilter.MatchString(path):
		value, err := filterValue(memberFilter.FindStringSubmatch(path)[1])
		if err != nil {
			return err
		}

		c.add = withoutMembers(c.add, value)
		c.remove = append(c.remove, value)
	case strings.EqualFold(path, "members"):
		if verb == "remove" && len(op.Value) == 0 {
			c.add, c.remove, c.replace = nil, nil, true
			return nil
		}

		ids, err := readMembers(op.Value)
		if err != nil {
			return err
		}

		switch verb {
		case "add":
			c.remove = withoutMembers(c.remove, ids...)
			c.add = append(c.add, ids...)
		case "remove":
			c.add = withoutMembers(c.add, ids...)
			c.remove = append(c.remove, ids...)
		case "replace":
			c.add, c.remove, c.replace = ids, nil, true
		default:
			return fmt.Errorf("%w: operation %q", sso.ErrInvalidValue, op.Op)
		}
	case strings.EqualFold(path, "displayName"):
		name, err := stringValue(op.Value)
		if err != nil {
			return err
		}

		return renamed(id, name)
	case path == "" && (verb == "add" || verb == "replace"):
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(op.Value, &attrs); err != nil {
			return fmt.Errorf("%w: a patch without a path needs an object", sso.ErrInvalidValue)
		}

		for k, v := range attrs {
			if err := c.apply(id, patchOperation{
				Op:    op.Op,
				Path:  k,
				Value: v,
			}); err != nil {
				return err
			}
		}
	case path == "" || strings.HasPrefix(strings.ToLower(path), "members") ||
		verb != "add" && verb != "replace" && verb != "remove":
		return fmt.Errorf("%w: cannot %s %q on a group", sso.ErrInvalidValue, op.Op, path)
	}

	return nil
}

func (h *Handler) patchGroup(w http.ResponseWriter, r *http.Request) {
	var in patchRequest
	if !read(w, r, &in) {
		return
	}

	id := r.PathValue("id")

	var change membersChange
	for _, op := range in.Operations {
		if err := change.apply(id, op); err != nil {
			h.fail(w, r, err)
			return
		}
	}

	if err := h.dir.ChangeMembers(r.Context(), id, change.add, change.remove, change.replace); err != nil {
		h.fail(w, r, err)
		return
	}

	h.getGroup(w, r)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := h.dir.DeleteGroup(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
