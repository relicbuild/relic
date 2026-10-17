package auth

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
)

// ErrInsecureProviderURL is an identity provider URL that is neither https
// nor plain http on this machine.
var ErrInsecureProviderURL = errors.New("not an https URL (http only on this machine)")

// ParseProviderURL parses the URL Relic fetches an identity provider's
// signing keys or metadata from: https, or plain http on a loopback host.
func ParseProviderURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("%q: %w", raw, ErrInsecureProviderURL)
	}

	return u, nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}

	addr, err := netip.ParseAddr(host)

	return err == nil && addr.IsLoopback()
}
