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

package postgres_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ObserveField reads a task's observation and writes it back whole. Two of
// them at once must both land: on Postgres' READ COMMITTED, without the
// adapter's writer lock, both read the same row and the later commit
// drops the earlier one's change -- which is how a task's close was lost
// to the orchestrator's next observation in the installer e2e.
func TestConcurrentObserveFieldsLoseNoWrite(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatalf("put: %v", err)
	}
	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.ObserveField(ctx, "a1b2", time.Now(), func(o *model.Observation) {
				o.MergeQueueRearms++
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ObserveField: %v", err)
		}
	}
	obs, err := store.GetObservation(ctx, "a1b2")
	if err != nil || obs == nil {
		t.Fatalf("reading the observation back: %v (nil=%v)", err, obs == nil)
	}
	if obs.MergeQueueRearms != writers {
		t.Errorf("%d concurrent increments left the counter at %d: the rest were lost", writers, obs.MergeQueueRearms)
	}
}
