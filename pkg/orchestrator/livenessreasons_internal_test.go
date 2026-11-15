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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The sub-case half of the liveness guards (livenessreasons.go): every
// reason a state can be held for is declared, classified, and says what
// ends it -- and a reason added without a classification fails the build,
// the way a state does in TestEveryStateIsClassified.

// TestEveryReasonIsClassified enumerates sub-cases the way the guard
// above enumerates states: off the file that declares them.
func TestEveryReasonIsClassified(t *testing.T) {
	for _, lane := range livenessLanes {
		t.Run(lane.name, func(t *testing.T) {
			if lane.reasonPrefix == "" || lane.reasons == nil {
				t.Fatalf("the %s lane has no sub-cases: every state needs at least one reason", lane.name)
			}
			declared := declaredConsts(t, livenessReasonSource, lane.reasonPrefix)
			real := map[string]bool{}
			for _, name := range sortedNames(declared) {
				reason := declared[name]
				real[reason] = true
				if _, ok := lane.reasons[reason]; !ok {
					t.Errorf("%s (%q) is declared and not classified in the %s lane: say what it "+
						"waits on, what drives it, and what happens when it stands past its "+
						"state's patience", name, reason, lane.name)
				}
				if _, ok := lane.of(reasonState(reason)); !ok {
					t.Errorf("%s (%q) belongs to state %q, which the %s lane does not classify",
						name, reason, reasonState(reason), lane.name)
				}
			}
			covered := map[string]bool{}
			for reason := range lane.reasons {
				covered[reasonState(reason)] = true
				if !real[reason] {
					t.Errorf("the %s lane classifies reason %q and %s declares no such constant",
						lane.name, reason, livenessReasonSource)
				}
			}
			for _, state := range sortedStates(lane) {
				if !covered[state] {
					t.Errorf("%q has no reasons in the %s lane: a state with no sub-case is the "+
						"coarse classification that let waiting_ci call itself driven while "+
						"nothing drove it", state, lane.name)
				}
			}
		})
	}
}

// TestEveryReasonSaysWhatItHasTo is the shape of a sub-case, per class.
// The rule (design/design/task-liveness.design.md): waiting on a named resource
// with something to free it, executing, or in a person's inbox with the
// control that ends it.
func TestEveryReasonSaysWhatItHasTo(t *testing.T) {
	funcs := packageFunctions(t)
	for _, lane := range livenessLanes {
		t.Run(lane.name, func(t *testing.T) {
			for reason, liveness := range lane.reasons {
				switch liveness.class {
				case livenessAwaitingPerson:
					if liveness.control == "" {
						t.Errorf("%q is a person's and names no control: the inbox row has to "+
							"offer the thing that ends the wait", reason)
					}
					if liveness.escalation != "" {
						t.Errorf("%q is already a person's and names an escalation (%q)", reason,
							liveness.escalation)
					}
					continue
				case livenessDriven:
					if liveness.driver == "" {
						t.Errorf("%q is driven and names no driver", reason)
					} else if !funcs[liveness.driver] {
						t.Errorf("%q names driver %s, which this package does not declare", reason,
							liveness.driver)
					}
				case livenessAwaitingExternal:
				default:
					t.Errorf("%q has no class", reason)
					continue
				}
				if liveness.resource == "" {
					t.Errorf("%q waits on no named resource", reason)
				}
				switch liveness.escalation {
				case stallToAgent, stallToResource:
				case stallToPerson:
					// The task lane has no merge entry to block, so its
					// escalation is the inbox row, which has to offer the
					// control.
					if lane.name == taskLane.name && liveness.control == "" {
						t.Errorf("%q escalates to a person's inbox and names no control", reason)
					}
				default:
					t.Errorf("%q says nothing about what happens past its patience: say "+
						"stallToAgent, stallToPerson or stallToResource", reason)
				}
				if liveness.escalation == stallToAgent && lane.name != mergeLane.name {
					t.Errorf("%q hands to an agent, which only the merge lane does", reason)
				}
			}
		})
	}
}

// TestMergeReasonOfIsTotal: whatever an entry looks like, the sub-case the
// runtime picks for it is one the table classifies, under the state it is
// in. The waiting_ci split is spelled out, since that is where the dead
// sub-case was.
func TestMergeReasonOfIsTotal(t *testing.T) {
	for _, state := range sortedStates(mergeLane) {
		for _, e := range []queueEntry{{}, {health: model.PrUnknown}} {
			reason := mergeReasonOf(e, state, nil)
			if _, ok := mergeReasons[reason]; !ok || reasonState(reason) != state {
				t.Errorf("mergeReasonOf(%q) = %q, which is not one of that state's classified reasons",
					state, reason)
			}
		}
	}
	for _, c := range []struct {
		grain forge.GrainCI
		want  string
	}{
		{forge.GrainCI{}, mergeReasonGitHubChecksPending},
		{forge.GrainCI{Required: true}, mergeReasonCINotStarted},
		{forge.GrainCI{Required: true, Running: true}, mergeReasonCIRunning},
		{forge.GrainCI{Required: true, Cancelled: true, Replacement: forge.GrainCIReplacementDue}, mergeReasonCIReplacementDue},
		{forge.GrainCI{Required: true, Errored: true, Replacement: forge.GrainCIReplacementSpent}, mergeReasonCIReplacementSpent},
		{forge.GrainCI{Required: true, Cancelled: true}, mergeReasonCINoVerdict},
		{forge.GrainCI{Required: true, Passed: true}, mergeReasonGitHubChecksPending},
	} {
		if got := mergeReasonOf(queueEntry{grain: c.grain}, mergeStateWaitingCI, nil); got != c.want {
			t.Errorf("waiting_ci with %+v = %q, want %q", c.grain, got, c.want)
		}
	}
}

// TestTaskReasonOfIsTotal is TestMergeReasonOfIsTotal for the task lane:
// whatever the particulars, every state answers one of its own classified
// reasons, and every classified reason is one some task can be in.
func TestTaskReasonOfIsTotal(t *testing.T) {
	wake := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	given := wake.Add(-time.Hour)
	tasks := []model.Task{{ID: "1"}, {ID: "1", DeferredUntil: &wake}, {ID: "1", Interactive: true}}
	askedFacts := []model.ReplyFacts{{}, {Question: true}, {Access: true}, {Stopped: true}, {UnansweredComment: true}}
	for _, kind := range model.GoalKinds() {
		tasks = append(tasks, model.Task{ID: "1", GoalKind: kind})
	}
	observations := []*model.Observation{nil, {}, {MergeQueueState: mergeStateWaitingCI},
		{MergeQueueState: mergeStateFailed}, {MergeQueueBlockedAt: &given}}
	reached := map[string]bool{}
	for _, state := range sortedStates(taskLane) {
		for _, task := range tasks {
			for _, obs := range observations {
				for _, blocked := range []struct {
					deps  []string
					holds []model.BlockerHold
				}{{}, {deps: []string{"2"}}, {deps: []string{"2"}, holds: []model.BlockerHold{{ID: "2", State: model.StateFailed}}}} {
					deps := blocked.deps
					for _, hold := range []queuedHold{holdNone, holdUsagePaused, holdLocalPolicy, holdSuspension, holdDispatchBlocked} {
						for _, asked := range askedFacts {
							reason := taskReasonOf(task, model.State(state), obs, deps, hold, asked, blocked.holds)
							if _, ok := taskReasons[reason]; !ok || reasonState(reason) != state {
								t.Errorf("taskReasonOf(%q, obs %+v, deps %v, hold %v, asked %+v) = %q, which is not "+
									"one of that state's classified reasons", state, obs, deps, hold, asked, reason)
							}
							reached[reason] = true
						}
					}
				}
			}
		}
	}
	for reason := range taskReasons {
		if !reached[reason] {
			t.Errorf("no task reaches %q: taskReasonOf has no arm for it", reason)
		}
	}
	for _, c := range []struct {
		state model.State
		task  model.Task
		obs   *model.Observation
		deps  []string
		hold  queuedHold
		want  string
	}{
		{model.StateQueued, model.Task{}, nil, nil, holdNone, taskReasonReady},
		{model.StateQueued, model.Task{}, nil, nil, holdUsagePaused, taskReasonUsagePaused},
		{model.StateQueued, model.Task{}, nil, nil, holdLocalPolicy, taskReasonLocalPolicy},
		{model.StateQueued, model.Task{}, nil, nil, holdSuspension, taskReasonHeldBySuspension},
		{model.StateQueued, model.Task{}, nil, []string{"2"}, holdSuspension, taskReasonBlocked},
		{model.StateQueued, model.Task{}, nil, []string{"2"}, holdUsagePaused, taskReasonBlocked},
		{model.StateQueued, model.Task{}, nil, []string{"2"}, holdLocalPolicy, taskReasonBlocked},
		{model.StateQueued, model.Task{}, nil, nil, holdDispatchBlocked, taskReasonDispatchBlocked},
		{model.StateQueued, model.Task{}, nil, []string{"2"}, holdDispatchBlocked, taskReasonDispatchBlocked},
		{model.StateRunning, model.Task{}, nil, nil, holdNone, taskReasonRunLive},
		{model.StateCompleted, model.Task{}, nil, nil, holdNone, taskReasonOnMergeQueue},
		{model.StateCompleted, model.Task{}, &model.Observation{MergeQueueState: mergeStateMerging}, nil, holdNone, taskReasonOnMergeQueue},
		{model.StateCompleted, model.Task{}, &model.Observation{MergeQueueBlockedAt: &given}, nil, holdNone, taskReasonMergeGaveUp},
		{model.StateDeferred, model.Task{}, nil, nil, holdNone, taskReasonDeferredNoWake},
		{model.StateDeferred, model.Task{DeferredUntil: &wake}, nil, nil, holdNone, taskReasonDeferredWake},
		{model.StateDeferred, model.Task{GoalKind: model.GoalBaseGreen}, nil, nil, holdNone, taskReasonGoalBaseGreen},
		{model.StateDeferred, model.Task{GoalKind: model.GoalBaseGreen, DeferredUntil: &wake}, nil, nil, holdNone, taskReasonDeferredWake},
	} {
		if got := taskReasonOf(c.task, c.state, c.obs, c.deps, c.hold, model.ReplyFacts{}, nil); got != c.want {
			t.Errorf("taskReasonOf(%q, obs %+v, deps %v, hold %v) = %q, want %q",
				c.state, c.obs, c.deps, c.hold, got, c.want)
		}
	}
	if got := TaskReason(model.Task{}, model.StateQueued, nil, nil, nil, nil, model.ReplyFacts{}, "", ""); got.Control != "Run now" ||
		got.Class != string(livenessDriven) || got.Reason != taskReasonReady {
		t.Errorf("TaskReason(queued) = %+v, want queued/ready, driven, with Run now", got)
	}
	if got := MergeReason("no_such/reason"); got != (ReasonLiveness{}) {
		t.Errorf("MergeReason of an unknown reason = %+v, want the zero value", got)
	}
}

// An awaiting_reply task's sub-case is which ask it is parked on, in the
// UI's askedCase order: a stopped turn, an access request, an interactive
// task's next message, a question, and otherwise a run that answered in
// a comment -- never "question" for all of them. Each names the control
// that ends it: Grant for an access request, Reply for the rest.
// TestTaskReasonNamesABlockerOnlyAPersonCanClose is task 4856: a queued
// task waiting on one that failed, or that nobody has approved, waits on
// a person rather than on dispatch, and its reason says so and names the
// blocker -- while one waiting on a blocker grain is still driving reads
// blocked_on_task as before, and a dispatch condition still outranks both.
func TestTaskReasonNamesABlockerOnlyAPersonCanClose(t *testing.T) {
	failed := model.BlockerHold{ID: "4822", State: model.StateFailed, Why: "failed -- Retry or close it"}
	proposed := model.BlockerHold{ID: "4830", State: model.StateProposed, Why: "proposed -- Approve or close it"}
	got := TaskReason(model.Task{}, model.StateQueued, nil, []string{"4822", "4830", "4831"},
		[]model.BlockerHold{failed, proposed}, nil, model.ReplyFacts{}, "", "")
	if got.Reason != taskReasonBlockedOnPerson || got.Class != string(livenessAwaitingPerson) || got.Driver != "" {
		t.Errorf("TaskReason(queued on a failed and a proposed task) = %+v, want %s, awaiting a person, no driver",
			got, taskReasonBlockedOnPerson)
	}
	for _, want := range []string{"task 4822 (failed -- Retry or close it)", "task 4830 (proposed -- Approve or close it)"} {
		if !strings.Contains(got.Resource, want) {
			t.Errorf("TaskReason(queued on a failed and a proposed task).Resource = %q, want it to name %q", got.Resource, want)
		}
	}
	if got := TaskReason(model.Task{}, model.StateQueued, nil, []string{"4831"}, nil, nil, model.ReplyFacts{}, "", ""); got.Reason != taskReasonBlocked {
		t.Errorf("TaskReason(queued on a driven task) = %+v, want %s", got, taskReasonBlocked)
	}
	if got := TaskReason(model.Task{}, model.StateQueued, nil, []string{"4822"}, []model.BlockerHold{failed}, nil,
		model.ReplyFacts{}, "", "git proxy down"); got.Reason != taskReasonDispatchBlocked {
		t.Errorf("TaskReason(queued on a failed task, dispatch blocked) = %+v, want %s", got, taskReasonDispatchBlocked)
	}
}

func TestAwaitingReplyReasonNamesTheAsk(t *testing.T) {
	plain, chat := model.Task{ID: "1"}, model.Task{ID: "1", Interactive: true}
	for _, c := range []struct {
		task        model.Task
		asked       model.ReplyFacts
		want, label string
	}{
		{plain, model.ReplyFacts{}, taskReasonAnswered, "Reply"},
		{plain, model.ReplyFacts{Question: true}, taskReasonQuestion, "Reply"},
		{plain, model.ReplyFacts{Question: true, Access: true}, taskReasonAccessRequest, "Grant"},
		{plain, model.ReplyFacts{Question: true, Stopped: true}, taskReasonStopped, "Reply"},
		{plain, model.ReplyFacts{Access: true, Stopped: true}, taskReasonStopped, "Reply"},
		{chat, model.ReplyFacts{}, taskReasonYourMessage, "Reply"},
		{chat, model.ReplyFacts{Question: true}, taskReasonYourMessage, "Reply"},
		{chat, model.ReplyFacts{Access: true}, taskReasonAccessRequest, "Grant"},
		{chat, model.ReplyFacts{Stopped: true}, taskReasonStopped, "Reply"},
		{plain, model.ReplyFacts{UnansweredComment: true}, taskReasonUnansweredComment, "Run now"},
		{chat, model.ReplyFacts{UnansweredComment: true}, taskReasonUnansweredComment, "Run now"},
		{plain, model.ReplyFacts{UnansweredComment: true, Stopped: true}, taskReasonStopped, "Reply"},
	} {
		got := TaskReason(c.task, model.StateAwaitingReply, nil, nil, nil, nil, c.asked, "", "")
		if got.Reason != c.want || got.Control != c.label || got.Class != string(livenessAwaitingPerson) {
			t.Errorf("TaskReason(awaiting_reply, interactive %v, %+v) = %+v, want %s, awaiting a person, with %q",
				c.task.Interactive, c.asked, got, c.want, c.label)
		}
	}
	// The facts are an awaiting_reply task's alone: another state's
	// sub-case does not move for them.
	if got := TaskReason(plain, model.StateQueued, nil, nil, nil, nil, model.ReplyFacts{Access: true}, "", "").Reason; got != taskReasonReady {
		t.Errorf("TaskReason(queued, access) = %q, want %q", got, taskReasonReady)
	}
}

// A deployment whose dispatch is down says so on every queued task,
// ahead of the task's own particulars, naming the condition; nothing
// about it is a person's to press, since what clears it is outside the
// task. A task in any other state is unaffected.
func TestTaskReasonNamesADispatchConditionOnEveryQueuedTask(t *testing.T) {
	why := "git proxy: listen tcp 0.0.0.0:7100: bind: address already in use"
	wait := &model.GrantWait{Capability: model.QuotaWaitCapability, Resource: dispatch.ConnectionPausedWhy}
	for _, c := range []struct {
		deps []string
		wait *model.GrantWait
	}{{nil, nil}, {[]string{"2"}, nil}, {nil, wait}} {
		got := TaskReason(model.Task{}, model.StateQueued, nil, c.deps, nil, c.wait, model.ReplyFacts{}, "", why)
		if got.Reason != taskReasonDispatchBlocked || got.Class != string(livenessAwaitingExternal) || got.Control != "" {
			t.Errorf("TaskReason(queued, deps %v, wait %v, blocked) = %+v, want %s, awaiting an external answer, no control",
				c.deps, c.wait, got, taskReasonDispatchBlocked)
		}
		if !strings.Contains(got.Resource, why) || !strings.HasPrefix(got.Resource, "dispatch blocked") {
			t.Errorf("Resource = %q, want it to say dispatch is blocked and name %q", got.Resource, why)
		}
	}
	if got := TaskReason(model.Task{}, model.StateRunning, nil, nil, nil, nil, model.ReplyFacts{}, "", why); got.Reason != taskReasonRunLive {
		t.Errorf("TaskReason(running, blocked) = %+v, want %s", got, taskReasonRunLive)
	}
	if got := TaskReason(model.Task{}, model.StateQueued, nil, nil, nil, nil, model.ReplyFacts{}, "", ""); got.Reason != taskReasonReady {
		t.Errorf("TaskReason(queued, not blocked) = %+v, want %s", got, taskReasonReady)
	}
}

// A queued task's usage-limit sub-case is read off the quota wait dispatch
// recorded for it: the Pause's own sentence or dispatch's, and not a wait
// for capacity.
func TestTaskReasonReadsAUsageLimitPauseOffItsWait(t *testing.T) {
	until := time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)
	gated := Coverage{windows: []pauseWindow{{scope: PauseScope{Team: model.DefaultTeamID}, until: until, reason: "usage limit"}}}
	covered := gated.Reason(PauseScope{Team: model.DefaultTeamID, Framework: "claude"})
	if covered == "" {
		t.Fatal("a pause on the whole team covers none of its runs")
	}
	for _, c := range []struct {
		wait *model.GrantWait
		want string
	}{
		{nil, taskReasonReady},
		{&model.GrantWait{Capability: model.QuotaWaitCapability, Resource: covered}, taskReasonUsagePaused},
		{&model.GrantWait{Capability: model.QuotaWaitCapability, Resource: dispatch.ConnectionPausedWhy}, taskReasonUsagePaused},
		{&model.GrantWait{Capability: model.QuotaWaitCapability, Resource: "2 worker runs are live of the 2 this cycle allows"}, taskReasonReady},
		{&model.GrantWait{Capability: model.ModelWaitCapability, Resource: covered}, taskReasonReady},
	} {
		if got := TaskReason(model.Task{}, model.StateQueued, nil, nil, nil, c.wait, model.ReplyFacts{}, "", "").Reason; got != c.want {
			t.Errorf("TaskReason(queued, wait %+v) = %q, want %q", c.wait, got, c.want)
		}
	}
}

// The merge queue records the sub-case with the state: the main pass's
// own, and the state's broadest where a writer names none.
func TestMergeQueueStateIsRecordedWithItsReason(t *testing.T) {
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	obs := &model.Observation{}
	applyMergeQueueState(obs, queueStatus{state: mergeStateWaitingCI, reason: mergeReasonCIRunning}, now)
	if obs.MergeQueueReason != mergeReasonCIRunning {
		t.Errorf("MergeQueueReason = %q, want %q", obs.MergeQueueReason, mergeReasonCIRunning)
	}
	broadest := mergeReasonOfState(mergeStateWaitingCI)
	if broadest == mergeReasonCIRunning {
		t.Fatalf("waiting_ci's broadest reason is %q, the one already recorded", broadest)
	}
	if !applyMergeQueueState(obs, queueStatus{state: mergeStateWaitingCI}, now) ||
		obs.MergeQueueReason != broadest {
		t.Errorf("a change of reason alone was not written: MergeQueueReason = %q, want %q",
			obs.MergeQueueReason, broadest)
	}
	applyMergeQueueState(obs, queueStatus{}, now)
	if obs.MergeQueueReason != "" {
		t.Errorf("MergeQueueReason = %q with no state, want empty", obs.MergeQueueReason)
	}
}
