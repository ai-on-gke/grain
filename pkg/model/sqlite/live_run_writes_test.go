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
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestLiveRunWritesCoalesceAndStayOffChangeTokenAndChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{Dir: dir, Name: "grain"})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	store.SetLiveRunFlushIntervalForTest(20 * time.Millisecond)

	startedTask(t, store, ctx, "a1b2")
	beforeToken := changeToken(t, store, ctx)
	changedCh := store.Changed()
	actCh := store.ActivityChanged()

	at1 := now.Add(time.Second)
	at2 := now.Add(2 * time.Second)
	if live, err := store.SetTaskActivity(ctx, "a1b2", "step 1", at1); err != nil || !live {
		t.Fatalf("SetTaskActivity 1 = (%v, %v), want (true, nil)", live, err)
	}
	select {
	case <-actCh:
	default:
		t.Fatal("ActivityChanged did not close on SetTaskActivity")
	}

	// Duplicate unchanged activity must not re-fire ActivityChanged.
	actCh2 := store.ActivityChanged()
	if live, err := store.SetTaskActivity(ctx, "a1b2", "step 1", at1); err != nil || !live {
		t.Fatalf("duplicate SetTaskActivity = (%v, %v), want (true, nil)", live, err)
	}
	select {
	case <-actCh2:
		t.Fatal("ActivityChanged fired on identical unchanged activity")
	default:
	}

	if live, err := store.SetTaskActivity(ctx, "a1b2", "step 2", at2); err != nil || !live {
		t.Fatalf("SetTaskActivity 2 = (%v, %v), want (true, nil)", live, err)
	}
	if err := store.SetRunTranscript(ctx, "run-a1b2", "transcript v1"); err != nil {
		t.Fatalf("SetRunTranscript v1: %v", err)
	}
	if err := store.SetRunTranscript(ctx, "run-a1b2", "transcript v2"); err != nil {
		t.Fatalf("SetRunTranscript v2: %v", err)
	}

	// Immediate read-your-own-writes before the flush completes.
	if got, err := store.TaskActivityOf(ctx, "a1b2"); err != nil || got == nil || got.Note != "step 2" {
		t.Fatalf("TaskActivityOf = (%+v, %v), want step 2", got, err)
	}
	if tr, found, err := store.RunTranscript(ctx, "a1b2", 1); err != nil || !found || tr != "transcript v2" {
		t.Fatalf("RunTranscript = (%q, %v, %v), want transcript v2", tr, found, err)
	}
	if sizes, err := store.RunTranscripts(ctx); err != nil || len(sizes) != 1 || sizes[0].Bytes != len("transcript v2") {
		t.Fatalf("RunTranscripts = (%+v, %v), want 1 entry of %d bytes", sizes, err, len("transcript v2"))
	}

	// Flush to disk and verify ChangeToken and Changed() stayed still.
	if err := store.FlushLiveRunWrites(ctx); err != nil {
		t.Fatalf("FlushLiveRunWrites: %v", err)
	}
	select {
	case <-changedCh:
		t.Fatal("Changed() fired for live-run activity/transcript writes")
	default:
	}
	if afterToken := changeToken(t, store, ctx); afterToken != beforeToken {
		t.Fatalf("ChangeToken moved across live-run flush: %s -> %s", beforeToken, afterToken)
	}

	// Another connection/process must see the flushed values on disk.
	otherDB, err := sqlite.Open(sqlite.Config{Dir: dir, Name: "grain"})
	if err != nil {
		t.Fatalf("opening second handle: %v", err)
	}
	t.Cleanup(func() { otherDB.Close() })
	other := model.New(otherDB)
	t.Cleanup(func() { _ = other.Close() })
	if got, err := other.TaskActivityOf(ctx, "a1b2"); err != nil || got == nil || got.Note != "step 2" {
		t.Fatalf("other Store TaskActivityOf = (%+v, %v), want flushed step 2", got, err)
	}
	if tr, found, err := other.RunTranscript(ctx, "a1b2", 1); err != nil || !found || tr != "transcript v2" {
		t.Fatalf("other Store RunTranscript = (%q, %v, %v), want flushed transcript v2", tr, found, err)
	}

	// A real structural write still moves ChangeToken and fires Changed().
	if err := store.PutTask(ctx, task("c3d4", true)); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	select {
	case <-changedCh:
	default:
		t.Fatal("Changed() did not fire on PutTask")
	}
	if afterPut := changeToken(t, store, ctx); afterPut == beforeToken {
		t.Fatalf("ChangeToken stayed at %s after PutTask", afterPut)
	}
}

func TestFinishRunFlushesPendingActivityFirstTurnAndTranscriptAtomically(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{Dir: dir, Name: "grain"})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Long interval so only FinishRun can flush these writes.
	store.SetLiveRunFlushIntervalForTest(time.Hour)

	startedTask(t, store, ctx, "a1b2")
	if err := store.RecordRunStartup(ctx, model.RunStartup{RunID: "run-a1b2", QueuedAt: &now}); err != nil {
		t.Fatalf("RecordRunStartup: %v", err)
	}
	agentStart := now.Add(time.Minute)
	if err := store.SetRunAgentStarted(ctx, "run-a1b2", agentStart); err != nil {
		t.Fatalf("SetRunAgentStarted: %v", err)
	}

	firstTurn := now.Add(2 * time.Minute)
	secondTurn := now.Add(3 * time.Minute)
	if _, err := store.SetTaskActivity(ctx, "a1b2", "reading code", firstTurn); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskActivity(ctx, "a1b2", "running tests", secondTurn); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunTranscript(ctx, "run-a1b2", "final buffered transcript"); err != nil {
		t.Fatal(err)
	}

	if err := store.FinishRun(ctx, "run-a1b2", now.Add(4*time.Minute), "succeeded", "done"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	// Verify from a fresh Store over the same DB (no in-memory state).
	fresh := model.New(db)
	t.Cleanup(func() { _ = fresh.Close() })
	runs, err := fresh.Runs(ctx, "a1b2")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = (%+v, %v)", runs, err)
	}
	if runs[0].Activity != "running tests" || runs[0].ActivityAt == nil || !runs[0].ActivityAt.Equal(secondTurn) {
		t.Errorf("finished run activity = (%q, %v), want (\"running tests\", %v)", runs[0].Activity, runs[0].ActivityAt, secondTurn)
	}
	if runs[0].FirstTurnAt == nil || !runs[0].FirstTurnAt.Equal(firstTurn) {
		t.Errorf("finished run FirstTurnAt = %v, want %v (the first post-agent activity)", runs[0].FirstTurnAt, firstTurn)
	}
	tr, found, err := fresh.RunTranscript(ctx, "a1b2", 1)
	if err != nil || !found || tr != "final buffered transcript" {
		t.Errorf("finished run transcript = (%q, %v, %v), want final buffered transcript", tr, found, err)
	}

	// Late activity after FinishRun must report live=false and leave the row intact.
	live, err := store.SetTaskActivity(ctx, "a1b2", "too late", now.Add(5*time.Minute))
	if err != nil || live {
		t.Fatalf("SetTaskActivity after FinishRun = (%v, %v), want (false, nil)", live, err)
	}
}
