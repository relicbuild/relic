package database

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// WithLock runs fn while holding the lock named key, across every process
// sharing the database.
func (db *DB) WithLock(ctx context.Context, key string, fn func() error) error {
	return db.engine.withLock(ctx, lockID(key), fn)
}

func lockID(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))

	return int64(h.Sum64()) //nolint:gosec // any 64 bits name the lock
}

const (
	lockConnectTimeout = 10 * time.Second
	lockPollMin        = 5 * time.Millisecond
	lockPollMax        = 200 * time.Millisecond
)

func (e *postgresEngine) withLock(ctx context.Context, id int64, fn func() error) error {
	release, err := e.localLocks.hold(ctx, id)
	if err != nil {
		return fmt.Errorf("take lock: %w", err)
	}

	defer release()

	for delay := lockPollMin; ; delay = min(2*delay, lockPollMax) {
		taken, err := e.advisoryLocks.try(ctx, id)
		if err != nil {
			return fmt.Errorf("take lock: %w", err)
		}

		if taken {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("take lock: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	defer e.advisoryLocks.unlock(ctx, id)

	return fn()
}

// advisoryLocks holds PostgreSQL advisory locks on one session of its own,
// so lock holders never take connections from the pool that fn uses.
type advisoryLocks struct {
	mu     sync.Mutex
	config *pgx.ConnConfig
	conn   *pgx.Conn
}

func (a *advisoryLocks) try(ctx context.Context, id int64) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.conn == nil || a.conn.IsClosed() {
		connectCtx, cancel := context.WithTimeout(ctx, lockConnectTimeout)
		conn, err := pgx.ConnectConfig(connectCtx, a.config)

		cancel()

		if err != nil {
			return false, fmt.Errorf("connect for locks: %w", err)
		}

		a.conn = conn
	}

	ctx = context.WithoutCancel(ctx)

	var taken bool
	if err := a.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&taken); err != nil {
		a.closeConn(ctx)

		return false, err
	}

	return taken, nil
}

func (a *advisoryLocks) unlock(ctx context.Context, id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.conn == nil {
		return
	}

	ctx = context.WithoutCancel(ctx)

	if _, err := a.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", id); err != nil {
		a.closeConn(ctx)
	}
}

func (a *advisoryLocks) close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closeConn(context.Background())
}

func (a *advisoryLocks) closeConn(ctx context.Context) {
	if a.conn != nil {
		_ = a.conn.Close(ctx)
		a.conn = nil
	}
}

// localLocks serializes the holders of a lock within this process: one
// channel per lock ID, so locks with different IDs never wait on each other.
type localLocks struct {
	mu   sync.Mutex
	held map[int64]*localLock
}

type localLock struct {
	token   chan struct{}
	waiters int
}

func newLocalLocks() *localLocks {
	return &localLocks{
		held: map[int64]*localLock{},
	}
}

func (l *localLocks) hold(ctx context.Context, id int64) (func(), error) {
	l.mu.Lock()

	lock, ok := l.held[id]
	if !ok {
		lock = &localLock{
			token: make(chan struct{}, 1),
		}
		l.held[id] = lock
	}

	lock.waiters++
	l.mu.Unlock()

	forget := func() {
		l.mu.Lock()
		lock.waiters--

		if lock.waiters == 0 {
			delete(l.held, id)
		}
		l.mu.Unlock()
	}

	select {
	case lock.token <- struct{}{}:
	case <-ctx.Done():
		forget()

		return nil, ctx.Err()
	}

	return func() {
		<-lock.token
		forget()
	}, nil
}

// SQLite has one process, so in-process locks serialize every holder.
var sqliteLocks = newLocalLocks()

func (e *sqliteEngine) withLock(ctx context.Context, id int64, fn func() error) error {
	release, err := sqliteLocks.hold(ctx, id)
	if err != nil {
		return fmt.Errorf("take lock: %w", err)
	}

	defer release()

	return fn()
}
