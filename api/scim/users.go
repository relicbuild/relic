package scim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"relic/internal/access/sso"
	"relic/internal/platform/httpserver"
)

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	name, err := equalsFilter(r.URL.Query().Get("filter"), "userName")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}

	start, count := page(r)

	users, total, err := h.dir.Users(r.Context(), name, start-1, count)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	resources := make([]any, 0, len(users))
	for _, u := range users {
		resources = append(resources, wireUser(r, u))
	}

	write(w, http.StatusOK, list(total, start, resources))
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.dir.User(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	write(w, http.StatusOK, wireUser(r, u))
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var in userResource
	if !read(w, r, &in) {
		return
	}

	httpserver.SetAuditTarget(r.Context(), strings.TrimSpace(in.UserName))

	u, err := h.dir.CreateUser(r.Context(), sso.DirectoryUser{
		UserName: strings.TrimSpace(in.UserName),
		Email:    primaryEmail(in.Emails),
		Active:   in.Active == nil || *in.Active,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	out := wireUser(r, u)
	w.Header().Set("Location", out.Meta.Location)
	write(w, http.StatusCreated, out)
}

func (h *Handler) replaceUser(w http.ResponseWriter, r *http.Request) {
	var in userResource
	if !read(w, r, &in) {
		return
	}

	h.auditUser(r)

	u, err := h.dir.UpdateUser(r.Context(), sso.DirectoryUser{
		ID:       r.PathValue("id"),
		UserName: strings.TrimSpace(in.UserName),
		Email:    primaryEmail(in.Emails),
		Active:   in.Active == nil || *in.Active,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	write(w, http.StatusOK, wireUser(r, u))
}

type patchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type patchRequest struct {
	Operations []patchOperation `json:"Operations"`
}

// boolValue reads a boolean, also as the strings some providers send.
func boolValue(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if parsed, err := strconv.ParseBool(strings.ToLower(s)); err == nil {
			return parsed, nil
		}
	}

	return false, fmt.Errorf("%w: %s is not a boolean", sso.ErrInvalidValue, raw)
}

func stringValue(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%w: %s is not a string", sso.ErrInvalidValue, raw)
	}

	return s, nil
}

var emailFilter = regexp.MustCompile(`(?i)^emails\[\s*(type|value)\s+eq\s+"((?:[^"\\]|\\.)*)"\s*\]`)

func selectsKeptEmail(path, keptEmail string) bool {
	m := emailFilter.FindStringSubmatch(strings.TrimSpace(path))
	if m == nil {
		return true
	}

	value, err := filterValue(m[2])
	if err != nil {
		return true
	}

	if strings.EqualFold(m[1], "type") {
		return strings.EqualFold(value, "work")
	}

	return strings.EqualFold(value, keptEmail)
}

// applyUserPatch applies one add or replace of a user attribute to u.
// Attributes Relic does not keep (names, titles, external IDs, extensions)
// are accepted and ignored.
func applyUserPatch(u *sso.DirectoryUser, path string, value json.RawMessage) error {
	var err error

	switch p := strings.ToLower(strings.TrimSpace(path)); {
	case p == "active":
		u.Active, err = boolValue(value)
	case p == "username":
		u.UserName, err = stringValue(value)
	case p == "emails":
		var emails []email
		if err := json.Unmarshal(value, &emails); err != nil {
			return fmt.Errorf("%w: emails is not a list", sso.ErrInvalidValue)
		}

		u.Email = primaryEmail(emails)
	case strings.HasPrefix(p, "emails[") && strings.HasSuffix(p, "].value") && selectsKeptEmail(path, u.Email):
		u.Email, err = stringValue(value)
	case p == "":
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(value, &attrs); err != nil {
			return fmt.Errorf("%w: a patch without a path needs an object", sso.ErrInvalidValue)
		}

		for k, v := range attrs {
			if err := applyUserPatch(u, k, v); err != nil {
				return err
			}
		}
	}

	return err
}

func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	var in patchRequest
	if !read(w, r, &in) {
		return
	}

	before, err := h.dir.User(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	httpserver.SetAuditTarget(r.Context(), before.UserName)

	u := before

	for _, op := range in.Operations {
		switch strings.ToLower(op.Op) {
		case "add", "replace":
			err = applyUserPatch(&u, op.Path, op.Value)
		case "remove":
			if p := strings.ToLower(strings.TrimSpace(op.Path)); p == "emails" ||
				strings.HasPrefix(p, "emails[") && selectsKeptEmail(op.Path, u.Email) {
				u.Email = ""
			}
		default:
			err = fmt.Errorf("%w: operation %q", sso.ErrInvalidValue, op.Op)
		}

		if err != nil {
			h.fail(w, r, err)
			return
		}
	}

	u, err = h.dir.PatchUser(r.Context(), before, u)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	write(w, http.StatusOK, wireUser(r, u))
}

func (h *Handler) auditUser(r *http.Request) {
	if u, err := h.dir.User(r.Context(), r.PathValue("id")); err == nil {
		httpserver.SetAuditTarget(r.Context(), u.UserName)
	}
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	h.auditUser(r)

	if err := h.dir.DeleteUser(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
