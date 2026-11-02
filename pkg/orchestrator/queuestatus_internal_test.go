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
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// baseInstant is this file's own clock. Nothing here measures elapsed
// time -- statusOf reads no clock at all -- so it only has to be an
// instant the stamps can point at.
var baseInstant = time.Date(2026, 9, 17, 2, 0, 0, 0, time.UTC)

// The merge queue's invariant, tested as an invariant rather than as a
// table of examples: every task grain is driving to a merge is in exactly
// one state at every moment, and each state is either grain waiting on a
// named thing or something working on the change. No combination of
// pull-request health, check outcome, grain CI verdict, review state,
// repair state, queue position and blocked flag renders as no activity at
// all.
//
// A table of examples would not have caught what this is here for. The
// silence that produced this file was not an arm anybody wrote wrongly;
// it was the gap between the arms -- "the checks are over and did not
// pass" belonged to none of them -- and gaps are found by enumerating the
// space, not by listing the cases somebody has already thought of. The
// next check conclusion GitHub invents, or health value model grows, is
// caught here on the day it is added.

// statusCase is one point of that cross-product, in the shape statusOf
// reads it: a queueEntry, who is at the head of the queue, and the two
// facts that reach statusOf as arguments rather than on the entry.
type statusCase struct {
	name        string
	health      model.PrHealth
	checks      []github.CheckRun
	grain       grainCIVerdict
	checksKnown bool
	review      reviewState
	repairAt    bool
	running     bool
	blocked     bool
	isMergeHead bool
	isHead      bool
}

func check(name, status, conclusion string) github.CheckRun {
	run := github.CheckRun{Name: name, Status: status}
	if conclusion != "" {
		c := conclusion
		run.Conclusion = &c
	}
	return run
}

// The dimensions. Each is every value that reaches a decision in
// statusOf, not every value the type has: two failing checks and one
// failing check take the same arm.
var (
	statusHealths = []model.PrHealth{
		model.PrUnknown, model.PrClean, model.PrPending, model.PrConflicted, model.PrFailing,
	}
	statusCheckSets = map[string][]github.CheckRun{
		"no checks":        nil,
		"all green":        {check("tests", "completed", "success")},
		"one unfinished":   {check("tests", "in_progress", "")},
		"one failing":      {check("tests", "completed", "failure")},
		"one cancelled":    {check("tests", "completed", "cancelled")},
		"failed+cancelled": {check("go", "completed", "failure"), check("js", "completed", "cancelled")},
	}
	statusGrainVerdicts = map[string]grainCIVerdict{
		"no grain CI":   {},
		"grain running": {Required: true, Running: true},
		"grain passed":  {Required: true, Passed: true},
		"grain failed":  {Required: true, Failed: true, Detail: "go: 1 job"},
		"grain errored": {Required: true, Errored: true, Detail: "no sandbox"},
		"grain cancelled": {Required: true, Cancelled: true,
			Detail: "cancelled: the daemon this run was going in shut down under it"},
		"grain no verdict":  {Required: true},
		"grain not granted": {},
	}
	statusReviews = map[string]reviewState{
		"no review":      {},
		"review pending": {pending: true, taskID: "t9"},
		"review owed":    {pending: true},
		"review overdue": {pending: true, taskID: "t9", overdue: true},
	}
)

// entryFor builds the queueEntry one case describes. The task is an
// ordinary auto-merge queue member with an open pull request, which is
// the population the invariant is about.
func entryFor(c statusCase) queueEntry {
	task := model.Task{ID: "t1", AutoMerge: true}
	obs := &model.Observation{TaskID: task.ID}
	if c.repairAt {
		obs.MergeQueueRepairAt = &baseInstant
	}
	if c.blocked {
		obs.MergeQueueBlockedAt = &baseInstant
	}
	return queueEntry{
		task: task,
		obs:  obs,
		ref: model.PullRequestRef{
			Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 7,
		},
		detail: github.PullRequestDetail{
			HeadSHA: "deadbeef", HeadRef: "grain/task-1", BaseRef: "main",
		},
		checks:        c.checks,
		checksKnown:   c.checksKnown,
		checksSettled: true,
		grain:         c.grain,
		health:        c.health,
		running:       c.running,
	}
}

func TestStatusOfIsTotal(t *testing.T) {

	var cases []statusCase
	for _, health := range statusHealths {
		for checksName, checks := range statusCheckSets {
			for grainName, grain := range statusGrainVerdicts {
				for reviewName, review := range statusReviews {
					for _, repairAt := range []bool{false, true} {
						for _, running := range []bool{false, true} {
							for _, blocked := range []bool{false, true} {
								for _, head := range []bool{false, true} {
									cases = append(cases, statusCase{
										name: fmt.Sprintf("%s/%s/%s/%s/repair=%t/running=%t/blocked=%t/head=%t",
											health, checksName, grainName, reviewName,
											repairAt, running, blocked, head),
										health: health, checks: checks, grain: grain,
										// Checks are readable except where the
										// health says they are not: PrUnknown is
										// the only answer a credential that cannot
										// read them produces.
										checksKnown: health != model.PrUnknown,
										review:      review, repairAt: repairAt, running: running,
										blocked: blocked, isMergeHead: head, isHead: head,
									})
								}
							}
						}
					}
				}
			}
		}
	}
	if len(cases) < 1000 {
		t.Fatalf("only %d combinations: the cross-product has stopped being one", len(cases))
	}

	states := map[string]int{}
	for _, c := range cases {
		e := entryFor(c)
		got := statusOf(e, c.isMergeHead, c.blocked, c.review, laneState{})
		if got.state == "" {
			t.Fatalf("%s: no state at all -- an auto-merge task with an open pull "+
				"request must always be waiting on something or being worked on", c.name)
		}
		if _, ok := mergeLane.of(got.state); !ok {
			t.Fatalf("%s: state %q is not in this file's vocabulary", c.name, got.state)
		}
		// Every state but merging carries a sentence saying what of.
		// "Merging" is the whole sentence: there is nothing to add to a
		// merge call that is being made right now.
		if got.note == "" && got.state != mergeStateMerging {
			t.Fatalf("%s: state %q with no note -- the row would say what grain is "+
				"doing and not what about", c.name, got.state)
		}
		states[got.state]++
	}

	// And every state is reachable: a vocabulary with a word nothing ever
	// writes is a word that has quietly stopped being true.
	for _, state := range statusOfStates() {
		if state == mergeStateUnreadable {
			// Reached from PrUnknown here; its other two routes (a pull
			// request that could not be read at all, a health value this
			// build does not know) are noteUnreadableEntry's and the
			// final arm's, neither of which this cross-product reaches.
		}
		if states[state] == 0 {
			t.Errorf("no combination produces %q", state)
		}
	}
}

// TestStatusOfNamesTheDeliberateBlanks is the other half of the
// invariant: the two states in which grain deliberately says nothing, so
// that a change making them speak is as visible as a change making a
// driven task fall silent.
func TestStatusOfNamesTheDeliberateBlanks(t *testing.T) {
	// A task nobody asked to be merged automatically. grain watches the
	// pull request to close the task out; the task's own state
	// (awaiting_submit) is the answer, and a queue activity line beside
	// it would claim work nobody asked for.
	watched := entryFor(statusCase{health: model.PrClean, checksKnown: true})
	watched.task.AutoMerge = false
	if got := statusOf(watched, true, false, reviewState{}, laneState{}); got.state != "" {
		t.Errorf("a task that is not auto-merge says %q, want nothing", got.state)
	}

	// And a pull request that has merged or closed: syncEntry is closing
	// the task out on this same cycle, so there is no merge left to be
	// waiting for.
	for _, health := range []model.PrHealth{model.PrMerged, model.PrClosed} {
		e := entryFor(statusCase{health: health, checksKnown: true})
		if got := statusOf(e, true, false, reviewState{}, laneState{}); got.state != "" {
			t.Errorf("a %s pull request says %q, want nothing", health, got.state)
		}
	}
}

// TestStatusOfNamesWhatEachCaseIsWaitingOn is the four rows a person
// actually complained about, each with the state and the sentence it
// should show. The cross-product above proves nothing is silent; this
// proves the sentences are the right ones.
func TestStatusOfNamesWhatEachCaseIsWaitingOn(t *testing.T) {
	heads := map[string]string{"acme/widgets": "t1"}
	behind := map[string]string{"acme/widgets": "t0"}

	for _, tc := range []struct {
		name      string
		entry     queueEntry
		head      bool
		blocked   bool
		review    reviewState
		heads     map[string]string
		wantState string
		wantNote  []string
		// wantNotNote is what the sentence must not say -- for the one
		// row where the wrong wording is a repetition rather than a
		// silence, and so would pass every wantNote above.
		wantNotNote []string
	}{{
		// grain/task-149 itself: grain CI cancelled twice, no GitHub
		// check outstanding, nothing clean, nothing refused. This is the
		// row that said nothing at all for a day.
		//
		// It now says why it was cancelled, too: the note whoever
		// stopped the run left is carried on the status
		// (model.CIStatus.Detail, off ci.Run.CancelReason), and the row
		// quotes it -- "cancelled" sends a person looking for a stuck
		// runner, "cancelled when the daemon restarted" sends them
		// nowhere. The conclusion word is not repeated on the way:
		// ciDetail writes the detail as "cancelled: ...", and the
		// sentence has already said "cancelled without a verdict".
		name: "grain CI cancelled",
		entry: entryFor(statusCase{
			health: model.PrPending, checksKnown: true,
			checks: statusCheckSets["all green"],
			grain:  statusGrainVerdicts["grain cancelled"],
		}),
		head:      true,
		wantState: mergeStateWaitingCI,
		wantNote: []string{"grain CI", "cancelled", "another run",
			"the daemon this run was going in shut down under it"},
		wantNotNote: []string{"cancelled: cancelled", "verdict: cancelled"},
	}, {
		// A required check that concluded failure: the change did not
		// pass, and the row says grain is sending it back to an agent.
		name: "required check failed",
		entry: entryFor(statusCase{
			health: model.PrFailing, checksKnown: true,
			checks: statusCheckSets["one failing"],
		}),
		head:      true,
		wantState: mergeStateRepairNeeded,
		wantNote:  []string{"tests", "back to an agent"},
	}, {
		// A conflicted branch behind another change in the same queue:
		// both facts on one line, because both are true and a person
		// reading only the first would wonder why nothing is happening.
		name: "conflicted branch behind another",
		entry: entryFor(statusCase{
			health: model.PrConflicted, checksKnown: true,
		}),
		heads:     behind,
		wantState: mergeStateRepairNeeded,
		wantNote:  []string{"conflicts with `main`", "sending this task back to an agent to repair it"},
	}, {
		// A repair filed onto a deployment whose workers are all busy.
		// This is the wait the merge queue could not previously describe
		// at all, and the one that lasts longest.
		name: "repair queued behind a full worker pool",
		entry: entryFor(statusCase{
			health: model.PrFailing, checksKnown: true,
			checks: statusCheckSets["one failing"], repairAt: true,
		}),
		head:      true,
		wantState: mergeStateRepairQueued,
		wantNote:  []string{"waiting for a worker", "tests"},
	}, {
		// The same repair once a worker has it.
		name: "repair running",
		entry: entryFor(statusCase{
			health: model.PrFailing, checksKnown: true,
			checks: statusCheckSets["one failing"], repairAt: true, running: true,
		}),
		head:      true,
		wantState: mergeStateRepairing,
		wantNote:  []string{"an agent is repairing this branch now"},
	}, {
		// A run the queue did not ask for, on a pull request it had
		// already given up on -- which is the likeliest way this happens,
		// since the task a person runs by hand is the one grain stopped
		// driving. Nothing merges under it, so the row says so rather
		// than leaving "Merge failed" standing over an agent at work.
		name: "somebody put an agent back on it",
		entry: func() queueEntry {
			e := entryFor(statusCase{
				health: model.PrFailing, checksKnown: true,
				checks: statusCheckSets["one failing"], blocked: true,
			})
			e.running = true
			return e
		}(),
		head:      true,
		blocked:   true,
		wantState: mergeStateWorking,
		wantNote:  []string{"an agent is working on this branch", "given up"},
	}, {
		// A pull request whose checks this deployment's credential cannot
		// read: genuinely unreadable, and the row says grain has no answer.
		name: "checks unreadable by credential",
		entry: entryFor(statusCase{
			health: model.PrUnknown, checksKnown: false,
		}),
		head:      true,
		wantState: mergeStateUnreadable,
		wantNote:  []string{"cannot read this repository's checks"},
	}, {
		// A pull request whose checks are green, but GitHub is still
		// computing mergeability: waiting in the merge queue, not unreadable.
		name: "computing mergeability",
		entry: entryFor(statusCase{
			health: model.PrUnknown, checksKnown: true,
		}),
		head:      true,
		wantState: mergeStateWaitingQueue,
		wantNote:  []string{"waiting for GitHub to work out whether this branch merges cleanly"},
	}, {
		// And the end of the cycle: the queue has given up, and the row
		// says which phase it died in rather than only that it died.
		name: "given up on",
		entry: entryFor(statusCase{
			health: model.PrFailing, checksKnown: true,
			checks: statusCheckSets["one failing"], blocked: true,
		}),
		head:      true,
		blocked:   true,
		wantState: mergeStateFailed,
		wantNote:  []string{"stopped driving", "Retry merge"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.heads
			if h == nil {
				h = heads
			}
			got := statusOf(tc.entry, tc.head, tc.blocked, tc.review, laneState{})
			if got.state != tc.wantState {
				t.Fatalf("state = %q, want %q (note %q)", got.state, tc.wantState, got.note)
			}
			for _, want := range tc.wantNote {
				if !strings.Contains(got.note, want) {
					t.Errorf("note = %q, want it to say %q", got.note, want)
				}
			}
			for _, avoid := range tc.wantNotNote {
				if strings.Contains(got.note, avoid) {
					t.Errorf("note = %q, want it not to say %q", got.note, avoid)
				}
			}
		})
	}
}

// TestBlockedStatusKeepsTheReasonTheQueueRecorded: the terminal state's
// note is the sentence blockMergeQueue wrote when it gave up, which is
// the only account anywhere of which phase of the cycle this died in.
// Recomputing it from the health in front of us each cycle would lose
// "the repair ran and did not take" and replace it with "its checks are
// failing", which is the symptom rather than the story.
func TestBlockedStatusKeepsTheReasonTheQueueRecorded(t *testing.T) {
	e := entryFor(statusCase{health: model.PrFailing, checksKnown: true, blocked: true})
	e.obs.MergeQueueState = mergeStateFailed
	e.obs.MergeQueueNote = "the repair ran and did not take -- checks are still failing"

	got := statusOf(e, true, true, reviewState{}, laneState{})
	if got.state != mergeStateFailed || got.note != e.obs.MergeQueueNote {
		t.Fatalf("status = %+v, want the recorded reason kept", got)
	}

	// An entry blocked by a build that recorded no reason still says
	// something a person can act on.
	e.obs.MergeQueueState, e.obs.MergeQueueNote = "", ""
	got = statusOf(e, true, true, reviewState{}, laneState{})
	if got.state != mergeStateFailed || !strings.Contains(got.note, "Retry merge") {
		t.Fatalf("status = %+v, want a fallback sentence naming the way out", got)
	}
}

// grain/task-256: which states survive their task leaving the queue's
// watched set is asked of liveness.go's classification rather than of a
// list of its own, and this is what says the two say the same thing.
//
// The rule is the classification read sideways. A state grain drives, or
// one waiting on GitHub, promises that something is going to happen to
// this change; a cycle that has stopped covering the task is not going to
// make it happen, so the row would be claiming work nobody is doing. A
// state that is terminal by design promises nothing and is kept.
//
// Written as a derivation with the answer spelled out beside it, because
// both halves can fail: a classification table that quietly reclassified
// merge_failed as driven would agree with this loop and be wrong, and a
// sweep that grew a second opinion of its own would disagree with it.
func TestOnlyAPersonsVerdictOutlivesTheQueuesAttention(t *testing.T) {
	declared := declaredStates(t, mergeLane)
	kept := map[string]bool{}
	for _, name := range sortedNames(declared) {
		state := declared[name]
		liveness, ok := mergeLane.of(state)
		if !ok {
			// TestEveryStateIsClassified is what reports this; here
			// it would only be noise on top of it.
			continue
		}
		want := liveness.class == livenessAwaitingPerson
		if got := outlivesTheQueuesAttention(state); got != want {
			t.Errorf("outlivesTheQueuesAttention(%q) = %t, want %t: %s is classified %q",
				state, got, want, name, liveness.class)
		}
		if want {
			kept[state] = true
		}
	}
	if len(kept) != 1 || !kept[mergeStateFailed] {
		t.Errorf("the states kept on a row nothing is watching are %v, want merge_failed and "+
			"nothing else -- a state that stands still on a row the queue has walked away from "+
			"has to be one a person is expected to come and clear", sortedKeysOf(kept))
	}
	// A word this build has no classification for is swept, which is the
	// safe direction: forgetting something true costs a row a sentence,
	// and keeping something false is the fault this sweep exists to end.
	if outlivesTheQueuesAttention("waiting_thing") {
		t.Error("an unclassified state was kept on a row the queue has stopped watching")
	}
}

// sortedKeysOf is the set above in a stable order, for the message.
func sortedKeysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// statusOfStates is the states this cross-product is expected to produce,
// which is not the same as the states the file declares -- and the name
// says so now, because it used to be called allMergeStates and was four
// states behind the vocabulary it claimed to be all of (grain/task-314).
// That was harmless here and is the exact shape of failure the liveness
// guard exists to stop, so the vocabulary check above reads the
// classification table instead, which is held against queuestatus.go's own
// constants (liveness_internal_test.go).
//
// The states missing from this list are the ones statusOf does not return:
// updating_branch, batching and base_broken are written imperatively by the
// arm that makes the call or forms the batch, and checks_unfixable needs a
// split of reds this fixture does not build.
func statusOfStates() []string {
	return []string{
		mergeStateWaitingCI, mergeStateWaitingQueue, mergeStateWaitingReview,
		mergeStateMerging, mergeStateRepairNeeded, mergeStateRepairQueued,
		mergeStateRepairing, mergeStateWorking, mergeStateFailed, mergeStateUnreadable,
	}
}
