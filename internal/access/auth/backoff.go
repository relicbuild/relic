package auth

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"relic/internal/platform/httperror"
)

// ErrThrottled is returned for password attempts from a client that failed
// too often.
var ErrThrottled = errors.New("too many failed sign-in attempts")

// ThrottledError says how long the client must wait.
type ThrottledError struct {
	Wait time.Duration
}

func (e *ThrottledError) Error() string { return ErrThrottled.Error() }

// Is makes errors.Is(err, ErrThrottled) match.
func (e *ThrottledError) Is(target error) bool { return target == ErrThrottled }

// WriteThrottled answers 429 with Retry-After when err is a ThrottledError,
// and reports whether it did.
func WriteThrottled(w http.ResponseWriter, r *http.Request, err error) bool {
	te, ok := errors.AsType[*ThrottledError](err)
	if !ok {
		return false
	}

	w.Header().Set("Retry-After", strconv.Itoa(int(max(te.Wait.Round(time.Second), time.Second)/time.Second)))
	httperror.Write(w, r, http.StatusTooManyRequests, "Too many failed sign-in attempts; retry later")

	return true
}

func clientAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}

	a, _ := netip.ParseAddr(r.RemoteAddr)

	return a.Unmap()
}

// Backoff limits password guessing per client address, not per user.
type Backoff struct {
	mu      sync.Mutex
	now     func() time.Time
	free    int
	clients map[netip.Addr]*failures
}

type failures struct {
	count int
	first time.Time
	until time.Time
}

const (
	freeFailures      = 20
	backoffWindow     = 10 * time.Minute
	minDelay          = time.Second
	maxDelay          = 5 * time.Minute
	backoffMaxClients = 100_000
)

// NewBackoff returns an empty Backoff.
func NewBackoff() *Backoff {
	return &Backoff{
		now:     time.Now,
		free:    freeFailures,
		clients: map[netip.Addr]*failures{},
	}
}

// SetFree sets how many failures a client gets within the window before
// it has to wait.
func (b *Backoff) SetFree(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.free = n
}

// Wait reports how long client must wait before its next password attempt
// (zero when it may try now).
func (b *Backoff) Wait(client netip.Addr) time.Duration {
	if b == nil || !client.IsValid() {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	f, ok := b.clients[client]
	if !ok {
		return 0
	}

	return max(f.until.Sub(b.now()), 0)
}

// Fail records a failed password attempt.
func (b *Backoff) Fail(client netip.Addr) {
	if b == nil || !client.IsValid() {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()

	f, ok := b.clients[client]
	if !ok || now.Sub(f.first) > backoffWindow && now.After(f.until) {
		if len(b.clients) >= backoffMaxClients {
			b.prune(now)
		}

		if len(b.clients) >= backoffMaxClients {
			return
		}

		f = &failures{
			first: now,
		}
		b.clients[client] = f
	}

	f.count++
	if over := f.count - b.free + 1; over > 0 {
		delay := maxDelay
		if over <= 20 {
			delay = min(minDelay<<(over-1), maxDelay)
		}

		f.until = now.Add(delay)
	}
}

func (b *Backoff) prune(now time.Time) {
	for c, f := range b.clients {
		if now.Sub(f.first) > backoffWindow && now.After(f.until) {
			delete(b.clients, c)
		}
	}

	if len(b.clients) < backoffMaxClients {
		return
	}

	for c, f := range b.clients {
		if now.After(f.until) {
			delete(b.clients, c)
		}
	}
}
