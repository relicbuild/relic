package ldap

import (
	"context"

	"relic/internal/platform/telemetry"

	goldap "github.com/go-ldap/ldap/v3"
)

type tracedConn struct {
	*goldap.Conn
	ctx context.Context //nolint:containedctx // the go-ldap calls take no context; spans need the caller's
}

func (c tracedConn) Bind(dn, password string) error {
	end := telemetry.StartLDAP(c.ctx, "bind")
	err := c.Conn.Bind(dn, password)
	end(ldapResult(err))

	return err
}

func (c tracedConn) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	end := telemetry.StartLDAP(c.ctx, "search")
	res, err := c.Conn.Search(req)
	end(searchResult(req, err))

	return res, err
}

func searchResult(req *goldap.SearchRequest, err error) string {
	if req.SizeLimit == 0 && goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) {
		return "error"
	}

	return ldapResult(err)
}

func ldapResult(err error) string {
	switch {
	case err == nil:
		return "success"
	case goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials),
		goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject),
		goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded):
		return "failure"
	default:
		return "error"
	}
}
