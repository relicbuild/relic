package scan

import (
	"context"
	"fmt"

	"golang.org/x/sync/semaphore"
)

// MemoryPerScanner is the least memory a scanner subprocess is given.
const MemoryPerScanner = 192 << 20

const maxScanners = 2

// ScannerSlots shares a memory budget equally between the scanner subprocesses that run at once, and gives all of
// it to a vulnerability database change, which runs alone.
type ScannerSlots struct {
	slots           *semaphore.Weighted
	count           int64
	budget          int64
	databaseChanges *semaphore.Weighted
}

// NewScannerSlots returns the slots for budget bytes: one scanner for every
// MemoryPerScanner, at least one and at most two.
func NewScannerSlots(budget int64) *ScannerSlots {
	count := min(max(budget/MemoryPerScanner, 1), maxScanners)

	return &ScannerSlots{
		slots:           semaphore.NewWeighted(count),
		count:           count,
		budget:          budget,
		databaseChanges: semaphore.NewWeighted(1),
	}
}

// Count is how many scanners run at once.
func (s *ScannerSlots) Count() int { return int(s.count) }

// ScannerMemory is the GOMEMLIMIT of each scanner subprocess.
func (s *ScannerSlots) ScannerMemory() int64 { return s.budget / s.count }

// Budget is the memory all scanner subprocesses share.
func (s *ScannerSlots) Budget() int64 { return s.budget }

// WithoutDatabaseChanges runs fn while no vulnerability database change runs.
func (s *ScannerSlots) WithoutDatabaseChanges(ctx context.Context, fn func(context.Context) error) error {
	if s == nil {
		return fn(ctx)
	}

	if err := s.databaseChanges.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("wait for the vulnerability database change: %w", err)
	}
	defer s.databaseChanges.Release(1)

	return fn(ctx)
}

func (s *ScannerSlots) acquire(ctx context.Context, databaseChange bool) (int64, func(), error) {
	if s == nil {
		return 0, func() {}, nil
	}

	if !databaseChange {
		if err := s.slots.Acquire(ctx, 1); err != nil {
			return 0, nil, fmt.Errorf("wait for a scanner slot: %w", err)
		}

		return s.ScannerMemory(), func() {
			s.slots.Release(1)
		}, nil
	}

	if err := s.databaseChanges.Acquire(ctx, 1); err != nil {
		return 0, nil, fmt.Errorf("wait for the vulnerability database change: %w", err)
	}

	if err := s.slots.Acquire(ctx, s.count); err != nil {
		s.databaseChanges.Release(1)

		return 0, nil, fmt.Errorf("wait for the running scanners: %w", err)
	}

	return s.budget, func() {
		s.slots.Release(s.count)
		s.databaseChanges.Release(1)
	}, nil
}
