package artifactory

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"strconv"
	"time"

	"relic/api/adapters/artifactory/access"
	"relic/api/adapters/artifactory/permissions"
	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/access/security"
)

// Success is exactly 200.
func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid form body")
		return
	}

	ctx := r.Context()
	if r.PostForm.Get("grant_type") == "refresh_token" {
		tok, err := h.tokens.Refresh(ctx, r.PostForm.Get("refresh_token"))
		if errors.Is(err, auth.ErrInvalidToken) {
			wire.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
			return
		}

		if err != nil {
			h.writeError(w, r, err)
			return
		}

		wire.WriteJSON(w, http.StatusOK, access.Response(tok, access.LegacyScope))

		return
	}

	caller := auth.FromContext(ctx)
	if caller.Anonymous() {
		auth.Unauthorized(w, r)
		return
	}

	username := r.PostForm.Get("username")
	if username == "" {
		username = caller.Username
	}

	if username != caller.Username && !caller.Admin {
		wire.WriteError(w, http.StatusForbidden, "only administrators can create tokens for other users")
		return
	}

	expires := time.Hour

	if v := r.PostForm.Get("expires_in"); v != "" {
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil || secs < 0 || secs > math.MaxInt64/int64(time.Second) {
			wire.WriteError(w, http.StatusBadRequest, "invalid expires_in")
			return
		}

		expires = time.Duration(secs) * time.Second
	}

	// Unknown scope parts are ignored on this route.
	scope, _ := access.ParseScope(r.PostForm.Get("scope"))
	if !caller.MayGrant(scope) {
		wire.WriteError(w, http.StatusForbidden, "only administrators can create admin or group-scoped tokens")
		return
	}

	tok, err := h.tokens.Issue(ctx, caller.LimitIssue(auth.IssueParams{
		Username:    username,
		Scope:       scope,
		ExpiresIn:   expires,
		Refreshable: r.PostForm.Get("refreshable") == "true",
		Description: r.PostForm.Get("description"),
	}, time.Now()))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, access.Response(tok, access.LegacyScope))
}

type userJSON struct {
	Name     string    `json:"name"`
	Email    string    `json:"email,omitempty"`
	Password string    `json:"password,omitempty"`
	Admin    *bool     `json:"admin,omitempty"`
	Realm    string    `json:"realm,omitempty"`
	Groups   *[]string `json:"groups,omitempty"`
}

var userFields = []string{
	"name",
	"email",
	"password",
	"admin",
	"realm",
	"groups",
}

const userExtension = "artifactory"

func extraFields(u security.User) map[string]any {
	out := map[string]any{}
	if raw := u.Extensions[userExtension]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}

	return out
}

func withExtraFields(u *security.User, fields map[string]any) error {
	if len(fields) == 0 {
		delete(u.Extensions, userExtension)
		return nil
	}

	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}

	if u.Extensions == nil {
		u.Extensions = map[string]json.RawMessage{}
	}

	u.Extensions[userExtension] = raw

	return nil
}

type nameURI struct {
	Name  string `json:"name"`
	URI   string `json:"uri"`
	Realm string `json:"realm,omitempty"`
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.security.ListUserRealms(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := make([]nameURI, len(users))
	for i, u := range users {
		out[i] = nameURI{
			Name:  u.Name,
			URI:   baseURL(r) + "/api/security/users/" + escapePath(u.Name),
			Realm: u.Realm,
		}
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.security.GetUser(r.Context(), r.PathValue("name"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := extraFields(u)
	out["name"] = u.Name
	out["email"] = u.Email
	out["admin"] = u.Admin
	out["realm"] = u.Realm
	out["groups"] = nonNilList(u.Groups)
	wire.WriteJSON(w, http.StatusOK, out)
}

func nonNilList(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// An absent "admin" means false.
func (h *Handler) putUser(w http.ResponseWriter, r *http.Request) {
	u, ok := h.readUser(w, r)
	if !ok {
		return
	}

	created, err := h.security.PutUser(r.Context(), u.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}

	w.WriteHeader(status)
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	patch, ok := h.readUser(w, r)
	if !ok {
		return
	}

	current, err := h.security.GetUser(r.Context(), patch.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if patch.Email == "" {
		patch.Email = current.Email
	}

	if !patch.adminSet {
		patch.Admin = current.Admin
	}

	patch.Viewer = current.Viewer

	if patch.Groups == nil {
		patch.Groups = current.Groups
	}

	merged := extraFields(current)
	maps.Copy(merged, patch.extra)

	patch.Extensions = current.Extensions
	if err := withExtraFields(&patch.User, merged); err != nil {
		h.writeError(w, r, err)
		return
	}

	if _, err := h.security.PutUser(r.Context(), patch.User); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type userInput struct {
	security.User
	extra    map[string]any
	adminSet bool
}

func (h *Handler) readUser(w http.ResponseWriter, r *http.Request) (userInput, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		wire.WriteBodyError(w, err, "request too large")
		return userInput{}, false
	}

	var (
		u       userJSON
		profile map[string]any
	)
	if err := json.Unmarshal(body, &u); err != nil || json.Unmarshal(body, &profile) != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid user JSON")
		return userInput{}, false
	}

	for _, k := range userFields {
		delete(profile, k)
	}

	name := r.PathValue("name")
	if u.Name != "" && u.Name != name {
		wire.WriteError(w, http.StatusBadRequest, "user name does not match the URL")
		return userInput{}, false
	}

	in := userInput{
		Name:     name,
		Email:    u.Email,
		Password: u.Password,
		extra:    profile,
		adminSet: u.Admin != nil,
	}
	if err := withExtraFields(&in.User, profile); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid user JSON")
		return userInput{}, false
	}

	if u.Admin != nil {
		in.Admin = *u.Admin
	}

	if u.Groups != nil {
		in.Groups = nonNilList(*u.Groups)
	}

	return in, true
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.security.DeleteUser(r.Context(), name); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "User "+name+" deleted.")
}

type groupJSON struct {
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	AutoJoin        *bool     `json:"autoJoin,omitempty"`
	AdminPrivileges *bool     `json:"adminPrivileges,omitempty"`
	Realm           string    `json:"realm,omitempty"`
	UserNames       *[]string `json:"userNames,omitempty"`
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	names, err := h.security.ListGroups(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := make([]nameURI, len(names))
	for i, n := range names {
		out[i] = nameURI{
			Name: n,
			URI:  baseURL(r) + "/api/security/groups/" + escapePath(n),
		}
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	withUsers := r.URL.Query().Get("includeUsers") == "true"

	g, err := h.security.GetGroup(r.Context(), r.PathValue("name"), withUsers)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := groupJSON{
		Name:            g.Name,
		Description:     g.Description,
		AutoJoin:        &g.DefaultForNewUsers,
		AdminPrivileges: &g.Admin,
		Realm:           "internal",
	}
	if withUsers {
		members := nonNilList(g.Members)
		out.UserNames = &members
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) putGroup(w http.ResponseWriter, r *http.Request) {
	var g groupJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&g); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid group JSON")
		return
	}

	in := security.Group{
		Name:        r.PathValue("name"),
		Description: g.Description,
	}
	if g.AutoJoin != nil {
		in.DefaultForNewUsers = *g.AutoJoin
	}

	if g.AdminPrivileges != nil {
		in.Admin = *g.AdminPrivileges
	}

	var members []string
	if g.UserNames != nil {
		members = nonNilList(*g.UserNames)
	}

	created, err := h.security.PutGroup(r.Context(), in, members)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}

	w.WriteHeader(status)
}

func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))

	var (
		g    groupJSON
		sent map[string]json.RawMessage
	)
	if err != nil || json.Unmarshal(body, &g) != nil || json.Unmarshal(body, &sent) != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid group JSON")
		return
	}

	name := r.PathValue("name")

	var users []string
	if g.UserNames != nil {
		users = *g.UserNames
	}

	for _, u := range users {
		if _, err := h.security.GetUser(r.Context(), u); err != nil {
			h.writeError(w, r, err)
			return
		}
	}

	_, hasDescription := sent["description"]
	if hasDescription || g.AutoJoin != nil || g.AdminPrivileges != nil {
		cur, err := h.security.GetGroup(r.Context(), name, false)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		if hasDescription {
			cur.Description = g.Description
		}

		if g.AutoJoin != nil {
			cur.DefaultForNewUsers = *g.AutoJoin
		}

		if g.AdminPrivileges != nil {
			cur.Admin = *g.AdminPrivileges
		}

		if _, err := h.security.PutGroup(r.Context(), cur, nil); err != nil {
			h.writeError(w, r, err)
			return
		}
	}

	if err := h.security.AddMembers(r.Context(), name, users); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.security.DeleteGroup(r.Context(), name); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "Group "+name+" deleted.")
}

func (h *Handler) listPermissionTargets(w http.ResponseWriter, r *http.Request) {
	names, err := h.security.ListPermissions(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := make([]nameURI, len(names))
	for i, n := range names {
		out[i] = nameURI{
			Name: n,
			URI:  baseURL(r) + "/api/v2/security/permissions/" + escapePath(n),
		}
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getPermissionTarget(w http.ResponseWriter, r *http.Request) {
	pt, err := h.security.GetPermission(r.Context(), r.PathValue("name"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	doc, err := permissions.ToArtifactory(pt)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(doc) //nolint:gosec // G705: admin-only JSON with nosniff
}

func (h *Handler) createPermissionTarget(w http.ResponseWriter, r *http.Request) {
	pt, ok := h.readPermissionTarget(w, r)
	if !ok {
		return
	}

	if err := h.security.CreatePermission(r.Context(), pt); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) updatePermissionTarget(w http.ResponseWriter, r *http.Request) {
	pt, ok := h.readPermissionTarget(w, r)
	if !ok {
		return
	}

	if err := h.security.UpdatePermission(r.Context(), pt); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) readPermissionTarget(w http.ResponseWriter, r *http.Request) (security.Permission, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		wire.WriteBodyError(w, err, "request too large")
		return security.Permission{}, false
	}

	pt, err := permissions.FromArtifactory(r.PathValue("name"), body)
	if err != nil {
		h.writeError(w, r, err)
		return security.Permission{}, false
	}

	return pt, true
}

func (h *Handler) deletePermissionTarget(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.security.DeletePermission(r.Context(), name); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "Permission "+name+" deleted.")
}

// ServiceID identifies this Relic instance in admin-scoped token scopes.
const ServiceID = "jfrt@relic"

func (h *Handler) serviceID(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, ServiceID)
}

func isSecurityError(err error) (int, bool) {
	switch {
	case errors.Is(err, security.ErrNotFound):
		return http.StatusNotFound, true
	case errors.Is(err, security.ErrExists):
		return http.StatusConflict, true
	case errors.Is(err, security.ErrInvalid):
		return http.StatusBadRequest, true
	}

	return 0, false
}
