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

package change

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Stage S3 (changelists.md §3, G3-3): a change's checks, as
// pull_request_status and wait_for_checks answer them for a task whose
// change is not a pull request, and the polling that keeps the task view's
// presubmit badge current. Both are optional interfaces a Provider may
// implement; core asks for them with a type assertion and never names the
// edition that answers.

// ChecksWait is one wait_for_checks on a non-PR change.
type ChecksWait struct {
	// Jobs narrows the wait to these checks (TAP targets or projects for
	// a CL); empty waits for every required one. A selective wait never
	// satisfies the merge gate.
	Jobs []string
	// Timeout is how long this call may block; Credited is how much
	// blocked time the run has already been credited, so a provider that
	// says what the wait bought can say it against the same cap.
	Timeout, Credited time.Duration
}

// ChecksReport is a change's checks, as one status read or wait saw them.
type ChecksReport struct {
	// Text is the whole answer the agent reads, rendered by the provider.
	Text   string `json:"text"`
	Health Health `json:"health,omitempty"`
	// Presubmit is the snapshot's presubmit state: passed, failed,
	// running, not_run or unknown (the Presubmit* constants).
	Presubmit string `json:"presubmit,omitempty"`
	Snapshot  int    `json:"snapshot,omitempty"`
	// Failing names the checks with a failing verdict, Waiting the ones
	// still without one when the call returned.
	Failing []string `json:"failing,omitempty"`
	Waiting []string `json:"waiting,omitempty"`
	// Started is a wait that started presubmit because none had run on
	// the current snapshot.
	Started bool `json:"started,omitempty"`
	// Selective is a wait narrowed by Jobs; SatisfiesGate is false for
	// every selective wait and for any wait without a full verdict.
	Selective     bool `json:"selective,omitempty"`
	SatisfiesGate bool `json:"satisfiesGate,omitempty"`
	TimedOut      bool `json:"timedOut,omitempty"`
	// Waited is how long the provider blocked; Credit what the provider
	// says the wait is worth against the run's deadline.
	Waited time.Duration `json:"waited,omitempty"`
	Credit time.Duration `json:"credit,omitempty"`
	// FaultNote names shared faults on the provider's CI (failing on
	// other changes too). grain files no repair task for them.
	FaultNote string `json:"faultNote,omitempty"`
	// Provider is the CI that answered, as metrics label it ("tap").
	Provider string `json:"provider,omitempty"`
}

// The presubmit states a ChecksReport or Observation carries.
const (
	PresubmitPassed  = "passed"
	PresubmitFailed  = "failed"
	PresubmitRunning = "running"
	PresubmitNotRun  = "not_run"
	PresubmitUnknown = "unknown"
)

// Checks is a Provider that answers pull_request_status and
// wait_for_checks for its changes, and refuses grain CI's own tools
// (retrigger_ci, run_ci_job, get_ci_log) in its own words.
type Checks interface {
	ChecksStatus(ctx context.Context, task model.Task, target string) (ChecksReport, error)
	WaitChecks(ctx context.Context, task model.Task, target string, req ChecksWait) (ChecksReport, error)
	GrainCIRefusal(tool string) string
}

// Poller is a Provider that observes many of its changes at once, at
// intervals and under a rate limit of its own choosing: handed every task
// with a live change link on each tick, it answers for the ones it read.
type Poller interface {
	PollTick(ctx context.Context, tasks []model.Task) []Observation
}

// ChecksFor is the Checks answering for task's changes, and its primary
// change's target ("" before it has one); ok is false when no provider
// registered for its changes implements Checks.
func ChecksFor(task model.Task) (Checks, Provider, string, bool) {
	target := Primary(task)
	p, ok := Lookup(model.LinkChangelist)
	if !ok {
		return nil, nil, target, false
	}
	c, ok := p.(Checks)
	return c, p, target, ok
}

// Observed is the last observation recorded for each change target, read
// by the task view and the inbox for a presubmit badge. It is memory
// only: a daemon restart forgets it until the next poll, which for a live
// change is minutes away.
var (
	observedMu sync.RWMutex
	observed   = map[string]Observation{}
)

// Remember records obs as its target's latest observation.
func Remember(obs Observation) {
	if obs.Target == "" {
		return
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	observed[obs.Target] = obs
}

// Forget drops target's recorded observation.
func Forget(target string) {
	observedMu.Lock()
	defer observedMu.Unlock()
	delete(observed, target)
}

// LastObserved is target's latest recorded observation.
func LastObserved(target string) (Observation, bool) {
	observedMu.RLock()
	defer observedMu.RUnlock()
	o, ok := observed[target]
	return o, ok
}

// Failing is obs's failing checks' names, in the order observed.
func (o Observation) Failing() []string {
	var out []string
	for _, c := range o.Checks {
		if checkFailed(c.State) && !slices.Contains(out, c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

func checkFailed(state string) bool {
	switch state {
	case "failed", "failure", "error", "broken":
		return true
	}
	return false
}
