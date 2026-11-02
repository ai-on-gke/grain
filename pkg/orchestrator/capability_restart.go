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

// Capability changes mid-task (design/design/durable-tasks.design.md#DT-2).
//
// A grant added to or removed from a task with a live run used to reach
// only its next run. Now the run is told -- an addendum, which is how a
// comment posted mid-run reaches it (model.CapabilityChangePrincipal) --
// and after capabilityRestartGrace, the grace a graceful task pause gives
// (model.PauseGracePeriod), it is cancelled with errCapabilityChanged,
// recorded as a `capability` suspension, and the task's next run is built
// with a sandbox for the grants it now holds.
//
// A change no sandbox needs to see is applied in place instead: an
// `api:*` grant is checked per request by the agent-API gate, so a route
// the sandbox's `grain api` may call takes effect at once
// (capabilityNeedsSandbox). The run is told which grants changed, since
// its `api_*` tools are still the ones its mcpserver was started with.
// An added scope whose routes no sandbox process may call is served only
// as those tools, so it does need a restart (toolOnlyScopesAdded).
//
// An interactive run blocked in chat on a person is different twice
// over. Its chat wait reads only what a person says, so no addendum can
// reach it; and it is doing nothing a grace would protect. It is
// restarted at once, and its task stays parked on the question it asked
// (awaiting_reply), so nothing is dispatched until the person answers --
// a run started with no new message would only answer again what the
// last one already had.
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
	// apiNoticed is, per run, the api grants it was last told about by
	// an in-place notice (noteAPIGrantsChanged).
	apiNoticed map[string]string
}

type capabilityNotice struct {
	grants string
	at     time.Time
}

// NewCapabilityRestarts returns an empty record.
func NewCapabilityRestarts() *CapabilityRestarts {
	return &CapabilityRestarts{noticed: map[string]capabilityNotice{}, apiNoticed: map[string]string{}}
}

// capabilityNeedsSandbox reports whether a run that started with the
// grants in old has to be restarted for its task to hold those in now:
// whether any capability was added or removed other than an `api:*`
// grant, which the agent-API gate reads per request, or an added `api:*`
// grant reaches a route the run could only call as an `api_*` tool it
// was not started with.
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
	return len(toolOnlyScopesAdded(old, now)) > 0
}

// toolOnlyScopesAdded is every scope the grants in now reach that those
// in old did not, and that the sandbox channel refuses
// (model.IsSandboxDeniedAPIScope, or a scope set only a person may
// grant): a route of one is served only as an `api_*` tool, and a run's
// tools are built once, when its mcpserver starts (ui.AgentAPITools).
//
// A scope removed needs nothing of the kind: the gate refuses a call the
// task no longer holds, whichever way the call arrives.
func toolOnlyScopesAdded(old, now []string) []model.APIScope {
	before := model.APIGrantsOf(old)
	sandbox := model.SandboxAPIGrantsOf(now)
	var added []model.APIScope
	for _, scope := range model.APIGrantsOf(now).Scopes() {
		if !before.Has(scope) && !sandbox.Has(scope) {
			added = append(added, scope)
		}
	}
	return added
}

// apiGrantsOf is the `api:*` grants among names, sorted.
func apiGrantsOf(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, model.APIGrantPrefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// blockedInChat reports whether task's live run is waiting on a person:
// an interactive task parked on a question while its run is live, which
// is the chat tool's own mark (ui.Client.PostAgentMessage points
// Observation.PendingQuestionCommentID at what the run said, and a
// person's reply clears it).
func blockedInChat(task model.Task, obs *model.Observation) bool {
	return task.Interactive && obs != nil && obs.PendingQuestionCommentID != nil
}

// reconcileCapabilityChanges tells every live run whose task's grants
// have changed in a way its sandbox has to see, and cancels it once
// capabilityRestartGrace has passed since it was told -- or at once, for
// an interactive run blocked in chat (blockedInChat). A change applied in
// place is announced, not restarted for (noteAPIGrantsChanged).
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
	if r.apiNoticed == nil {
		r.apiNoticed = map[string]string{}
	}
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
			if err := r.noteAPIChange(ctx, deps.Store, *task, run, standing.Grants, current, now); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		delete(r.apiNoticed, run.ID)
		obs, err := deps.Store.GetObservation(ctx, run.TaskID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		blocked := blockedInChat(*task, obs)
		key := strings.Join(current, "\n")
		notice, told := r.noticed[run.ID]
		if !told || notice.grants != key {
			// Told, or told again for a second change: the grace runs
			// from the change the run will be restarted for.
			if err := noteCapabilityChanged(ctx, deps.Store, *task, run, blocked, now); err != nil {
				errs = append(errs, err)
				continue
			}
			notice = capabilityNotice{grants: key, at: now}
			r.noticed[run.ID] = notice
			if !blocked {
				continue
			}
		}
		if !blocked && now.Sub(notice.at) < capabilityRestartGrace {
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
	for id := range r.apiNoticed {
		if !live[id] {
			delete(r.apiNoticed, id)
		}
	}
	return errors.Join(errs...)
}

// noteAPIChange tells a run whose `api:*` grants changed in place what
// changed, once per change: nothing restarts it, so without this it would
// learn of a new route only by trying one, and of a removed one only by
// being refused.
func (r *CapabilityRestarts) noteAPIChange(ctx context.Context, store *model.Store, task model.Task,
	run model.Run, old, now []string, at time.Time) error {

	was, is := apiGrantsOf(old), apiGrantsOf(now)
	if slices.Equal(was, is) {
		delete(r.apiNoticed, run.ID)
		return nil
	}
	key := strings.Join(is, "\n")
	if r.apiNoticed[run.ID] == key {
		return nil
	}
	var added, removed []string
	for _, g := range is {
		if !slices.Contains(was, g) {
			added = append(added, g)
		}
	}
	for _, g := range was {
		if !slices.Contains(is, g) {
			removed = append(removed, g)
		}
	}
	var b strings.Builder
	b.WriteString("_Capabilities changed._ ")
	if task.Interactive {
		b.WriteString("This session's `api:*` grants changed; nothing restarts for it.")
	} else {
		b.WriteString("This task's `api:*` grants changed while run " + run.ID + " was live; nothing restarts for it.")
	}
	if len(added) > 0 {
		fmt.Fprintf(&b, " Added: %s -- call their routes with `grain api` from the sandbox now; "+
			"the `api_*` tools for them arrive only with the next run.", strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		fmt.Fprintf(&b, " Removed: %s -- their routes are refused from now on, "+
			"whether called with `grain api` or as an `api_*` tool.", strings.Join(removed, ", "))
	}
	if err := postCapabilityNotice(ctx, store, task.ID, b.String(), at); err != nil {
		return fmt.Errorf("orchestrator: telling run %s its api grants changed: %w", run.ID, err)
	}
	r.apiNoticed[run.ID] = key
	return nil
}

// noteCapabilityChanged posts the addendum a run is told by, which reaches
// a live run the way any mid-run comment does (model.CapabilityChangePrincipal).
//
// An interactive task's is written for the person reading the chat as
// much as for the run -- a chat usually has no repository, so "commit and
// push" means nothing to either -- and blocked is a run waiting on that
// person, which is restarted at once and picks the change up when they
// next write.
func noteCapabilityChanged(ctx context.Context, store *model.Store, task model.Task, run model.Run,
	blocked bool, now time.Time) error {

	var body string
	switch {
	case blocked:
		body = "_Capabilities changed._ This session's capabilities changed while it was waiting " +
			"for you. It picks them up when you next write -- nothing said here is lost."
	case task.Interactive:
		body = fmt.Sprintf("_Capabilities changed._ This session's capabilities changed, and it "+
			"will restart in %s to pick them up -- nothing said here is lost. Finish the step "+
			"you are on and push anything you would not want to redo: the next run resumes this "+
			"conversation with the new grants.", capabilityRestartGrace)
	default:
		body = fmt.Sprintf("grain: a capability of this task changed while run %s was live. Commit, push "+
			"and write your handover: this run will be restarted with the new grants in %s, and the "+
			"next one resumes from what you pushed.", run.ID, capabilityRestartGrace)
	}
	if err := postCapabilityNotice(ctx, store, task.ID, body, now); err != nil {
		return fmt.Errorf("orchestrator: telling run %s its capabilities changed: %w", run.ID, err)
	}
	return nil
}

func postCapabilityNotice(ctx context.Context, store *model.Store, taskID, body string, now time.Time) error {
	_, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.CapabilityChangePrincipal},
		Body:      body,
		CreatedAt: now,
	})
	return err
}
