package database

import (
	"context"
	"database/sql"
	"sync"
)

// Only the generated queries are cached, so the set of statements is bounded.
type sqliteStatements struct {
	db      *sql.DB
	mu      sync.Mutex
	byQuery map[string]*sql.Stmt
}

func newSQLiteStatements(db *sql.DB) *sqliteStatements {
	return &sqliteStatements{
		db:      db,
		byQuery: map[string]*sql.Stmt{},
	}
}

func (s *sqliteStatements) statement(ctx context.Context, query string) (*sql.Stmt, error) {
	if stmt, ok := s.cached(query); ok { //nolint:sqlclosecheck // the statements live until close
		return stmt, nil
	}

	stmt, err := s.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.byQuery[query]; ok {
		_ = stmt.Close() //nolint:sqlclosecheck // the statements live until close

		return existing, nil
	}

	s.byQuery[query] = stmt

	return stmt, nil
}

func (s *sqliteStatements) prepare(ctx context.Context, queries *sync.Map) {
	for query := range queries.Range {
		_, _ = s.statement(ctx, query.(string)) //nolint:sqlclosecheck // the statements live until close
	}
}

func (s *sqliteStatements) cached(query string) (*sql.Stmt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stmt, ok := s.byQuery[query]

	return stmt, ok
}

func (s *sqliteStatements) prepared() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.byQuery)
}

func (s *sqliteStatements) close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for query, stmt := range s.byQuery {
		_ = stmt.Close()

		delete(s.byQuery, query)
	}
}

func (s *sqliteStatements) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	stmt, err := s.statement(ctx, query) //nolint:sqlclosecheck // the statements live until close
	if err != nil {
		return nil, err
	}

	return stmt.ExecContext(ctx, args...)
}

func (s *sqliteStatements) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	stmt, err := s.statement(ctx, query) //nolint:sqlclosecheck // the statements live until close
	if err != nil {
		return nil, err
	}

	return stmt.QueryContext(ctx, args...)
}

// sqliteTxStatements runs the queries of a transaction from the statements
// its pool prepared. A query the pool has not prepared yet runs unprepared and
// is recorded in missed, for the pool to prepare once the transaction ends:
// preparing it now would wait for the one write connection, which the
// transaction holds.
type sqliteTxStatements struct {
	tx         *sql.Tx
	statements *sqliteStatements
	missed     *sync.Map
}

func (t sqliteTxStatements) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if stmt, ok := t.statements.cached(query); ok {
		//nolint:sqlclosecheck // the transaction closes it when it ends
		inTx := t.tx.StmtContext(ctx, stmt)

		return inTx.ExecContext(ctx, args...)
	}

	t.missed.Store(query, struct{}{})

	return t.tx.ExecContext(ctx, query, args...)
}

func (t sqliteTxStatements) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if stmt, ok := t.statements.cached(query); ok {
		//nolint:sqlclosecheck // the transaction closes it when it ends
		inTx := t.tx.StmtContext(ctx, stmt)

		return inTx.QueryContext(ctx, args...)
	}

	t.missed.Store(query, struct{}{})

	return t.tx.QueryContext(ctx, query, args...)
}
