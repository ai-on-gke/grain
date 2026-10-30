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
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ChangePollInterval is how often every live non-PR change is handed to
// its provider's change.Poller. The provider decides which of them are
// due (a CL's own 2/10/30-minute intervals and its rate limit); this is
// only how often it is asked.
const ChangePollInterval = time.Minute

// ChangePollStore is what the change poll reads: every task, and whether
// one has closed.
type ChangePollStore interface {
	ListTasks(ctx context.Context) ([]model.Task, error)
	GetObservation(ctx context.Context, taskID string) (*model.Observation, error)
}

// PollChanges runs PollChangesOnce every ChangePollInterval until ctx
// ends (changelists.md §3, G3-3).
func PollChanges(ctx context.Context, store ChangePollStore) {
	t := time.NewTicker(ChangePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			PollChangesOnce(ctx, store)
		}
	}
}

// PollChangesOnce hands every open task with a changelist link to each
// registered change.Poller whose kind is not blocked on expired
// credentials, and records what each answered (change.Remember), where the
// task view and inbox read their presubmit badge. It reports how many
// observations were recorded.
func PollChangesOnce(ctx context.Context, store ChangePollStore) int {
	var pollers []change.Poller
	for _, kind := range change.Kinds() {
		p, _ := change.Lookup(kind)
		poller, ok := p.(change.Poller)
		if !ok {
			continue
		}
		if change.HealthOf(change.HealthKind(p)).Blocked != "" {
			continue
		}
		pollers = append(pollers, poller)
	}
	if len(pollers) == 0 {
		return 0
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		log.Printf("grain: polling changes: listing tasks: %v", err)
		return 0
	}
	var live []model.Task
	for _, task := range tasks {
		if change.Primary(task) == "" {
			continue
		}
		if obs, err := store.GetObservation(ctx, task.ID); err == nil && obs != nil && obs.ClosedAt != nil {
			continue
		}
		live = append(live, task)
	}
	if len(live) == 0 {
		return 0
	}
	byTarget := map[string]model.Task{}
	for _, task := range live {
		for _, l := range change.Changes(task) {
			byTarget[l.Target] = task
		}
	}
	writer, _ := store.(ChangeOutcomeStore)
	n := 0
	for _, poller := range pollers {
		for _, obs := range poller.PollTick(ctx, live) {
			change.Remember(obs)
			n++
			if task, ok := byTarget[obs.Target]; ok && writer != nil {
				if err := applyChangeObservation(ctx, writer, poller.(change.Provider), task, obs, time.Now().UTC()); err != nil {
					log.Printf("grain: polling changes: %s on task %s: %v", obs.Target, task.ID, err)
				}
			}
		}
	}
	return n
}

// ChangeOutcomeStore is the daemon's store as a poll that acts on what it
// saw needs it: a CL submitted or abandoned outside grain, and the syncs
// a CL's health calls for (changelists.md §6-§7).
type ChangeOutcomeStore interface {
	ChangePollStore
	change.Store
	State(ctx context.Context, id string) (model.State, error)
}

// applyChangeObservation acts on one polled observation of task's CL: a
// CL found submitted closes the task the way Submit does, saying it was
// submitted outside grain; one found abandoned (or deleted), or found
// holding no files outside a run, is marked abandoned; and a live one
// whose health or age calls for a sync is synced, once per snapshot and
// never during a run (change.SyncPlanner).
func applyChangeObservation(ctx context.Context, st ChangeOutcomeStore, p change.Provider, task model.Task,
	obs change.Observation, now time.Time) error {

	if change.Settled(task, obs.Target) {
		return nil
	}
	switch obs.Status {
	case change.StatusSubmitted:
		return change.RecordSubmitted(ctx, st, task, obs.Target, obs.Target, "", true, now)
	case change.StatusAbandoned:
		return change.RecordAbandoned(ctx, st, task, obs.Target, now)
	}
	state, err := st.State(ctx, task.ID)
	if err != nil {
		return err
	}
	if obs.Empty {
		// A run may still be uploading to the CL it just made; once none
		// is, an empty CL is nothing to submit.
		if state == model.StateRunning {
			return nil
		}
		return change.RecordEmpty(ctx, st, task, obs.Target, now)
	}
	return AutoSync(ctx, st, p, change.Planner, task, obs, state, now)
}

// AutoSync makes the automatic sync obs calls for, if planner says one is
// due: a conflicted result wakes the task to resolve it, any other
// failure is left for the person's Sync button.
func AutoSync(ctx context.Context, st change.Store, p change.Provider, planner *change.SyncPlanner, task model.Task,
	obs change.Observation, state model.State, now time.Time) error {

	trigger := planner.Due(obs, state == model.StateRunning, state == model.StateAwaitingSubmit, change.SyncAfterOf(p), now)
	if trigger == "" {
		return nil
	}
	res, err := p.Sync(ctx, task, "")
	planner.Synced(obs.Target, obs.Snapshot, err != nil)
	if err != nil {
		change.NoteError(ctx, p, err, nil, now)
		if f := change.FailureOf(err); f.Outcome == change.OutcomeConflicts {
			return change.Wake(ctx, st, task, syncNote(trigger, res)+"\n\n"+change.ConflictNote(obs.Target, f.Files), now)
		}
		log.Printf("grain: automatic sync (%s) of %s failed; not retrying until it is uploaded again: %v", trigger, obs.Target, err)
		return nil
	}
	_, err = st.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: change.SubmitPrincipal},
		Body: syncNote(trigger, res), CreatedAt: now})
	return err
}

func syncNote(trigger string, res change.SyncResult) string {
	why := map[string]string{
		change.SyncNeedsSync:  "Submit said it needs a sync",
		change.SyncBehind:     "it was behind head",
		change.SyncConflicted: "it conflicted with head",
		change.SyncStale:      "its snapshot had waited on Submit too long",
	}[trigger]
	msg := res.Message
	if msg == "" {
		msg = "synced to head"
	}
	return "grain synced this task's CL because " + why + ": " + msg + "."
}
