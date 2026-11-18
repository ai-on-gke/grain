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

package daemon

import (
	"bytes"
	"context"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestPostgresSchemaUpgradeResyncsSequences verifies that rebuilding a
// PostgreSQL store across a schema change through openStoreForSchemaUpgrade
// advances every serial sequence past the imported explicit IDs so subsequent
// inserts into task_sequence, task_comment, task_event, config_audit and
// suite_item succeed with IDs strictly greater than the imported maxima.
func TestPostgresSchemaUpgradeResyncsSequences(t *testing.T) {
	ctx := context.Background()
	dsn, _, _, _ := daemonPostgresTestSchemaDSN(t)
	dataDir := t.TempDir()

	store0, db0, err := OpenStore("postgres", dsn, dataDir)
	if err != nil {
		t.Fatalf("OpenStore initial: %v", err)
	}

	by := model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}
	author := model.Attribution{Actor: by}

	var maxTaskSeq int64
	var lastTaskID string
	for i := range 3 {
		id, err := store0.NewTaskID(ctx)
		if err != nil {
			db0.Close()
			t.Fatalf("NewTaskID %d: %v", i, err)
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			db0.Close()
			t.Fatalf("parsing task id %q: %v", id, err)
		}
		if n > maxTaskSeq {
			maxTaskSeq = n
		}
		lastTaskID = id
		if err := store0.PutTask(ctx, model.Task{
			ID:    id,
			Title: "Seeded task " + id,
		}); err != nil {
			db0.Close()
			t.Fatalf("PutTask %s: %v", id, err)
		}
	}

	var maxCommentID int64
	for i := range 3 {
		cid, err := store0.AddComment(ctx, model.Comment{
			TaskID:    lastTaskID,
			Author:    author,
			Body:      "Seeded comment " + strconv.Itoa(i),
			CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			db0.Close()
			t.Fatalf("AddComment %d: %v", i, err)
		}
		if cid > maxCommentID {
			maxCommentID = cid
		}
	}

	var maxEventID int64
	for i := range 3 {
		eid, err := store0.RecordTaskEvent(ctx, model.TaskEvent{
			TaskID: lastTaskID,
			Kind:   model.TaskEventSubmitted,
			At:     time.Now().UTC(),
			By:     by,
			Reason: "seeded event " + strconv.Itoa(i),
		})
		if err != nil {
			db0.Close()
			t.Fatalf("RecordTaskEvent %d: %v", i, err)
		}
		if eid > maxEventID {
			maxEventID = eid
		}
	}

	var maxAuditID int64
	for i := range 3 {
		aid, err := store0.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At:      time.Now().UTC(),
			By:      by,
			Surface: "settings",
			Summary: "seeded audit " + strconv.Itoa(i),
		})
		if err != nil {
			db0.Close()
			t.Fatalf("RecordConfigAudit %d: %v", i, err)
		}
		if aid > maxAuditID {
			maxAuditID = aid
		}
	}

	if err := store0.PutTemplate(ctx, model.Template{
		ID:    "tpl-seq-check",
		Name:  "tpl-seq-check",
		Title: "Seq Check",
		Body:  "Check suite_item sequence.",
	}); err != nil {
		db0.Close()
		t.Fatalf("PutTemplate: %v", err)
	}
	if err := store0.PutSuite(ctx, model.Suite{
		ID:        "suite-seq-check",
		Name:      "suite-seq-check",
		Mode:      model.SuiteCount,
		Count:     1,
		CreatedAt: time.Now().UTC(),
		Items: []model.SuiteItem{
			{TemplateID: "tpl-seq-check"},
			{TemplateID: "tpl-seq-check"},
		},
	}); err != nil {
		db0.Close()
		t.Fatalf("PutSuite: %v", err)
	}
	var maxSuiteItemID int64
	if err := db0.QueryRowContext(ctx, "SELECT MAX(`id`) FROM `suite_item`").Scan(&maxSuiteItemID); err != nil {
		db0.Close()
		t.Fatalf("reading max suite_item id: %v", err)
	}

	// Export db0 as a schema-change note from futureSchema -> SchemaVersion and
	// stamp db0 at futureSchema so openStoreForSchemaUpgrade archives db0 into
	// an aside Postgres schema, creates a fresh store, and runs
	// importSchemaUpgrade -> staterepo.ImportMigrated.
	otherSchema := model.SchemaVersion + 1
	if _, err := WriteSchemaUpgrade(ctx, dataDir, db0, "latest", otherSchema, model.SchemaVersion); err != nil {
		db0.Close()
		t.Fatalf("WriteSchemaUpgrade: %v", err)
	}
	if _, err := db0.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", otherSchema); err != nil {
		db0.Close()
		t.Fatalf("stamping store at schema %d: %v", otherSchema, err)
	}
	if err := db0.Close(); err != nil {
		t.Fatalf("closing initial db: %v", err)
	}

	store1, db1, migrated, err := openStoreForSchemaUpgrade(ctx, "postgres", dsn, dataDir)
	if err != nil {
		t.Fatalf("openStoreForSchemaUpgrade: %v", err)
	}
	defer db1.Close()
	if !migrated.note || migrated.from != otherSchema {
		t.Fatalf("migrated = %+v, want note=true from=%d", migrated, otherSchema)
	}

	newTaskIDStr, err := store1.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("NewTaskID after schema-change import failed: %v", err)
	}
	newTaskSeq, err := strconv.ParseInt(newTaskIDStr, 10, 64)
	if err != nil {
		t.Fatalf("parsing new task id %q: %v", newTaskIDStr, err)
	}
	if newTaskSeq <= maxTaskSeq {
		t.Errorf("new task_sequence number = %d, want > %d", newTaskSeq, maxTaskSeq)
	}

	newCommentID, err := store1.AddComment(ctx, model.Comment{
		TaskID:    lastTaskID,
		Author:    author,
		Body:      "Post-upgrade comment",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("AddComment after schema-change import failed: %v", err)
	}
	if newCommentID <= maxCommentID {
		t.Errorf("new task_comment id = %d, want > %d", newCommentID, maxCommentID)
	}

	newEventID, err := store1.RecordTaskEvent(ctx, model.TaskEvent{
		TaskID: lastTaskID,
		Kind:   model.TaskEventRetried,
		At:     time.Now().UTC(),
		By:     by,
		Reason: "post-upgrade event",
	})
	if err != nil {
		t.Fatalf("RecordTaskEvent after schema-change import failed: %v", err)
	}
	if newEventID <= maxEventID {
		t.Errorf("new task_event id = %d, want > %d", newEventID, maxEventID)
	}

	newAuditID, err := store1.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At:      time.Now().UTC(),
		By:      by,
		Surface: "settings",
		Summary: "post-upgrade audit",
	})
	if err != nil {
		t.Fatalf("RecordConfigAudit after schema-change import failed: %v", err)
	}
	if newAuditID <= maxAuditID {
		t.Errorf("new config_audit id = %d, want > %d", newAuditID, maxAuditID)
	}

	if err := store1.PutSuite(ctx, model.Suite{
		ID:        "suite-seq-check-2",
		Name:      "suite-seq-check-2",
		Mode:      model.SuiteCount,
		Count:     1,
		CreatedAt: time.Now().UTC(),
		Items:     []model.SuiteItem{{TemplateID: "tpl-seq-check"}},
	}); err != nil {
		t.Fatalf("PutSuite after schema-change import failed: %v", err)
	}
	var newSuiteItemID int64
	if err := db1.QueryRowContext(ctx, "SELECT MAX(`id`) FROM `suite_item` WHERE `suite_id` = 'suite-seq-check-2'").Scan(&newSuiteItemID); err != nil {
		t.Fatalf("reading new suite_item id: %v", err)
	}
	if newSuiteItemID <= maxSuiteItemID {
		t.Errorf("new suite_item id = %d, want > %d", newSuiteItemID, maxSuiteItemID)
	}
}

// TestPostgresOpenStoreDetectsAndResyncsBehindSequences verifies the defensive
// startup check: when a PostgreSQL store has serial sequences behind MAX(col),
// opening the store logs the discrepancy and advances them without rewinding
// sequences that are already ahead.
func TestPostgresOpenStoreDetectsAndResyncsBehindSequences(t *testing.T) {
	ctx := context.Background()
	dsn, _, _, _ := daemonPostgresTestSchemaDSN(t)
	dataDir := t.TempDir()

	store0, db0, err := OpenStore("postgres", dsn, dataDir)
	if err != nil {
		t.Fatalf("OpenStore initial: %v", err)
	}

	by := model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}
	author := model.Attribution{Actor: by}
	var maxTaskSeq int64
	var taskID string
	for range 3 {
		id, err := store0.NewTaskID(ctx)
		if err != nil {
			db0.Close()
			t.Fatal(err)
		}
		taskID = id
		maxTaskSeq, _ = strconv.ParseInt(id, 10, 64)
	}
	if err := store0.PutTask(ctx, model.Task{ID: taskID, Title: "Startup sequence check"}); err != nil {
		db0.Close()
		t.Fatal(err)
	}
	maxCommentID, err := store0.AddComment(ctx, model.Comment{
		TaskID: taskID, Author: author, Body: "first", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		db0.Close()
		t.Fatal(err)
	}
	maxEventID, err := store0.RecordTaskEvent(ctx, model.TaskEvent{
		TaskID: taskID, Kind: model.TaskEventSubmitted, At: time.Now().UTC(), By: by,
	})
	if err != nil {
		db0.Close()
		t.Fatal(err)
	}
	// Advance config_audit past MAX(id) by deleting the highest row, to confirm
	// startup resync never rewinds a sequence that is already ahead.
	for range 2 {
		if _, err := store0.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At: time.Now().UTC(), By: by, Surface: "settings", Summary: "entry",
		}); err != nil {
			db0.Close()
			t.Fatal(err)
		}
	}
	var aheadAuditID int64
	aheadAuditID, err = store0.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: time.Now().UTC(), By: by, Surface: "settings", Summary: "deleted-top",
	})
	if err != nil {
		db0.Close()
		t.Fatal(err)
	}
	if _, err := db0.ExecContext(ctx, "DELETE FROM `config_audit` WHERE `id` = ?", aheadAuditID); err != nil {
		db0.Close()
		t.Fatal(err)
	}

	// Simulate a bulk explicit-id insert that left sequences at 1.
	for _, reset := range []string{
		"SELECT setval(pg_get_serial_sequence('task_sequence', 'number'), 1, false)",
		"SELECT setval(pg_get_serial_sequence('task_comment', 'id'), 1, false)",
		"SELECT setval(pg_get_serial_sequence('task_event', 'id'), 1, false)",
	} {
		if _, err := db0.ExecContext(ctx, reset); err != nil {
			db0.Close()
			t.Fatalf("resetting sequence (%s): %v", reset, err)
		}
	}
	if err := db0.Close(); err != nil {
		t.Fatal(err)
	}

	prevOut := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	store1, db1, err := OpenStore("postgres", dsn, dataDir)
	if err != nil {
		t.Fatalf("OpenStore reopen: %v", err)
	}
	defer db1.Close()

	logs := buf.String()
	for _, want := range []string{"task_sequence.number", "task_comment.id", "task_event.id"} {
		if !strings.Contains(logs, want) {
			t.Errorf("expected startup log to report behind sequence for %s; got logs:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "config_audit.id") {
		t.Errorf("expected ahead sequence config_audit.id not to be reported behind; got logs:\n%s", logs)
	}

	nextTaskID, err := store1.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("NewTaskID after reopen: %v", err)
	}
	if got, _ := strconv.ParseInt(nextTaskID, 10, 64); got <= maxTaskSeq {
		t.Errorf("next task id = %d, want > %d", got, maxTaskSeq)
	}
	nextCommentID, err := store1.AddComment(ctx, model.Comment{
		TaskID: taskID, Author: author, Body: "second", CreatedAt: time.Now().UTC(),
	})
	if err != nil || nextCommentID <= maxCommentID {
		t.Fatalf("AddComment after reopen = (%d, %v), want id > %d", nextCommentID, err, maxCommentID)
	}
	nextEventID, err := store1.RecordTaskEvent(ctx, model.TaskEvent{
		TaskID: taskID, Kind: model.TaskEventRetried, At: time.Now().UTC(), By: by,
	})
	if err != nil || nextEventID <= maxEventID {
		t.Fatalf("RecordTaskEvent after reopen = (%d, %v), want id > %d", nextEventID, err, maxEventID)
	}
	nextAuditID, err := store1.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: time.Now().UTC(), By: by, Surface: "settings", Summary: "after-reopen",
	})
	if err != nil {
		t.Fatalf("RecordConfigAudit after reopen: %v", err)
	}
	if nextAuditID <= aheadAuditID {
		t.Errorf("RecordConfigAudit rewound ahead sequence: got %d, want > %d", nextAuditID, aheadAuditID)
	}
}
