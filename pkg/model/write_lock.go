// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import (
	"context"
	"sync"
)

type taskWriteScopeKey struct{}

// WithTaskWriteScope marks ctx as a write transaction whose mutations are
// confined to a single scope key (a task ID, or "run:<runID>" for single-run
// column updates) plus append-only logs.
//
// Backends that serialise read-write transactions at BEGIN
// (pkg/model/postgres) read this scope to take a shared schema lock plus an
// exclusive per-key advisory lock instead of the schema-wide exclusive lock,
// letting writes to distinct tasks run concurrently while preserving strict
// serialisation for writes to the same task and for schema-wide writes.
func WithTaskWriteScope(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, taskWriteScopeKey{}, key)
}

// TaskWriteScopeFrom reports the task write scope key attached to ctx by
// WithTaskWriteScope, if any.
func TaskWriteScopeFrom(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	key, ok := ctx.Value(taskWriteScopeKey{}).(string)
	return key, ok && key != ""
}

// writeCoordinator serialises conflicting in-process write transactions before
// they claim a *sql.DB connection from the pool. Without it, writers queuing
// behind a database advisory lock pin open pool connections while asleep,
// starving concurrent readers and inflating sql.DBStats.WaitDuration.
//
// Lock ordering is strictly per-key -> shared/exclusive schema gate. Exclusive
// writers never acquire a per-key slot, so no deadlock cycle can form, and
// waiters for the same key do not hold the shared gate while queued.
type writeCoordinator struct {
	mu              sync.Mutex
	activeExclusive bool
	waitingExcl     int
	activeShared    int
	notify          chan struct{}
	keys            map[string]*keyLockSlot
}

type keyLockSlot struct {
	refs   int
	locked bool
	notify chan struct{}
}

func (c *writeCoordinator) waitChanLocked() <-chan struct{} {
	if c.notify == nil {
		c.notify = make(chan struct{})
	}
	return c.notify
}

func (c *writeCoordinator) broadcastLocked() {
	if c.notify != nil {
		close(c.notify)
		c.notify = nil
	}
}

func (c *writeCoordinator) lockExclusive(ctx context.Context) (func(), error) {
	c.mu.Lock()
	c.waitingExcl++
	for c.activeExclusive || c.activeShared > 0 {
		ch := c.waitChanLocked()
		c.mu.Unlock()
		select {
		case <-ch:
			c.mu.Lock()
		case <-ctx.Done():
			c.mu.Lock()
			c.waitingExcl--
			c.broadcastLocked()
			c.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	c.waitingExcl--
	c.activeExclusive = true
	c.mu.Unlock()

	return func() {
		c.mu.Lock()
		c.activeExclusive = false
		c.broadcastLocked()
		c.mu.Unlock()
	}, nil
}

func (c *writeCoordinator) lockTask(ctx context.Context, key string) (func(), error) {
	if err := c.acquireKey(ctx, key); err != nil {
		return nil, err
	}

	c.mu.Lock()
	for c.activeExclusive || c.waitingExcl > 0 {
		ch := c.waitChanLocked()
		c.mu.Unlock()
		select {
		case <-ch:
			c.mu.Lock()
		case <-ctx.Done():
			c.releaseKey(key)
			return nil, ctx.Err()
		}
	}
	c.activeShared++
	c.mu.Unlock()

	return func() {
		c.mu.Lock()
		c.activeShared--
		if c.activeShared == 0 {
			c.broadcastLocked()
		}
		c.releaseKeyLocked(key)
		c.mu.Unlock()
	}, nil
}

func (c *writeCoordinator) acquireKey(ctx context.Context, key string) error {
	c.mu.Lock()
	if c.keys == nil {
		c.keys = make(map[string]*keyLockSlot)
	}
	slot, ok := c.keys[key]
	if !ok {
		slot = &keyLockSlot{}
		c.keys[key] = slot
	}
	slot.refs++
	for slot.locked {
		if slot.notify == nil {
			slot.notify = make(chan struct{})
		}
		ch := slot.notify
		c.mu.Unlock()
		select {
		case <-ch:
			c.mu.Lock()
		case <-ctx.Done():
			c.mu.Lock()
			slot.refs--
			if slot.refs == 0 && !slot.locked {
				delete(c.keys, key)
			}
			c.mu.Unlock()
			return ctx.Err()
		}
	}
	slot.locked = true
	c.mu.Unlock()
	return nil
}

func (c *writeCoordinator) releaseKey(key string) {
	c.mu.Lock()
	c.releaseKeyLocked(key)
	c.mu.Unlock()
}

func (c *writeCoordinator) releaseKeyLocked(key string) {
	slot, ok := c.keys[key]
	if !ok {
		return
	}
	slot.locked = false
	slot.refs--
	if slot.notify != nil {
		close(slot.notify)
		slot.notify = nil
	}
	if slot.refs == 0 {
		delete(c.keys, key)
	}
}
