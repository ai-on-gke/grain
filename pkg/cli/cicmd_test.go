package cli

// `grain ci`'s own rendering and dispatch (grain/task-247). The wire
// calls behind it are pkg/ui's (ci_test.go's HTTPClient round trip);
// what is only decidable here is what an operator at a terminal reads --
// in particular that a red run leads with the job that failed and prints
// what that step printed, and that a run still going says so rather than
// leaving a blank where its conclusion will be.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	cicli "github.com/ai-on-gke/grain/pkg/ci/cli"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// instant is a time as a ci.Run carries one: its timestamps are
// pointers, so that a job with no start has no field rather than an
// instant in the year 1.
func instant(t time.Time) *time.Time { return &t }

// failedRun is one finished run with one failing job in it, which is the
// case every reader of this command is here for.
func failedRun() ci.Run {
	started := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	return ci.Run{
		ID: "9f2c1a", Repo: "acme/widgets", Ref: "grain/task-1",
		Commit: "0123456789abcdef0123", Event: ci.Event("push"), RequestedBy: "alice",
		StartedAt: &started, FinishedAt: instant(started.Add(83 * time.Second)),
		Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{
			{
				Workflow: "tests", WorkflowPath: "tests.yml", JobID: "lint", Name: "lint",
				Conclusion: ci.ConclusionSuccess, Sandbox: "ci-9f2c1a-tests-lint",
				StartedAt: &started, FinishedAt: instant(started.Add(10 * time.Second)),
				Steps: []ci.StepRun{{
					Name: "golangci-lint", Conclusion: ci.ConclusionSuccess, Output: "0 issues",
				}},
			},
			{
				Workflow: "tests", WorkflowPath: "tests.yml", JobID: "test", Name: "go test",
				Conclusion: ci.ConclusionFailure, Sandbox: "ci-9f2c1a-tests-test",
				StartedAt: &started, FinishedAt: instant(started.Add(80 * time.Second)),
				Steps: []ci.StepRun{
					{Name: "build", Conclusion: ci.ConclusionSuccess, Output: "ok"},
					{Name: "go test ./...", Conclusion: ci.ConclusionFailure, ExitCode: 1,
						Output: "--- FAIL: TestThing\n"},
					{Name: "upload", Conclusion: ci.ConclusionSkipped, ExitCode: -1},
				},
			},
		},
	}
}

func TestPrintCIRunLeadsWithTheFailure(t *testing.T) {
	out := captureStdout(t, func() { (&printer{}).ciRun(failedRun(), cicli.OutputFailed) })

	for _, want := range []string{
		"run 9f2c1a", "failure", "acme/widgets", "grain/task-1 (0123456789ab)",
		"asked by:  alice", "job go test", "exit 1", "--- FAIL: TestThing",
		"ci-9f2c1a-tests-test",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	// Failures first: the job that failed is what the reader came for,
	// and a green job scrolling past first on a long run buries it.
	if strings.Index(out, "job go test") > strings.Index(out, "job lint") {
		t.Errorf("the failing job should come first:\n%s", out)
	}
	// A successful step's output is not what somebody opening a red run
	// asked for -- but they are told it is there.
	if strings.Contains(out, "0 issues") {
		t.Errorf("a green step's output is printed by default:\n%s", out)
	}
	if !strings.Contains(out, "-output all") {
		t.Errorf("output left out should say how to ask for it:\n%s", out)
	}
}

func TestPrintCIRunOutputModes(t *testing.T) {
	all := captureStdout(t, func() { (&printer{}).ciRun(failedRun(), cicli.OutputAll) })
	if !strings.Contains(all, "0 issues") || !strings.Contains(all, "--- FAIL: TestThing") {
		t.Errorf("-output all prints every step's output:\n%s", all)
	}
	none := captureStdout(t, func() { (&printer{}).ciRun(failedRun(), cicli.OutputNone) })
	if strings.Contains(none, "FAIL: TestThing") || strings.Contains(none, "0 issues") {
		t.Errorf("-output none prints no output at all:\n%s", none)
	}
	if !strings.Contains(none, "job go test") {
		t.Errorf("-output none still prints the jobs and steps:\n%s", none)
	}
}

// A run that has not finished has no conclusion, and a blank where a
// verdict goes reads as a run that ended with nothing to say. It is
// readable while it is going -- that is the whole point of the engine
// reporting each job as it finishes -- so every unfinished thing in it
// says "running".
func TestPrintCIRunStillGoing(t *testing.T) {
	run := ci.Run{
		ID: "abc", Repo: "acme/widgets", Ref: "main", StartedAt: instant(time.Now().Add(-30 * time.Second)),
		Jobs: []ci.JobRun{
			{Name: "lint", Conclusion: ci.ConclusionSuccess, StartedAt: instant(time.Now().Add(-30 * time.Second)),
				FinishedAt: instant(time.Now().Add(-20 * time.Second))},
			{Name: "test", StartedAt: instant(time.Now().Add(-20 * time.Second))},
		},
	}
	out := captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) })
	if !strings.Contains(out, "running") {
		t.Errorf("an unfinished run should say it is still going:\n%s", out)
	}

	listing := captureStdout(t, func() {
		(&printer{}).ciRuns(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{run}}, ci.RunFilter{})
	})
	if !strings.Contains(listing, "running") || !strings.Contains(listing, "2 jobs") {
		t.Errorf("the listing should say how far a live run has got:\n%s", listing)
	}
}

// A job that has never been given a sandbox is not running: it is
// waiting, exactly as the journal says while `ci show` and `ci list`
// both used to call it "running -" (grain/task-41 -- most of why a job
// stuck for an hour and three quarters behind another sandbox read as a
// hang rather than a queue).
func TestPrintCIJobNeverStartedSaysWaitingNotRunning(t *testing.T) {
	run := ci.Run{
		ID: "abc", Repo: "acme/widgets", Ref: "main", StartedAt: instant(time.Now().Add(-30 * time.Second)),
		Jobs: []ci.JobRun{
			{Name: "lint", Conclusion: ci.ConclusionSuccess, StartedAt: instant(time.Now().Add(-30 * time.Second)),
				FinishedAt: instant(time.Now().Add(-20 * time.Second))},
			{Name: "go", StartedAt: instant(time.Now().Add(-20 * time.Second))},
			// Never got a machine: refused by the gate so far, not run.
			{Name: "deploy"},
		},
	}

	out := captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) })
	if !strings.Contains(out, "job deploy  waiting") {
		t.Errorf("a job holding no sandbox is not running:\n%s", out)
	}
	if strings.Contains(out, "job deploy  running") {
		t.Errorf("a job that never started must not read as running:\n%s", out)
	}
	if !strings.Contains(out, "job go  running") {
		t.Errorf("a job that did get a sandbox still reads as running:\n%s", out)
	}

	listing := captureStdout(t, func() {
		(&printer{}).ciRuns(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{run}}, ci.RunFilter{})
	})
	if !strings.Contains(listing, "1 waiting") {
		t.Errorf("the listing should count the never-started job as waiting, not running:\n%s", listing)
	}
	if !strings.Contains(listing, "1 running") {
		t.Errorf("the listing should still count the job that did get a sandbox as running:\n%s", listing)
	}
}

// The question somebody looking at a cancelled run is actually asking is
// whether anything is going to run this commit again -- the merge queue
// replaces any cancellation, a person's included -- and who stopped it.
// The note left with one cannot say: it is optional, and a cancellation
// with nothing written on it is what both a timeout and an unexplained
// cancel by hand look like.
func TestPrintCIRunSaysWhoStoppedIt(t *testing.T) {
	run := ci.Run{
		ID: "abc", Repo: "acme/widgets", Ref: "grain/task-264",
		StartedAt:  instant(time.Now().Add(-36 * time.Minute)),
		Conclusion: ci.ConclusionCancelled,
		Jobs:       []ci.JobRun{{Name: "image", Conclusion: ci.ConclusionCancelled}},
	}
	out := captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) })
	if !strings.Contains(out, "grain itself") || !strings.Contains(out, "starts another") {
		t.Errorf("a cancellation nobody claimed should say it is grain's own and is replaced:\n%s", out)
	}

	run.CancelledBy = ci.CancelByOperator
	out = captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) })
	if !strings.Contains(out, "a person") || !strings.Contains(out, "starts another") {
		t.Errorf("a person's cancellation should say it is a person's and is replaced:\n%s", out)
	}
}

// The detail view says which build of grain produced the run, and says
// it in full: a reader with one run in front of them is about to ask git
// whether that build predates the commit they are reading, and git takes
// the whole hash (ci.Build).
func TestPrintCIRunSaysWhichBuildProducedIt(t *testing.T) {
	committed := time.Date(2026, 9, 18, 11, 22, 33, 0, time.UTC)
	run := ci.Run{
		ID: "abc", Repo: "acme/widgets", Ref: "main", StartedAt: instant(time.Now()),
		Conclusion: ci.ConclusionErrored,
		Error:      "no workflow matched",
		Build:      &ci.Build{Revision: "acc97d35b0f1e2d3c4b5a69788776655443322110", CommittedAt: &committed},
	}
	out := captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) })
	if !strings.Contains(out, "acc97d35b0f1e2d3c4b5a69788776655443322110") {
		t.Errorf("the run should name the build in full:\n%s", out)
	}
	if !strings.Contains(out, "2026-09-18T11:22:33Z") {
		t.Errorf("the run should say when that build's commit was made:\n%s", out)
	}

	// A run no build stamped -- one recorded before grain wrote this
	// down -- prints no line at all rather than an empty one.
	run.Build = nil
	if out := captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) }); strings.Contains(out, "build:") {
		t.Errorf("an unstamped run printed a build line anyway:\n%s", out)
	}
}

// The step that is running is the only place anything is being written,
// so it is shown by default as a failing one is: grain CI publishes a
// step's output while it prints it, and a reader watching a long job
// would otherwise be shown nothing until it ended.
func TestPrintCIRunShowsARunningStepsOutput(t *testing.T) {
	started := time.Now().Add(-30 * time.Second)
	run := ci.Run{
		ID: "abc", Repo: "acme/widgets", Ref: "main", StartedAt: &started,
		Jobs: []ci.JobRun{{
			Name: "test", StartedAt: &started,
			Steps: []ci.StepRun{
				{Name: "build", Conclusion: ci.ConclusionSuccess, Output: "built it"},
				{Name: "go test ./...", StartedAt: &started, ExitCode: -1, Output: "ok pkg/ci 2.3s\n"},
			},
		}},
	}
	out := captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) })
	if !strings.Contains(out, "ok pkg/ci 2.3s") {
		t.Errorf("a running step's output so far should be shown:\n%s", out)
	}
	if strings.Contains(out, "built it") {
		t.Errorf("a finished green step's output is still not shown by default:\n%s", out)
	}
	if !strings.Contains(out, "-output all") {
		t.Errorf("output left out should still say how to ask for it:\n%s", out)
	}
}

func TestPrintCIRunsListing(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).ciRuns(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}}, ci.RunFilter{})
	})
	for _, want := range []string{"9f2c1a", "failure", "acme/widgets", "grain/task-1", "push", "1m23s", "1 failure"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing does not mention %q:\n%s", want, out)
		}
	}

	// The two empties are different answers and must not read alike: a
	// deployment that runs no CI at all, and one that has simply not run
	// anything yet.
	unwired := captureStdout(t, func() { (&printer{}).ciRuns(ui.CIRunsResponse{}, ci.RunFilter{}) })
	if !strings.Contains(unwired, "-ci=false") {
		t.Errorf("a deployment with no CI wired should say so:\n%s", unwired)
	}
	empty := captureStdout(t, func() { (&printer{}).ciRuns(ui.CIRunsResponse{Enabled: true}, ci.RunFilter{}) })
	if !strings.Contains(empty, "grain ci run") {
		t.Errorf("a deployment that has run nothing should say how to start one:\n%s", empty)
	}
}

// -json prints the route's own body, so a listing piped into jq and one
// fetched with curl are the same object -- the enabled flag included,
// which is the part a script has to branch on.
func TestPrintCIRunsJSON(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{json: true}).ciRuns(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}}, ci.RunFilter{})
	})
	var got ui.CIRunsResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if !got.Enabled || len(got.Runs) != 1 || got.Runs[0].ID != "9f2c1a" {
		t.Fatalf("got %+v", got)
	}
	if len(got.Runs[0].Jobs) != 2 {
		t.Errorf("the jobs travel with the run: %+v", got.Runs[0].Jobs)
	}
}

// ciCLI runs one `grain ci ...` invocation against a fake daemon and
// returns what an operator would have read.
func ciCLI(t *testing.T, handler http.HandlerFunc, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	var err error
	out := captureStdout(t, func() {
		err = cmdCI(context.Background(), ui.NewHTTPClient(srv.URL), &printer{}, args)
	})
	return out, err
}

func TestCICommandDispatch(t *testing.T) {
	var seen []string
	handler := func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(ci.Run{ID: "run-1", Repo: "acme/widgets", Ref: "main",
				Jobs: []ci.JobRun{{Name: "test", Workflow: "tests"}}})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(ci.Run{ID: "run-1"})
		case strings.HasPrefix(r.URL.Path, "/api/ci/runs/"):
			json.NewEncoder(w).Encode(failedRun())
		default:
			json.NewEncoder(w).Encode(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}})
		}
	}

	// A bare "grain ci" lists, and a leading flag still belongs to the
	// listing rather than being read as a subcommand.
	if out, err := ciCLI(t, handler); err != nil || !strings.Contains(out, "9f2c1a") {
		t.Fatalf("bare listing: %q, %v", out, err)
	}
	if _, err := ciCLI(t, handler, "-repo", "acme/widgets"); err != nil {
		t.Fatalf("listing with a flag and no subcommand: %v", err)
	}
	// Both the repo and the limit reach the daemon, which asks its run
	// store: the filter is answered over every run this deployment keeps
	// rather than over the page a listing would otherwise have sent
	// (grain/task-289), and -limit is how much of the answer crosses the
	// wire rather than how much of it is printed.
	if got := seen[len(seen)-1]; got != "GET /api/ci/runs?limit=20&repo=acme%2Fwidgets" {
		t.Errorf("the repo filter reached the daemon as %q", got)
	}

	if out, err := ciCLI(t, handler, "show", "9f2c1a"); err != nil || !strings.Contains(out, "FAIL: TestThing") {
		t.Fatalf("show: %q, %v", out, err)
	}
	if out, err := ciCLI(t, handler, "run", "-repo", "acme/widgets", "-branch", "main"); err != nil {
		t.Fatalf("run: %q, %v", out, err)
	} else if !strings.Contains(out, "grain ci watch run-1") {
		t.Errorf("starting a run should say how to follow it:\n%s", out)
	}
	if out, err := ciCLI(t, handler, "cancel", "run-1"); err != nil || !strings.Contains(out, "run-1") {
		t.Fatalf("cancel: %q, %v", out, err)
	}

	// Every argument that cannot mean anything is refused by the command
	// the operator ran rather than by a route relaying a 400 back.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"frobnicate"}, "unknown"},
		{[]string{"show"}, "run id is required"},
		{[]string{"cancel"}, "run id is required"},
		{[]string{"run", "-branch", "main"}, "-repo is required"},
		{[]string{"run", "-repo", "acme/widgets"}, "-branch is required"},
		{[]string{"show", "-output", "some", "9f2c1a"}, "-output"},
	} {
		_, err := ciCLI(t, handler, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("grain ci %v: err = %v, want one mentioning %q", tc.args, err, tc.want)
		}
	}
}

func TestCIRunRecheckFlags(t *testing.T) {
	for _, flag := range []string{"-recheck", "-no-cache"} {
		var req ui.CIRunRequest
		handler := func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/ci/runs" {
				json.NewDecoder(r.Body).Decode(&req)
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(ci.Run{ID: "run-1", Repo: "acme/widgets", Ref: "main"})
			}
		}
		if _, err := ciCLI(t, handler, "run", "-repo", "acme/widgets", "-branch", "main", flag); err != nil {
			t.Fatalf("grain ci run %s failed: %v", flag, err)
		}
		if !req.Recheck {
			t.Errorf("flag %s did not set Recheck: true on CIRunRequest: %+v", flag, req)
		}
	}
}

// The listing narrows by the same words its three sibling readers do
// (ci.RunFilter, and list_ci_runs, GET /api/ci/runs and the CI pane over
// it), and every one of them reaches the daemon -- which answers them
// over its whole run store rather than over the newest page of it, so a
// filter here is a question about this deployment's history rather than
// about the last twenty runs of it.
func TestCIListSendsTheWholeFilterVocabularyToTheDaemon(t *testing.T) {
	var query url.Values
	handler := func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}})
	}
	if _, err := ciCLI(t, handler, "list",
		"-repo", "acme/widgets", "-ref", "task-231", "-conclusion", "failure,errored",
		"-conclusion", "cancelled", "-job", "ui e2e", "-since", "2026-09-07T12:00:00Z",
		"-sort", "duration", "-order", "asc", "-limit", "5"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, want string }{
		{"repo", "acme/widgets"},
		{"ref", "task-231"},
		{"job", "ui e2e"},
		{"since", "2026-09-07T12:00:00Z"},
		{"sort", "duration"},
		{"order", "asc"},
		{"limit", "5"},
	} {
		if got := query.Get(tc.key); got != tc.want {
			t.Errorf("?%s= reached the daemon as %q, want %q (whole query: %v)", tc.key, got, tc.want, query)
		}
	}
	// Repeatable and comma-separated are both how somebody writes two of
	// these at a shell, and both mean the same list.
	if got := query["conclusion"]; !slices.Equal(got, []string{"failure", "errored", "cancelled"}) {
		t.Errorf("the results reached the daemon as %v", got)
	}
}

// -since takes a span as well as an instant, and the span is measured by
// this command's own clock: what crosses the wire is the window's edge,
// so the daemon cannot re-read "24h" from a different now.
func TestCIListResolvesASpanBeforeSendingIt(t *testing.T) {
	var query url.Values
	handler := func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}})
	}
	before := time.Now().Add(-24 * time.Hour)
	if _, err := ciCLI(t, handler, "-since", "24h"); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, query.Get("since"))
	if err != nil {
		t.Fatalf("since= reached the daemon as %q: %v", query.Get("since"), err)
	}
	if at.Before(before.Add(-time.Minute)) || at.After(time.Now().Add(-23*time.Hour)) {
		t.Errorf("-since 24h resolved to %s, which is not a day ago", at)
	}
}

// -compact asks the daemon for view=compact and prints a line a run and
// a line a job that did not succeed; -until closes the window -since
// opens, so a week that is over can be audited a page at a time.
func TestCIListCompactAsksForTheCompactView(t *testing.T) {
	var query url.Values
	handler := func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ui.CICompactRunsResponse{Enabled: true, Matched: 1, Stored: 1,
			Runs: []ui.CICompactRun{{ID: "run-7", Repo: "acme/widgets", Ref: "grain-merge-queue/main/b1",
				Commit: "0123456789abcdef", Conclusion: "failure",
				Jobs: []ui.CICompactJob{{Name: "go", Conclusion: ci.ConclusionFailure, Step: "go test",
					Test: "TestThing", Fault: "d1g3st"}}}}})
	}
	out, err := ciCLI(t, handler, "list", "-compact", "-ref", "grain-merge-queue/",
		"-since", "2026-10-12", "-until", "2026-10-19")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, want string }{
		{"view", "compact"},
		{"ref", "grain-merge-queue/"},
		{"since", "2026-10-12T00:00:00Z"},
		{"until", "2026-10-19T00:00:00Z"},
	} {
		if got := query.Get(tc.key); got != tc.want {
			t.Errorf("?%s= reached the daemon as %q, want %q (whole query: %v)", tc.key, got, tc.want, query)
		}
	}
	for _, want := range []string{"run-7", "failure", "0123456789ab", "grain-merge-queue/main/b1",
		"go: failure, step go test, test TestThing, shared fault d1g3st"} {
		if !strings.Contains(out, want) {
			t.Errorf("the compact listing does not say %q:\n%s", want, out)
		}
	}
}

// A word the vocabulary does not hold is refused by the command, naming
// the words it does hold. The alternative is worse than an error: a
// filter that were quietly dropped would answer with every run in the
// deployment, and a list of green runs reads as a fact about CI rather
// than as a typo.
func TestCIListRefusesAWordItDoesNotKnow(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a filter that cannot mean anything should not reach the daemon")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ui.CIRunsResponse{Enabled: true})
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"-conclusion", "failed"}, []string{"failed", "failure", "pending"}},
		{[]string{"-conclusion", "failure,nonsense"}, []string{"nonsense", "failure"}},
		{[]string{"-sort", "length"}, []string{"length", "duration", "started"}},
		{[]string{"-order", "sideways"}, []string{"sideways", "asc", "desc"}},
		{[]string{"-since", "last tuesday"}, []string{"last tuesday", "24h", "RFC 3339"}},
	} {
		_, err := ciCLI(t, handler, tc.args...)
		if err == nil {
			t.Errorf("grain ci list %v was accepted", tc.args)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("grain ci list %v: %v, want an error mentioning %q", tc.args, err, want)
			}
		}
	}
}

// What is *not* in a listing is the question a bounded list raises, and
// the two counts the response carries are what answer it: how many runs
// matched, and how many this deployment remembers at all.
func TestPrintCIRunsListingSaysWhatItLeftOut(t *testing.T) {
	page := ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}, Matched: 12, Stored: 4812}
	narrowed := captureStdout(t, func() {
		(&printer{}).ciRuns(page, ci.RunFilter{Repo: "acme/widgets", States: []string{"failure"}})
	})
	for _, want := range []string{"repo acme/widgets", "result failure", "12 of 4812 remembered runs match", "-limit"} {
		if !strings.Contains(narrowed, want) {
			t.Errorf("the summary does not mention %q:\n%s", want, narrowed)
		}
	}

	// Unfiltered, the counts still say that the limit cut the list short.
	whole := captureStdout(t, func() {
		(&printer{}).ciRuns(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}, Matched: 9, Stored: 9}, ci.RunFilter{})
	})
	if !strings.Contains(whole, "1 of 9 remembered runs") {
		t.Errorf("a list cut by the limit should say so:\n%s", whole)
	}

	// And a listing with nothing left out says nothing: "1 of 1
	// remembered runs" above one run is noise in front of the answer.
	all := captureStdout(t, func() {
		(&printer{}).ciRuns(ui.CIRunsResponse{Enabled: true, Runs: []ci.Run{failedRun()}, Matched: 1, Stored: 1}, ci.RunFilter{})
	})
	if strings.Contains(all, "remembered runs") {
		t.Errorf("an unbounded, unfiltered listing needs no summary:\n%s", all)
	}
}

// An empty answer to a narrow question is not an empty deployment, and
// the two must not read alike: telling a reader nothing has ever run,
// when thousands have and none of them matched, is the one way this
// listing can be actively misleading.
func TestPrintCIRunsEmptyUnderAFilter(t *testing.T) {
	out := captureStdout(t, func() {
		(&printer{}).ciRuns(ui.CIRunsResponse{Enabled: true, Stored: 4812},
			ci.RunFilter{Ref: "task-231", States: []string{"failure", "errored"}})
	})
	for _, want := range []string{"no CI run matches", "ref task-231", "failure or errored", "4812 runs"} {
		if !strings.Contains(out, want) {
			t.Errorf("an empty filtered listing does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "grain ci run -repo") {
		t.Errorf("a deployment with a history should not be told to start its first run:\n%s", out)
	}
}

func TestCIRunPassesSelectedJobsToTheDaemon(t *testing.T) {
	var request ui.CIRunRequest
	handler := func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(ci.Run{ID: "run-1"})
	}
	if _, err := ciCLI(t, handler, "run", "-repo", "bwsalmon/grain", "-branch", "main",
		"-job", ".grain/workflows/release.yml:images", "-job", "manifest"); err != nil {
		t.Fatal(err)
	}
	want := []string{".grain/workflows/release.yml:images", "manifest"}
	if !slices.Equal(request.Jobs, want) {
		t.Fatalf("selected jobs = %v, want %v", request.Jobs, want)
	}
}

// -watch waits for the run and then exits non-zero if it did not
// succeed, so a script can branch on the status rather than on the text.
func TestCIRunWatchReportsTheVerdict(t *testing.T) {
	calls := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(ci.Run{ID: "9f2c1a", Repo: "acme/widgets", Ref: "grain/task-1"})
			return
		}
		calls++
		json.NewEncoder(w).Encode(failedRun())
	}
	out, err := ciCLI(t, handler, "run", "-repo", "acme/widgets", "-branch", "grain/task-1", "-watch")
	if err == nil || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("a failed run should be a non-zero exit naming the conclusion, got %v", err)
	}
	if !strings.Contains(out, "FAIL: TestThing") {
		t.Errorf("-watch should print the finished run:\n%s", out)
	}
	if calls != 1 {
		t.Errorf("a run that is already over should be read once, not %d times", calls)
	}
}

func TestWatchCIRunGivesUpWithTheContext(t *testing.T) {
	t.Parallel()
	// A deadline rather than an already-cancelled context: this is about
	// the wait between polls giving up, which means the first read has
	// to get through.
	//
	// The clock therefore starts when that read has been answered rather
	// than when this test did. It used to be a flat two seconds from
	// here, which made the read itself race it -- and a read that lost
	// came back as the transport's "context deadline exceeded" rather
	// than the "still going" below, on a CI host where two seconds of
	// stall is an ordinary amount for eight race-instrumented test
	// binaries to cost each other (grain/task-510). Half of
	// cicli.WatchInterval is left for the client to decode the forty bytes
	// the handler has just written, which is all that is still in front
	// of it, and lands the cancellation inside the wait rather than in
	// the poll after it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var answered sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ci.Run{ID: "9f2c1a"}) // no conclusion: still going
		answered.Do(func() { time.AfterFunc(cicli.WatchInterval/2, cancel) })
	}))
	t.Cleanup(srv.Close)
	if _, err := cicli.Watch(ctx, ui.NewHTTPClient(srv.URL), "9f2c1a"); err == nil {
		t.Fatal("watching under a cancelled context should stop")
	} else if !strings.Contains(err.Error(), "still going") {
		t.Errorf("giving up should say the run has not stopped: %v", err)
	}
}

// grain/task-304: the job cache from a terminal.
func TestCICacheCommand(t *testing.T) {
	stats := &ui.CICacheStats{
		Dir: "/var/lib/grain/data/state/ci-cache", Entries: 12, Bytes: 3 << 20,
		Hits: 7, Misses: 3, LastSweep: time.Now().Add(-time.Minute),
		Oldest: time.Now().Add(-48 * time.Hour), Newest: time.Now(),
		MaxAgeSeconds: 30 * 24 * 60 * 60, MaxBytes: 256 << 20, MaxEntries: 4096,
	}
	var seen []string
	handler := func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		resp := ui.CICacheResponse{Enabled: true, Available: true, Cache: stats}
		if r.Method == http.MethodDelete {
			removed := 12
			resp.Removed, resp.Cache = &removed, &ui.CICacheStats{
				Dir: stats.Dir, MaxAgeSeconds: stats.MaxAgeSeconds,
				MaxBytes: stats.MaxBytes, MaxEntries: stats.MaxEntries,
			}
		}
		json.NewEncoder(w).Encode(resp)
	}

	out, err := ciCLI(t, handler, "cache")
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	if got := seen[len(seen)-1]; got != "GET /api/ci/cache" {
		t.Errorf("a bare \"grain ci cache\" reads rather than clears: %q", got)
	}
	for _, want := range []string{"12 entries", "3.0MB", "/var/lib/grain/data/state/ci-cache",
		"7 hits", "70% hit rate", "for 30 days"} {
		if !strings.Contains(out, want) {
			t.Errorf("the cache should say %q:\n%s", want, out)
		}
	}

	out, err = ciCLI(t, handler, "cache", "clear")
	if err != nil {
		t.Fatalf("cache clear: %v", err)
	}
	if got := seen[len(seen)-1]; got != "DELETE /api/ci/cache" {
		t.Errorf("clearing reached the daemon as %q", got)
	}
	if !strings.Contains(out, "cleared 12 entries") || !strings.Contains(out, "0 entries") {
		t.Errorf("clearing should say what went and what is left:\n%s", out)
	}

	if _, err := ciCLI(t, handler, "cache", "frobnicate"); err == nil ||
		!strings.Contains(err.Error(), "one of show, clear") {
		t.Errorf("an unknown action should be refused here: %v", err)
	}
}

// The two answers that are not a cache: no CI at all, and CI running
// with no cache behind it. Neither is "the cache is empty".
func TestCICacheCommandSaysWhenThereIsNone(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp ui.CICacheResponse
		want string
	}{
		{"no CI", ui.CICacheResponse{}, "does not run grain CI"},
		{"no cache", ui.CICacheResponse{Enabled: true}, "every job runs, every time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ciCLI(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tc.resp)
			}, "cache")
			if err != nil {
				t.Fatalf("cache: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, out)
			}
		})
	}
}

// ci run -watch` does.
func TestCIWatchStreamsThenReportsTheVerdict(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(failedRun())
	}
	out, err := ciCLI(t, handler, "watch", "9f2c1a")
	if err == nil || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("a failed run should be a non-zero exit naming the conclusion, got %v", err)
	}
	for _, want := range []string{
		"following run 9f2c1a", "=== job go test, step go test ./... ===",
		"--- FAIL: TestThing", "run 9f2c1a", "job go test",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("following a run does not mention %q:\n%s", want, out)
		}
	}
	// The summary does not repeat what has already scrolled past: on a
	// twenty-minute job that would be the whole log, twice.
	if n := strings.Count(out, "--- FAIL: TestThing"); n != 1 {
		t.Errorf("the failing output should be printed once, not %d times:\n%s", n, out)
	}
	if !strings.Contains(out, "grain ci show 9f2c1a") {
		t.Errorf("the summary should say how to read the output again:\n%s", out)
	}

	if _, err := ciCLI(t, handler, "watch"); err == nil ||
		!strings.Contains(err.Error(), "grain ci watch <id>") {
		t.Errorf("a watch with no run id should say so in its own words: %v", err)
	}
}

// -json stays one document per run rather than becoming a stream of
// half-written ones: -follow with -json waits, then prints the finished
// run once, so "grain ci watch <id> -json | jq" is fed what a plain
// "grain ci show -json" would have given it.
func TestCIFollowJSONIsOneDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(failedRun())
	}))
	t.Cleanup(srv.Close)
	var err error
	out := captureStdout(t, func() {
		err = cmdCI(context.Background(), ui.NewHTTPClient(srv.URL), &printer{json: true},
			[]string{"watch", "9f2c1a"})
	})
	if err == nil || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("the verdict is still the exit status under -json: %v", err)
	}
	var got ci.Run
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.ID != "9f2c1a" || len(got.Jobs) != 2 {
		t.Fatalf("got %+v", got)
	}
	if err := dec.Decode(new(ci.Run)); err == nil {
		t.Errorf("-follow -json should print one run, not a stream of them:\n%s", out)
	}
}

// The case this report used to have no words for: three `image` jobs
// cancelled at exactly their timeout, and nobody able to say whether
// they had been running or waiting (grain/task-326). A job that never
// got a machine has no duration at all, so without its wait the row
// said nothing whatever about thirty-six minutes.
func TestPrintCIRunSaysWhatWasQueueingAndWhatWasRunning(t *testing.T) {
	started := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	run := ci.Run{
		ID: "abc", Repo: "acme/widgets", Ref: "grain/task-264",
		StartedAt:  &started,
		FinishedAt: instant(started.Add(40 * time.Minute)),
		Conclusion: ci.ConclusionCancelled,
		Jobs: []ci.JobRun{
			{
				Name: "image", Conclusion: ci.ConclusionCancelled,
				Queued: 36 * time.Minute,
				QueuedReason: "for room in this deployment's sandbox budget -- memory: " +
					"52.0 GiB is committed of this deployment's 64.0 GiB sandbox budget",
			},
			{
				Name: "go", Conclusion: ci.ConclusionSuccess,
				StartedAt: instant(started.Add(18 * time.Minute)),
				// 18 minutes behind other work, four and a half on a machine.
				FinishedAt: instant(started.Add(22*time.Minute + 30*time.Second)),
				Queued:     18 * time.Minute,
				QueuedReason: "behind higher-priority work; 2 jobs this deployment would rather " +
					"run first are queued ahead of it",
			},
		},
	}

	out := captureStdout(t, func() { (&printer{}).ciRun(run, cicli.OutputFailed) })

	// The job that only ever queued: its wait is the whole of what
	// happened to it, where the row used to read "-".
	if !strings.Contains(out, "36m0s queued") {
		t.Errorf("a job cancelled while it queued should say how long it queued:\n%s", out)
	}
	if !strings.Contains(out, "52.0 GiB is committed") {
		t.Errorf("and what was holding it, which is what says it was starved rather than slow:\n%s", out)
	}
	// And the job that did both, as two figures rather than one: "4m30s
	// (+18m0s queued)" sends a reader to this deployment's capacity,
	// where "22m30s" sends them to the job.
	if !strings.Contains(out, "4m30s (+18m0s queued)") {
		t.Errorf("a job that queued and then ran should say both:\n%s", out)
	}
	if !strings.Contains(out, "behind higher-priority work") {
		t.Errorf("and which kind of waiting it was:\n%s", out)
	}
	// The suite's own total, which no single job's wait says.
	if !strings.Contains(out, "queued:    54m0s of job-time") {
		t.Errorf("the run should add up what its jobs spent waiting:\n%s", out)
	}
}

func TestCIFaultsCommandListsAndFiltersBySourceAndRepo(t *testing.T) {
	faults := []ui.CIFault{
		{
			SharedFault: ci.SharedFault{
				Source:      ci.FaultSourceGrain,
				Repo:        "acme/widgets",
				JobID:       "go",
				JobName:     "go test",
				Step:        "go test ./...",
				Digest:      "d1g3stgrain",
				Refs:        []string{"grain/task-1", "grain/task-2"},
				Paths:       []string{"pkg/foo/bar.go"},
				OwnerTaskID: "1",
				OwnerRef:    "grain/task-1",
			},
		},
		{
			SharedFault: ci.SharedFault{
				Source:      ci.FaultSourceGitHub,
				Repo:        "acme/widgets",
				JobID:       "CI / test",
				JobName:     "CI / test",
				Step:        "Run go test ./...",
				Digest:      "d1g3stgithub",
				Refs:        []string{"grain/task-10", "grain/task-11", "grain/task-12"},
				Paths:       []string{"pkg/ci/sharedfault.go"},
				OwnerTaskID: "10",
				OwnerRef:    "grain/task-10",
			},
			Fixes: []ui.CIFaultFix{
				{PullRequest: "acme/widgets#99", Task: "99", Title: "Fix sharedfault", Files: []string{"pkg/ci/sharedfault.go"}},
			},
		},
	}

	var seen string
	handler := func(w http.ResponseWriter, r *http.Request) {
		seen = r.Method + " " + r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ui.CIFaultsResponse{Faults: faults})
	}

	out, err := ciCLI(t, handler, "faults")
	if err != nil {
		t.Fatalf("grain ci faults: %v", err)
	}
	if seen != "GET /api/ci/faults" {
		t.Fatalf("seen = %q, want GET /api/ci/faults", seen)
	}
	for _, want := range []string{
		"[grain] acme/widgets  job go test, step go test ./...  (2 branches)",
		"branches: grain/task-1, grain/task-2",
		"[github] acme/widgets  job CI / test, step Run go test ./...  (3 branches)",
		"branches: grain/task-10, grain/task-11, grain/task-12",
		"files:    pkg/ci/sharedfault.go",
		"owner:    task #10 (grain/task-10)",
		"fix:      acme/widgets#99 (task #99) -- Fix sharedfault [touches pkg/ci/sharedfault.go]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grain ci faults output missing %q:\n%s", want, out)
		}
	}

	ghOnly, err := ciCLI(t, handler, "faults", "-source", "github", "-repo", "acme/widgets")
	if err != nil {
		t.Fatalf("grain ci faults -source github: %v", err)
	}
	if strings.Contains(ghOnly, "[grain]") || !strings.Contains(ghOnly, "[github]") {
		t.Errorf("expected only github faults, got:\n%s", ghOnly)
	}

	emptyOut, err := ciCLI(t, handler, "faults", "-repo", "other/repo")
	if err != nil {
		t.Fatalf("grain ci faults -repo other/repo: %v", err)
	}
	if !strings.Contains(emptyOut, "no shared CI faults right now") {
		t.Errorf("expected empty message, got:\n%s", emptyOut)
	}

	if _, err := ciCLI(t, handler, "faults", "-source", "bogus"); err == nil || !strings.Contains(err.Error(), "grain, github, or all") {
		t.Errorf("expected invalid -source error, got %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	jsonOut := captureStdout(t, func() {
		err = cmdCI(context.Background(), ui.NewHTTPClient(srv.URL), &printer{json: true},
			[]string{"faults", "-source", "github"})
	})
	if err != nil {
		t.Fatalf("grain ci faults -json: %v", err)
	}
	var gotJSON []ui.CIFault
	if err := json.Unmarshal([]byte(jsonOut), &gotJSON); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, jsonOut)
	}
	if len(gotJSON) != 1 || gotJSON[0].Source != ci.FaultSourceGitHub {
		t.Fatalf("gotJSON = %+v, want 1 github fault", gotJSON)
	}
}
