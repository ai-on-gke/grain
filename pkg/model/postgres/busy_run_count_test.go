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

// BusyRunCount on PostgreSQL, where `task`.`interactive` is BOOLEAN: the
// query once compared it through COALESCE(`interactive`, 0), which
// PostgreSQL refuses outright ("COALESCE types boolean and integer cannot
// be matched"), so `grain upgrade start -after-tasks-finish` never
// started. A live non-interactive run counts, a live interactive one does
// not, a finished one does not, and a live run whose task row is gone
// counts -- the cautious reading of work nobody can attribute.
func TestBusyRunCountOnPostgres(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	if n, err := store.BusyRunCount(ctx); err != nil || n != 0 {
		t.Fatalf("BusyRunCount on an empty store = (%d, %v), want (0, nil)", n, err)
	}

	chat := task("b2c3", true)
	chat.Interactive = true
	for _, tk := range []model.Task{task("a1b2", true), chat, task("c3d4", true), task("d4e5", true)} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("creating %s: %v", tk.ID, err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: "run-" + tk.ID, TaskID: tk.ID, Sandbox: "sandbox-" + tk.ID,
			Attempt: 1, StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatalf("starting a run on %s: %v", tk.ID, err)
		}
	}
	if err := store.FinishRun(ctx, "run-c3d4", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatalf("finishing run-c3d4: %v", err)
	}
	// An orphan: the run is still live, but its task row has gone.
	if _, err := db.ExecContext(ctx, `DELETE FROM "task" WHERE "id" = $1`, "d4e5"); err != nil {
		t.Fatalf("deleting d4e5's task row: %v", err)
	}

	n, err := store.BusyRunCount(ctx)
	if err != nil {
		t.Fatalf("BusyRunCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("BusyRunCount = %d, want 2 (a1b2's live run and d4e5's orphan; "+
			"not b2c3's interactive run nor c3d4's finished one)", n)
	}
}
