package auth

import (
	"net/netip"
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_000_000, 0)
	b := NewBackoff()
	b.now = func() time.Time {
		return now
	}
	client, other := netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("198.51.100.8")

	for range freeFailures - 1 {
		b.Fail(client)
	}

	if w := b.Wait(client); w != 0 {
		t.Fatalf("wait after %d failures = %v, want 0", freeFailures-1, w)
	}

	b.Fail(client)

	if w := b.Wait(client); w != minDelay {
		t.Errorf("wait after the last free failure = %v, want %v", w, minDelay)
	}

	b.Fail(client)

	if w := b.Wait(client); w != 2*minDelay {
		t.Errorf("wait after the first extra failure = %v, want %v", w, 2*minDelay)
	}

	for range 40 {
		b.Fail(client)
	}

	if w := b.Wait(client); w != maxDelay {
		t.Errorf("wait is capped at %v, got %v", maxDelay, w)
	}

	if w := b.Wait(other); w != 0 {
		t.Errorf("another client waits %v", w)
	}

	now = now.Add(maxDelay)

	if w := b.Wait(client); w != 0 {
		t.Errorf("wait after the delay = %v", w)
	}

	for range freeFailures {
		b.Fail(other)

		now = now.Add(backoffWindow / 5)
	}

	b.Fail(other)

	if w := b.Wait(other); w != 0 {
		t.Errorf("failures outside the window still count: wait = %v", w)
	}

	for range freeFailures + 1 {
		b.Fail(client)
	}

	for i := range backoffMaxClients {
		b.clients[netip.AddrFrom16([16]byte{
			0x20,
			0x01,
			0x0d,
			0xb8,
			12: byte(i >> 24),
			13: byte(i >> 16),
			14: byte(i >> 8),
			15: byte(i),
		})] = &failures{
			count: 1,
			first: now,
		}
	}

	b.Fail(netip.MustParseAddr("203.0.113.1"))

	if w := b.Wait(client); w == 0 {
		t.Error("a full client table released a throttled client")
	}

	if len(b.clients) > backoffMaxClients {
		t.Errorf("client table holds %d addresses, the cap is %d", len(b.clients), backoffMaxClients)
	}

	var nilBackoff *Backoff
	nilBackoff.Fail(client)

	if nilBackoff.Wait(client) != 0 {
		t.Error("a nil Backoff throttles")
	}
}

func TestBackoffSetFree(t *testing.T) {
	t.Parallel()

	b := NewBackoff()
	b.SetFree(100)

	client := netip.MustParseAddr("198.51.100.9")
	for range 99 {
		b.Fail(client)
	}

	if w := b.Wait(client); w != 0 {
		t.Fatalf("wait after 99 of 100 free failures = %v, want 0", w)
	}

	b.Fail(client)

	if w := b.Wait(client); w <= 0 {
		t.Error("no wait after the free failures are spent")
	}
}
