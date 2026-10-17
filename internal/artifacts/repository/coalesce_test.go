package repository

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoalescer(t *testing.T) {
	t.Parallel()

	t.Run("concurrent callers share one call", func(t *testing.T) {
		t.Parallel()

		var (
			c     Coalescer[int]
			calls atomic.Int32
		)

		release := make(chan struct{})
		results := make(chan int, 10)

		for range 10 {
			go func() {
				v, _ := c.Do(t.Context(), "k", func(context.Context) (int, error) {
					calls.Add(1)
					<-release

					return 42, nil
				})
				results <- v
			}()
		}

		waitFor(t, func() bool {
			return waiters(&c, "k") == 10
		})
		close(release)

		for range 10 {
			if v := <-results; v != 42 {
				t.Fatalf("result = %d, want 42", v)
			}
		}

		if n := calls.Load(); n != 1 {
			t.Fatalf("calls = %d, want 1", n)
		}
	})

	t.Run("a caller giving up does not cancel the others", func(t *testing.T) {
		t.Parallel()

		var c Coalescer[int]

		release := make(chan struct{})
		fn := func(ctx context.Context) (int, error) {
			select {
			case <-release:
				return 7, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}

		leaving, leave := context.WithCancel(t.Context())
		gone := make(chan error, 1)

		go func() {
			_, err := c.Do(leaving, "k", fn)
			gone <- err
		}()

		waitFor(t, func() bool {
			return waiters(&c, "k") == 1
		})

		stayed := make(chan int, 1)

		go func() {
			v, _ := c.Do(t.Context(), "k", fn)
			stayed <- v
		}()

		waitFor(t, func() bool {
			return waiters(&c, "k") == 2
		})
		leave()

		if err := <-gone; !errors.Is(err, context.Canceled) {
			t.Fatalf("leaving caller: err = %v, want context.Canceled", err)
		}

		close(release)

		if v := <-stayed; v != 7 {
			t.Fatalf("remaining caller: result = %d, want 7", v)
		}
	})

	t.Run("the fetch is cancelled when every caller has gone", func(t *testing.T) {
		t.Parallel()

		var c Coalescer[int]

		cancelled := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())

		go func() {
			_, _ = c.Do(ctx, "k", func(ctx context.Context) (int, error) {
				<-ctx.Done()
				close(cancelled)

				return 0, ctx.Err()
			})
		}()

		waitFor(t, func() bool {
			return waiters(&c, "k") == 1
		})
		cancel()

		select {
		case <-cancelled:
		case <-time.After(5 * time.Second):
			t.Fatal("the fetch was not cancelled")
		}

		v, err := c.Do(t.Context(), "k", func(context.Context) (int, error) {
			return 3, nil
		})
		if err != nil || v != 3 {
			t.Fatalf("later caller: %d, %v; want 3, nil", v, err)
		}
	})

	t.Run("a panic becomes an error", func(t *testing.T) {
		t.Parallel()

		var c Coalescer[int]

		_, err := c.Do(t.Context(), "k", func(context.Context) (int, error) {
			panic("boom")
		})
		if err == nil {
			t.Fatal("err = nil, want the panic as an error")
		}
	})
}

func waiters(c *Coalescer[int], key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cl, ok := c.calls[key]; ok {
		return cl.waiters
	}

	return 0
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 5s")
		}

		time.Sleep(time.Millisecond)
	}
}
