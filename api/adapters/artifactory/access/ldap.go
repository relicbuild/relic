package access

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"relic/api/adapters/artifactory/ldapsettings"

	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/access/ldap"
	"relic/internal/platform/httpserver"
)

// SetLDAP enables the LDAP settings endpoints, backed by svc.
func (h *Handler) SetLDAP(svc *ldap.Service) { h.ldap = svc }

func (h *Handler) registerLDAP(rt *httpserver.Router) {
	rt.HandleFunc("GET /access/api/v1/ldap/settings", auth.Admin, h.listLDAP)
	rt.HandleFunc("POST /access/api/v1/ldap/settings", auth.Admin, h.createLDAP)
	rt.HandleFunc("GET /access/api/v1/ldap/settings/{key}", auth.Admin, h.getLDAP)
	rt.HandleFunc("PUT /access/api/v1/ldap/settings/{key}", auth.Admin, h.updateLDAP)
	rt.HandleFunc("DELETE /access/api/v1/ldap/settings/{key}", auth.Admin, h.deleteLDAP)
	rt.HandleFunc("GET /access/api/v1/ldap/groups", auth.Admin, h.listLDAPGroups)
	rt.HandleFunc("POST /access/api/v1/ldap/groups", auth.Admin, h.createLDAPGroup)
	rt.HandleFunc("GET /access/api/v1/ldap/groups/{name}", auth.Admin, h.getLDAPGroup)
	rt.HandleFunc("PUT /access/api/v1/ldap/groups/{name}", auth.Admin, h.updateLDAPGroup)
	rt.HandleFunc("DELETE /access/api/v1/ldap/groups/{name}", auth.Admin, h.deleteLDAPGroup)
}

// the manager password never leaves the server.
func masked(s ldap.Server) ldapsettings.Settings {
	out := ldapsettings.ToArtifactory(s)
	out.Search.ManagerPassword = ""

	return out
}

func (h *Handler) ldapError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ldap.ErrNotFound):
		wire.WriteError(w, http.StatusNotFound, "LDAP setting not found")
	case errors.Is(err, ldap.ErrInvalid):
		wire.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		h.log.ErrorContext(r.Context(), "ldap settings", "error", err)
		wire.WriteError(w, http.StatusInternalServerError, "internal error")
	}
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return v, false
	}

	return v, true
}

func (h *Handler) listLDAP(w http.ResponseWriter, r *http.Request) {
	all, err := h.ldap.Servers(r.Context())
	if err != nil {
		h.ldapError(w, r, err)
		return
	}

	out := make([]ldapsettings.Settings, 0, len(all))
	for _, s := range all {
		out = append(out, masked(s))
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getLDAP(w http.ResponseWriter, r *http.Request) {
	s, err := h.ldap.Server(r.Context(), r.PathValue("key"))
	if err != nil {
		h.ldapError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, masked(s))
}

func (h *Handler) createLDAP(w http.ResponseWriter, r *http.Request) {
	doc, ok := decode[ldapsettings.Settings](w, r)
	if !ok {
		return
	}

	s := ldapsettings.FromArtifactory(doc)

	err := h.ldap.CreateLdapServer(r.Context(), s)
	if errors.Is(err, ldap.ErrExists) {
		wire.WriteError(w, http.StatusConflict, "LDAP setting "+s.Name+" already exists")
		return
	}

	if err != nil {
		h.ldapError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) updateLDAP(w http.ResponseWriter, r *http.Request) {
	doc, ok := decode[ldapsettings.Settings](w, r)
	if !ok {
		return
	}

	s := ldapsettings.FromArtifactory(doc)

	key := r.PathValue("key")
	if s.Name == "" {
		s.Name = key
	}

	if s.Name != key {
		wire.WriteError(w, http.StatusBadRequest, "the key in the body must match the path")
		return
	}

	if _, err := h.ldap.Server(r.Context(), key); err != nil {
		h.ldapError(w, r, err)
		return
	}

	if err := h.ldap.PutLdapServer(r.Context(), s); err != nil {
		h.ldapError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteLDAP(w http.ResponseWriter, r *http.Request) {
	if err := h.ldap.DeleteLdapServer(r.Context(), r.PathValue("key")); err != nil {
		h.ldapError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listLDAPGroups(w http.ResponseWriter, r *http.Request) {
	all, err := h.ldap.GroupMappings(r.Context())
	if err != nil {
		h.ldapError(w, r, err)
		return
	}

	out := make([]ldapsettings.GroupSettings, 0, len(all))
	for _, g := range all {
		out = append(out, ldapsettings.GroupToArtifactory(g))
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getLDAPGroup(w http.ResponseWriter, r *http.Request) {
	g, err := h.ldap.GroupMapping(r.Context(), r.PathValue("name"))
	if err != nil {
		h.ldapError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, ldapsettings.GroupToArtifactory(g))
}

func (h *Handler) createLDAPGroup(w http.ResponseWriter, r *http.Request) {
	doc, ok := decode[ldapsettings.GroupSettings](w, r)
	if !ok {
		return
	}

	g := ldapsettings.GroupFromArtifactory(doc)

	_, err := h.ldap.GroupMapping(r.Context(), g.Name)
	if err == nil {
		wire.WriteError(w, http.StatusConflict, "LDAP group setting "+g.Name+" already exists")
		return
	}

	if !errors.Is(err, ldap.ErrNotFound) {
		h.ldapError(w, r, err)
		return
	}

	if err = h.ldap.PutLdapGroupMapping(r.Context(), g); err != nil {
		h.ldapError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) updateLDAPGroup(w http.ResponseWriter, r *http.Request) {
	doc, ok := decode[ldapsettings.GroupSettings](w, r)
	if !ok {
		return
	}

	g := ldapsettings.GroupFromArtifactory(doc)

	name := r.PathValue("name")
	if g.Name == "" {
		g.Name = name
	}

	if g.Name != name {
		wire.WriteError(w, http.StatusBadRequest, "the name in the body must match the path")
		return
	}

	if _, err := h.ldap.GroupMapping(r.Context(), name); err != nil {
		h.ldapError(w, r, err)
		return
	}

	if err := h.ldap.PutLdapGroupMapping(r.Context(), g); err != nil {
		h.ldapError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteLDAPGroup(w http.ResponseWriter, r *http.Request) {
	if err := h.ldap.DeleteLdapGroupMapping(r.Context(), r.PathValue("name")); err != nil {
		h.ldapError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
