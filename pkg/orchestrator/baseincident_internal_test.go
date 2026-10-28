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

package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// docs/test-runs/2026-10-13-fixes-red-base-branch.md, finding 1: a task a
// person marked as fixing main, dispatched alongside grain's own repair of
// it, was told in its prompt that the other task was repairing main and
// not to fix shared base faults -- the very fault it was marked to fix.
// Work that is not a repair of the branch is still told.
func TestBaseIncidentNoteIsNotForARepairOfTheBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo, _ := redMain(t, ctx, store)
	fileMarkedTask(t, ctx, store, "deflake", repo)
	fileTaggedTask(t, ctx, store, "feature", repo)
	fileTaggedTask(t, ctx, store, "older-repair", repo, BaseCIRepairBranchTag(repo, "main"))

	for id, want := range map[string]bool{
		"feature": true, "deflake": false, "grain-repair": false, "older-repair": false,
	} {
		task, err := store.GetTask(ctx, id)
		if err != nil || task == nil {
			t.Fatalf("reading %s: %v", id, err)
		}
		note := baseIncidentNote(ctx, store, *task)
		if got := strings.Contains(note, "Do not attempt to fix shared base faults"); got != want {
			t.Errorf("%s: told to leave main's fault alone = %v, want %v (note %q)", id, got, want, note)
		}
	}
}
