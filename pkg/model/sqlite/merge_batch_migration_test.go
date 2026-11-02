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

package sqlite_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Migration 85 rebuilds merge_batch with task_id in its primary key
// (grain/task-2390). An overlap re-check in flight across the upgrade is
// carried over with an empty task_id, which the queue still reads and
// cleans up, and once migrated one branch holds a re-check for each of
// several changes.
func TestMigration85KeysMergeBatchesByTask(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	// merge_batch as schema 84 had it: keyed by branch alone.
	for _, stmt := range []string{
		"DROP TABLE `merge_batch`",
		"CREATE TABLE `merge_batch` (`owner` TEXT NOT NULL, `name` TEXT NOT NULL, `branch` TEXT NOT NULL, " +
			"`ref` TEXT NOT NULL, `base_commit` TEXT NOT NULL, `commit` TEXT NOT NULL, " +
			"`members` TEXT NOT NULL DEFAULT '', `run_id` TEXT NOT NULL DEFAULT '', `started_at` DATETIME NOT NULL, " +
			"`finished_at` DATETIME NULL, `conclusion` TEXT NOT NULL DEFAULT '', `detail` TEXT NOT NULL DEFAULT '', " +
			"`splits` INTEGER NOT NULL DEFAULT 0, `queued` INTEGER NOT NULL DEFAULT 0, " +
			"`recheck_of` TEXT NOT NULL DEFAULT '', `recheck_detail` TEXT NOT NULL DEFAULT '', `recheck_at` DATETIME NULL, " +
			"PRIMARY KEY (`owner`, `name`, `branch`))",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	started := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	members, err := json.Marshal([]model.MergeBatchMember{{TaskID: "t1", Number: 7, HeadSHA: "head1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `merge_batch` (`owner`,`name`,`branch`,`ref`,`base_commit`,`commit`,`members`,`run_id`,`started_at`,`queued`) "+
			"VALUES ('acme','widgets','main','grain-merge-queue/main','base','tip',?,'run-1',?,1)",
		string(members), started); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 84 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-84 store: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	legacy, err := store.GetMergeBatch(ctx, repo, "main", "")
	if err != nil || legacy == nil {
		t.Fatalf("the re-check in flight across the upgrade = %+v (%v), want it carried over", legacy, err)
	}
	if legacy.Ref != "grain-merge-queue/main" || legacy.RunID != "run-1" || len(legacy.Members) != 1 ||
		legacy.Members[0].TaskID != "t1" || !legacy.StartedAt.Equal(started) {
		t.Errorf("carried-over row = %+v", legacy)
	}

	for _, task := range []string{"t2", "t3"} {
		if err := store.PutMergeBatch(ctx, model.MergeBatch{
			Repo: repo, Branch: "main", TaskID: task, Ref: model.MergeRecheckRef("", task, "main"),
			BaseCommit: "base", Commit: "tip-" + task, RunID: "run-" + task, StartedAt: started,
			Members: []model.MergeBatchMember{{TaskID: task}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListMergeBatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []string
	for _, r := range rows {
		tasks = append(tasks, r.TaskID)
	}
	if len(tasks) != 3 || tasks[0] != "" || tasks[1] != "t2" || tasks[2] != "t3" {
		t.Fatalf("re-checks on main = %q, want the carried-over one, t2 and t3", tasks)
	}
	if err := store.DeleteMergeBatch(ctx, repo, "main", "t2"); err != nil {
		t.Fatal(err)
	}
	if gone, _ := store.GetMergeBatch(ctx, repo, "main", "t2"); gone != nil {
		t.Errorf("t2's re-check survived its delete: %+v", gone)
	}
	if kept, _ := store.GetMergeBatch(ctx, repo, "main", "t3"); kept == nil {
		t.Error("deleting t2's re-check took t3's with it")
	}
}
