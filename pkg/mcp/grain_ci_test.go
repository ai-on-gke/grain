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

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// fakeGrainCI is a scripted GrainCIReader, counting its reads because
// "asked once per answer" is as much a property worth pinning as what
// the answer said.
type fakeGrainCI struct {
	verdict *GrainCIVerdict
	err     error
	reads   int
}

func (f *fakeGrainCI) GrainCI(context.Context) (*GrainCIVerdict, error) {
	f.reads++
	return f.verdict, f.err
}

// testHeadSHA is the commit every fixture here pushes, and grainCIAt
// builds a verdict about a named commit -- the field the whole policy
// turns on (orchestrator.grainCIVerdictOf's "only about this commit").
const testHeadSHA = "abcdef0123456789"

func grainCIAt(commit, conclusion, detail string) *fakeGrainCI {
	return &fakeGrainCI{verdict: &GrainCIVerdict{
		RunID: "ci-7", Commit: commit, Conclusion: conclusion, Detail: detail,
	}}
}

// greenChecks is a commit GitHub is perfectly happy with -- the case
// this whole file is about, since it is the answer a run finishes on.
func greenChecks() *fakePullRequests {
	return &fakePullRequests{
		head:   &github.BranchHead{SHA: testHeadSHA, Message: "the change"},
		pr:     &github.PullRequest{Number: 42, HTMLURL: "https://github.example/pr/42"},
		detail: github.PullRequestDetail{State: "open", Mergeable: boolPtr(true), BaseRef: "main"},
		checks: []github.CheckRun{done("go", "success"), done("frontend", "success")},
	}
}

func statusWith(t *testing.T, client PullRequestReader, grainCI GrainCIReader) string {
	t.Helper()
	res := namedTool(t, NewPullRequestTools(client, grainCI, testScope), "pull_request_status").
		Handler(context.Background(), map[string]any{})
	if res.IsError {
		t.Fatalf("pull_request_status failed: %s", res.Text)
	}
	return res.Text
}

// The gap this whole change exists to close: every GitHub check green,
// grain CI red against the same commit, and the merge queue holding the
// pull request on the second of those (orchestrator.withGrainCI). A run
// told only the first finishes on a branch that does not merge -- and a
// repair run dispatched *because* grain CI is red is told its build is
// fine by the tool it was pointed at.
func TestPullRequestStatusReportsAGrainCIFailureBehindGreenChecks(t *testing.T) {
	grainCI := grainCIAt(testHeadSHA, "failure", "job \"unit\" failed")
	text := statusWith(t, greenChecks(), grainCI)

	if !strings.Contains(text, "none of them failed") {
		t.Fatalf("expected GitHub's own checks still reported as green, got:\n%s", text)
	}
	for _, want := range []string{
		"FAILED against abcdef0",
		`job "unit" failed`,
		"counts this pull request as failing",
		"git push origin grain/task-9",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected the report to say %q, got:\n%s", want, text)
		}
	}
	if grainCI.reads != 1 {
		t.Errorf("read grain CI %d times, want exactly one read per answer", grainCI.reads)
	}
}

// The merge queue's first rule, restated here: a verdict about another
// commit is about a different tree and cannot satisfy this gate. Reporting it as
// this commit's would have a run chasing a failure the queue is not
// holding it on -- and, worse the other way round, reading a stale
// success as permission to finish.
func TestGrainCIVerdictAboutAnotherCommitIsNotAVerdictAboutThisOne(t *testing.T) {
	text := statusWith(t, greenChecks(), grainCIAt("0123456789abcdef", "failure", "job \"unit\" failed"))

	if strings.Contains(text, "FAILED against") {
		t.Errorf("a verdict about another commit was reported as this commit's:\n%s", text)
	}
	for _, want := range []string{"no verdict about abcdef0", "its last is about 0123456"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected the report to say %q, got:\n%s", want, text)
		}
	}
}

// The second rule: only "failure" is failure. A cancelled run is this
// deployment's own supersede or a daemon shutting down, and a run told
// it had failed would go looking for a bug that does not exist.
func TestOnlyAGrainCIFailureIsReportedAsOneHoldingTheMerge(t *testing.T) {
	for _, conclusion := range []string{"cancelled", "skipped"} {
		text := statusWith(t, greenChecks(), grainCIAt(testHeadSHA, conclusion, ""))
		if strings.Contains(text, "FAILED against") {
			t.Errorf("grain CI %q was reported as a failure:\n%s", conclusion, text)
		}
		if !strings.Contains(text, "not the required full-suite success") {
			t.Errorf("expected grain CI %q reported as not satisfying the gate, got:\n%s", conclusion, text)
		}
	}
}

// The third rule, and the one the whole gate rests on: absence is never
// failure. Neither a task with no verdict recorded nor a read that could
// not be made is a branch that failed, and neither may be reported as
// one.
func TestAbsentGrainCIVerdictIsNeverReportedAsAFailure(t *testing.T) {
	none := statusWith(t, greenChecks(), &fakeGrainCI{})
	if strings.Contains(none, "FAILED against") {
		t.Errorf("no recorded verdict was reported as a failure:\n%s", none)
	}
	if !strings.Contains(none, "no full-suite verdict about it yet") {
		t.Errorf("expected the report to say grain CI has recorded nothing, got:\n%s", none)
	}

	unreadable := statusWith(t, greenChecks(), &fakeGrainCI{err: errors.New("daemon is down")})
	if strings.Contains(unreadable, "FAILED against") {
		t.Errorf("an unreadable verdict was reported as a failure:\n%s", unreadable)
	}
	for _, want := range []string{"could not be read", "daemon is down"} {
		if !strings.Contains(unreadable, want) {
			t.Errorf("expected the report to say %q, got:\n%s", want, unreadable)
		}
	}
}

// A run grain CI does not judge -- no daemon to ask, or a task without
// the grain-ci capability -- hears nothing about it at all, in the
// answer or in the roster. Every deployment that has not turned grain CI
// on reads exactly as it did before this existed.
func TestARunGrainCIDoesNotJudgeIsToldNothingAboutIt(t *testing.T) {
	text := statusWith(t, greenChecks(), nil)
	if strings.Contains(text, ".grain/workflows") {
		t.Errorf("a run with no grain CI reader was told about grain CI:\n%s", text)
	}
	for _, name := range []string{"pull_request_status", "wait_for_checks"} {
		desc := namedTool(t, NewPullRequestTools(greenChecks(), nil, testScope), name).Description
		if strings.Contains(desc, "grain's own CI") {
			t.Errorf("%s's description names grain CI for a run it does not judge:\n%s", name, desc)
		}
		with := namedTool(t, NewPullRequestTools(greenChecks(), &fakeGrainCI{}, testScope), name).Description
		if !strings.Contains(with, "grain's own CI") {
			t.Errorf("%s's description says nothing about grain CI for a run it judges:\n%s", name, with)
		}
	}
}

// wait_for_checks is where finishing on a green answer is decided, so
// the paragraph matters most there: every check finished, none failed,
// and the branch still does not merge because grain CI is not done with
// it.
func TestWaitForChecksGreenReportSaysGrainCIStillHoldsTheBranch(t *testing.T) {
	client := &scriptedChecks{
		head:   pushed(),
		rounds: [][]github.CheckRun{{done("tests", "success")}},
	}
	waiter := waiterFor(client, &fakeClock{})
	waiter.grainCI = &fakeGrainCI{verdict: &GrainCIVerdict{RunID: "ci-7", Commit: testHeadSHA}}

	text, err := waiter.wait(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(text, "is still running against abcdef0") {
		t.Errorf("expected the report to say grain CI has not reported yet, got:\n%s", text)
	}
	if !strings.Contains(text, "holds this pull request pending") {
		t.Errorf("expected the report to say what that costs the pull request, got:\n%s", text)
	}
	// The paragraph is appended to a report that is also a record: a
	// verdict grain could no longer read back would quietly measure this
	// deployment's whole CI loop as never having happened
	// (telemetry.go).
	wait, ok := ReadCheckWait(text)
	if !ok || wait.Verdict != WaitVerdictPassed {
		t.Errorf("ReadCheckWait(%q) = %+v, %v; want the passed verdict", text, wait, ok)
	}
}

// base_branch_status compares this branch's checks with the base's, and
// grain CI is the one thing in the answer that has no counterpart to
// compare with: it runs against the branch a task pushed and never
// against the base (orchestrator.reconcileCI). A run left to assume
// otherwise reads a green base as evidence that a red grain CI run is
// somebody else's problem.
func TestBaseBranchStatusSaysTheComparisonDoesNotCoverGrainCI(t *testing.T) {
	client := &fakePullRequests{
		heads: map[string]*github.BranchHead{
			"main": {SHA: "1111111111111111", Message: "trunk"},
		},
		checks: []github.CheckRun{done("go", "success")},
	}
	scope := PullRequestScope{Owner: "acme", Repo: "widgets", Branch: "grain/task-9", Base: "main"}

	res := namedTool(t, NewPullRequestTools(client, &fakeGrainCI{}, scope), "base_branch_status").
		Handler(context.Background(), map[string]any{})
	if res.IsError {
		t.Fatalf("base_branch_status failed: %s", res.Text)
	}
	if !strings.Contains(res.Text, "covers GitHub's checks only") {
		t.Errorf("expected the base comparison to disclaim grain CI, got:\n%s", res.Text)
	}

	without := namedTool(t, NewPullRequestTools(client, nil, scope), "base_branch_status").
		Handler(context.Background(), map[string]any{})
	if strings.Contains(without.Text, "covers GitHub's checks only") {
		t.Errorf("a run with no grain CI reader was told about grain CI:\n%s", without.Text)
	}
}
