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

package model_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Compacting to the handover a task already has is a confirmation, as
// writing it again with write_handover is: the cursor moves and the age
// resets, but the version stays and no second copy is posted
// (durable-tasks.design.md, "A required, versioned handover";
// docs/test-runs/2026-11-11-durable-tasks.md, finding 3).
func TestCompactingToTheSameHandoverConfirmsIt(t *testing.T) {
	store, ctx := open(t)
	tk := queuedTask(t, store, ctx, "9101", &widgets)
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	say := func(body string, at time.Time) int64 {
		t.Helper()
		id, err := store.AddComment(ctx, model.Comment{TaskID: tk.ID, Author: human, Body: body, CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	say("first note", now)
	first, err := store.CompactTask(ctx, tk.ID, "the hub is green", 0, human, "alice", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	later := say("second note", now.Add(2*time.Minute))
	again, err := store.CompactTask(ctx, tk.ID, "  the hub is green\n", 0, human, "alice", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if again.HandoverVersion != first.HandoverVersion || again.CompactedThrough != later {
		t.Errorf("after the same handover again: version %d (was %d), cursor %d (want %d)",
			again.HandoverVersion, first.HandoverVersion, again.CompactedThrough, later)
	}
	if again.HandoverConfirmedAt == nil || !again.HandoverConfirmedAt.Equal(now.Add(3*time.Minute)) {
		t.Errorf("handover confirmed at %v, want the second compaction's time", again.HandoverConfirmedAt)
	}
	comments, err := store.Comments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	handovers := 0
	for _, c := range comments {
		if c.Handover {
			handovers++
		}
	}
	if handovers != 1 {
		t.Errorf("%d handover comments on the task, want the one", handovers)
	}
	changed, err := store.CompactTask(ctx, tk.ID, "the hub is green; watch the nightly", 0, human, "alice", now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if changed.HandoverVersion != first.HandoverVersion+1 {
		t.Errorf("a new handover body left the version at %d, want %d", changed.HandoverVersion, first.HandoverVersion+1)
	}
}
