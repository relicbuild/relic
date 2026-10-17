// Package scim serves SCIM 2.0 (RFC 7643 and 7644) at /scim/v2, so an
// identity provider provisions single sign-on users and their groups.
package scim

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/access/sso"
	"relic/internal/platform/httperror"
	"relic/internal/platform/httpserver"
	"relic/internal/platform/telemetry"
)

// Prefix is where SCIM is served.
const Prefix = "/scim/v2"

// PathPrefix is the path under which every request belongs to SCIM.
const PathPrefix = "/scim/"

const (
	contentType  = "application/scim+json"
	userSchema   = "urn:ietf:params:scim:schemas:core:2.0:User"
	groupSchema  = "urn:ietf:params:scim:schemas:core:2.0:Group"
	listSchema   = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	errorSchema  = "urn:ietf:params:scim:api:messages:2.0:Error"
	maxResults   = 200
	defaultCount = 100
	maxBodySize  = 1 << 20
)

// Handler serves SCIM.
type Handler struct {
	dir *sso.Directory
	log *slog.Logger
}

// New returns a Handler provisioning through dir.
func New(dir *sso.Directory, log *slog.Logger) *Handler {
	return &Handler{
		dir: dir,
		log: log,
	}
}

// RegisterPaths makes the HTTP layer answer errors under PathPrefix in SCIM's
// error shape, and count its requests toward the access family.
func RegisterPaths() {
	httperror.Register(PathPrefix, func(w http.ResponseWriter, status int, message string) {
		writeError(w, status, "", message)
	})
	telemetry.RegisterFamily(PathPrefix, "access")
}

// Register mounts SCIM on rt, for administrators' tokens.
func (h *Handler) Register(rt *httpserver.Router) {
	rt.HandleFunc("GET "+Prefix+"/ServiceProviderConfig", auth.Admin, serviceProviderConfig)
	rt.HandleFunc("GET "+Prefix+"/ResourceTypes", auth.Admin, resourceTypes)
	rt.HandleFunc("GET "+Prefix+"/Schemas", auth.Admin, schemas)
	rt.HandleFunc("GET "+Prefix+"/Users", auth.Admin, h.listUsers)
	rt.HandleFunc("POST "+Prefix+"/Users", auth.Admin, h.createUser)
	rt.HandleFunc("GET "+Prefix+"/Users/{id}", auth.Admin, h.getUser)
	rt.HandleFunc("PUT "+Prefix+"/Users/{id}", auth.Admin, h.replaceUser)
	rt.HandleFunc("PATCH "+Prefix+"/Users/{id}", auth.Admin, h.patchUser)
	rt.HandleFunc("DELETE "+Prefix+"/Users/{id}", auth.Admin, h.deleteUser)
	rt.HandleFunc("GET "+Prefix+"/Groups", auth.Admin, h.listGroups)
	rt.HandleFunc("POST "+Prefix+"/Groups", auth.Admin, h.createGroup)
	rt.HandleFunc("GET "+Prefix+"/Groups/{id}", auth.Admin, h.getGroup)
	rt.HandleFunc("PUT "+Prefix+"/Groups/{id}", auth.Admin, h.replaceGroup)
	rt.HandleFunc("PATCH "+Prefix+"/Groups/{id}", auth.Admin, h.patchGroup)
	rt.HandleFunc("DELETE "+Prefix+"/Groups/{id}", auth.Admin, h.deleteGroup)
}

type meta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	Location     string `json:"location,omitempty"`
}

type email struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type userResource struct {
	Schemas  []string `json:"schemas"`
	ID       string   `json:"id,omitempty"`
	UserName string   `json:"userName"`
	Active   *bool    `json:"active,omitempty"`
	Emails   []email  `json:"emails,omitempty"`
	Meta     *meta    `json:"meta,omitempty"`
}

type member struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
}

type groupResource struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	DisplayName string   `json:"displayName"`
	Members     []member `json:"members,omitempty"`
	Meta        *meta    `json:"meta,omitempty"`
}

type listResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int64    `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []any    `json:"Resources"`
}

type errorResponse struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail"`
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, scimType, detail string) {
	write(w, status, errorResponse{
		Schemas: []string{
			errorSchema,
		},
		Status:   strconv.Itoa(status),
		ScimType: scimType,
		Detail:   detail,
	})
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sso.ErrEntryNotFound):
		writeError(w, http.StatusNotFound, "", err.Error())
	case errors.Is(err, sso.ErrConflict):
		writeError(w, http.StatusConflict, "uniqueness", err.Error())
	case errors.Is(err, sso.ErrImmutable):
		writeError(w, http.StatusBadRequest, "mutability", err.Error())
	case errors.Is(err, sso.ErrInvalidValue):
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
	default:
		h.log.ErrorContext(r.Context(), "scim", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "", "internal error")
	}
}

func read(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "the request body is not valid JSON")
		return false
	}

	return true
}

func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}

func wireUser(r *http.Request, u sso.DirectoryUser) userResource {
	active := u.Active

	out := userResource{
		Schemas: []string{
			userSchema,
		},
		ID:       u.ID,
		UserName: u.UserName,
		Active:   &active,
		Meta: &meta{
			ResourceType: "User",
			Created:      timestamp(u.Created),
			Location:     httpserver.Origin(r) + Prefix + "/Users/" + u.ID,
		},
	}
	if u.Email != "" {
		out.Emails = []email{
			{
				Value:   u.Email,
				Type:    "work",
				Primary: true,
			},
		}
	}

	return out
}

func wireGroup(r *http.Request, g sso.DirectoryGroup) groupResource {
	out := groupResource{
		Schemas: []string{
			groupSchema,
		},
		ID:          g.Name,
		DisplayName: g.Name,
		Members:     make([]member, 0, len(g.Members)),
		Meta: &meta{
			ResourceType: "Group",
			Created:      timestamp(g.Created),
			Location:     httpserver.Origin(r) + Prefix + "/Groups/" + url.PathEscape(g.Name),
		},
	}
	for _, m := range g.Members {
		out.Members = append(out.Members, member{
			Value:   m.ID,
			Display: m.UserName,
		})
	}

	return out
}

// primaryEmail is the primary email, else a work one, else the first.
func primaryEmail(emails []email) string {
	for _, e := range emails {
		if e.Primary {
			return e.Value
		}
	}

	for _, e := range emails {
		if strings.EqualFold(e.Type, "work") {
			return e.Value
		}
	}

	if len(emails) > 0 {
		return emails[0].Value
	}

	return ""
}

var eqFilter = regexp.MustCompile(`(?i)^\s*([\w.:]+)\s+eq\s+"((?:[^"\\]|\\.)*)"\s*$`)

// equalsFilter reads a filter of the form `<attribute> eq "<value>"`; attr
// is the only attribute it may name.
func equalsFilter(filter, attr string) (string, error) {
	if filter == "" {
		return "", nil
	}

	m := eqFilter.FindStringSubmatch(filter)
	if m == nil || !strings.EqualFold(m[1], attr) {
		return "", fmt.Errorf("only %s eq \"…\" filters are supported", attr)
	}

	value, err := filterValue(m[2])
	if err != nil {
		return "", errors.New("the filter's value does not parse")
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("the filter's value is empty")
	}

	return value, nil
}

func filterValue(quoted string) (string, error) {
	var value string
	if err := json.Unmarshal([]byte(`"`+quoted+`"`), &value); err != nil {
		return "", fmt.Errorf("%w: %q is not a JSON string", sso.ErrInvalidValue, quoted)
	}

	return value, nil
}

// page reads startIndex (from 1) and count (at most maxResults).
func page(r *http.Request) (int, int) {
	start, err := strconv.Atoi(r.URL.Query().Get("startIndex"))
	if err != nil || start < 1 {
		start = 1
	}

	start = min(start, math.MaxInt32)

	count, err := strconv.Atoi(r.URL.Query().Get("count"))
	if err != nil || count < 0 {
		count = defaultCount
	}

	return start, min(count, maxResults)
}

func list(total int64, start int, resources []any) listResponse {
	return listResponse{
		Schemas: []string{
			listSchema,
		},
		TotalResults: total,
		StartIndex:   start,
		ItemsPerPage: len(resources),
		Resources:    resources,
	}
}
