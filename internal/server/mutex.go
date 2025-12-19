// Copyright 2025 ThanhDeptr
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may not use this file except in compliance with the License.
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package server provides mutex mechanism for iPerf3 exporter to prevent concurrent tests
package server

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// TestLock manages a global mutex for iPerf3 tests using sync.Cond for thread safety.
type TestLock struct {
	mu        sync.Mutex
	cond      *sync.Cond
	isLocked  bool
	lockedBy  string // identifier for who holds the lock
	lockedAt  time.Time
	logger    *slog.Logger
	waitCount int // number of requesters waiting for the lock
}

// NewTestLock creates a new TestLock instance.
func NewTestLock(logger *slog.Logger) *TestLock {
	tl := &TestLock{
		logger: logger,
	}
	tl.cond = sync.NewCond(&tl.mu)
	return tl
}

// TryLock attempts to acquire the lock, respecting the context for timeout/cancellation.
// It returns true if the lock was acquired, false otherwise.
func (tl *TestLock) TryLock(ctx context.Context, requesterID string) bool {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	// If the context is already done, fail fast.
	if ctx.Err() != nil {
		tl.logger.Warn("Lock acquisition cancelled: context already done", "requester", requesterID)
		return false
	}

	// This is the idiomatic Go pattern for a cancellable wait on a condition.
	// Create a channel to signal the helper goroutine to exit if the lock is acquired.
	done := make(chan struct{})
	defer close(done) // Ensure the helper goroutine is always cleaned up.

	// Start a goroutine that will broadcast to wake up waiters when the context is cancelled.
	go func() {
		select {
		case <-ctx.Done():
			// Wake up all waiters when the context is cancelled.
			// No need to lock mutex here as Broadcast is safe to call from multiple goroutines.
			tl.cond.Broadcast()
		case <-done:
			// The lock was acquired successfully, or the function is returning.
			// This goroutine can now exit cleanly, preventing a leak.
		}
	}()

	// Increment wait count if lock is held
	if tl.isLocked {
		tl.waitCount++
		tl.logger.Info("Added to wait queue", "requester", requesterID, "queue_size", tl.waitCount)
	}

	// Wait while the lock is held AND the context is still valid.
	// This is the core of the safe waiting pattern.
	for tl.isLocked && ctx.Err() == nil {
		tl.cond.Wait()
	}

	// Decrement wait count when we get the lock or timeout
	if tl.waitCount > 0 {
		tl.waitCount--
		if ctx.Err() != nil {
			tl.logger.Info("Removed from wait queue due to timeout", "requester", requesterID, "queue_size", tl.waitCount)
		}
	}

	// After waking up, check if it was due to cancellation.
	if ctx.Err() != nil {
		tl.logger.Warn("Lock acquisition cancelled by context", "requester", requesterID)
		return false
	}

	// If we are here, it means the lock is now available.
	tl.isLocked = true
	tl.lockedBy = requesterID
	tl.lockedAt = time.Now()
	tl.logger.Info("Lock acquired", "requester", requesterID, "timestamp", tl.lockedAt)
	return true
}

// Unlock releases the lock and notifies the next waiter.
func (tl *TestLock) Unlock(requesterID string) {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	if !tl.isLocked || tl.lockedBy != requesterID {
		tl.logger.Warn("Attempted to unlock by non-owner", "requester", requesterID, "locked_by", tl.lockedBy)
		return
	}

	lockDuration := time.Since(tl.lockedAt)
	tl.logger.Info("Lock released", "requester", requesterID, "duration", lockDuration)

	// Release lock state.
	tl.isLocked = false
	tl.lockedBy = ""
	tl.lockedAt = time.Time{}

	// Wake up ONE waiting goroutine. This is more efficient than Broadcast
	// for the normal unlock case.
	tl.cond.Signal()
}

// GetStatus returns the current lock status.
func (tl *TestLock) GetStatus() map[string]interface{} {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	status := map[string]interface{}{
		"is_locked":  tl.isLocked,
		"queue_size": tl.waitCount,
	}

	if tl.isLocked {
		status["locked_by"] = tl.lockedBy
		status["locked_at"] = tl.lockedAt
		status["lock_duration"] = time.Since(tl.lockedAt).String()
	}

	return status
}

// --- Global Instance Management ---

var globalTestLock *TestLock

func InitGlobalTestLock(logger *slog.Logger) {
	globalTestLock = NewTestLock(logger)
}

func GetGlobalTestLock() *TestLock {
	return globalTestLock
}
