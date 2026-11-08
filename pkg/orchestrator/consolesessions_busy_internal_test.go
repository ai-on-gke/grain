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

package orchestrator

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestConsoleSessionIsNotParkedWhileItsResultIsStillBeingProcessed covers
// the window between a run's row being finished and its result being
// turned into what it implies (InFlight.Busy). Parking the session there
// posted the resting line above the run's answer, so the next park could
// not reuse it and posted a second one (console-resting-notes-walkthrough
// spec, "count: 3").
func TestConsoleSessionIsNotParkedWhileItsResultIsStillBeingProcessed(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	cfg := model.DefaultConfig()
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:            model.DirectorSessionID,
		Title:         "Director",
		Capabilities:  []string{},
		NetworkAccess: model.NetworkOpen,
	}}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := reconcileConsoleSessions(ctx, Deps{Store: store}, standingNow); err != nil {
		t.Fatal(err)
	}
	id, err := store.TaskIDWithTag(ctx, model.ConsoleSessionTag(model.DirectorSessionID))
	if err != nil || id == "" {
		t.Fatalf("director task id = %q, err = %v", id, err)
	}

	said := standingNow.Add(time.Minute)
	replyToSessionInternal(t, store, id, said)
	started := said.Add(time.Second)
	runID := id + "-1"
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Attempt: 1, StartedAt: started}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	comments, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, id, comments[len(comments)-1].ID, started); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(time.Second)
	if err := store.FinishRun(ctx, runID, finished, "dispatched", ""); err != nil {
		t.Fatal(err)
	}

	// The row is finished; the result is not processed yet.
	runs := &InFlight{}
	release := make(chan struct{})
	runs.Go(runID, id, func() { <-release })
	if err := reconcileConsoleSessions(ctx, Deps{Store: store, Runs: runs}, finished.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Fatalf("session parked on comment %d while its run was still busy", *obs.PendingQuestionCommentID)
	}
	after, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(comments) {
		t.Fatalf("reconcile posted %d comment(s) while the run was busy: %+v", len(after)-len(comments), after[len(comments):])
	}
	close(release)
	if err := runs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
