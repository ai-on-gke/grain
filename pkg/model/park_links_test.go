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
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// A park on tasks holds its task through depends-on links, and once the
// park is resumed those links have done their work. A wait_for_task park
// of A on B, B closing, A resuming, then B being reopened must not hold A
// out of dispatch again: the park is over and no run is waiting on B any
// more. A dependency A declared itself is another matter -- it was
// declared, not parked on -- and a reopened target of one still holds A,
// even when A's park named it too.
func TestAResumedParkDropsTheLinksItWrote(t *testing.T) {
	backends := []struct {
		name string
		open func(t *testing.T) *model.Store
	}{
		{
			name: "sqlite",
			open: func(t *testing.T) *model.Store {
				db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				st := model.New(db)
				if err := st.Init(context.Background()); err != nil {
					t.Fatal(err)
				}
				return st
			},
		},
		{
			name: "postgres",
			open: func(t *testing.T) *model.Store {
				st, _ := postgrestest.Open(t)
				return st
			},
		},
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			store := b.open(t)

			parkedOn := queuedTask(t, store, ctx, "b", &widgets)
			declared := queuedTask(t, store, ctx, "c", &widgets)
			// A declared c when it was filed; its run then parked on both
			// b and c, and only b's link is the park's own
			// (ui.Client.ResumeLater, which found c's already there).
			a := dependentOf(t, store, ctx, "a",
				model.Link{Kind: model.LinkDependsOn, Target: declared.ID},
				model.Link{Kind: model.LinkDependsOn, Target: parkedOn.ID})
			if err := store.StartRun(ctx, model.Run{ID: "a-1", TaskID: a.ID, Attempt: 1, StartedAt: now},
				model.Limits{}); err != nil {
				t.Fatal(err)
			}
			if err := store.Suspend(ctx, model.Suspension{
				RunID: "a-1", Kind: model.SuspendRequested,
				Detail: model.ParkedDetail([]string{parkedOn.ID, declared.ID}),
				ResumeOn: model.ResumeOn{
					Tasks: []string{parkedOn.ID, declared.ID},
					Links: []string{parkedOn.ID},
				}.JSON(),
				CreatedAt: now.Add(time.Minute),
			}); err != nil {
				t.Fatal(err)
			}

			closeTask(t, store, ctx, parkedOn.ID)
			closeTask(t, store, ctx, declared.ID)
			if n := openBlockers(t, store, ctx, a.ID); n != 0 {
				t.Fatalf("open blockers = %d once both targets closed, want 0", n)
			}

			// A reopen while the park still stands holds the task again:
			// that is the park doing its job.
			reopenTask(t, store, ctx, parkedOn.ID)
			if n := openBlockers(t, store, ctx, a.ID); n != 1 {
				t.Fatalf("open blockers = %d with b reopened before the park resumed, want 1", n)
			}
			closeTask(t, store, ctx, parkedOn.ID)

			// The next run resumes the park.
			if err := store.StartRun(ctx, model.Run{ID: "a-2", TaskID: a.ID, Attempt: 2,
				StartedAt: now.Add(time.Hour)}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			if err := store.FinishRun(ctx, "a-2", now.Add(2*time.Hour), "failed", ""); err != nil {
				t.Fatal(err)
			}
			links := storedLinks(t, store, ctx, a.ID)
			if hasLink(links, model.LinkDependsOn, parkedOn.ID) {
				t.Errorf("links = %+v after the park resumed, want the park's own link to b gone", links)
			}
			if !hasLink(links, model.LinkDependsOn, declared.ID) {
				t.Errorf("links = %+v after the park resumed, want the declared link to c kept", links)
			}
			susps, err := store.Suspensions(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(susps) != 1 || susps[0].ResumedRunID != "a-2" {
				t.Fatalf("suspensions = %+v, want a-1's park resumed by a-2", susps)
			}

			reopenTask(t, store, ctx, parkedOn.ID)
			if n := openBlockers(t, store, ctx, a.ID); n != 0 {
				t.Errorf("open blockers = %d with b reopened after the park resumed, want 0", n)
			}
			reopenTask(t, store, ctx, declared.ID)
			if n := openBlockers(t, store, ctx, a.ID); n != 1 {
				t.Errorf("open blockers = %d with declared c reopened, want 1", n)
			}
		})
	}
}

func reopenTask(t *testing.T, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	if err := store.ObserveField(ctx, id, now, func(o *model.Observation) {
		o.ClosedAt = nil
	}); err != nil {
		t.Fatalf("reopening %s: %v", id, err)
	}
}
