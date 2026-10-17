package repository

import (
	"context"
	"fmt"
	"sync"
)

// Coalescer runs one fetch per key at a time: callers asking for a key
// while it is being fetched wait for that fetch and share its result, so a
// hundred clients pulling the same uncached file make one upstream request.
type Coalescer[T any] struct {
	mu    sync.Mutex
	calls map[string]*call[T]
}

type call[T any] struct {
	done    chan struct{}
	val     T
	err     error
	waiters int
	cancel  context.CancelFunc
}

// Do returns fn's result for key, running fn unless a call for key is in
// flight.
func (c *Coalescer[T]) Do(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]*call[T]{}
	}

	cl, ok := c.calls[key]
	if !ok {
		//nolint:gosec // G118: run calls cl.cancel when the fetch ends, leave when every caller has gone.
		fetchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		cl = &call[T]{
			done:   make(chan struct{}),
			cancel: cancel,
		}
		c.calls[key] = cl

		go c.run(fetchCtx, key, cl, fn)
	}

	cl.waiters++
	c.mu.Unlock()

	select {
	case <-cl.done:
		return cl.val, cl.err
	case <-ctx.Done():
		c.leave(key, cl)

		var zero T

		return zero, ctx.Err()
	}
}

func (c *Coalescer[T]) run(ctx context.Context, key string, cl *call[T], fn func(context.Context) (T, error)) {
	defer cl.cancel()
	defer close(cl.done)
	defer c.forget(key, cl)
	defer func() {
		if p := recover(); p != nil {
			cl.err = fmt.Errorf("fetch %s: panic: %v", key, p)
		}
	}()

	cl.val, cl.err = fn(ctx)
}

func (c *Coalescer[T]) leave(key string, cl *call[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cl.waiters--
	if cl.waiters == 0 {
		cl.cancel()

		if c.calls[key] == cl {
			delete(c.calls, key)
		}
	}
}

func (c *Coalescer[T]) forget(key string, cl *call[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.calls[key] == cl {
		delete(c.calls, key)
	}
}
