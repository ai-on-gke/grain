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

package dispatch

import (
	"context"
	"fmt"
	"log"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A base-branch repair (model.FixesBaseBranch) is the one kind of work
// that dispatch lets past a full sandbox budget (grain/task-488).
//
// A red base branch holds its repository's whole merge queue, so its
// repair is the run whose finishing frees everything else -- and it used
// to wait for capacity exactly as everything else did. On 2026-10-23 the
// repair of bwsalmon/grain's main sat at the front of the backlog for
// more than three hours, "waiting: memory: 480.0 GiB is committed …",
// behind dozens of runs and CI jobs most of which were waiting on that
// same red main. Being first in line orders who is dispatched; it does
// not make anything fit.
//
// So a repair gets two things nothing else does, in this order:
//
//  1. The budget's repair reserve (model.Budget.RepairReserve): a slice
//     of the machine that every other admission keeps clear, so a
//     saturated deployment still has one repair's worth of room.
//  2. Pre-emption (Preempt), when even the reserve is not enough --
//     because the deployment is over its budget, or the reserve is set
//     to nothing. The hook frees room by stopping work whose result is
//     moot while the base is red; dispatch only decides *when* to ask.
//
// Both are rationed per base branch: one repair of a branch may use them
// at a time (repairBases.eligible). A flood of repairs for one branch
// cannot hold the reserve or pre-empt work any more than one repair can,
// and the rest of them wait for capacity like everybody else.
//
// What none of this changes is the counts (model.Limits.Admits): a
// deployment that caps its workers caps its repairs too, as Run now
// does not override capacity either.

// Preempter is what dispatch asks to make room for repair, a ready
// base-branch repair the budget cannot admit even with its reserve. It
// stops at most one piece of lower-priority work and returns the
// sentence saying what it stopped, with fresh set, or "" when there is
// nothing it may stop. Something it already stopped for this base that
// has not yet given its sandbox back is reported again with fresh unset
// and nothing more is stopped, so that one repair waiting for one
// release does not take a second victim on every cycle.
//
// The capacity it frees is not this cycle's: a stopped run or job gives
// its sandbox back as it winds down, and the repair is admitted on the
// cycle after that.
type Preempter func(ctx context.Context, repair model.Task, base string) (what string, fresh bool)

// Preempt installs the pre-emption hook (see Preempter). Without one a
// repair the reserve cannot hold simply waits.
func Preempt(p Preempter) Option {
	return func(o *options) { o.preempt = p }
}

// repairBases is one cycle's reading of which tasks are base-branch
// repairs and which bases already have a repair running -- read lazily,
// on the first candidate the budget refuses, since a cycle on a
// deployment with room never needs it.
type repairBases struct {
	store *model.Store

	loaded bool
	// tags is every task with a branch repair tag, by id
	// (Store.BaseRepairBases).
	tags map[string]string
	// live is the bases a repair is already running against, and those
	// this cycle has started one for.
	live map[string]bool
}

func (r *repairBases) load(ctx context.Context) error {
	if r.loaded {
		return nil
	}
	tags, err := r.store.BaseRepairBases(ctx)
	if err != nil {
		return fmt.Errorf("dispatch: reading base-branch repairs: %w", err)
	}
	runs, err := r.store.LiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("dispatch: reading live runs: %w", err)
	}
	r.tags, r.live = tags, map[string]bool{}
	for _, run := range runs {
		if base, ok := tags[run.TaskID]; ok {
			r.live[base] = true
		}
	}
	r.loaded = true
	return nil
}

// eligible reports whether taskID is a base-branch repair that may use
// the reserve and pre-emption now, and which base it repairs: one whose
// base has no repair running already. A read that fails is logged and
// answered no, which is what dispatch did before repairs had a reserve.
func (r *repairBases) eligible(ctx context.Context, taskID string) (string, bool) {
	if r == nil || r.store == nil {
		return "", false
	}
	if err := r.load(ctx); err != nil {
		log.Printf("%v", err)
		return "", false
	}
	base, ok := r.tags[taskID]
	if !ok || r.live[base] {
		return "", false
	}
	return base, true
}

// started records that taskID's run is live, so that a second repair of
// the same base later in this cycle is not eligible.
func (r *repairBases) started(taskID string) {
	if r == nil || !r.loaded {
		return
	}
	if base, ok := r.tags[taskID]; ok {
		r.live[base] = true
	}
}

// repairAdmission is what Cycle does with a candidate the counts admit
// and the ordinary budget refuses: whether it is a repair the reserve
// admits (repair true, and the candidate goes ahead), and otherwise the
// sentence its wait should say instead of the budget's own, if a
// pre-emption was asked for on its behalf.
func repairAdmission(ctx context.Context, store *model.Store, limits model.Limits, rb *repairBases,
	preempt Preempter, taskID string, want model.Commitment) (repair bool, wait string) {
	base, ok := rb.eligible(ctx, taskID)
	if !ok {
		return false, ""
	}
	if limits.RepairSandboxVerdictFor(want).OK {
		log.Printf("dispatch: base-branch repair %s admitted from the repair reserve (%s)", taskID, base)
		return true, ""
	}
	if preempt == nil || !sandboxShapeShortFor(limits, false, want) {
		return false, ""
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return false, ""
	}
	what, fresh := preempt(ctx, *task, base)
	if what == "" {
		return false, ""
	}
	if fresh {
		log.Printf("dispatch: base-branch repair %s: %s", taskID, what)
	}
	return false, "waiting for a sandbox to be freed for this base-branch repair: " + what
}
