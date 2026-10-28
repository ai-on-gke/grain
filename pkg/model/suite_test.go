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

// Suites' own model tests (bwsalmon/agents#642) -- Suite. Validate and
// the pass reductions SyncSuites decides from, the same discipline
// qualification_test.go already holds QualificationPlan.Validate and
// QualificationStatus to. Nothing here touches a store: every function
// under test is a pure reduction over values, which is exactly why it
// is worth pinning down here rather than only through the reconciler's
// own end-to-end tests.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func countSuite() model.Suite {
	return model.Suite{
		Name:  "smoke",
		Items: []model.SuiteItem{{TemplateID: "template-smoke"}},
		Mode:  model.SuiteCount, Count: 3,
	}
}

func untilCleanSuite() model.Suite {
	return model.Suite{
		Name:  "sweep",
		Items: []model.SuiteItem{{TemplateID: "template-sweep"}},
		Mode:  model.SuiteUntilClean, MaxPasses: 5,
	}
}

func TestSuiteModeValid(t *testing.T) {
	for _, m := range []model.SuiteMode{model.SuiteCount, model.SuiteUntilClean} {
		if !m.Valid() {
			t.Errorf("%q.Valid() = false, want true", m)
		}
	}
	for _, m := range []model.SuiteMode{"", "COUNT", "untilclean", "forever"} {
		if m.Valid() {
			t.Errorf("%q.Valid() = true, want false", m)
		}
	}
}

func TestSuiteValidateAcceptsBothModes(t *testing.T) {
	if err := countSuite().Validate(); err != nil {
		t.Errorf("count suite: %v", err)
	}
	if err := untilCleanSuite().Validate(); err != nil {
		t.Errorf("until_clean suite: %v", err)
	}
}

func TestSuiteValidateRejectsNoItems(t *testing.T) {
	s := countSuite()
	s.Items = nil
	if err := s.Validate(); err == nil {
		t.Fatal("a suite with no templates validated, want an error")
	}
}

func TestSuiteValidateRejectsABlankTemplateID(t *testing.T) {
	s := countSuite()
	s.Items = []model.SuiteItem{{TemplateID: "template-smoke"}, {TemplateID: ""}}
	if err := s.Validate(); err == nil {
		t.Fatal("a suite item with no template validated, want an error")
	}
}

func TestSuiteValidateRejectsCountBelowOne(t *testing.T) {
	s := countSuite()
	for _, n := range []int{0, -1} {
		s.Count = n
		if err := s.Validate(); err == nil {
			t.Errorf("count %d validated, want an error", n)
		}
	}
}

func TestSuiteValidateRejectsMaxPassesBelowOne(t *testing.T) {
	s := untilCleanSuite()
	for _, n := range []int{0, -1} {
		s.MaxPasses = n
		if err := s.Validate(); err == nil {
			t.Errorf("max passes %d validated, want an error", n)
		}
	}
}

// Task 3323: a count or max passes of 1000 used to save, committing the
// deployment to a thousand passes from one click.
func TestSuiteValidateCapsThePassCount(t *testing.T) {
	c := countSuite()
	c.Count = model.MaxSuitePasses
	if err := c.Validate(); err != nil {
		t.Errorf("count %d: %v", c.Count, err)
	}
	u := untilCleanSuite()
	u.MaxPasses = model.MaxSuitePasses
	if err := u.Validate(); err != nil {
		t.Errorf("max passes %d: %v", u.MaxPasses, err)
	}
	limit := strconv.Itoa(model.MaxSuitePasses)
	for _, n := range []int{model.MaxSuitePasses + 1, 1000} {
		c.Count = n
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), limit) {
			t.Errorf("count %d: got %v, want an error naming %s", n, err, limit)
		}
		u.MaxPasses = n
		if err := u.Validate(); err == nil || !strings.Contains(err.Error(), limit) {
			t.Errorf("max passes %d: got %v, want an error naming %s", n, err, limit)
		}
	}
}

func TestSuiteValidateIgnoresTheOtherModesBound(t *testing.T) {
	// Count is meaningless to an until_clean suite and MaxPasses to a
	// count one -- ui.CreateSuite leaves whichever the form did not ask
	// for at zero, so Validate must not read it.
	s := untilCleanSuite()
	s.Count = 0
	if err := s.Validate(); err != nil {
		t.Errorf("until_clean suite with Count 0: %v", err)
	}
	c := countSuite()
	c.MaxPasses = 0
	if err := c.Validate(); err != nil {
		t.Errorf("count suite with MaxPasses 0: %v", err)
	}
}

func TestSuiteValidateRejectsAnUnknownMode(t *testing.T) {
	s := countSuite()
	s.Mode = "forever"
	if err := s.Validate(); err == nil {
		t.Fatal("an unknown mode validated, want an error")
	}
}

// passTask is one instance in a pass, in whatever state a case needs.
func passTask(pass int, state model.State) model.SuiteTaskStatus {
	return model.SuiteTaskStatus{
		TaskID: "task-" + string(state), PassNumber: pass, State: state,
	}
}

func TestCurrentPassIsTheHighestPassNumber(t *testing.T) {
	run := model.SuiteRun{Tasks: []model.SuiteTaskStatus{
		passTask(1, model.StateCompleted), passTask(2, model.StateCompleted), passTask(3, model.StateRunning),
	}}
	if got := run.CurrentPass(); got != 3 {
		t.Fatalf("CurrentPass() = %d, want 3", got)
	}
}

func TestCurrentPassIsZeroBeforeAnyPassHasFired(t *testing.T) {
	if got := (model.SuiteRun{}).CurrentPass(); got != 0 {
		t.Fatalf("CurrentPass() = %d on a run with no tasks, want 0", got)
	}
}

func TestPassTasksReturnsOnlyThatPass(t *testing.T) {
	a := model.SuiteTaskStatus{TaskID: "task-1", PassNumber: 1, State: model.StateCompleted}
	b := model.SuiteTaskStatus{TaskID: "task-2", PassNumber: 2, State: model.StateCompleted}
	c := model.SuiteTaskStatus{TaskID: "task-3", PassNumber: 2, State: model.StateRunning}
	run := model.SuiteRun{Tasks: []model.SuiteTaskStatus{a, b, c}}

	got := run.PassTasks(2)
	if len(got) != 2 || got[0].TaskID != "task-2" || got[1].TaskID != "task-3" {
		t.Fatalf("PassTasks(2) = %+v, want task-2 and task-3 in order", got)
	}
	if got := run.PassTasks(3); got != nil {
		t.Fatalf("PassTasks(3) = %+v on a run with no pass 3, want nil", got)
	}
}

func TestOutcomeOfPassCleanOnceEveryTaskCompletesWithNothingToShow(t *testing.T) {
	tasks := []model.SuiteTaskStatus{passTask(1, model.StateCompleted), passTask(1, model.StateCompleted)}
	if got := model.OutcomeOfPass(tasks); got != model.PassClean {
		t.Fatalf("OutcomeOfPass = %v, want PassClean", got)
	}
}

func TestOutcomeOfPassChangedOnAPullRequestOrAProposal(t *testing.T) {
	opened := passTask(1, model.StateCompleted)
	opened.OpenedPullRequest = true
	if got := model.OutcomeOfPass([]model.SuiteTaskStatus{passTask(1, model.StateCompleted), opened}); got != model.PassChanged {
		t.Fatalf("OutcomeOfPass with a pull request = %v, want PassChanged", got)
	}
	proposed := passTask(1, model.StateCompleted)
	proposed.Proposed = true
	if got := model.OutcomeOfPass([]model.SuiteTaskStatus{passTask(1, model.StateCompleted), proposed}); got != model.PassChanged {
		t.Fatalf("OutcomeOfPass with a proposal = %v, want PassChanged", got)
	}
}

func TestOutcomeOfPassPendingWhileAnyTaskIsStillInFlight(t *testing.T) {
	for _, state := range []model.State{model.StateProposed, model.StateQueued, model.StateRunning} {
		tasks := []model.SuiteTaskStatus{passTask(1, model.StateCompleted), passTask(1, state)}
		if got := model.OutcomeOfPass(tasks); got != model.PassPending {
			t.Errorf("OutcomeOfPass alongside a %s task = %v, want PassPending", state, got)
		}
	}
}

func TestOutcomeOfPassFailedOnAFailureOrACloseWithoutCompleting(t *testing.T) {
	for _, state := range []model.State{model.StateFailed, model.StateClosed} {
		tasks := []model.SuiteTaskStatus{passTask(1, model.StateCompleted), passTask(1, state)}
		if got := model.OutcomeOfPass(tasks); got != model.PassFailed {
			t.Errorf("OutcomeOfPass alongside a %s task = %v, want PassFailed", state, got)
		}
	}
}

func TestOutcomeOfPassFailureOutranksAStragglerInEitherOrder(t *testing.T) {
	// A pass fires every item at once, so one item failing while another
	// is still running is the ordinary shape of a pass going wrong --
	// and it must read the same whichever order the two come back in,
	// rather than letting the straggler hide a failure already known.
	failed, running := passTask(1, model.StateFailed), passTask(1, model.StateRunning)
	if got := model.OutcomeOfPass([]model.SuiteTaskStatus{failed, running}); got != model.PassFailed {
		t.Errorf("failure first: got %v, want PassFailed", got)
	}
	if got := model.OutcomeOfPass([]model.SuiteTaskStatus{running, failed}); got != model.PassFailed {
		t.Errorf("straggler first: got %v, want PassFailed", got)
	}
}

func TestOutcomeOfPassFailureOutranksAChange(t *testing.T) {
	changed := passTask(1, model.StateCompleted)
	changed.OpenedPullRequest = true
	tasks := []model.SuiteTaskStatus{changed, passTask(1, model.StateFailed)}
	if got := model.OutcomeOfPass(tasks); got != model.PassFailed {
		t.Fatalf("OutcomeOfPass = %v, want PassFailed", got)
	}
}

func TestOutcomeOfPassOfNoTasksIsClean(t *testing.T) {
	// Unreachable through the store -- CreateSuiteRun always files a
	// pass, and a suite always has at least one item -- but a zero
	// value must not read as pending, which would strand a run forever.
	if got := model.OutcomeOfPass(nil); got != model.PassClean {
		t.Fatalf("OutcomeOfPass(nil) = %v, want PassClean", got)
	}
}
