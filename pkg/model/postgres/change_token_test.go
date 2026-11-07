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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ChangeToken is what the UI's response caches trust to say "nothing was
// committed since": a commit must move it at once, not when the server's
// statistics next catch up. A token that lagged served a task's
// pre-close detail right after its close (the installer e2e, on a fresh
// install's Postgres).
func TestChangeTokenMovesOnEveryCommit(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatalf("put: %v", err)
	}
	for i := 0; i < 20; i++ {
		before, err := store.ChangeToken(ctx)
		if err != nil {
			t.Fatalf("change token: %v", err)
		}
		if err := store.ObserveField(ctx, "a1b2", time.Now(), func(o *model.Observation) {
			o.MergeQueueRearms++
		}); err != nil {
			t.Fatalf("observe: %v", err)
		}
		after, err := store.ChangeToken(ctx)
		if err != nil {
			t.Fatalf("change token: %v", err)
		}
		if after == before {
			t.Fatalf("commit %d left the change token at %q", i, before)
		}
	}
}
