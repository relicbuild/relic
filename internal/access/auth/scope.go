package auth

import (
	"errors"
	"slices"
	"time"
)

// ScopeKind is what a token acts as.
type ScopeKind string

// Scope kinds.
const (
	// ScopeUser acts as the user, with all their groups.
	ScopeUser ScopeKind = "user"
	// ScopeGroups acts with the scope's groups only, never as an
	// administrator.
	ScopeGroups ScopeKind = "groups"
	// ScopeAdmin acts as an administrator while the user is one; with
	// groups, it also acts with those groups only.
	ScopeAdmin ScopeKind = "admin"
)

// ErrUnknownScope is a stored scope kind this version does not know.
var ErrUnknownScope = errors.New("unknown token scope")

// TokenScope narrows what a token may do.
type TokenScope struct {
	Kind ScopeKind
	// Groups are the groups a groups-scoped token acts with (an admin scope
	// may name them too).
	Groups []string
}

// UserScope acts as the user.
var UserScope = TokenScope{
	Kind: ScopeUser,
}

// Valid reports whether the kind is known.
func (s TokenScope) Valid() bool {
	return s.Kind == ScopeUser || s.Kind == ScopeGroups || s.Kind == ScopeAdmin
}

func (s TokenScope) narrowed() bool {
	return s.Kind == ScopeGroups || s.Kind == ScopeAdmin && len(s.Groups) > 0
}

// Apply narrows the principal p of a token's user by the token's scope.
func (s TokenScope) Apply(p Principal) Principal {
	if !s.narrowed() {
		return p
	}

	kept := slices.Clone(s.Groups)
	if !p.Admin {
		kept = slices.DeleteFunc(kept, func(g string) bool {
			return !slices.Contains(p.Groups, g)
		})
	}

	return Principal{
		Username: p.Username,
		Admin:    s.Kind == ScopeAdmin && p.Admin,
		Viewer:   s.Kind == ScopeAdmin && p.Viewer,
		Groups:   kept,
		Scoped:   true,
	}
}

// CeilScope limits a scope requested by p for a new token to p's own
// rights.
func (p Principal) CeilScope(s TokenScope) TokenScope {
	if !p.Scoped {
		return s
	}

	return TokenScope{
		Kind:   ScopeGroups,
		Groups: slices.Clone(p.Groups),
	}
}

// LimitIssue keeps a token p mints for itself within p's rights: its scope
// within p's, and, when a workload identity's token signed p in, its life
// within that token's, without a refresh token.
func (p Principal) LimitIssue(params IssueParams, now time.Time) IssueParams {
	params.Scope = p.CeilScope(params.Scope)
	if p.WorkloadIdentity == "" {
		return params
	}

	remaining := max(p.WorkloadTokenExpires.Sub(now), time.Second)
	if params.ExpiresIn == 0 || params.ExpiresIn > remaining {
		params.ExpiresIn = remaining
	}

	params.Refreshable = false
	params.WorkloadIdentity = p.WorkloadIdentity

	return params
}

// MayGrant reports whether p may mint a token with scope s for themselves
// without being an administrator.
func (p Principal) MayGrant(s TokenScope) bool {
	if p.Admin {
		return true
	}

	switch s.Kind {
	case ScopeUser:
		return true
	case ScopeGroups:
		return !slices.ContainsFunc(s.Groups, func(g string) bool {
			return !slices.Contains(p.Groups, g)
		})
	default:
		return false
	}
}

// TokenFormat names the parts of a token that clients decode: the issuer,
// the subject a user is known by, and the scope claim.
type TokenFormat interface {
	// Issuer is the iss claim.
	Issuer() string
	// Audience is the aud claim.
	Audience() string
	// Subject is the sub claim of username's tokens.
	Subject(username string) string
	// Username reads a sub claim; "" if it is not one of Relic's.
	Username(subject string) string
	// Scope is the scp claim of a scope.
	Scope(TokenScope) string
	// ParseScope reads a scp claim.
	ParseScope(claim string) TokenScope
}
