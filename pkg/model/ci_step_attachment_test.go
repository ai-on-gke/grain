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

func TestCIStepAttachmentsAreCheckpointedAndClearedWithTheirRun(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{"live", "dead"} {
		if err := store.PutCIRun(ctx, model.CIRun{ID: id, Key: id, Repo: "acme/widgets", Ref: "main", Event: "push", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	put := func(a model.CIStepAttachment) {
		t.Helper()
		if err := store.PutCIStepAttachment(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	put(model.CIStepAttachment{RunID: "live", JobID: "go", StepID: "a1-s0", Sandbox: "c1", ExecID: "exec-1", Cursor: 10, UpdatedAt: now})
	// A checkpoint moves the same row.
	put(model.CIStepAttachment{RunID: "live", JobID: "go", StepID: "a1-s0", Sandbox: "c1", ExecID: "exec-1", Cursor: 4096, UpdatedAt: now})
	put(model.CIStepAttachment{RunID: "live", JobID: "lint", StepID: "a1-s2", Sandbox: "c2", ExecID: "exec-9", UpdatedAt: now})
	put(model.CIStepAttachment{RunID: "dead", JobID: "go", StepID: "a1-s0", Sandbox: "c3", ExecID: "exec-3", UpdatedAt: now})

	got, err := store.CIStepAttachments(ctx, "live")
	if err != nil || len(got) != 2 || got[0].Cursor != 4096 || got[0].ExecID != "exec-1" || got[1].JobID != "lint" {
		t.Fatalf("CIStepAttachments(live) = %+v, %v; want go at 4096 and lint", got, err)
	}

	if err := store.DeleteCIStepAttachments(ctx, "live", "lint"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.CIStepAttachments(ctx, "live"); len(got) != 1 || got[0].JobID != "go" {
		t.Fatalf("after clearing lint: %+v, want only go", got)
	}

	// Recovery stamps the run nothing reattached, and its attachments go
	// with it; the attached run keeps its own.
	if n, err := store.RecoverInterruptedCIRunsExcept(ctx, "cancelled", "interrupted", now, []string{"live"}); err != nil || n != 1 {
		t.Fatalf("recover = %d, %v; want 1", n, err)
	}
	if got, _ := store.CIStepAttachments(ctx, "dead"); len(got) != 0 {
		t.Fatalf("a stamped run's attachments = %+v, want none", got)
	}
	if got, _ := store.CIStepAttachments(ctx, "live"); len(got) != 1 {
		t.Fatalf("an attached run's attachments = %+v, want its one", got)
	}
}
