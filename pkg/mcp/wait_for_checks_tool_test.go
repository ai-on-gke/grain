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

// scriptedChecks is a PullRequestReader whose CI answer changes with
// every read, which is the whole thing wait_for_checks is about: a real
// build is queued, then running, then done, and a tool that only ever
// saw one answer could not tell those apart.
type scriptedChecks struct {
	head    *github.BranchHead
	headErr error

	// rounds[i] is the answer to the i'th read of CI; the last entry
	// answers every read after it, so a script does not have to guess
	// how many polls a wait will make.
	rounds [][]github.CheckRun
	errs   []error

	jobLogs    []github.JobLog
	jobLogsErr error
	// actions is what GitHub Actions is doing against the commit, read
	// only once a failure has come back with no log to show for it.
	actions github.ActionsState

	reads int
	// logReads counts the reads of the failing jobs' logs, which is how a
	// test pins that they are read on the failing path only rather than
	// once per poll.
	logReads int
	repos    []string
}

func (s *scriptedChecks) GetBranchHead(owner, repo, branch string) (*github.BranchHead, error) {
	s.repos = append(s.repos, owner+"/"+repo)
	return s.head, s.headErr
}

// wait_for_checks waits on the branch it was given and never resolves a
// base: it points at base_branch_status in words when something fails
// (baseComparisonPointer) rather than reading that branch itself, and a
// call here would mean it had started to.
func (s *scriptedChecks) DefaultBranch(string, string) (string, error) {
	return "", errors.New("wait_for_checks must not resolve the default branch")
}

func (s *scriptedChecks) FindOpenPullRequestForBranch(string, string, string) (*github.PullRequest, error) {
	return nil, errors.New("wait_for_checks must not look for a pull request")
}

func (s *scriptedChecks) GetPullRequest(string, string, int) (github.PullRequestDetail, error) {
	return github.PullRequestDetail{}, errors.New("wait_for_checks must not read a pull request")
}

func (s *scriptedChecks) ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error) {
	s.repos = append(s.repos, owner+"/"+repo)
	i := s.reads
	s.reads++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if len(s.rounds) == 0 {
		return nil, nil
	}
	if i >= len(s.rounds) {
		i = len(s.rounds) - 1
	}
	return s.rounds[i], nil
}

func (s *scriptedChecks) ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error) {
	return nil, &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible"}`)}
}

func (s *scriptedChecks) FailedJobLogs(owner, repo, headSHA string) ([]github.JobLog, error) {
	s.repos = append(s.repos, owner+"/"+repo)
	s.logReads++
	return s.jobLogs, s.jobLogsErr
}

func (s *scriptedChecks) ActionsState(owner, repo, headSHA string) (github.ActionsState, error) {
	s.repos = append(s.repos, owner+"/"+repo)
	return s.actions, nil
}

func (s *scriptedChecks) ListReviewComments(owner, repo string, number int) ([]github.ReviewComment, error) {
	s.repos = append(s.repos, owner+"/"+repo)
	return nil, nil
}

// fakeClock is a clock that only ever moves when the waiter sleeps, so a
// test can run a full-length wait in no time and know exactly how much
// waiting the loop believed it did.
type fakeClock struct {
	t      time.Time
	slept  []time.Duration
	cancel error
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if c.cancel != nil {
		return c.cancel
	}
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
	return nil
}

func waiterFor(client PullRequestReader, clock *fakeClock) checkWaiter {
	clock.t = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	return checkWaiter{
		client: client,
		scope:  testScope,
		poll:   15 * time.Second,
		grace:  3 * time.Minute,
		now:    clock.now,
		sleep:  clock.sleep,
	}
}

func pushed() *github.BranchHead {
	return &github.BranchHead{SHA: "abcdef0123456789", Message: "the change"}
}

func running(name string) github.CheckRun {
	return github.CheckRun{Name: name, Status: "in_progress"}
}

func done(name, verdict string) github.CheckRun {
	return github.CheckRun{Name: name, Status: "completed", Conclusion: conclusion(verdict)}
}

// The point of the tool: it keeps reading while checks are unfinished
// and answers once -- with a verdict, not a snapshot -- rather than
// handing the polling back to the agent a turn at a time.
func TestWaitForChecksBlocksUntilEveryCheckFinishes(t *testing.T) {
	client := &scriptedChecks{
		head: pushed(),
		rounds: [][]github.CheckRun{
			{running("tests"), running("build")},
			{done("tests", "success"), running("build")},
			{done("tests", "success"), done("build", "success")},
		},
	}
	clock := &fakeClock{}

	text, err := waiterFor(client, clock).wait(context.Background(), DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if client.reads != 3 {
		t.Errorf("read CI %d times, want 3 -- one per scripted round", client.reads)
	}
	if len(clock.slept) != 2 {
		t.Errorf("slept %v, want one nap between each pair of reads", clock.slept)
	}
	for _, want := range []string{"Waited 30s", "0 failing", "2 otherwise done", "none of them failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, text)
		}
	}
}

// A failure ends the wait on the spot: everything the agent needs to
// start fixing is already known, and the turns left are better spent
// fixing than watching.
func TestWaitForChecksReturnsAsSoonAsACheckFails(t *testing.T) {
	client := &scriptedChecks{
		head: pushed(),
		rounds: [][]github.CheckRun{
			{running("tests"), running("build")},
			{done("tests", "failure"), running("build")},
			{done("tests", "failure"), done("build", "success")},
		},
	}
	clock := &fakeClock{}

	text, err := waiterFor(client, clock).wait(context.Background(), DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if client.reads != 2 {
		t.Errorf("read CI %d times, want 2 -- the wait should end at the first failure", client.reads)
	}
	for _, want := range []string{"FAILING   tests (failure)", "1 failing", "CI has failed", "still running"} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, text)
		}
	}
}

// ...and it ends with what the failing job printed, not just its name.
// A run told "FAILING tests" and nothing else spends its next turns
// guessing at the failure; a run handed the tail of the job's own log
// starts on the fix. The logs are read once, on the failing path -- a
// wait that fetched them at every fifteen-second poll would spend a
// full-length wait reading a lot of nothing.
func TestWaitForChecksCarriesTheFailingJobsLog(t *testing.T) {
	client := &scriptedChecks{
		head: pushed(),
		rounds: [][]github.CheckRun{
			{running("tests")},
			{running("tests")},
			{done("tests", "failure")},
		},
		jobLogs: []github.JobLog{{
			Name: "tests",
			URL:  "https://github.com/acme/widgets/actions/runs/42/job/7",
			Log:  "2026-01-02T03:04:05.1234567Z --- FAIL: TestThing (0.00s)\n",
		}},
	}

	text, err := waiterFor(client, &fakeClock{}).wait(context.Background(), DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if client.logReads != 1 {
		t.Errorf("read the failing jobs' logs %d times, want once -- on the failure, not per poll",
			client.logReads)
	}
	for _, want := range []string{
		"CI has failed", "--- FAIL: TestThing",
		"https://github.com/acme/widgets/actions/runs/42/job/7",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "2026-01-02T03:04:05") {
		t.Errorf("answer carries Actions' per-line timestamps:\n%s", text)
	}
}

// A green wait reads no logs at all: there is nothing to read, and
// finding that out costs three GitHub calls per poll.
func TestWaitForChecksReadsNoLogsWhileNothingHasFailed(t *testing.T) {
	client := &scriptedChecks{
		head: pushed(),
		rounds: [][]github.CheckRun{
			{running("tests")},
			{done("tests", "success")},
		},
	}
	if _, err := waiterFor(client, &fakeClock{}).wait(context.Background(), DefaultWaitForChecksTimeout); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if client.logReads != 0 {
		t.Errorf("read the failing jobs' logs %d times on a wait that never saw a failure",
			client.logReads)
	}
}

// The three conclusions checkFailed treats as broken end the wait, not
// just the plain "failure" one.
func TestWaitForChecksStopsForEveryFailingConclusion(t *testing.T) {
	for _, verdict := range []string{"failure", "timed_out", "startup_failure"} {
		t.Run(verdict, func(t *testing.T) {
			client := &scriptedChecks{
				head:   pushed(),
				rounds: [][]github.CheckRun{{done("tests", verdict)}},
			}
			text, err := waiterFor(client, &fakeClock{}).wait(context.Background(), DefaultWaitForChecksTimeout)
			if err != nil {
				t.Fatalf("wait: %v", err)
			}
			if !strings.Contains(text, "CI has failed") {
				t.Errorf("a %q check did not end the wait as a failure:\n%s", verdict, text)
			}
		})
	}
}

// A cancelled check ends the wait -- there is nothing left to wait for,
// since a cancelled check reports nothing more however long this blocks
// -- and the report says what the cancellation costs. A run that waited
// for CI, was told "none of them failed", and went home is exactly the
// run the merge queue then holds for hours before waking somebody
// (cancelledHold).
//
// The wait is still recorded as a pass: the verdict words are what a
// deployment's CI loop is measured by (telemetry.go, ReadCheckWait), so
// the marker sentence stays and the news is added under it.
func TestWaitForChecksSaysACancelledCheckHoldsTheMerge(t *testing.T) {
	client := &scriptedChecks{
		head: pushed(),
		rounds: [][]github.CheckRun{
			{done("tests", "cancelled"), running("build")},
			{done("tests", "cancelled"), done("build", "success")},
		},
	}
	clock := &fakeClock{}

	text, err := waiterFor(client, clock).wait(context.Background(), DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	// The cancellation on its own is not a reason to stop waiting: the
	// build was still running, and it could still have gone red.
	if client.reads != 2 {
		t.Errorf("read CI %d times, want 2 -- a cancelled check is not a verdict to stop on", client.reads)
	}
	for _, want := range []string{
		"cancelled tests (cancelled)",
		"0 failing, 0 not finished, 1 cancelled, 1 otherwise done",
		"merge queue does not read a cancellation as a pass",
		"git push origin " + testScope.Branch,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, text)
		}
	}
	if wait, ok := ReadCheckWait(text); !ok || wait.Verdict != WaitVerdictPassed {
		t.Errorf("census reads this wait as %+v (ok=%v), want a recorded pass", wait, ok)
	}
	if client.logReads != 0 {
		t.Errorf("read the failing jobs' logs %d times on a wait that saw no failure", client.logReads)
	}
}

// A wait that runs out of time reports what was still running and says
// plainly that it is not a verdict -- the one reading of a timeout that
// must never be "it was fine".
func TestWaitForChecksReportsATimeoutAsUnfinishedNotAsPassing(t *testing.T) {
	client := &scriptedChecks{
		head:   pushed(),
		rounds: [][]github.CheckRun{{done("build", "success"), running("tests")}},
	}
	clock := &fakeClock{}

	text, err := waiterFor(client, clock).wait(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(text, "Waited 1m0s") {
		t.Errorf("answer does not say it waited the whole timeout:\n%s", text)
	}
	for _, want := range []string{"running   tests", "timed out", "has not passed"} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "none of them failed") {
		t.Errorf("a timeout reads as a pass:\n%s", text)
	}
}

// A timeout does not overshoot: the last nap is trimmed to what is left
// of the clock rather than a whole poll interval past it.
func TestWaitForChecksDoesNotWaitPastItsDeadline(t *testing.T) {
	client := &scriptedChecks{head: pushed(), rounds: [][]github.CheckRun{{running("tests")}}}
	clock := &fakeClock{}

	if _, err := waiterFor(client, clock).wait(context.Background(), 40*time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
	var total time.Duration
	for _, d := range clock.slept {
		total += d
	}
	if total != 40*time.Second {
		t.Errorf("slept %v (%v in total), want exactly the 40s timeout", clock.slept, total)
	}
}

// An empty check list is CI that has not registered yet *or* a repo with
// no CI, and blocking the full timeout to find out would waste the run's
// clock -- so the empty answer gets the grace period and is then
// reported for what it most likely is, without ever reading as a pass.
func TestWaitForChecksGivesUpOnAnEmptyCheckListAfterTheGracePeriod(t *testing.T) {
	client := &scriptedChecks{head: pushed()}
	clock := &fakeClock{}

	text, err := waiterFor(client, clock).wait(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(text, "Waited 3m0s") {
		t.Errorf("answer did not stop at the grace period:\n%s", text)
	}
	for _, want := range []string{"no checks at all", "nobody looked"} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, text)
		}
	}
}

// The grace period bounds an *entirely* empty CI, not a slow one: a
// build that registers immediately and then runs for half an hour is
// waited out to the timeout, not abandoned three minutes in.
func TestWaitForChecksOnlyAppliesTheGracePeriodToAnEmptyCI(t *testing.T) {
	slow := make([][]github.CheckRun, 40)
	for i := range slow {
		slow[i] = []github.CheckRun{running("tests")}
	}
	client := &scriptedChecks{
		head:   pushed(),
		rounds: append(slow, []github.CheckRun{done("tests", "success")}),
	}
	clock := &fakeClock{}

	text, err := waiterFor(client, clock).wait(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(text, "Waited 10m0s") {
		t.Errorf("answer did not wait the slow build out:\n%s", text)
	}
	if strings.Contains(text, "no checks at all") {
		t.Errorf("a slow build was reported as a repo with no CI:\n%s", text)
	}
	if !strings.Contains(text, "none of them failed") {
		t.Errorf("answer is not the green verdict it waited for:\n%s", text)
	}
}

// A branch nobody pushed is answered at once. Blocking fifteen minutes
// before saying "you have not pushed" would be a particularly slow way
// to say something knowable immediately.
func TestWaitForChecksDoesNotWaitOnABranchThatWasNeverPushed(t *testing.T) {
	client := &scriptedChecks{}
	clock := &fakeClock{}

	text, err := waiterFor(client, clock).wait(context.Background(), DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(clock.slept) != 0 {
		t.Errorf("slept %v, want no waiting at all", clock.slept)
	}
	if !strings.Contains(text, "does not exist") || !strings.Contains(text, "git push origin grain/task-9") {
		t.Errorf("answer does not say the branch was never pushed:\n%s", text)
	}
}

// One failed read of CI in the middle of a long wait is a blip and is
// retried; a credential that cannot read CI at all fails every read, and
// that has to surface as an error rather than as a timeout half an hour
// later.
func TestWaitForChecksToleratesABlipButNotAPermanentFailure(t *testing.T) {
	blip := &scriptedChecks{
		head:   pushed(),
		errs:   []error{errors.New("502 Bad Gateway")},
		rounds: [][]github.CheckRun{nil, {done("tests", "success")}},
	}
	text, err := waiterFor(blip, &fakeClock{}).wait(context.Background(), DefaultWaitForChecksTimeout)
	if err != nil {
		t.Fatalf("a single failed read ended the wait: %v", err)
	}
	if !strings.Contains(text, "none of them failed") {
		t.Errorf("answer is not the verdict the retry found:\n%s", text)
	}

	broken := &scriptedChecks{
		head: pushed(),
		errs: []error{errors.New("502"), errors.New("502"), errors.New("502"), errors.New("502")},
	}
	if _, err := waiterFor(broken, &fakeClock{}).wait(context.Background(), time.Hour); err == nil {
		t.Fatal("a CI that could never be read did not end the wait")
	} else if !strings.Contains(err.Error(), "consecutive failed reads") {
		t.Errorf("error does not say why it gave up: %v", err)
	}
	if broken.reads != checkReadAttempts {
		t.Errorf("read CI %d times, want %d before giving up", broken.reads, checkReadAttempts)
	}
}

// Cancelling the run has to reach into the sleep: this is the only place
// a wait spends time, so a wait that ignored ctx would keep a torn-down
// run alive for the rest of its timeout.
func TestWaitForChecksStopsWhenTheRunIsCancelled(t *testing.T) {
	client := &scriptedChecks{head: pushed(), rounds: [][]github.CheckRun{{running("tests")}}}
	clock := &fakeClock{cancel: context.Canceled}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waiterFor(client, clock).wait(ctx, DefaultWaitForChecksTimeout)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait returned %v, want it to carry the cancellation", err)
	}
}

// The scope is fixed at construction, exactly as pull_request_status's
// is: no argument moves the wait onto another repo.
func TestWaitForChecksOnlyEverReadsItsOwnScope(t *testing.T) {
	client := &scriptedChecks{head: pushed(), rounds: [][]github.CheckRun{{done("tests", "success")}}}
	tool := namedTool(t, NewPullRequestTools(client, nil, testScope), "wait_for_checks")

	res := tool.Handler(context.Background(), map[string]any{
		"owner": "attacker", "repo": "secrets", "branch": "main", "timeout_seconds": 30,
	})
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, repo := range client.repos {
		if repo != "acme/widgets" {
			t.Errorf("read %q, want only acme/widgets", repo)
		}
	}
	if len(client.repos) == 0 {
		t.Error("the tool read nothing at all, so this proves nothing")
	}
}

// An unconfigured run gets the same explanation pull_request_status
// gives, rather than a tool that is simply missing.
func TestWaitForChecksWithoutAScopeSaysSo(t *testing.T) {
	for name, tc := range map[string]struct {
		client PullRequestReader
		scope  PullRequestScope
	}{
		"no client": {nil, testScope},
		"no scope":  {&scriptedChecks{}, PullRequestScope{}},
	} {
		t.Run(name, func(t *testing.T) {
			tool := namedTool(t, NewWaitForChecksTools(tc.client, nil, tc.scope), "wait_for_checks")
			res := tool.Handler(context.Background(), map[string]any{})
			if !res.IsError {
				t.Errorf("IsError = false, want an error result: %s", res.Text)
			}
			if !strings.Contains(res.Text, "no GitHub repository configured") {
				t.Errorf("answer does not explain why there is nothing to wait for:\n%s", res.Text)
			}
		})
	}
}

// timeout_seconds is clamped rather than refused, and the clamp is said
// out loud so a wait that came back early is never mistaken for CI that
// finished early.
func TestWaitForChecksClampsTheTimeoutAndSaysSo(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want time.Duration
		note string
	}{
		"unset":     {map[string]any{}, DefaultWaitForChecksTimeout, ""},
		"in range":  {map[string]any{"timeout_seconds": 120.0}, 2 * time.Minute, ""},
		"too long":  {map[string]any{"timeout_seconds": 7200.0}, MaxWaitForChecksTimeout, "longest wait allowed"},
		"too short": {map[string]any{"timeout_seconds": 1.0}, minWaitForChecksTimeout, "shortest wait allowed"},
		"not a number": {map[string]any{"timeout_seconds": "ten minutes"},
			DefaultWaitForChecksTimeout, "was not a number"},
	} {
		t.Run(name, func(t *testing.T) {
			got := waitForChecksTimeout(tc.args)
			if got.timeout != tc.want {
				t.Errorf("waitForChecksTimeout = %v, want %v", got.timeout, tc.want)
			}
			if tc.note == "" && got.note != "" {
				t.Errorf("note = %q, want none", got.note)
			}
			if tc.note != "" && !strings.Contains(got.note, tc.note) {
				t.Errorf("note = %q, want it to mention %q", got.note, tc.note)
			}
		})
	}
}

// The description has to name the failure mode that makes the tool
// useless if an agent gets it wrong: it reports on the commit already
// pushed, so calling it before pushing waits for the wrong build.
func TestWaitForChecksDescriptionSaysToPushFirst(t *testing.T) {
	tool := namedTool(t, NewWaitForChecksTools(nil, nil, PullRequestScope{}), "wait_for_checks")
	if !strings.Contains(tool.Description, "Push first") {
		t.Errorf("description does not tell the agent to push first:\n%s", tool.Description)
	}
}

// runWithLeft is a ctx for a run the given distance from the wall,
// carrying its deadline the way a real call gets it: put there by the
// registry that was told about it, rather than by a value made up here.
func runWithLeft(left time.Duration) context.Context {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	registry := NewRegistry()
	registry.AnnounceDeadline(now.Add(left), func() time.Time { return now })
	return registry.withRunDeadline(context.Background())
}

// The wait is bounded by the run as well as by the argument: a run with
// ten minutes left that asks for fifteen would otherwise spend the whole
// of its remaining life inside this one call, be cancelled mid-wait, and
// never see the verdict it blocked for.
func TestWaitForChecksClampsTheWaitToWhatTheRunHasLeft(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx     context.Context
		args    map[string]any
		want    time.Duration
		clamped bool
		tooLate bool
		note    string
	}{
		// "nobody told this server about a deadline" is not "no time
		// left": those runs wait exactly as they always did.
		"no deadline announced": {
			ctx: context.Background(), want: DefaultWaitForChecksTimeout},
		"time to spare": {
			ctx: runWithLeft(90 * time.Minute), want: DefaultWaitForChecksTimeout},
		"less of the run left than the wait asked for": {
			ctx: runWithLeft(10 * time.Minute), want: 8 * time.Minute, clamped: true,
			note: "not the 15m0s asked for"},
		"a wait that already fits is left alone": {
			ctx:  runWithLeft(10 * time.Minute),
			args: map[string]any{"timeout_seconds": 60.0}, want: time.Minute},
		"exactly enough for the shortest wait there is": {
			ctx:  runWithLeft(waitForChecksDeadlineSlack + minWaitForChecksTimeout),
			want: minWaitForChecksTimeout, clamped: true},
		"not even that": {
			ctx:     runWithLeft(waitForChecksDeadlineSlack + minWaitForChecksTimeout - time.Second),
			tooLate: true},
		"past the deadline already": {
			ctx: runWithLeft(-time.Minute), tooLate: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := waitForChecksBudget(tc.ctx, tc.args)
			if got.tooLate != tc.tooLate {
				t.Fatalf("tooLate = %v, want %v", got.tooLate, tc.tooLate)
			}
			if got.tooLate {
				return
			}
			if got.timeout != tc.want {
				t.Errorf("timeout = %v, want %v", got.timeout, tc.want)
			}
			if got.deadlineClamped != tc.clamped {
				t.Errorf("deadlineClamped = %v, want %v", got.deadlineClamped, tc.clamped)
			}
			// A clamp the run cannot see is a clamp it reads as CI being
			// slow, and answers by asking for a longer wait it has even
			// less room for.
			switch {
			case tc.clamped && !strings.Contains(got.note, "before grain cancels it"):
				t.Errorf("note = %q, want it to say the run's own clock is what bounded the wait",
					got.note)
			case !tc.clamped && got.note != "":
				t.Errorf("note = %q, want none for a wait that was not clamped", got.note)
			}
			if tc.note != "" && !strings.Contains(got.note, tc.note) {
				t.Errorf("note = %q, want it to mention %q", got.note, tc.note)
			}
		})
	}
}

// With less than a minimum wait's worth of run left there is nothing
// worth waiting for: the call answers on the spot, saying so and saying
// what to do with the turn instead, rather than spending it watching a
// build whose verdict this run will never read.
func TestWaitForChecksDoesNotWaitWhenTheRunIsAlmostOver(t *testing.T) {
	client := &scriptedChecks{head: pushed(), rounds: [][]github.CheckRun{{running("tests")}}}
	tool := namedTool(t, NewWaitForChecksTools(client, nil, testScope), "wait_for_checks")

	res := tool.Handler(runWithLeft(2*time.Minute), map[string]any{})
	if res.IsError {
		t.Errorf("IsError = true, want an answer: there being no time is a fact about the "+
			"run, not a failed call:\n%s", res.Text)
	}
	if client.reads != 0 {
		t.Errorf("read GitHub %d times for a wait it did not run", client.reads)
	}
	for _, want := range []string{"no time to wait on CI", "2m is left", "git push origin grain/task-9"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, res.Text)
		}
	}

	past := tool.Handler(runWithLeft(-time.Minute), map[string]any{})
	if !strings.Contains(past.Text, "already past the deadline") {
		t.Errorf("a run past its deadline is not told so:\n%s", past.Text)
	}
}

// A wait cut short by the run's own clock must not tell the run to call
// again with a bigger timeout: the thing that ran out is not the
// timeout, and there is no more of it to ask for.
func TestATimedOutClampedWaitDoesNotSayToWaitLonger(t *testing.T) {
	client := &scriptedChecks{head: pushed(), rounds: [][]github.CheckRun{{running("tests")}}}
	waiter := waiterFor(client, &fakeClock{})
	waiter.deadlineClamped = true

	text, err := waiter.wait(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if strings.Contains(text, "timeout_seconds") {
		t.Errorf("answer tells the run to wait longer than the run has:\n%s", text)
	}
	for _, want := range []string{"timed out", "this run's own clock", "pushed to grain/task-9"} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not contain %q:\n%s", want, text)
		}
	}
}

// The description says the wait is bounded by the run as well, since a
// bound an agent does not know about is one it will argue with by asking
// for a longer wait.
func TestWaitForChecksDescriptionSaysTheRunsClockBoundsItToo(t *testing.T) {
	tool := namedTool(t, NewWaitForChecksTools(nil, nil, PullRequestScope{}), "wait_for_checks")
	if !strings.Contains(tool.Description, "before grain cancels it") {
		t.Errorf("description does not mention the run's own deadline:\n%s", tool.Description)
	}
}
