package repository

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"relic/internal/platform/telemetry"
)

// ErrCoolingDown refuses a version its upstream published more recently than
// the remote repository's cooldown allows.
var ErrCoolingDown = errors.New("the version was published too recently: it is cooling down")

// errUpstreamAbsent is an upstream that answered 404 or 410: remembered for
// the repository's missed-retrieval period.
var errUpstreamAbsent = fmt.Errorf("%w: absent upstream", ErrNotFound)

const (
	defaultMissedRetrievalPeriod = 30 * time.Minute
	missedMaxEntries             = 100_000
	maxRetryAfter                = 30 * time.Second
)

// Cooldown is how long after its upstream publishes a version Relic serves
// it; 0 when the repository has no cooldown.
func (c RemoteConfig) Cooldown() time.Duration {
	return time.Duration(c.CooldownDays) * 24 * time.Hour
}

// MissedRetrievalPeriod is how long an upstream's 404 is remembered; 0 turns
// remembering off.
func (c RemoteConfig) MissedRetrievalPeriod() time.Duration {
	if c.MissedRetrievalCachePeriodSeconds == nil {
		return defaultMissedRetrievalPeriod
	}

	return time.Duration(*c.MissedRetrievalCachePeriodSeconds) * time.Second
}

// missedRetrievals remembers paths upstreams did not have.
type missedRetrievals struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func (m *missedRetrievals) recent(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	exp, ok := m.expires[key]
	if ok && !now.Before(exp) {
		delete(m.expires, key)
		return false
	}

	return ok
}

func (m *missedRetrievals) remember(key string, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.expires == nil || len(m.expires) >= missedMaxEntries {
		m.expires = map[string]time.Time{}
	}

	m.expires[key] = until
}

func (m *missedRetrievals) forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.expires, key)
}

// retryable reports whether a failed request may be tried again: not when the
// name does not resolve or the certificate does not verify.
func retryable(err error) bool {
	var (
		dnsErr     *net.DNSError
		unknownCA  x509.UnknownAuthorityError
		hostname   x509.HostnameError
		invalid    x509.CertificateInvalidError
		recordHdr  tls.RecordHeaderError
		verifyFail *tls.CertificateVerificationError
	)

	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound,
		errors.As(err, &unknownCA),
		errors.As(err, &hostname),
		errors.As(err, &invalid),
		errors.As(err, &recordHdr),
		errors.As(err, &verifyFail):
		return false
	}

	return true
}

// retryAfter reads a Retry-After header (seconds or an HTTP date), at most
// maxRetryAfter.
func retryAfter(h http.Header, now time.Time) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}

	var wait time.Duration

	if secs, err := strconv.Atoi(v); err == nil {
		wait = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		wait = at.Sub(now)
	}

	return min(max(wait, 0), maxRetryAfter)
}

// UpstreamCheck checks a file of remote repository r before Relic caches it
// from upstreamURL: it returns the checksums the file's format publishes (a
// Maven .sha1 file, an npm dist.shasum, a PyPI #sha256= fragment), zero when
// there are none, or ErrCoolingDown for a version published too recently.
// sent are the checksums the upstream sent with the file, if any.
type UpstreamCheck func(ctx context.Context, r Repository, upstreamURL, path string, sent Checksums) (Checksums, error)

// SetUpstreamCheck makes remote repositories of format check the files
// they cache with fn.
func (s *Service) SetUpstreamCheck(format Format, fn UpstreamCheck) {
	s.upstreamChecksMu.Lock()
	defer s.upstreamChecksMu.Unlock()

	if s.upstreamChecks == nil {
		s.upstreamChecks = map[Format]UpstreamCheck{}
	}

	s.upstreamChecks[format] = fn
}

func (s *Service) upstreamCheckFor(format Format) UpstreamCheck {
	s.upstreamChecksMu.Lock()
	defer s.upstreamChecksMu.Unlock()

	return s.upstreamChecks[format]
}

// FetchUpstream GETs a small document (at most limit bytes, accepting
// accept when set) from remote repository r's upstream, with its credentials
// when the URL is on the upstream's site. A 404 or 410 is ErrNotFound.
func (s *Service) FetchUpstream(
	ctx context.Context,
	r Repository,
	upstreamURL, accept string,
	limit int64,
) ([]byte, error) {
	cfg := r.Upstream()

	timeout := 15 * time.Second
	if cfg.TimeoutMilliseconds > 0 {
		timeout = time.Duration(cfg.TimeoutMilliseconds) * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(telemetry.WithUpstream(ctx, r.Name, string(r.Format)), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}

	req.Header.Set("User-Agent", UserAgent)

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	if cfg.Username != "" && SameSite(cfg.URL, upstreamURL) {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}

	resp, err := s.httpClient(timeout).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("%w: %s returned %s", ErrUpstream, req.URL.Redacted(), resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", ErrUpstream, req.URL.Redacted(), err)
	}

	return body, nil
}
