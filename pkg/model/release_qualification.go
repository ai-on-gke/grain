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

package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Stage 2 of release-candidate qualification
// (design/playbook/releases.playbook.md §2): the `release-qualification`
// suite run against a `vX.Y.Z-rc.N` tag, judged objectively and projected
// onto the tag's commit as one GitHub check run. Everything in this file
// is pure -- what the reconciler (orchestrator/releasequalification.go)
// reads off the store and GitHub goes in, a verdict comes out -- so the
// rules the playbook writes down are tested here without either.

// ReleaseQualificationSuiteStem is the repo-declared suite Stage 2 runs:
// `.grain/suites/release-qualification.yml`, stored as
// RepoDeclaredID(DeclaredSuite, repo, ReleaseQualificationSuiteStem).
const ReleaseQualificationSuiteStem = "release-qualification"

// ReleaseQualificationCheckName is the check run the verdict is posted as
// on rc_sha, which the release train reads (playbook §2, condition 4).
const ReleaseQualificationCheckName = "grain/release-qualification"

// ReleaseQualificationStage1Workflow is the workflow name Stage 1 runs as
// (`.github/workflows/nightly.yml`'s `name:`). Stage 2 starts only once
// its newest run on rc_sha has succeeded (playbook §13).
const ReleaseQualificationStage1Workflow = "nightly"

// PriorityReleaseGate is the task priority every Stage 2 task is filed
// at: ahead of the ordinary backlog (DefaultTaskPriority), so a full
// worker queue does not starve a release, and below the top of the
// scale, which is left for the work a person is waiting on (playbook
// §13). It is a task priority -- workflows.PriorityReleaseGate is the CI
// rank of the same idea.
const PriorityReleaseGate = 8

// ReleaseQualificationTag is the SuiteRunOptions.Tag every suite run the
// reconciler starts for one candidate carries, so all of that
// candidate's attempts can be found again (Store.SuiteRunsTagged)
// without a table of their own.
func ReleaseQualificationTag(repo RepoRef, rcTag string) string {
	return "release-qualification:" + strings.ToLower(repo.Owner) + "/" + strings.ToLower(repo.Name) + "@" + rcTag
}

// QualificationScratchBranch is the scratch branch a qualification task
// may create, and must delete before it exits (playbook §2, condition 3).
func QualificationScratchBranch(taskID string) string {
	return "qualification/" + taskID
}

// RCTag is one parsed `vX.Y.Z-rc.N` release-candidate tag.
type RCTag struct {
	Major, Minor, Patch, RC int
}

var rcTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)-rc\.(\d+)$`)

// ParseRCTag parses name as a release-candidate tag. A `-discarded`
// marker, a promoted `vX.Y.Z`, or anything else is not one.
func ParseRCTag(name string) (RCTag, bool) {
	m := rcTagPattern.FindStringSubmatch(name)
	if m == nil {
		return RCTag{}, false
	}
	var n [4]int
	for i := range n {
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			return RCTag{}, false
		}
		n[i] = v
	}
	return RCTag{Major: n[0], Minor: n[1], Patch: n[2], RC: n[3]}, true
}

func (t RCTag) String() string {
	return fmt.Sprintf("v%d.%d.%d-rc.%d", t.Major, t.Minor, t.Patch, t.RC)
}

// Version is the release this candidate would be promoted to.
func (t RCTag) Version() string {
	return fmt.Sprintf("v%d.%d.%d", t.Major, t.Minor, t.Patch)
}

// Less orders candidates by version, then by attempt.
func (t RCTag) Less(o RCTag) bool {
	a := [4]int{t.Major, t.Minor, t.Patch, t.RC}
	b := [4]int{o.Major, o.Minor, o.Patch, o.RC}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// QualificationVerdict is the value of a closing note's
// `Qualification-Verdict:` footer.
type QualificationVerdict string

const (
	QualificationPass  QualificationVerdict = "PASS"
	QualificationFail  QualificationVerdict = "FAIL"
	QualificationInfra QualificationVerdict = "INFRA"
)

// QualificationFooter is the machine-read footer every qualification
// template requires its closing note to end with (playbook §2,
// condition 2).
type QualificationFooter struct {
	Verdict QualificationVerdict
	Tag     string
	Commit  string
}

var (
	footerLine = regexp.MustCompile(`^\s*(Qualification-Verdict|Qualified-Tag|Qualified-Commit)\s*:\s*(\S+)\s*$`)
	fullSHA    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ParseQualificationFooter reads the footer out of a closing note. It
// takes the last value of each key, so a note quoting the format before
// writing its own is read by its own; ok is false unless all three keys
// are present, the verdict is one of the three values, and the commit is
// a full 40-character sha. Anything short of that is a missing footer,
// which fails the item.
func ParseQualificationFooter(note string) (QualificationFooter, bool) {
	var f QualificationFooter
	for _, line := range strings.Split(note, "\n") {
		m := footerLine.FindStringSubmatch(strings.Trim(line, "`*"))
		if m == nil {
			continue
		}
		value := strings.Trim(m[2], "`*")
		switch m[1] {
		case "Qualification-Verdict":
			f.Verdict = QualificationVerdict(strings.ToUpper(value))
		case "Qualified-Tag":
			f.Tag = value
		case "Qualified-Commit":
			f.Commit = strings.ToLower(value)
		}
	}
	switch f.Verdict {
	case QualificationPass, QualificationFail, QualificationInfra:
	default:
		return f, false
	}
	if f.Tag == "" || !fullSHA.MatchString(f.Commit) {
		return f, false
	}
	return f, true
}

// QualificationAttempt is one task one Stage 2 item was filed as, with
// everything about it the verdict is read from.
type QualificationAttempt struct {
	TaskID     string
	TemplateID string
	State      State
	// OpenedPullRequest is SuiteTaskStatus's own: a pull request is
	// PassChanged, and fails the item. Proposed is narrower than
	// SuiteTaskStatus's: true for a follow-up proposed on any branch but
	// the task's own qualification/<task-id> scratch branch -- a defect,
	// which fails the item. Proposals on the scratch branch are the
	// experiment's own fixtures, and are judged by OpenChildren instead.
	OpenedPullRequest bool
	Proposed          bool
	// Note is the task's closing note -- its newest comment carrying a
	// Qualification-Verdict footer, or its newest agent comment.
	Note string
	// LastOutcome is the newest run's Outcome, which is how a failed task
	// is told apart from an infrastructure casualty.
	LastOutcome string
	// LeakedScratchRef is true when the task's qualification/<id> branch
	// still exists after it finished.
	LeakedScratchRef bool
	// OpenChildren is every scratch fixture this task proposed that is
	// still live -- proposed, queued, running, or failed and due another
	// run -- after the task itself finished.
	OpenChildren []string
}

// ItemResult is one attempt's (or one item's) reading.
type ItemResult string

const (
	ItemPending ItemResult = "pending"
	ItemPass    ItemResult = "pass"
	ItemFail    ItemResult = "fail"
	ItemInfra   ItemResult = "infra"
)

// ItemVerdict is one attempt's result and, unless it passed or is
// pending, why.
type ItemVerdict struct {
	TaskID string
	Result ItemResult
	Reason string
}

// infraOutcomes are the run outcomes that are the environment's fault
// rather than the release's (playbook §4): a failed task whose last run
// ended in one of these is re-run, not judged.
var infraOutcomes = map[string]bool{
	PausedOutcome:      true,
	CapacityOutcome:    true,
	InterruptedOutcome: true,
	EnvironmentOutcome: true,
	OOMOutcome:         true,
	SetupFailedOutcome: true,
	SuspendedOutcome:   true,
}

// JudgeQualificationAttempt applies the playbook's four objective
// conditions to one attempt against the candidate it was filed for.
// Failures are checked in the order a reader would want to be told
// them: how the task ended, what it changed, what its footer says, and
// what it left behind.
func JudgeQualificationAttempt(a QualificationAttempt, rcTag, rcSHA string) ItemVerdict {
	v := ItemVerdict{TaskID: a.TaskID}
	fail := func(format string, args ...any) ItemVerdict {
		v.Result, v.Reason = ItemFail, fmt.Sprintf(format, args...)
		return v
	}
	switch a.State {
	case StateCompleted, StateAwaitingReply, StateAwaitingSubmit:
	case StateFailed:
		if infraOutcomes[a.LastOutcome] {
			v.Result, v.Reason = ItemInfra, fmt.Sprintf("task %s failed on infrastructure (%s)", a.TaskID, a.LastOutcome)
			return v
		}
		return fail("task %s failed (%s)", a.TaskID, orNone(a.LastOutcome))
	case StateClosed, StateDeferred:
		return fail("task %s was %s without completing", a.TaskID, a.State)
	default:
		v.Result = ItemPending
		return v
	}
	if a.OpenedPullRequest {
		return fail("task %s opened a pull request", a.TaskID)
	}
	if a.Proposed {
		return fail("task %s proposed a follow-up task", a.TaskID)
	}
	footer, ok := ParseQualificationFooter(a.Note)
	if !ok {
		return fail("task %s's closing note has no valid Qualification-Verdict/Qualified-Tag/Qualified-Commit footer", a.TaskID)
	}
	if footer.Commit != strings.ToLower(rcSHA) {
		return fail("task %s qualified commit %s, not %s", a.TaskID, footer.Commit, rcSHA)
	}
	if footer.Tag != rcTag {
		return fail("task %s qualified tag %s, not %s", a.TaskID, footer.Tag, rcTag)
	}
	if a.LeakedScratchRef {
		return fail("task %s left refs/heads/%s behind", a.TaskID, QualificationScratchBranch(a.TaskID))
	}
	if len(a.OpenChildren) > 0 {
		return fail("task %s left scratch fixture task(s) %s open", a.TaskID, strings.Join(a.OpenChildren, ", "))
	}
	switch footer.Verdict {
	case QualificationInfra:
		v.Result, v.Reason = ItemInfra, fmt.Sprintf("task %s reported Qualification-Verdict: INFRA", a.TaskID)
		return v
	case QualificationFail:
		return fail("task %s reported Qualification-Verdict: FAIL", a.TaskID)
	}
	v.Result = ItemPass
	return v
}

func orNone(s string) string {
	if s == "" {
		return "no outcome recorded"
	}
	return s
}

// QualificationPolicy is the retry budget of playbook §3 and §4.
type QualificationPolicy struct {
	// FlakeRetries is how many product failures of one item are re-run
	// on the same candidate before the item is confirmed red.
	FlakeRetries int
	// MaxFlakyItems is how many distinct items may spend a flake retry
	// on one candidate; one more is systemic breakage, and fails at once.
	MaxFlakyItems int
	// MaxInfraReruns is how many consecutive infrastructure casualties of
	// one item are re-run before the candidate is left stalled.
	MaxInfraReruns int
}

// DefaultQualificationPolicy is the playbook's: one retry, two items,
// three infrastructure re-runs.
var DefaultQualificationPolicy = QualificationPolicy{FlakeRetries: 1, MaxFlakyItems: 2, MaxInfraReruns: 3}

// Check-run conclusions the verdict is posted as.
const (
	ConclusionSuccess = "success"
	ConclusionFailure = "failure"
	ConclusionNeutral = "neutral"
)

// QualificationDecision is what the reconciler does next for one
// candidate.
type QualificationDecision struct {
	// Conclusion is empty while the candidate is still qualifying, and
	// otherwise the check run's conclusion: success, failure, or neutral
	// (infrastructure stalled -- neither a pass nor a red candidate).
	Conclusion string
	// Rerun is every item to file again now, flake retries and
	// infrastructure re-runs alike.
	Rerun []string
	// StartFinal is true when every gating item has passed and the final
	// item has never been filed.
	StartFinal bool
	// Lines is one line per item, for the check run's summary.
	Lines []string
}

// DecideReleaseQualification folds every attempt of every item (oldest
// first, keyed by template id) into the next step. items is the suite's
// own order; its last item is the final one, which is filed only after
// every other item has passed (qualification-system-smoke, playbook §2).
//
// A confirmed red item fails the candidate at once, whatever else is in
// flight (fail-fast, §13); so does a product failure in more items than
// MaxFlakyItems (§3). An item out of infrastructure re-runs leaves the
// candidate neutral rather than red (§4).
func DecideReleaseQualification(items []string, attempts map[string][]ItemVerdict, policy QualificationPolicy) QualificationDecision {
	var d QualificationDecision
	if len(items) == 0 {
		d.Conclusion = ConclusionFailure
		d.Lines = []string{"the release-qualification suite has no items"}
		return d
	}
	final := items[len(items)-1]

	var red, stalled, flaky []string
	gatesPassed, finalPassed := true, false
	for _, item := range items {
		history := attempts[item]
		if len(history) == 0 {
			if item != final {
				gatesPassed = false
			}
			d.Lines = append(d.Lines, fmt.Sprintf("%s: not started", item))
			continue
		}
		last := history[len(history)-1]
		failures, infraRun := 0, 0
		for _, a := range history {
			switch a.Result {
			case ItemFail:
				failures++
				infraRun = 0
			case ItemInfra:
				infraRun++
			default:
				infraRun = 0
			}
		}
		if failures > 0 {
			flaky = append(flaky, item)
		}
		line := fmt.Sprintf("%s: %s (task %s, attempt %d)", item, last.Result, last.TaskID, len(history))
		if last.Reason != "" {
			line += " -- " + last.Reason
		}
		d.Lines = append(d.Lines, line)
		switch last.Result {
		case ItemPass:
			if item == final {
				finalPassed = true
			}
			continue
		case ItemFail:
			if failures > policy.FlakeRetries {
				red = append(red, item)
			} else {
				d.Rerun = append(d.Rerun, item)
			}
		case ItemInfra:
			if infraRun > policy.MaxInfraReruns {
				stalled = append(stalled, item)
			} else {
				d.Rerun = append(d.Rerun, item)
			}
		}
		if item != final {
			gatesPassed = false
		}
	}

	switch {
	case len(red) > 0:
		d.Conclusion, d.Rerun = ConclusionFailure, nil
		d.Lines = append(d.Lines, fmt.Sprintf("confirmed red after %d retry: %s", policy.FlakeRetries, strings.Join(red, ", ")))
	case len(flaky) > policy.MaxFlakyItems:
		d.Conclusion, d.Rerun = ConclusionFailure, nil
		d.Lines = append(d.Lines, fmt.Sprintf("%d items failed (%s): systemic breakage, not retried", len(flaky), strings.Join(flaky, ", ")))
	case len(stalled) > 0 && len(d.Rerun) == 0 && !anyPending(items, attempts):
		d.Conclusion = ConclusionNeutral
		d.Lines = append(d.Lines, fmt.Sprintf("stalled on infrastructure after %d re-runs: %s", policy.MaxInfraReruns, strings.Join(stalled, ", ")))
	case len(stalled) > 0:
		// Let what is still in flight finish first: a red item among
		// them is a verdict worth more than neutral.
	case gatesPassed && finalPassed:
		d.Conclusion = ConclusionSuccess
	case gatesPassed && len(attempts[final]) == 0:
		d.StartFinal = true
	}
	return d
}

func anyPending(items []string, attempts map[string][]ItemVerdict) bool {
	for _, item := range items {
		if h := attempts[item]; len(h) > 0 && h[len(h)-1].Result == ItemPending {
			return true
		}
	}
	return false
}
