package objectstoretest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"

	"relic/internal/platform/objectstore"
)

// ErrInjected is the error a Faulty store returns for an injected fault.
var ErrInjected = errors.New("injected fault")

// Op names a Store method a Fault applies to.
type Op string

// The Store methods a Fault can target.
const (
	OpPut    Op = "Put"
	OpGet    Op = "Get"
	OpHead   Op = "Head"
	OpCopy   Op = "Copy"
	OpDelete Op = "Delete"
	OpList   Op = "List"
)

// Fault makes calls of one Store method fail.
type Fault struct {
	Op Op
	// KeyPrefix limits the fault to keys with this prefix; empty matches all.
	KeyPrefix string
	// Skip is how many matching calls succeed before the fault applies.
	Skip int
	// Times is how many matching calls fail; 0 means every one.
	Times int
	// Err is returned instead of ErrInjected when set.
	Err error
	// ShortRead makes Get succeed, then fail with Err after this many bytes.
	ShortRead int64
	// Corrupt makes Get succeed with the first byte of the content flipped.
	Corrupt bool
	// ShortWrite makes Put store only this many bytes and report success.
	ShortWrite int64
}

// Faulty wraps a Store and injects faults into chosen calls.
type Faulty struct {
	objectstore.Store

	mu     sync.Mutex
	faults []*Fault
	calls  map[Op]int
}

// NewFaulty wraps s.
func NewFaulty(s objectstore.Store) *Faulty {
	return &Faulty{
		Store: s,
		calls: map[Op]int{},
	}
}

// Inject adds a fault; faults are matched in the order they were added.
func (f *Faulty) Inject(fault Fault) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.faults = append(f.faults, &fault)
}

// Clear removes every fault.
func (f *Faulty) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.faults = nil
}

// Calls returns how many times op was called, faulted or not.
func (f *Faulty) Calls(op Op) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[op]
}

func (f *Faulty) match(op Op, key string) *Fault {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls[op]++

	for i, fault := range f.faults {
		if fault.Op != op || !strings.HasPrefix(key, fault.KeyPrefix) {
			continue
		}

		if fault.Skip > 0 {
			fault.Skip--

			return nil
		}

		if fault.Times > 0 {
			fault.Times--
			if fault.Times == 0 {
				f.faults = append(f.faults[:i], f.faults[i+1:]...)
			}
		}

		return fault
	}

	return nil
}

func (fault *Fault) err() error {
	if fault.Err != nil {
		return fault.Err
	}

	return ErrInjected
}

// Put implements objectstore.Store.
func (f *Faulty) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	fault := f.match(OpPut, key)

	switch {
	case fault == nil:
		return f.Store.Put(ctx, key, r, size)
	case fault.ShortWrite > 0:
		if err := f.Store.Put(ctx, key, io.LimitReader(r, fault.ShortWrite), fault.ShortWrite); err != nil {
			return err
		}

		_, err := io.Copy(io.Discard, r)

		return err
	default:
		return fmt.Errorf("put %s: %w", key, fault.err())
	}
}

// Get implements objectstore.Store.
func (f *Faulty) Get(
	ctx context.Context,
	key string,
	rng *objectstore.Range,
) (io.ReadCloser, objectstore.Info, error) {
	fault := f.match(OpGet, key)
	if fault == nil {
		return f.Store.Get(ctx, key, rng)
	}

	if fault.ShortRead == 0 && !fault.Corrupt {
		return nil, objectstore.Info{}, fmt.Errorf("get %s: %w", key, fault.err())
	}

	body, info, err := f.Store.Get(ctx, key, rng)
	if err != nil {
		return nil, info, err
	}

	var r io.Reader = body
	if fault.Corrupt {
		r = &corruptReader{
			r: r,
		}
	}

	if fault.ShortRead > 0 {
		r = io.MultiReader(
			io.LimitReader(r, fault.ShortRead),
			&errorReader{
				err: fault.err(),
			},
		)
	}

	return readCloser{
		Reader: r,
		Closer: body,
	}, info, nil
}

// Head implements objectstore.Store.
func (f *Faulty) Head(ctx context.Context, key string) (objectstore.Info, error) {
	if fault := f.match(OpHead, key); fault != nil {
		return objectstore.Info{}, fmt.Errorf("head %s: %w", key, fault.err())
	}

	return f.Store.Head(ctx, key)
}

// Copy implements objectstore.Store.
func (f *Faulty) Copy(ctx context.Context, srcKey, dstKey string) error {
	if fault := f.match(OpCopy, dstKey); fault != nil {
		return fmt.Errorf("copy %s: %w", dstKey, fault.err())
	}

	return f.Store.Copy(ctx, srcKey, dstKey)
}

// Delete implements objectstore.Store.
func (f *Faulty) Delete(ctx context.Context, key string) error {
	if fault := f.match(OpDelete, key); fault != nil {
		return fmt.Errorf("delete %s: %w", key, fault.err())
	}

	return f.Store.Delete(ctx, key)
}

type readCloser struct {
	io.Reader
	io.Closer
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type corruptReader struct {
	r    io.Reader
	done bool
}

func (c *corruptReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && !c.done {
		p[0] ^= 0xff
		c.done = true
	}

	return n, err
}

// List implements objectstore.Store.
func (f *Faulty) List(ctx context.Context, prefix string) iter.Seq2[objectstore.Object, error] {
	if fault := f.match(OpList, prefix); fault != nil {
		return func(yield func(objectstore.Object, error) bool) {
			yield(objectstore.Object{}, fmt.Errorf("list %s: %w", prefix, fault.err()))
		}
	}

	return f.Store.List(ctx, prefix)
}
