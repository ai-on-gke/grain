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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestTaskOwnerNormalization(t *testing.T) {
	valid := map[string]string{
		"alice@example.com":       "alice@example.com",
		" user:bob@example.com ":  "bob@example.com",
		"HUMAN:carol@example.com": "carol@example.com",
		"operator":                "operator",
		"serviceAccount:ci@proj.iam.gserviceaccount.com": "serviceAccount:ci@proj.iam.gserviceaccount.com",
	}
	for in, want := range valid {
		got, err := model.NormalizeTaskOwner(in)
		if err != nil {
			t.Errorf("NormalizeTaskOwner(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeTaskOwner(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"user:",
		"grain",
		"unknown",
		"agent:42",
		"external:ci-bot",
		"automation:schedule",
		"run:7",
		"session:3",
		"alice @example.com",
		"alice\n@example.com",
	}
	for _, in := range invalid {
		if got, err := model.NormalizeTaskOwner(in); err == nil {
			t.Errorf("NormalizeTaskOwner(%q) = %q, want error", in, got)
		}
	}
}

func TestTaskOwnerDefaultsInheritanceAndAudit(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)

	// 1. Default owner from Creator when set.
	alice := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP}
	root := model.Task{
		ID:        "1",
		Intent:    model.IntentImplement,
		Title:     "Root task",
		Body:      "Body",
		Creator:   "alice@example.com",
		Binding:   model.BindingDirective,
		CreatedAt: &now,
		Approval:  &model.Attribution{Actor: alice},
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor: alice,
			},
			Reason: model.ReasonDirect,
		},
	}
	if err := store.PutTask(ctx, root); err != nil {
		t.Fatal(err)
	}
	gotRoot, err := store.GetTask(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot.Owner != "alice@example.com" {
		t.Fatalf("root.Owner = %q, want alice@example.com", gotRoot.Owner)
	}

	// 2. Derived task inherits owner via InheritRequester and records proposing run.
	runStart := now.Add(time.Minute)
	if err := store.StartRun(ctx, model.Run{
		ID:        "run-10",
		TaskID:    "1",
		Sandbox:   "sb-10",
		Attempt:   1,
		StartedAt: runStart,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	childCreated := now.Add(2 * time.Minute)
	child := model.Task{
		ID:        "2",
		Intent:    model.IntentImplement,
		Title:     "Proposed child",
		Body:      "Follow-up",
		Binding:   model.BindingDirective,
		CreatedAt: &childCreated,
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: "1"}},
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor:      model.GrainPrincipal,
				OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "1", Source: model.SourceSandbox},
			},
			Reason: model.ReasonProposal,
		},
	}
	model.InheritRequester(&child, *gotRoot)
	if err := store.PutTask(ctx, child); err != nil {
		t.Fatal(err)
	}
	gotChild, err := store.GetTask(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if gotChild.Owner != "alice@example.com" {
		t.Fatalf("child.Owner = %q, want alice@example.com", gotChild.Owner)
	}
	if gotChild.Proposer.TaskID != "1" || gotChild.Proposer.RunID != "run-10" {
		t.Fatalf("child.Proposer = %+v, want TaskID=1 RunID=run-10", gotChild.Proposer)
	}

	ts := model.Transitions(*gotChild, nil, nil, nil, nil)
	if len(ts) == 0 || ts[0].State != model.StateProposed {
		t.Fatalf("Transitions(child) = %+v, want StateProposed first", ts)
	}
	if ts[0].ProposingTaskID != "1" || ts[0].ProposingRunID != "run-10" || ts[0].OnBehalfOfOwner != "alice@example.com" {
		t.Fatalf("Proposed transition = %+v, want task 1 run-10 on behalf of alice@example.com", ts[0])
	}

	// 3. Reassigning parent owner records audit event; existing child keeps its owner,
	// while a future child inherits the updated owner.
	bob := model.Principal{Kind: model.PrincipalHuman, ID: "bob@example.com", Source: model.SourceIAP}
	gotRoot.Owner = "carol@example.com"
	if err := store.PutTask(model.WithOwnerChange(ctx, model.Attribution{Actor: bob}), *gotRoot); err != nil {
		t.Fatal(err)
	}
	updatedRoot, err := store.GetTask(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if updatedRoot.Owner != "carol@example.com" {
		t.Fatalf("updatedRoot.Owner = %q, want carol@example.com", updatedRoot.Owner)
	}
	stillAliceChild, err := store.GetTask(ctx, "2")
	if err != nil {
		t.Fatal(err)
	}
	if stillAliceChild.Owner != "alice@example.com" {
		t.Fatalf("existing child owner changed to %q, want alice@example.com", stillAliceChild.Owner)
	}

	events, err := store.TaskEvents(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	var foundAudit *model.TaskEvent
	for i := range events {
		if events[i].Kind == model.TaskEventOwnerChanged {
			foundAudit = &events[i]
		}
	}
	if foundAudit == nil {
		t.Fatalf("TaskEvents(1) = %+v, want TaskEventOwnerChanged", events)
	}
	oldOwn, newOwn, ok := model.ParseOwnerChangeReason(foundAudit.Reason)
	if !ok || oldOwn != "alice@example.com" || newOwn != "carol@example.com" || foundAudit.By.ID != "bob@example.com" {
		t.Fatalf("owner audit event = %+v, want alice->carol by bob", *foundAudit)
	}

	futureChild := model.Task{
		ID:        "3",
		Intent:    model.IntentImplement,
		Title:     "Future child",
		Body:      "Follow-up 2",
		Binding:   model.BindingDirective,
		CreatedAt: &childCreated,
		Links:     []model.Link{{Kind: model.LinkProposedBy, Target: "1"}},
	}
	model.InheritRequester(&futureChild, *updatedRoot)
	if err := store.PutTask(ctx, futureChild); err != nil {
		t.Fatal(err)
	}
	gotFuture, err := store.GetTask(ctx, "3")
	if err != nil {
		t.Fatal(err)
	}
	if gotFuture.Owner != "carol@example.com" {
		t.Fatalf("futureChild.Owner = %q, want carol@example.com", gotFuture.Owner)
	}

	// 4. Storing an ownerless row is refused at the database level and for whitespace/invalid owners.
	if _, err := db.ExecContext(ctx, "UPDATE `task` SET `owner` = '' WHERE `id` = '1'"); err == nil {
		t.Fatal("expected SQL UPDATE setting empty task.owner to fail, but it succeeded")
	}
	bad := *updatedRoot
	bad.Owner = "   "
	if err := store.PutTask(ctx, bad); err == nil {
		t.Fatal("expected PutTask with whitespace owner to fail")
	}
}

func TestTaskOwnerMigrationBackfillAndProposer(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Build a schema-112 database before owner and proposer_* columns existed.
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DROP TRIGGER IF EXISTS `task_owner_nonempty_insert`",
		"DROP TRIGGER IF EXISTS `task_owner_nonempty_update`",
		"ALTER TABLE `task` DROP COLUMN `owner`",
		"ALTER TABLE `task` DROP COLUMN `proposer_actor_kind`",
		"ALTER TABLE `task` DROP COLUMN `proposer_actor_id`",
		"ALTER TABLE `task` DROP COLUMN `proposer_actor_source`",
		"ALTER TABLE `task` DROP COLUMN `proposer_behalf_kind`",
		"ALTER TABLE `task` DROP COLUMN `proposer_behalf_id`",
		"ALTER TABLE `task` DROP COLUMN `proposer_behalf_source`",
		"ALTER TABLE `task` DROP COLUMN `proposer_task_id`",
		"ALTER TABLE `task` DROP COLUMN `proposer_run_id`",
		"UPDATE `grain_schema` SET `version` = 112 WHERE `id` = 1",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("downgrading test store to 112 (%s): %v", stmt, err)
		}
	}

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	insert112 := func(id, creator, oaKind, oaID, oaSource, oReason, originRef string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `task` (`id`,`branch`,`intent`,`title`,`body`,`origin_actor_kind`,`origin_actor_id`,`origin_actor_source`,`origin_reason`,`binding`,`auto_merge`,`creator`,`origin_ref`,`created_at`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			id, "grain/task-"+id, "implement", "Task "+id, "Body", oaKind, oaID, oaSource, oReason, "directive", 0, creator, originRef, now); err != nil {
			t.Fatalf("inserting 112 task %s: %v", id, err)
		}
	}

	// Task 10: human creator alice@example.com.
	insert112("10", "alice@example.com", "human", "alice@example.com", "iap", "direct", "task:10")
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`) VALUES ('run-10a','10','sb-10a',1,?)",
		now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Task 11: derived from 10 via proposed-by, empty creator -> backfills owner alice@example.com, proposer task 10 / run-10a.
	insert112("11", "", "automation", "grain", "", "proposed", "task:10")
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_link` (`task_id`,`kind`,`target`,`blocks`) VALUES ('11','proposed-by','10',0)"); err != nil {
		t.Fatal(err)
	}
	// Task 12: schedule-filed task, empty creator -> backfills deployment owner ("operator") and proposer "schedule".
	insert112("12", "", "automation", "schedule", "internal", "schedule", "task:12")
	// Task 13: completely unrecorded legacy row -> backfills deployment owner ("operator") and proposer "unknown".
	insert112("13", "", "", "", "", "", "task:13")

	// Re-open / Init triggers in-place migration 112 -> 113.
	if err := store.Init(ctx); err != nil {
		t.Fatalf("migrating 112 -> 113: %v", err)
	}

	t10, err := store.GetTask(ctx, "10")
	if err != nil {
		t.Fatal(err)
	}
	if t10.Owner != "alice@example.com" || t10.Proposer.Attribution.Actor.ID != "alice@example.com" {
		t.Fatalf("t10 = owner %q proposer %+v", t10.Owner, t10.Proposer)
	}

	t11, err := store.GetTask(ctx, "11")
	if err != nil {
		t.Fatal(err)
	}
	if t11.Owner != "alice@example.com" {
		t.Fatalf("t11.Owner = %q, want inherited alice@example.com", t11.Owner)
	}
	if t11.Proposer.TaskID != "10" || t11.Proposer.RunID != "run-10a" {
		t.Fatalf("t11.Proposer = %+v, want TaskID=10 RunID=run-10a", t11.Proposer)
	}

	t12, err := store.GetTask(ctx, "12")
	if err != nil {
		t.Fatal(err)
	}
	if t12.Owner != "operator" || t12.Proposer.Attribution.Actor.Kind != model.PrincipalAutomation || t12.Proposer.Attribution.Actor.ID != "schedule" {
		t.Fatalf("t12 = owner %q proposer %+v, want operator & automation:schedule", t12.Owner, t12.Proposer)
	}

	t13, err := store.GetTask(ctx, "13")
	if err != nil {
		t.Fatal(err)
	}
	if t13.Owner != "operator" || t13.Proposer.Attribution.Actor.Kind != model.PrincipalUnknown {
		t.Fatalf("t13 = owner %q proposer %+v, want operator & unknown", t13.Owner, t13.Proposer)
	}

	// After migration, inserting an ownerless row directly into SQL must fail.
	_, err = db.ExecContext(ctx,
		"INSERT INTO `task` (`id`,`branch`,`intent`,`title`,`body`,`origin_actor_kind`,`origin_actor_id`,`origin_reason`,`binding`,`auto_merge`,`owner`) "+
			"VALUES ('14','grain/task-14','implement','No owner','Body','human','operator','direct','directive',0,'')")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "owner") {
		t.Fatalf("expected inserting empty owner after migration to fail, got err=%v", err)
	}
}
