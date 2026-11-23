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
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The reads Store prepares once and reuses (GetTask, IsReady,
// GrantWaitOf, ...) answer from the store as it is now, not as it was
// when the statement was first run: a reused statement's rows are reset
// when closed rather than left holding a read snapshot open, so a write
// made between two reads is seen by the second -- and closing the store
// closes the statements without breaking a later read of the same
// database.
func TestPreparedReadsSeeLaterWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)
	now := time.Now().UTC()
	task := model.Task{ID: "prepared-1", Title: "first", Intent: model.IntentImplement, CreatedAt: &now}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	for _, want := range []string{"first", "second", "third"} {
		if want != "first" {
			task.Title = want
			if err := st.PutTask(ctx, task); err != nil {
				t.Fatalf("PutTask: %v", err)
			}
		}
		got, err := st.GetTask(ctx, task.ID)
		if err != nil || got == nil {
			t.Fatalf("GetTask = %v, %v", got, err)
		}
		if got.Title != want {
			t.Fatalf("GetTask title = %q after writing %q", got.Title, want)
		}
	}

	if w, err := st.GrantWaitOf(ctx, task.ID); err != nil || w != nil {
		t.Fatalf("GrantWaitOf before any wait = %v, %v", w, err)
	}
	if err := st.SetGrantWait(ctx, model.GrantWait{TaskID: task.ID, Capability: model.QuotaWaitCapability,
		Resource: "full", WaitingAt: now}); err != nil {
		t.Fatalf("SetGrantWait: %v", err)
	}
	if w, err := st.GrantWaitOf(ctx, task.ID); err != nil || w == nil || w.Resource != "full" {
		t.Fatalf("GrantWaitOf after SetGrantWait = %+v, %v", w, err)
	}
	if err := st.ClearGrantWait(ctx, task.ID); err != nil {
		t.Fatalf("ClearGrantWait: %v", err)
	}
	if w, err := st.GrantWaitOf(ctx, task.ID); err != nil || w != nil {
		t.Fatalf("GrantWaitOf after ClearGrantWait = %+v, %v", w, err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, err := st.GetTask(ctx, task.ID); err != nil || got == nil || got.Title != "third" {
		t.Fatalf("GetTask after Close = %v, %v", got, err)
	}
}
