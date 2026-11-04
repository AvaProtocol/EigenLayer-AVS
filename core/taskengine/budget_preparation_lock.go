package taskengine

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// These locks serialize unsigned preparations for one owner, never executions.
// Entries exist only while a preparation holds or waits for the lock.
type budgetPreparationLockSet struct {
	mu     sync.Mutex
	owners map[common.Address]*budgetPreparationLock
}

type budgetPreparationLock struct {
	available chan struct{}
	users     int
}

func (s *budgetPreparationLockSet) acquire(ctx context.Context, owner common.Address) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.owners == nil {
		s.owners = make(map[common.Address]*budgetPreparationLock)
	}
	entry := s.owners[owner]
	if entry == nil {
		entry = &budgetPreparationLock{available: make(chan struct{}, 1)}
		entry.available <- struct{}{}
		s.owners[owner] = entry
	}
	entry.users++
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.release(owner, entry)
		return nil, ctx.Err()
	case <-entry.available:
		return func() {
			entry.available <- struct{}{}
			s.release(owner, entry)
		}, nil
	}
}

func (s *budgetPreparationLockSet) release(owner common.Address, entry *budgetPreparationLock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.users--
	if entry.users == 0 {
		delete(s.owners, owner)
	}
}
