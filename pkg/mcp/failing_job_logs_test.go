package mcp

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

// Every test in this package that renders a failing check goes through
// the retry in readJobLogs, and the real schedule is tens of seconds
// (jobLogRetryDelays). Shrunk once for the whole package rather than per
// test: what is worth pinning is how many times the log is asked for and
// what the answer says, never how long the waiting took.
func init() {
	jobLogRetryDelays = []time.Duration{
		time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond,
	}
}

// The bug this whole path exists for. wait_for_checks returns the moment
// a check run reports completed/failure, and at that moment the workflow
// run behind it is often still in progress -- so FailedJobLogs, which
// walks *completed failed* runs, finds nothing and the log is not
// downloadable either. Measured over one deployment's 185 runs, that was
// what every "no readable log" report actually was: the same check in
// the same repo went both ways, 39 answers with a log and 38 without,
// and the log turned up later in the same session in 15 of the 29
// affected runs. So the fetch asks again before it accepts the silence.
func TestFailingJobLogsAsksAgainForALogThatIsNotServedYet(t *testing.T) {
	client := &fakePullRequests{
		head:   &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks: []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("failure")}},
		jobLogRounds: [][]github.JobLog{
			nil,
			nil,
			{{Name: "go", URL: "https://github.test/o/r/runs/1", Log: "FAIL\tpkg/thing\n"}},
		},
	}

	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if !strings.Contains(res.Text, "FAIL\tpkg/thing") {
		t.Errorf("answer does not carry the log the third read produced:\n%s", res.Text)
	}
	if client.logReads != 3 {
		t.Errorf("read the logs %d times, want 3 -- it should keep asking until one arrives", client.logReads)
	}
	// The whole point of retrying is not to reach the "no log" answer,
	// so none of its machinery should have run either.
	if client.actionReads != 0 {
		t.Errorf("read the Actions state %d times for a failure that did produce a log", client.actionReads)
	}
}

// TestFailingJobLogsShareOneAnswerBetweenThem: four failing jobs at
// github.JobLogTailBytes each is four times what a tool answer carries,
// and a report cut blind at the end loses the last job's log and the
// sentence that introduced it. Every job's tail stays visible instead,
// and the cut says where the whole of it is.
func TestFailingJobLogsShareOneAnswerBetweenThem(t *testing.T) {
	logs := make([]github.JobLog, 0, 4)
	for _, name := range []string{"go", "ui", "installer", "e2e"} {
		logs = append(logs, github.JobLog{
			Name: name, URL: "https://github.test/o/r/jobs/" + name,
			// Long lines: github.JobLogExcerpt keeps the last 80 of them
			// whatever they weigh, which is the case its own doc comment
			// names -- "a log of very long lines fits the byte cap and
			// still buries the failure".
			Log: strings.Repeat(strings.Repeat("a line of somebody else's build, ", 8)+"\n", 500) +
				"FAIL\t" + name + "\n",
		})
	}
	checks := make([]github.CheckRun, 0, len(logs))
	for _, l := range logs {
		checks = append(checks, github.CheckRun{Name: l.Name, Status: "completed", Conclusion: conclusion("failure")})
	}
	client := &fakePullRequests{
		head:         &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks:       checks,
		jobLogRounds: [][]github.JobLog{logs},
	}

	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if len(res.Text) > maxResultTextBytes() {
		t.Errorf("report is %d bytes, more than one answer carries", len(res.Text))
	}
	// Each job's own verdict is the last line of its log, and it is the
	// reason the whole block is quoted.
	for _, l := range logs {
		if !strings.Contains(res.Text, "FAIL\t"+l.Name) {
			t.Errorf("job %s lost the end of its log:\n%s", l.Name, res.Text)
		}
	}
	if !strings.Contains(res.Text, "elided here by grain") {
		t.Errorf("four full logs came through uncut, which no answer has room for")
	}
	if !strings.Contains(res.Text, "reproduce the failure in your checkout") {
		t.Errorf("the cut does not say what a sandbox can do about it:\n%s", res.Text)
	}
}

// A log that never arrives is not an excuse to guess at why. The report
// says which of the possible causes it was -- here, a run still in
// progress -- and hands over the job and the failed step, which the jobs
// endpoint gives up without any log read at all.
func TestFailingJobLogsSaysTheRunIsStillGoingWhenNoLogArrives(t *testing.T) {
	client := &fakePullRequests{
		head:   &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks: []github.CheckRun{{Name: "ui e2e", Status: "completed", Conclusion: conclusion("failure")}},
		actions: github.ActionsState{
			Runs:       1,
			Unfinished: 1,
			Jobs: []github.ActionsJob{{
				Name:        "ui e2e",
				RunName:     "CI",
				URL:         "https://github.test/o/r/runs/1/job/2",
				FailedSteps: []string{"Test e2e"},
			}},
		},
	}

	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, want := range []string{
		"Test e2e",
		"https://github.test/o/r/runs/1/job/2",
		"still running",
		"Call this again",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not carry %q:\n%s", want, res.Text)
		}
	}
	// The three stories the old sentence offered, none of which was
	// what happened here.
	for _, wrong := range []string{
		"expired",
		"90-day retention",
		"posted through the Checks API by something else",
		"Reproduce the failure in your checkout",
	} {
		if strings.Contains(res.Text, wrong) {
			t.Errorf("answer still offers %q for a run that is merely unfinished:\n%s", wrong, res.Text)
		}
	}
	if client.logReads != len(jobLogRetryDelays)+1 {
		t.Errorf("read the logs %d times, want %d", client.logReads, len(jobLogRetryDelays)+1)
	}
}

// The one case where "this is not GitHub Actions" is the truth rather
// than a guess: Actions has no run against the commit at all. The
// failing check's own page goes with it, since it is the only place its
// output exists.
func TestFailingJobLogsNamesTheProviderOnlyWhenActionsHasNoRun(t *testing.T) {
	res := call(t, &fakePullRequests{
		head: &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks: []github.CheckRun{{
			Name: "buildkite/tests", Status: "completed", Conclusion: conclusion("failure"),
			HTMLURL: "https://buildkite.test/builds/7",
		}},
		actions: github.ActionsState{},
	}, testScope)

	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	for _, want := range []string{
		"no workflow run against this commit",
		"buildkite/tests: https://buildkite.test/builds/7",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not carry %q:\n%s", want, res.Text)
		}
	}
	// 10 runs of one deployment reached for `gh` after a message like
	// this one and found it was not installed. The page is for whoever
	// reads the report, and the answer says so rather than sending a
	// sandbox somewhere it cannot go.
	if !strings.Contains(res.Text, "no `gh` and no GitHub credential") {
		t.Errorf("answer sends the run off to fetch that page itself:\n%s", res.Text)
	}
}

// The other half of that sentence, which nothing else in this package can
// reach: it is true because of what two build files do not install, and
// it stops being true the moment one of them does.
//
// So this reads them. "The sandbox has no `gh`" is a decision
// (docs/design.md, "The sandbox image has no `gh`") rather than an
// accident, and the way a decision like that rots is a package added to a
// list for a good local reason by somebody who never saw the sentence it
// falsifies -- leaving every failing-check answer telling agents they
// cannot do something they now can. Adding `gh` is allowed; adding it
// without changing this answer, the dispatch prompt every run reads
// (orchestrator.sandboxToolingSection) and the github-sandbox
// capability's prompt in the same commit is what is not.
func TestTheSandboxImageStillCarriesNoGH(t *testing.T) {
	// Comment lines are skipped, because the Granulefile discusses `gh`
	// by name and it is installing it that this cares about.
	installsGH := regexp.MustCompile(`\bgh\b|github-cli|cli\.github\.com`)
	for _, path := range []string{
		"../../Granulefile",
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if installsGH.MatchString(line) {
				t.Errorf("%s:%d looks like it installs the GitHub CLI:\n\t%s\n\n"+
					"If that is deliberate, it is a change to make in the same commit: "+
					"failingCheckPages (failing_job_logs.go) tells every run with a "+
					"non-Actions failure that \"the sandbox has no `gh` and no GitHub "+
					"credential\", orchestrator.sandboxToolingSection tells every "+
					"dispatched run the same thing in its opening prompt, "+
					"githubsandbox.Provider.PromptSection drives its scratch "+
					"repo with git and curl for the same reason, and docs/design.md's "+
					"\"The sandbox image has no `gh`\" is the argument all four rest on.",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Every Actions run finished, none of them with a failed job, is the
// other honest "not Actions" -- and it is a different sentence from
// "Actions has nothing here", because it says the runs were looked at.
func TestFailingJobLogsSaysWhenEveryActionsRunFinishedClean(t *testing.T) {
	res := call(t, &fakePullRequests{
		head:    &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks:  []github.CheckRun{{Name: "codecov/patch", Status: "completed", Conclusion: conclusion("failure")}},
		actions: github.ActionsState{Runs: 2},
	}, testScope)

	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if !strings.Contains(res.Text, "2 workflow runs") ||
		!strings.Contains(res.Text, "no failed job among them") {
		t.Errorf("answer does not say what Actions was asked and what it said:\n%s", res.Text)
	}
}

// A run that failed before any job of it started has no job log coming,
// ever -- so waiting for one would spend the retry budget learning
// nothing. github.OnlyStartupFailures' own reasoning, applied to the
// question "is this silence temporary".
func TestFailingJobLogsDoesNotWaitOutAStartupFailure(t *testing.T) {
	client := &fakePullRequests{
		head:   &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks: []github.CheckRun{{Name: "grain can load this", Status: "completed", Conclusion: conclusion("failure")}},
		jobLogs: []github.JobLog{{
			Name:          "grain state check",
			URL:           "https://github.test/o/r/actions/runs/42",
			RunConclusion: github.StartupFailure,
		}},
	}

	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if client.logReads != 1 {
		t.Errorf("read the logs %d times for a run that never started a job, want 1", client.logReads)
	}
}

// And the shape that sits between those two: failing jobs under the run,
// which is the "wait, the log is on its way" signal, but jobs GitHub
// created and never started, which have no log on the way at all. The
// retry budget must not be spent on it -- and it would be, every single
// time, since a repository out of Actions minutes fails this way on every
// commit.
func TestFailingJobLogsDoesNotWaitOutAJobThatNeverStarted(t *testing.T) {
	client := &fakePullRequests{
		head:   &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks: []github.CheckRun{{Name: "grain can load this", Status: "completed", Conclusion: conclusion("failure")}},
		jobLogs: []github.JobLog{{
			Name:                "grain state check",
			URL:                 "https://github.test/o/r/actions/runs/42",
			RunConclusion:       "failure",
			RunJobsFailed:       1,
			RunJobsNeverStarted: true,
			RunFailureReason: "The job was not started because recent account payments " +
				"have failed or your spending limit needs to be increased",
		}},
	}

	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if client.logReads != 1 {
		t.Errorf("read the logs %d times for a job that never ran a step, want 1", client.logReads)
	}
	if !strings.Contains(res.Text, "recent account payments have failed") {
		t.Errorf("answer does not carry GitHub's own reason:\n%s", res.Text)
	}
}

// A run that concluded with failed jobs and served no log for any of
// them is the same race one step later, so it is waited out -- and when
// the wait does not help, the jobs and their failed steps are added
// under the run's own verdict, which names only the workflow.
func TestFailingJobLogsAddsTheFailedStepsToARunWithNoServedLog(t *testing.T) {
	client := &fakePullRequests{
		head:   &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks: []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("failure")}},
		jobLogs: []github.JobLog{{
			Name:          "CI",
			URL:           "https://github.test/o/r/actions/runs/42",
			RunConclusion: "failure",
			RunJobsFailed: 1,
		}},
		actions: github.ActionsState{
			Runs: 1,
			Jobs: []github.ActionsJob{{
				Name: "go", RunName: "CI", RunFinished: true,
				URL:         "https://github.test/o/r/runs/42/job/9",
				FailedSteps: []string{"go test ./..."},
			}},
		},
	}

	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if client.logReads != len(jobLogRetryDelays)+1 {
		t.Errorf("read the logs %d times, want %d", client.logReads, len(jobLogRetryDelays)+1)
	}
	if !strings.Contains(res.Text, "go test ./...") {
		t.Errorf("answer does not name the step that failed:\n%s", res.Text)
	}
}

func TestFailingJobLogsRendersStructuredFailureSummary(t *testing.T) {
	logContent := `
=== RUN   TestLogin
    auth_test.go:45: status code mismatch: want 200 got 401
--- FAIL: TestLogin (0.02s)
FAIL
`
	client := &fakePullRequests{
		head:   &github.BranchHead{SHA: "0123456789abcdef", Message: "fix: auth"},
		checks: []github.CheckRun{{Name: "backend-tests", Status: "completed", Conclusion: conclusion("failure")}},
		jobLogs: []github.JobLog{{
			Name: "backend-tests",
			URL:  "https://github.test/o/r/actions/runs/42/job/10",
			Log:  logContent,
		}},
	}

	res := call(t, client, testScope)
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Failure summary:") {
		t.Errorf("expected failure summary in report:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "TestLogin") || !strings.Contains(res.Text, "auth_test.go:45") {
		t.Errorf("expected test failure details in report:\n%s", res.Text)
	}
}
