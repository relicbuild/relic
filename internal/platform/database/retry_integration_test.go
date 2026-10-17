//go:build integration

package database_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestWithTxRetries(t *testing.T) {
	t.Parallel()

	db := databasetest.NewDB(t)
	errOther := errors.New("not retryable")

	tests := []struct {
		name      string
		failures  int
		err       error
		wantCalls int
		wantErr   error
		cancels   bool
	}{
		{
			name:      "a conflict runs the function again",
			failures:  2,
			err:       fmt.Errorf("insert: %w", database.ErrRetryTransaction),
			wantCalls: 3,
		},
		{
			name:      "retries stop after the last attempt",
			failures:  10,
			err:       fmt.Errorf("insert: %w", database.ErrRetryTransaction),
			wantCalls: 4,
			wantErr:   database.ErrRetryTransaction,
		},
		{
			name:      "a cancelled context ends the retries with its error",
			failures:  10,
			err:       fmt.Errorf("insert: %w", database.ErrRetryTransaction),
			wantCalls: 1,
			wantErr:   context.Canceled,
			cancels:   true,
		},
		{
			name:      "other errors are returned at once",
			failures:  10,
			err:       errOther,
			wantCalls: 1,
			wantErr:   errOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			calls := 0
			err := db.WithTx(ctx, func(database.Querier) error {
				calls++

				if tt.cancels {
					cancel()
				}

				if calls <= tt.failures {
					return tt.err
				}

				return nil
			})

			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("WithTx = %v, want %v", err, tt.wantErr)
			}

			if calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}

func TestWithTxRetriesADeadlock(t *testing.T) {
	t.Parallel()
	databasetest.RequirePostgreSQL(t)

	db := databasetest.NewDB(t)
	pool := db.Pool()

	if _, err := pool.Exec(t.Context(), `CREATE TABLE lock_order (id int PRIMARY KEY);
		INSERT INTO lock_order VALUES (1), (2)`); err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{}, 2)
	proceed := make(chan struct{})
	errs := make(chan error, 2)

	for _, order := range [][2]int{
		{
			1,
			2,
		},
		{
			2,
			1,
		},
	} {
		go func() {
			first := true

			errs <- db.WithTx(t.Context(), func(database.Querier) error {
				tx, err := pool.Begin(t.Context())
				if err != nil {
					return err
				}

				defer func() {
					_ = tx.Rollback(t.Context())
				}()

				if _, err := tx.Exec(t.Context(), `SELECT 1 FROM lock_order WHERE id = $1 FOR UPDATE`, order[0]); err != nil {
					return err
				}

				if first {
					first = false

					locked <- struct{}{}

					<-proceed
				}

				if _, err := tx.Exec(t.Context(), `SELECT 1 FROM lock_order WHERE id = $1 FOR UPDATE`, order[1]); err != nil {
					return err
				}

				return tx.Commit(t.Context())
			})
		}()
	}

	<-locked
	<-locked
	close(proceed)

	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("WithTx after a deadlock = %v, want a retried success", err)
		}
	}
}
