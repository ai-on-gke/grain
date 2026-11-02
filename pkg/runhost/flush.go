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

package runhost

import (
	"context"
	"sync"
)

// tails is the set of runs a Host is tailing right now, so an upgrade
// can tell every one of them to let go before it restarts the daemon
// (Host.Flush). Its zero value is ready to use.
type tails struct {
	mu sync.Mutex
	// live maps a run to the channel its Attach watches: closed, it
	// detaches.
	live map[string]chan struct{}
	// flushing is set by Flush and cleared by Resume. An Attach that
	// starts while it is set -- a run host whose creation was already
	// under way when the hold went on -- detaches on its first poll.
	flushing bool
	wg       sync.WaitGroup
}

// track registers run as tailed, returning the channel that tells its
// Attach to detach and the func Attach calls as it returns.
func (t *tails) track(run string) (<-chan struct{}, func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.live == nil {
		t.live = map[string]chan struct{}{}
	}
	ch := make(chan struct{})
	if t.flushing {
		close(ch)
	}
	t.live[run] = ch
	t.wg.Add(1)
	return ch, func() {
		t.mu.Lock()
		if t.live[run] == ch {
			delete(t.live, run)
		}
		t.mu.Unlock()
		t.wg.Done()
	}
}

// Flush is step 3 of the upgrade protocol (design/design/non-disruptive-upgrade.design.md,
// piece 6): it stops tailing every run this Host is tailing, has each
// write its spool cursor where it stands, and waits until every one of
// them has handed back ErrDetached -- after which nothing of theirs
// writes the store again, and their run hosts carry on with no
// controller until the next daemon adopts them. It returns the runs it
// detached.
//
// It waits for the tails only, which is seconds: nothing here waits on
// the work. ctx bounds the wait; a tail still going when it is done is
// reported as ctx's error, and is detached by the daemon's own stop as
// before (Host.Detach).
//
// Until Resume, a run that starts tailing detaches at once.
func (h *Host) Flush(ctx context.Context) ([]string, error) {
	return h.tails.flushAll(ctx)
}

// flushAll tells every tracked run to let go, and waits until each has.
func (t *tails) flushAll(ctx context.Context) ([]string, error) {
	t.mu.Lock()
	t.flushing = true
	runs := make([]string, 0, len(t.live))
	for run, ch := range t.live {
		runs = append(runs, run)
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	t.mu.Unlock()
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return runs, nil
	case <-ctx.Done():
		return runs, ctx.Err()
	}
}

// Resume undoes Flush's refusal of new tails, for an upgrade whose
// restart did not happen: this daemon carries on, and it re-adopts the
// runs Flush detached the way a new one would (orchestrator.RecoverRunHosts).
func (h *Host) Resume() { h.tails.resume() }

func (t *tails) resume() {
	t.mu.Lock()
	t.flushing = false
	t.mu.Unlock()
}
