package repository

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"relic/internal/platform/telemetry"

	"golang.org/x/net/publicsuffix"
)

// ErrEgressDenied is returned for a blocked upstream address.
var ErrEgressDenied = errors.New("upstream address not allowed")

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// PublicAddr reports whether an upstream may be reached at addr.
func PublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}

	return true
}

func egressControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrEgressDenied, address)
	}

	if !PublicAddr(ip) {
		return fmt.Errorf("%w: %s", ErrEgressDenied, ip)
	}

	return nil
}

// PublicAddressDialer returns a dialer that connects only to public
// addresses, never to loopback, private or link-local ones.
func PublicAddressDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   egressControl,
	}
}

func noDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}

	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("%w: redirect from https to %s", ErrEgressDenied, req.URL.Scheme)
	}

	return nil
}

func newUpstreamClient(timeout time.Duration, allowPrivate bool) *http.Client {
	return &http.Client{
		Transport: retryLostConnection{
			next: telemetry.UpstreamTransport(upstreamTransport(timeout, allowPrivate)),
		},
		CheckRedirect: noDowngrade,
	}
}

type retryLostConnection struct {
	next http.RoundTripper
}

func (t retryLostConnection) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err == nil || req.Context().Err() != nil ||
		(req.Method != http.MethodGet && req.Method != http.MethodHead) ||
		(req.Body != nil && req.Body != http.NoBody) {
		return resp, err
	}

	return t.next.RoundTrip(req)
}

func upstreamTransport(timeout time.Duration, allowPrivate bool) *http.Transport {
	return upstreamTransportFrom(http.DefaultTransport.(*http.Transport), timeout, allowPrivate)
}

func upstreamTransportFrom(base *http.Transport, timeout time.Duration, allowPrivate bool) *http.Transport {
	t := base.Clone()
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = timeout
	t.MaxIdleConnsPerHost = 16
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	// A new slice: the clone shares the default transport's.
	if t.TLSClientConfig != nil {
		var protos []string

		for _, p := range t.TLSClientConfig.NextProtos {
			if p != "h2" {
				protos = append(protos, p)
			}
		}

		t.TLSClientConfig.NextProtos = protos
	}

	if !allowPrivate {
		t.DialContext = PublicAddressDialer().DialContext
		t.Proxy = nil // a proxy would hide the destination from the filter
	}

	return t
}

// SameSite reports whether target may receive the credentials configured
// for upstream: the same host, or hosts with the same registrable domain
// (registry-1.docker.io and auth.docker.io), over https unless upstream
// itself is plain http.
func SameSite(upstream, target string) bool {
	u, err1 := url.Parse(upstream)

	t, err2 := url.Parse(target)
	if err1 != nil || err2 != nil || u.Hostname() == "" {
		return false
	}

	if u.Scheme == "https" && t.Scheme != "https" {
		return false
	}

	uh, th := strings.ToLower(u.Hostname()), strings.ToLower(t.Hostname())
	if uh == th {
		return true
	}

	us, err1 := publicsuffix.EffectiveTLDPlusOne(uh)
	ts, err2 := publicsuffix.EffectiveTLDPlusOne(th)

	return err1 == nil && err2 == nil && us == ts
}
