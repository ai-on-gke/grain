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
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// operator is the {human, operator} every write recorded before
// migration 85, with no source.
var operator = model.Principal{Kind: model.PrincipalHuman, ID: "operator"}

// seedPrincipalRows writes one row with operator, given source, in it to
// every table migration 85 gives a source column.
func seedPrincipalRows(t *testing.T, ctx context.Context, store *model.Store, source model.AuthSource) {
	t.Helper()
	operator := operator
	operator.Source = source
	tk := task("t1", false)
	tk.Origin.Attribution = model.Attribution{Actor: operator, OnBehalfOf: &model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: source}}
	tk.Approval = &model.Attribution{Actor: operator}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: "t1", Author: model.Attribution{Actor: operator}, Body: "hi", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pause(ctx, model.TaskPause{TaskID: "t1", PausedAt: now, PausedBy: operator, FromState: model.StateQueued}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPipelinePause(ctx, model.PipelinePause{PausedAt: now, Reason: "r", By: operator}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{Branch: "main", RequestedAt: now, By: operator}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemory(ctx, model.Memory{Text: "remember", UpdatedBy: operator, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{Name: "notes", Summary: "s", Text: "t", UpdatedBy: operator, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutReleaseNote(ctx, model.ReleaseNote{Period: "2026-W44", Text: "shipped", UpdatedBy: operator, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordTaskEvent(ctx, model.TaskEvent{TaskID: "t1", Kind: model.TaskEventClosed, At: now, By: operator}); err != nil {
		t.Fatal(err)
	}
}

// checkPrincipalRows reads every row seedPrincipalRows wrote back and
// requires each principal to be operator with the source want.
func checkPrincipalRows(t *testing.T, ctx context.Context, store *model.Store, want model.AuthSource) {
	t.Helper()
	wantP := operator
	wantP.Source = want
	check := func(what string, got model.Principal) {
		t.Helper()
		if got != wantP {
			t.Errorf("%s = %+v, want %+v", what, got, wantP)
		}
	}
	got, err := store.GetTask(ctx, "t1")
	if err != nil || got == nil {
		t.Fatalf("reading the task: %v, %v", got, err)
	}
	check("origin actor", got.Origin.Attribution.Actor)
	if b := got.Origin.Attribution.OnBehalfOf; b == nil || b.ID != "alice" || b.Source != want {
		t.Errorf("origin on-behalf-of = %+v, want alice with source %q", b, want)
	}
	if got.Approval == nil {
		t.Fatal("the approval did not survive")
	}
	check("approval actor", got.Approval.Actor)
	comments, err := store.Comments(ctx, "t1")
	if err != nil || len(comments) != 1 {
		t.Fatalf("reading comments: %v, %v", comments, err)
	}
	check("comment author", comments[0].Author.Actor)
	pause, err := store.GetPause(ctx, "t1")
	if err != nil || pause == nil {
		t.Fatalf("reading the pause: %v, %v", pause, err)
	}
	check("task pause", pause.PausedBy)
	pp, err := store.GetPipelinePause(ctx)
	if err != nil || pp == nil {
		t.Fatalf("reading the pipeline pause: %v, %v", pp, err)
	}
	check("pipeline pause", pp.By)
	up, err := store.GetScheduledUpgrade(ctx)
	if err != nil || up == nil {
		t.Fatalf("reading the scheduled upgrade: %v, %v", up, err)
	}
	check("scheduled upgrade", up.By)
	mem, err := store.GetMemory(ctx, nil)
	if err != nil || mem == nil {
		t.Fatalf("reading the memory: %v, %v", mem, err)
	}
	check("memory", mem.UpdatedBy)
	doc, err := store.GetMemoryDocument(ctx, nil, "notes")
	if err != nil || doc == nil {
		t.Fatalf("reading the memory document: %v, %v", doc, err)
	}
	check("memory document", doc.UpdatedBy)
	note, err := store.GetReleaseNote(ctx, "2026-W44")
	if err != nil || note == nil {
		t.Fatalf("reading the release note: %v, %v", note, err)
	}
	check("release note", note.UpdatedBy)
	events, err := store.TaskEvents(ctx, "t1")
	if err != nil || len(events) != 1 {
		t.Fatalf("reading task events: %v, %v", events, err)
	}
	check("task event", events[0].By)
	if events[0].OnBehalfOf != nil {
		t.Errorf("task event on-behalf-of = %+v, want none", events[0].OnBehalfOf)
	}
}

// TestMigration85AddsPrincipalSourcesWithoutRewritingRows is
// operator-attribution spec §7.1: a schema-84 store gains every *_source
// column and config_audit, every row it held reads back with an empty
// source, and a row written afterwards keeps the source it was given.
func TestMigration85AddsPrincipalSourcesWithoutRewritingRows(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	seedPrincipalRows(t, ctx, store, "")
	rewindToSchema84(t, ctx, db)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-84 store: %v", err)
	}
	checkPrincipalRows(t, ctx, store, "")

	fresh, _, _ := openStore(t)
	seedPrincipalRows(t, ctx, fresh, model.SourceAccessCode)
	checkPrincipalRows(t, ctx, fresh, model.SourceAccessCode)

	by := model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceAccessCode}
	at := now.Add(time.Minute)
	if _, err := store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: at, By: by, Surface: "settings", Summary: "changed the default model",
	}); err != nil {
		t.Fatalf("recording into the new config_audit: %v", err)
	}
	audit, err := store.ConfigAudit(ctx, 0)
	if err != nil || len(audit) != 1 {
		t.Fatalf("reading config_audit: %v, %v", audit, err)
	}
	if a := audit[0]; a.By != by || a.Surface != "settings" || a.OnBehalfOf != nil || !a.At.Equal(at) {
		t.Errorf("config audit entry = %+v", a)
	}
}

// rewindToSchema84 is rewindSchemaTo(84), which drops migration 85's
// columns, and config_audit with them.
func rewindToSchema84(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rewindSchemaTo(t, ctx, db, 84)
	if _, err := db.ExecContext(ctx, "DROP TABLE `config_audit`"); err != nil {
		t.Fatal(err)
	}
}
