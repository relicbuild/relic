package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

var forwardedHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Proto",
	"X-Forwarded-Host",
	"X-Real-Ip",
	"Forwarded",
}

// ParsePrefixes parses a comma-separated list of CIDRs or addresses.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix

	for f := range strings.SplitSeq(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}

		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", f, err)
			}

			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))

			continue
		}

		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", f, err)
		}

		out = append(out, p.Masked())
	}

	return out, nil
}

// TrustProxies believes X-Forwarded-* headers only from the given proxies.
func TrustProxies(trusted []netip.Prefix, next http.Handler) http.Handler {
	isTrusted := func(a netip.Addr) bool {
		a = a.Unmap()
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}

		return false
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := remoteAddr(r.RemoteAddr)
		if !ok || !isTrusted(peer) {
			for _, h := range forwardedHeaders {
				r.Header.Del(h)
			}

			next.ServeHTTP(w, r)

			return
		}

		client := peer

		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for _, hop := range slices.Backward(hops) {
			a, err := netip.ParseAddr(strings.TrimSpace(hop))
			if err != nil {
				break
			}

			client = a.Unmap()
			if !isTrusted(client) {
				break
			}
		}

		if client != peer {
			r2 := r.Clone(r.Context())
			r2.RemoteAddr = net.JoinHostPort(client.String(), "0")
			r = r2
		}

		next.ServeHTTP(w, r)
	})
}

// Secure reports whether the client reached Relic over HTTPS, directly or
// through a trusted TLS-terminating proxy.
func Secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// Origin is the scheme and host the client used to reach Relic.
func Origin(r *http.Request) string {
	if Secure(r) {
		return "https://" + r.Host
	}

	return "http://" + r.Host
}

// HSTS asks browsers to use HTTPS for a year on responses that went out
// over HTTPS.
func HSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Secure(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}

		next.ServeHTTP(w, r)
	})
}

// ClientIP is the request's client address (after TrustProxies).
func ClientIP(r *http.Request) netip.Addr {
	a, _ := remoteAddr(r.RemoteAddr)
	return a
}

func remoteAddr(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}

	a, err := netip.ParseAddr(s)

	return a.Unmap(), err == nil
}
