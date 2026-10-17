package access

import (
	"regexp"
	"strings"
	"time"

	"relic/internal/access/auth"
)

const issuer = "jfrt@relic"

var serviceAdminScope = regexp.MustCompile(`^[a-z]+@[A-Za-z0-9*]+:admin$`)

// Format is the token format the adapter's clients decode.
type Format struct{}

var _ auth.TokenFormat = Format{}

// Issuer is the iss claim.
func (Format) Issuer() string { return issuer }

// Audience is the aud claim.
func (Format) Audience() string { return "*@*" }

// Subject is the sub claim of username's tokens.
func (Format) Subject(username string) string { return issuer + "/users/" + username }

// Username reads a sub claim.
func (Format) Username(subject string) string {
	name, ok := strings.CutPrefix(subject, issuer+"/users/")
	if !ok {
		return ""
	}

	return name
}

// Scope renders a scope as a token's scope string.
func (Format) Scope(s auth.TokenScope) string { return RenderScope(s) }

// ParseScope reads a scope claim.
func (Format) ParseScope(claim string) auth.TokenScope {
	s, _ := ParseScope(claim)
	return s
}

// ParseScope reads a scope string.
func ParseScope(scope string) (s auth.TokenScope, known bool) {
	known = true
	admin, narrowed := false, false

	var groups []string

	for part := range strings.FieldsSeq(scope) {
		switch {
		case part == "applied-permissions/user":
		case part == "applied-permissions/admin", serviceAdminScope.MatchString(part):
			admin = true
		case strings.HasPrefix(part, "applied-permissions/groups:"), strings.HasPrefix(part, "member-of-groups:"):
			_, list, _ := strings.Cut(part, ":")
			if list == "*" {
				continue
			}

			narrowed = true

			for g := range strings.SplitSeq(list, ",") {
				if g != "" {
					groups = append(groups, g)
				}
			}
		default:
			known = false
		}
	}

	switch {
	case admin:
		return auth.TokenScope{
			Kind:   auth.ScopeAdmin,
			Groups: groups,
		}, known
	case narrowed:
		return auth.TokenScope{
			Kind:   auth.ScopeGroups,
			Groups: groups,
		}, known
	default:
		return auth.UserScope, known
	}
}

// RenderScope writes a scope as a token's scope string.
func RenderScope(s auth.TokenScope) string {
	groups := "applied-permissions/groups:" + strings.Join(s.Groups, ",")
	switch s.Kind {
	case auth.ScopeGroups:
		return groups
	case auth.ScopeAdmin:
		if len(s.Groups) > 0 {
			return groups + " applied-permissions/admin"
		}

		return "applied-permissions/admin"
	default:
		return "applied-permissions/user"
	}
}

// LegacyScope writes a scope in the form api/security/token answers with.
func LegacyScope(s auth.TokenScope) string {
	if s.Kind == auth.ScopeUser {
		return "member-of-groups:*"
	}

	return RenderScope(s)
}

// TokenResponse is the response of both token APIs.
type TokenResponse struct {
	TokenID      string `json:"token_id,omitempty"`
	Scope        string `json:"scope"`
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Refreshable  bool   `json:"refreshable"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ReferenceToken is the access token itself, which works wherever a
	// reference token does.
	ReferenceToken string `json:"reference_token,omitempty"`
}

// Response renders an issued token.
func Response(t auth.IssuedToken, scope func(auth.TokenScope) string) TokenResponse {
	return TokenResponse{
		TokenID:      t.ID,
		Scope:        scope(t.Scope),
		AccessToken:  t.AccessToken,
		ExpiresIn:    int64(t.ExpiresIn / time.Second),
		TokenType:    "Bearer",
		Refreshable:  t.Refreshable,
		RefreshToken: t.RefreshToken,
	}
}
