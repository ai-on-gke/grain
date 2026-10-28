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

// Capability changes mid-task (docs/design/durable-tasks.md#DT-2).
//
// A grant added to or removed from a task with a live run used to reach
// only its next run. Now the run is told -- an addendum, which is how a
// comment posted mid-run reaches it -- and after capabilityRestartGrace,
// the grace a graceful task pause gives (model.PauseGracePeriod), it is cancelled with
// errCapabilityChanged, recorded as a `capability` suspension, and the
// task's next run is built with a sandbox for the grants it now holds.
//
// A change no sandbox needs to see is applied in place instead: an
// `api:*` preset is checked per request by the agent-API gate, so it
// takes effect at once (capabilityNeedsSandbox).
//
// What has been noticed is this process's own record, like Preemptions:
// a daemon that restarts under a run either adopts it -- and notices the
// change again, with a fresh grace -- or does not, and the run's restart
// builds the new sandbox anyway.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// errCapabilityChanged is the cancellation cause of a run stopped to pick
// up a change to its task's grants.
var errCapabilityChanged = errors.New("orchestrator: run restarted to pick up a change to its task's capabilities")

// capabilityRestartGrace is how long a run is given, after being told
// its task's grants changed, to commit, push and write its handover: the
// grace a graceful operator pause gives (model.PauseGracePeriod).
const capabilityRestartGrace = model.PauseGracePeriod

// CapabilityChangedDetail is the detail of a run suspended for a grant
// change.
const CapabilityChangedDetail = "the task's capabilities changed while it ran; restarted with a sandbox built for the new grants"

// CapabilityRestarts is this process's record of the live runs it has
// told about a grant change, and when. The zero value is not usable;
// NewCapabilityRestarts builds one, and a nil one restarts nothing.
type CapabilityRestarts struct {
	mu      sync.Mutex
	noticed map[string]capabilityNotice
}

type capabilityNotice struct {
	grants string
	at     time.Time
}

// NewCapabilityRestarts returns an empty record.
func NewCapabilityRestarts() *CapabilityRestarts {
	return &CapabilityRestarts{noticed: map[string]capabilityNotice{}}
}

// capabilityNeedsSandbox reports whether a run that started with the
// grants in old has to be restarted for its task to hold those in now:
// whether any capability was added or removed other than an `api:*`
// preset, which the agent-API gate reads per request.
func capabilityNeedsSandbox(old, now []string) bool {
	for _, c := range old {
		if !slices.Contains(now, c) && !strings.HasPrefix(c, model.APIGrantPrefix) {
			return true
		}
	}
	for _, c := range now {
		if !slices.Contains(old, c) && !strings.HasPrefix(c, model.APIGrantPrefix) {
			return true
		}
	}
	return false
}

// reconcileCapabilityChanges tells every live run whose task's grants
// have changed in a way its sandbox has to see, and cancels it once
// capabilityRestartGrace has passed since it was told.
func reconcileCapabilityChanges(ctx context.Context, deps Deps, now time.Time) error {
	r := deps.Config.CapabilityRestarts
	if r == nil || deps.Store == nil {
		return nil
	}
	runs, err := deps.Store.LiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing live runs for capability changes: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	live := map[string]bool{}
	var errs []error
	for _, run := range runs {
		live[run.ID] = true
		standing, err := deps.Store.RunStanding(ctx, run.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		task, err := deps.Store.GetTask(ctx, run.TaskID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if task == nil || standing == nil {
			continue
		}
		current := make([]string, 0, len(task.Grants))
		for _, g := range task.Grants {
			current = append(current, g.Capability)
		}
		sort.Strings(current)
		if !capabilityNeedsSandbox(standing.Grants, current) {
			delete(r.noticed, run.ID)
			continue
		}
		key := strings.Join(current, "\n")
		notice, told := r.noticed[run.ID]
		if !told || notice.grants != key {
			// Told, or told again for a second change: the grace runs
			// from the change the run will be restarted for.
			if err := noteCapabilityChanged(ctx, deps.Store, run, now); err != nil {
				errs = append(errs, err)
				continue
			}
			r.noticed[run.ID] = capabilityNotice{grants: key, at: now}
			continue
		}
		if now.Sub(notice.at) < capabilityRestartGrace {
			continue
		}
		if deps.Config.TaskCancellers.Cancel(run.TaskID, errCapabilityChanged) {
			log.Printf("orchestrator: run %s of task %s restarted to pick up a change to its capabilities", run.ID, run.TaskID)
			delete(r.noticed, run.ID)
		}
	}
	for id := range r.noticed {
		if !live[id] {
			delete(r.noticed, id)
		}
	}
	return errors.Join(errs...)
}

// noteCapabilityChanged posts the addendum a run is told by: a comment
// from grain, which reaches a live run the way any mid-run comment does.
func noteCapabilityChanged(ctx context.Context, store *model.Store, run model.Run, now time.Time) error {
	_, err := store.AddComment(ctx, model.Comment{
		TaskID: run.TaskID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: fmt.Sprintf("grain: a capability of this task changed while run %s was live. Commit, push "+
			"and write your handover: this run will be restarted with the new grants in %s, and the "+
			"next one resumes from what you pushed.", run.ID, capabilityRestartGrace),
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: telling run %s its capabilities changed: %w", run.ID, err)
	}
	return nil
}
