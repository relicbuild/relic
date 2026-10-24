package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/access/security"
	"relic/internal/platform/httpserver"
)

// SetSecurity enables the Access v2 users, groups and permissions APIs.
func (h *Handler) SetSecurity(s *security.Service) { h.security = s }

func (h *Handler) registerV2(rt *httpserver.Router) {
	rt.HandleFunc("GET /access/api/v2/users", auth.Admin, h.v2ListUsers)
	rt.HandleFunc("POST /access/api/v2/users", auth.Admin, h.v2CreateUser)
	rt.HandleFunc("GET /access/api/v2/users/{username}", auth.Admin, h.v2GetUser)
	rt.HandleFunc("PATCH /access/api/v2/users/{username}", auth.Admin, h.v2UpdateUser)
	rt.HandleFunc("DELETE /access/api/v2/users/{username}", auth.Admin, h.v2DeleteUser)
	rt.HandleFunc("PATCH /access/api/v2/users/{username}/groups", auth.Admin, h.v2UserGroups)

	rt.HandleFunc("GET /access/api/v2/groups", auth.Admin, h.v2ListGroups)
	rt.HandleFunc("POST /access/api/v2/groups", auth.Admin, h.v2CreateGroup)
	rt.HandleFunc("GET /access/api/v2/groups/{name}", auth.Admin, h.v2GetGroup)
	rt.HandleFunc("PATCH /access/api/v2/groups/{name}", auth.Admin, h.v2UpdateGroup)
	rt.HandleFunc("DELETE /access/api/v2/groups/{name}", auth.Admin, h.v2DeleteGroup)
	rt.HandleFunc("PATCH /access/api/v2/groups/{name}/members", auth.Admin, h.v2GroupMembers)

	rt.HandleFunc("GET /access/api/v2/permissions", auth.Admin, h.v2ListPermissions)
	rt.HandleFunc("POST /access/api/v2/permissions", auth.Admin, h.v2CreatePermission)
	rt.HandleFunc("GET /access/api/v2/permissions/{name}", auth.Admin, h.v2GetPermission)
	rt.HandleFunc("DELETE /access/api/v2/permissions/{name}", auth.Admin, h.v2DeletePermission)
	rt.HandleFunc("GET /access/api/v2/permissions/{name}/{resource}", auth.Admin, h.v2GetPermissionResource)
	rt.HandleFunc("PUT /access/api/v2/permissions/{name}/{resource}", auth.Admin, h.v2PutPermissionResource)
	rt.HandleFunc("PATCH /access/api/v2/permissions/{name}/{resource}", auth.Admin, h.v2PatchPermissionResource)
	rt.HandleFunc("DELETE /access/api/v2/permissions/{name}/{resource}", auth.Admin, h.v2DeletePermissionResource)
}

func accessError(w http.ResponseWriter, status int, message string) {
	code := map[int]string{
		http.StatusBadRequest:          "BAD_REQUEST",
		http.StatusNotFound:            "NOT_FOUND",
		http.StatusConflict:            "CONFLICT",
		http.StatusInternalServerError: "INTERNAL_ERROR",
	}[status]

	writeJSON(w, status, map[string]any{
		"errors": []map[string]string{
			{
				"code":    code,
				"message": message,
			},
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // plain values
}

func (h *Handler) securityError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, security.ErrNotFound):
		accessError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, security.ErrExists):
		accessError(w, http.StatusConflict, err.Error())
	case errors.Is(err, security.ErrInvalid):
		accessError(w, http.StatusBadRequest, err.Error())
	default:
		h.log.ErrorContext(r.Context(), "access v2", "error", err)
		accessError(w, http.StatusInternalServerError, "internal error")
	}
}

func decodeV2(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		accessError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())

		return false
	}

	return true
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	return scheme + "://" + r.Host
}

func limit[T any](r *http.Request, list []T) []T {
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n >= 0 && n < len(list) {
		return list[:n]
	}

	return list
}

const userExtension = "access"

type v2User struct {
	Username                 string   `json:"username"`
	Email                    string   `json:"email"`
	Password                 string   `json:"password,omitempty"`
	Admin                    *bool    `json:"admin,omitempty"`
	ProfileUpdatable         *bool    `json:"profile_updatable,omitempty"`
	DisableUIAccess          *bool    `json:"disable_ui_access,omitempty"`
	InternalPasswordDisabled *bool    `json:"internal_password_disabled,omitempty"`
	Groups                   []string `json:"groups,omitempty"`
	Realm                    string   `json:"realm,omitempty"`
	Status                   string   `json:"status,omitempty"`
}

type userExtras struct {
	ProfileUpdatable         *bool `json:"profile_updatable,omitempty"`
	DisableUIAccess          *bool `json:"disable_ui_access,omitempty"`
	InternalPasswordDisabled *bool `json:"internal_password_disabled,omitempty"`
}

func boolOr(b *bool, def bool) *bool {
	if b == nil {
		return &def
	}

	return b
}

func v2UserOf(u security.User) v2User {
	var x userExtras
	if raw := u.Extensions[userExtension]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &x)
	}

	return v2User{
		Username:                 u.Name,
		Email:                    u.Email,
		Admin:                    &u.Admin,
		ProfileUpdatable:         boolOr(x.ProfileUpdatable, true),
		DisableUIAccess:          boolOr(x.DisableUIAccess, false),
		InternalPasswordDisabled: boolOr(x.InternalPasswordDisabled, false),
		Groups:                   nonNil(u.Groups),
		Realm:                    cmpOr(u.Realm, "internal"),
		Status:                   "enabled",
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

func applyUser(u *security.User, in v2User) error {
	if in.Email != "" {
		u.Email = in.Email
	}

	if in.Password != "" {
		u.Password = in.Password
	}

	if in.Admin != nil {
		u.Admin = *in.Admin
	}

	if in.Groups != nil {
		u.Groups = in.Groups
	}

	var x userExtras
	if raw := u.Extensions[userExtension]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &x)
	}

	if in.ProfileUpdatable != nil {
		x.ProfileUpdatable = in.ProfileUpdatable
	}

	if in.DisableUIAccess != nil {
		x.DisableUIAccess = in.DisableUIAccess
	}

	if in.InternalPasswordDisabled != nil {
		x.InternalPasswordDisabled = in.InternalPasswordDisabled
	}

	raw, err := json.Marshal(x)
	if err != nil {
		return err
	}

	if u.Extensions == nil {
		u.Extensions = map[string]json.RawMessage{}
	}

	u.Extensions[userExtension] = raw

	return nil
}

func (h *Handler) v2ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.security.ListUserRealms(r.Context())
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	out := []map[string]string{}
	for _, u := range limit(r, users) {
		out = append(out, map[string]string{
			"username": u.Name,
			"uri":      baseURL(r) + "/access/api/v2/users/" + url.PathEscape(u.Name),
			"realm":    cmpOr(u.Realm, "internal"),
			"status":   "enabled",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"users": out,
	})
}

func (h *Handler) v2CreateUser(w http.ResponseWriter, r *http.Request) {
	var in v2User
	if !decodeV2(w, r, &in) {
		return
	}

	if in.Username == "" || in.Email == "" {
		accessError(w, http.StatusBadRequest, "username and email are required")

		return
	}

	if _, err := h.security.GetUser(r.Context(), in.Username); err == nil {
		accessError(w, http.StatusConflict, "user "+in.Username+" already exists")

		return
	}

	u := security.User{
		Name: in.Username,
	}
	if err := applyUser(&u, in); err != nil {
		h.securityError(w, r, err)

		return
	}

	err := h.security.CreateUser(r.Context(), u)
	if errors.Is(err, security.ErrExists) {
		accessError(w, http.StatusConflict, "user "+in.Username+" already exists")

		return
	}

	if err != nil {
		h.securityError(w, r, err)

		return
	}

	h.writeUser(w, r, in.Username, http.StatusCreated)
}

func (h *Handler) writeUser(w http.ResponseWriter, r *http.Request, name string, status int) {
	u, err := h.security.GetUser(r.Context(), name)
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	writeJSON(w, status, v2UserOf(u))
}

func (h *Handler) v2GetUser(w http.ResponseWriter, r *http.Request) {
	h.writeUser(w, r, r.PathValue("username"), http.StatusOK)
}

func (h *Handler) v2UpdateUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")

	u, err := h.security.GetUser(r.Context(), name)
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	var in v2User
	if !decodeV2(w, r, &in) {
		return
	}

	if err := applyUser(&u, in); err != nil {
		h.securityError(w, r, err)

		return
	}

	if _, err := h.security.PutUser(r.Context(), u); err != nil {
		h.securityError(w, r, err)

		return
	}

	h.writeUser(w, r, name, http.StatusOK)
}

func (h *Handler) v2DeleteUser(w http.ResponseWriter, r *http.Request) {
	if err := h.security.DeleteUser(r.Context(), r.PathValue("username")); err != nil {
		h.securityError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type addRemove struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

func (c addRemove) apply(list []string) []string {
	out := slices.DeleteFunc(slices.Clone(list), func(s string) bool {
		return slices.Contains(c.Remove, s)
	})

	for _, a := range c.Add {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}

	return nonNil(out)
}

func (h *Handler) v2UserGroups(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")

	u, err := h.security.GetUser(r.Context(), name)
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	var in addRemove
	if !decodeV2(w, r, &in) {
		return
	}

	u.Groups = in.apply(u.Groups)
	u.Password = ""

	if _, err := h.security.PutUser(r.Context(), u); err != nil {
		h.securityError(w, r, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"username": name,
		"groups":   u.Groups,
	})
}

type v2Group struct {
	Name            string   `json:"name"`
	Description     *string  `json:"description,omitempty"`
	AutoJoin        *bool    `json:"auto_join,omitempty"`
	AdminPrivileges *bool    `json:"admin_privileges,omitempty"`
	ExternalID      string   `json:"external_id"`
	Realm           string   `json:"realm"`
	RealmAttributes string   `json:"realm_attributes"`
	Members         []string `json:"members"`
}

func v2GroupOf(g security.Group) v2Group {
	return v2Group{
		Name:            g.Name,
		Description:     &g.Description,
		AutoJoin:        &g.DefaultForNewUsers,
		AdminPrivileges: &g.Admin,
		Realm:           "internal",
		Members:         nonNil(g.Members),
	}
}

func applyGroup(g *security.Group, in v2Group) {
	if in.Description != nil {
		g.Description = *in.Description
	}

	if in.AutoJoin != nil {
		g.DefaultForNewUsers = *in.AutoJoin
	}

	if in.AdminPrivileges != nil {
		g.Admin = *in.AdminPrivileges
	}
}

func (h *Handler) v2ListGroups(w http.ResponseWriter, r *http.Request) {
	names, err := h.security.ListGroups(r.Context())
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	out := []map[string]string{}
	for _, n := range limit(r, names) {
		out = append(out, map[string]string{
			"group_name": n,
			"uri":        baseURL(r) + "/access/api/v2/groups/" + url.PathEscape(n),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"groups": out,
	})
}

func (h *Handler) writeGroup(w http.ResponseWriter, r *http.Request, name string, status int) {
	g, err := h.security.GetGroup(r.Context(), name, true)
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	writeJSON(w, status, v2GroupOf(g))
}

func (h *Handler) v2CreateGroup(w http.ResponseWriter, r *http.Request) {
	var in v2Group
	if !decodeV2(w, r, &in) {
		return
	}

	if in.Name == "" {
		accessError(w, http.StatusBadRequest, "name is required")

		return
	}

	if _, err := h.security.GetGroup(r.Context(), in.Name, false); err == nil {
		accessError(w, http.StatusConflict, "group "+in.Name+" already exists")

		return
	}

	g := security.Group{
		Name: in.Name,
	}
	applyGroup(&g, in)

	err := h.security.CreateGroup(r.Context(), g, nonNil(in.Members))
	if errors.Is(err, security.ErrExists) {
		accessError(w, http.StatusConflict, "group "+in.Name+" already exists")

		return
	}

	if err != nil {
		h.securityError(w, r, err)

		return
	}

	h.writeGroup(w, r, in.Name, http.StatusCreated)
}

func (h *Handler) v2GetGroup(w http.ResponseWriter, r *http.Request) {
	h.writeGroup(w, r, r.PathValue("name"), http.StatusOK)
}

func (h *Handler) v2UpdateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	g, err := h.security.GetGroup(r.Context(), name, true)
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	var in v2Group
	if !decodeV2(w, r, &in) {
		return
	}

	applyGroup(&g, in)

	members := g.Members
	if in.Members != nil {
		members = in.Members
	}

	if _, err := h.security.PutGroup(r.Context(), g, nonNil(members)); err != nil {
		h.securityError(w, r, err)

		return
	}

	h.writeGroup(w, r, name, http.StatusOK)
}

func (h *Handler) v2DeleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := h.security.DeleteGroup(r.Context(), r.PathValue("name")); err != nil {
		h.securityError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) v2GroupMembers(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	g, err := h.security.GetGroup(r.Context(), name, true)
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	var in addRemove
	if !decodeV2(w, r, &in) {
		return
	}

	members := in.apply(g.Members)
	if _, err := h.security.PutGroup(r.Context(), g, members); err != nil {
		h.securityError(w, r, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"members": members,
	})
}

type v2Resource struct {
	Actions struct {
		Users  map[string][]string `json:"users,omitempty"`
		Groups map[string][]string `json:"groups,omitempty"`
	} `json:"actions"`
	Targets map[string]v2Target `json:"targets"`
}

func (res *v2Resource) mergePatch(patch any) (*v2Resource, error) {
	stored, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("encode the stored resource: %w", err)
	}

	var document any
	if err := json.Unmarshal(stored, &document); err != nil {
		return nil, fmt.Errorf("decode the stored resource: %w", err)
	}

	patched, err := json.Marshal(mergePatch(document, patch))
	if err != nil {
		return nil, fmt.Errorf("encode the patched resource: %w", err)
	}

	merged := &v2Resource{}
	if err := json.Unmarshal(patched, merged); err != nil {
		return nil, fmt.Errorf("invalid resource: %w", err)
	}

	return merged, nil
}

func mergePatch(stored, patch any) any {
	patchObject, isObject := patch.(map[string]any)
	if !isObject {
		return patch
	}

	storedObject, isObject := stored.(map[string]any)
	if !isObject {
		storedObject = map[string]any{}
	}

	for key, value := range patchObject {
		if value == nil {
			delete(storedObject, key)

			continue
		}

		storedObject[key] = mergePatch(storedObject[key], value)
	}

	return storedObject
}

type v2Target struct {
	Include []string `json:"include_patterns"`
	Exclude []string `json:"exclude_patterns"`
}

type v2Permission struct {
	Name      string                 `json:"name"`
	Resources map[string]*v2Resource `json:"resources"`
}

var actionNames = map[string]string{
	"READ":                  "read",
	"WRITE":                 "write",
	"ANNOTATE":              "annotate",
	"DELETE":                "delete",
	"MANAGE":                "manage",
	"MANAGED_XRAY_METADATA": "managedXrayMeta",
	"DISTRIBUTE":            "distribute",
}

var resourceTypes = []string{
	"artifact",
	"build",
	"release_bundle",
}

func convertActions(m map[string][]string, to func(string) (string, bool)) (map[string][]string, error) {
	if m == nil {
		return nil, nil
	}

	out := map[string][]string{}

	for who, actions := range m {
		for _, a := range actions {
			name, ok := to(a)
			if !ok {
				return nil, fmt.Errorf("unknown action %q", a)
			}

			out[who] = append(out[who], name)
		}
	}

	return out, nil
}

func toSection(res *v2Resource) (*Section, error) {
	s := &Section{
		Repositories: slices.Sorted(maps.Keys(res.Targets)),
		Actions:      &Actions{},
	}

	for i, name := range s.Repositories {
		t := res.Targets[name]
		if i == 0 {
			s.IncludePatterns, s.ExcludePatterns = t.Include, t.Exclude

			continue
		}

		if !slices.Equal(t.Include, s.IncludePatterns) || !slices.Equal(t.Exclude, s.ExcludePatterns) {
			return nil, errors.New(
				"relic keeps one include and one exclude list per resource: give every target the same patterns",
			)
		}
	}

	upper := func(a string) (string, bool) {
		n, ok := actionNames[strings.ToUpper(a)]

		return n, ok
	}

	var err error
	if s.Actions.Users, err = convertActions(res.Actions.Users, upper); err != nil {
		return nil, err
	}

	if s.Actions.Groups, err = convertActions(res.Actions.Groups, upper); err != nil {
		return nil, err
	}

	return s, nil
}

func fromSection(s *Section) *v2Resource {
	res := &v2Resource{
		Targets: map[string]v2Target{},
	}

	for _, repo := range s.Repositories {
		res.Targets[repo] = v2Target{
			Include: nonNil(s.IncludePatterns),
			Exclude: nonNil(s.ExcludePatterns),
		}
	}

	lower := func(a string) (string, bool) {
		for k, v := range actionNames {
			if v == a {
				return k, true
			}
		}

		return strings.ToUpper(a), true
	}

	if s.Actions != nil {
		res.Actions.Users, _ = convertActions(s.Actions.Users, lower)
		res.Actions.Groups, _ = convertActions(s.Actions.Groups, lower)
	}

	return res
}

func sectionOf(t *Target, resource string) **Section {
	switch resource {
	case "artifact":
		return &t.Repo
	case "build":
		return &t.Build
	case "release_bundle":
		return &t.ReleaseBundle
	}

	return nil
}

func toTarget(p v2Permission) (Target, error) {
	t := Target{
		Name: p.Name,
	}

	for resource, res := range p.Resources {
		slot := sectionOf(&t, resource)
		if slot == nil {
			return t, fmt.Errorf("relic keeps no %s permissions (it has no distribution destinations)", resource)
		}

		if res == nil {
			continue
		}

		s, err := toSection(res)
		if err != nil {
			return t, fmt.Errorf("%s: %w", resource, err)
		}

		*slot = s
	}

	return t, nil
}

func fromTarget(t Target) v2Permission {
	p := v2Permission{
		Name:      t.Name,
		Resources: map[string]*v2Resource{},
	}

	for _, resource := range resourceTypes {
		if s := *sectionOf(&t, resource); s != nil {
			p.Resources[resource] = fromSection(s)
		}
	}

	return p
}

func (h *Handler) loadPermission(w http.ResponseWriter, r *http.Request, name string) (Target, bool) {
	p, err := h.security.GetPermission(r.Context(), name)
	if err != nil {
		h.securityError(w, r, err)

		return Target{}, false
	}

	doc, err := PermissionToArtifactory(p)
	if err != nil {
		h.securityError(w, r, err)

		return Target{}, false
	}

	var t Target
	if err := json.Unmarshal(doc, &t); err != nil {
		h.securityError(w, r, err)

		return Target{}, false
	}

	t.Name = name

	return t, true
}

func (h *Handler) savePermission(w http.ResponseWriter, r *http.Request, t Target, create bool) bool {
	doc, err := json.Marshal(t)
	if err != nil {
		h.securityError(w, r, err)

		return false
	}

	p, err := PermissionFromArtifactory(t.Name, doc)
	if err != nil {
		accessError(w, http.StatusBadRequest, err.Error())

		return false
	}

	if create {
		err = h.security.CreatePermission(r.Context(), p)
	} else {
		err = h.security.UpdatePermission(r.Context(), p)
	}

	if err != nil {
		h.securityError(w, r, err)

		return false
	}

	return true
}

func (h *Handler) v2ListPermissions(w http.ResponseWriter, r *http.Request) {
	names, err := h.security.ListPermissions(r.Context())
	if err != nil {
		h.securityError(w, r, err)

		return
	}

	out := []map[string]string{}
	for _, n := range limit(r, names) {
		out = append(out, map[string]string{
			"name": n,
			"uri":  baseURL(r) + "/access/api/v2/permissions/" + url.PathEscape(n),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"permissions": out,
	})
}

func (h *Handler) v2CreatePermission(w http.ResponseWriter, r *http.Request) {
	var in v2Permission
	if !decodeV2(w, r, &in) {
		return
	}

	if in.Name == "" {
		accessError(w, http.StatusBadRequest, "name is required")

		return
	}

	t, err := toTarget(in)
	if err != nil {
		accessError(w, http.StatusBadRequest, err.Error())

		return
	}

	if !h.savePermission(w, r, t, true) {
		return
	}

	h.writePermission(w, r, in.Name, http.StatusCreated)
}

func (h *Handler) writePermission(w http.ResponseWriter, r *http.Request, name string, status int) {
	t, ok := h.loadPermission(w, r, name)
	if !ok {
		return
	}

	writeJSON(w, status, fromTarget(t))
}

func (h *Handler) v2GetPermission(w http.ResponseWriter, r *http.Request) {
	h.writePermission(w, r, r.PathValue("name"), http.StatusOK)
}

func (h *Handler) v2DeletePermission(w http.ResponseWriter, r *http.Request) {
	if err := h.security.DeletePermission(r.Context(), r.PathValue("name")); err != nil {
		h.securityError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) permissionResource(
	w http.ResponseWriter,
	r *http.Request,
) (*Target, **Section, bool) {
	t, ok := h.loadPermission(w, r, r.PathValue("name"))
	if !ok {
		return nil, nil, false
	}

	slot := sectionOf(&t, r.PathValue("resource"))
	if slot == nil {
		accessError(w, http.StatusBadRequest, "unknown resource type "+r.PathValue("resource"))

		return nil, nil, false
	}

	return &t, slot, true
}

func (h *Handler) v2GetPermissionResource(w http.ResponseWriter, r *http.Request) {
	_, slot, ok := h.permissionResource(w, r)
	if !ok {
		return
	}

	if *slot == nil {
		accessError(w, http.StatusNotFound, "the permission has no "+r.PathValue("resource")+" resource")

		return
	}

	writeJSON(w, http.StatusOK, fromSection(*slot))
}

func (h *Handler) v2PutPermissionResource(w http.ResponseWriter, r *http.Request) {
	t, slot, ok := h.permissionResource(w, r)
	if !ok {
		return
	}

	var in v2Resource
	if !decodeV2(w, r, &in) {
		return
	}

	s, err := toSection(&in)
	if err != nil {
		accessError(w, http.StatusBadRequest, err.Error())

		return
	}

	*slot = s
	if h.savePermission(w, r, *t, false) {
		writeJSON(w, http.StatusOK, fromSection(s))
	}
}

func (h *Handler) v2PatchPermissionResource(w http.ResponseWriter, r *http.Request) {
	t, slot, ok := h.permissionResource(w, r)
	if !ok {
		return
	}

	current := &v2Resource{
		Targets: map[string]v2Target{},
	}
	if *slot != nil {
		current = fromSection(*slot)
	}

	var patch any
	if !decodeV2(w, r, &patch) {
		return
	}

	merged, err := current.mergePatch(patch)
	if err != nil {
		accessError(w, http.StatusBadRequest, err.Error())

		return
	}

	s, err := toSection(merged)
	if err != nil {
		accessError(w, http.StatusBadRequest, err.Error())

		return
	}

	*slot = s
	if h.savePermission(w, r, *t, false) {
		writeJSON(w, http.StatusOK, fromSection(s))
	}
}

func (h *Handler) v2DeletePermissionResource(w http.ResponseWriter, r *http.Request) {
	t, slot, ok := h.permissionResource(w, r)
	if !ok {
		return
	}

	*slot = nil

	if h.savePermission(w, r, *t, false) {
		w.WriteHeader(http.StatusNoContent)
	}
}
