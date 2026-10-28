package cli

// The daemon side of open_pull_request: the two pieces of cmd/grain glue
// that stand between a run's own mcpserver and
// orchestrator.OpenPullRequestForTask.
//
// Every layer around them is covered elsewhere -- pkg/mcp renders the
// report, pkg/ui carries it over HTTP, pkg/orchestrator opens the real
// pull request against a githubsim -- and all of those run against a fake
// standing in for the layer below. What is left over is the join, and
// the join here is two field-for-field conversions between three structs
// that deliberately do not import each other
// (orchestrator.PullRequestStatus -> ui.PullRequestStatus ->
// mcp.PullRequestReport, see ui.PullRequestStatus' own doc comment on
// why), plus the gate that answers before runDaemon has built a GitHub
// client at all. A swapped Repo and URL, a dropped ChecksError, or
// ChecksKnown wired to the wrong side of ChecksAvailable all compile and
// pass every test named above, so these run the whole controller-side
// chain in one process against a real store and a real
// github.RESTClient.
//
// The last test is the outer end of that chain: mcp.PullRequestReport
// <- ui.HTTPClient <- a real ui.Server <- pullRequestOpener <-
// orchestrator <- githubsim, which is where a mismatch between the two
// conversions shows up as a wrong answer rather than as a compile error.

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// pullRequestWorld is one queued task, on one real bare repo, with its
// branch already pushed -- everything OpenPullRequestForTask needs to
// have something real to open, and nothing else. The sim is exposed so a
// test can seed checks against the branch or wrap the transport;
// client is the plain one, over the sim itself.
type pullRequestWorld struct {
	store  *model.Store
	sim    *githubsim.Sim
	client *github.RESTClient
	task   model.Task
	branch string
}

const prWorldOwner, prWorldRepo = "acme", "widgets"

// newPullRequestWorld seeds a bare repo with a default branch and the
// task's own branch on top of it, the same way pkg/orchestrator's own
// newSim/pushBranch pair does -- restated here rather than shared,
// matching this codebase's standing choice to duplicate a test helper
// across packages (tests/e2e/harness_test.go's own comment on why).
func newPullRequestWorld(t *testing.T, taskID string) *pullRequestWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	bare := initBareRepo(t, filepath.Join(dir, prWorldRepo+".git"))
	seed := filepath.Join(dir, "seed")
	runLive(t, dir, "git", "clone", "-q", bare, seed)
	runLive(t, seed, "git", "config", "user.email", "seed@example.com")
	runLive(t, seed, "git", "config", "user.name", "seed")
	runLive(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "seed")
	runLive(t, seed, "git", "push", "-q", "origin", "main")

	// The push a dispatched run would have made before asking for its own
	// pull request: OpenPullRequestForTask refuses a branch that is not
	// on the remote yet.
	branch := model.BranchName(taskID)
	work := filepath.Join(dir, "work")
	runLive(t, dir, "git", "clone", "-q", bare, work)
	runLive(t, work, "git", "config", "user.email", "agent@example.com")
	runLive(t, work, "git", "config", "user.name", "agent")
	runLive(t, work, "git", "checkout", "-q", "-b", branch)
	runLive(t, work, "git", "commit", "-q", "--allow-empty", "-m", "agent commit")
	runLive(t, work, "git", "push", "-q", "origin", branch)

	store := testStore(t)
	seedReconcileTask(t, store, taskID, prWorldOwner, prWorldRepo)
	task, err := store.GetTask(context.Background(), taskID)
	if err != nil || task == nil {
		t.Fatalf("reading back the task just filed: %+v, %v", task, err)
	}

	sim := githubsim.New(prWorldOwner, prWorldRepo, bare, "main")
	return &pullRequestWorld{
		store: store, sim: sim, client: github.NewClient(sim, nil),
		task: *task, branch: branch,
	}
}

// onlyPullRequest is the one pull request the sim holds, read under the
// same lock every request into it takes -- a method on
// daemon_live_test.go's syncedSim, here because this is the only test in
// this package that needs the whole record rather than a count.
func (s *syncedSim) onlyPullRequest(t *testing.T) githubsim.PullRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sim.PullRequests) != 1 {
		t.Fatalf("expected exactly one pull request, got %+v", s.sim.PullRequests)
	}
	return s.sim.PullRequests[0]
}

// brokenCheckRuns answers every check-runs read with a 500 and forwards
// everything else -- a GitHub that is up enough to open a pull request
// and broken enough not to say what CI thinks of it. Deliberately a 500
// rather than a 403: a permission denial is the *other* case
// (orchestrator's checkRunsFor falls back to workflow runs and then
// latches a process-global ChecksUnavailable), and this test wants the
// error that reaches ChecksError.
type brokenCheckRuns struct{ inner github.Transport }

func (b brokenCheckRuns) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if strings.Contains(path, "/check-runs") {
		return github.ApiResponse{Status: 500, Body: []byte(`{"message":"check runs are having a day"}`)}, nil
	}
	return b.inner.Request(method, path, headers, body)
}

// The UI/API server is started before runDaemon builds a GitHub client
// (see livePullRequests' own doc comment on why that ordering is
// deliberate), so there is a window in which POST
// /api/tasks/{id}/pull-request has nothing to call. What a caller gets in
// that window has to be an honest sentence naming the reconcile loop --
// the thing an operator would have to go look at -- rather than a nil
// dereference.
func TestPullRequestGateRefusesUntilRunDaemonHasAnOpener(t *testing.T) {
	// Serial: this test is about what livePullRequests holds, and it
	// writes it. A t.Parallel here would let it set an opener while
	// another test was asserting there is none -- which is exactly the
	// failure it caused next door before this note existed.
	// livePullRequests is process-global, the same shape reconcilerDown
	// is (daemon_reconciler_down_test.go's own note on why that needs a
	// t.Cleanup): a test that leaves an opener in it would hand every
	// later test in this binary a GitHub client it never asked for.
	t.Cleanup(func() { daemon.LivePullRequests.Store(nil) })
	daemon.LivePullRequests.Store(nil)

	ctx := context.Background()
	if _, err := (daemon.PullRequestGate{}).OpenForTask(ctx, "t1"); err == nil {
		t.Fatal("the gate answered before runDaemon set an opener, want a refusal")
	} else {
		for _, want := range []string{"GitHub client", "reconcile loop"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal = %q, want it to mention %q", err, want)
			}
		}
	}

	// And once runDaemon has put one there, the very same gate value
	// answers with the real opener's answer -- nothing about the gate is
	// latched or cached.
	w := newPullRequestWorld(t, "t1")
	daemon.LivePullRequests.Store(&daemon.PullRequestOpener{Store: w.store, Client: w.client})

	status, err := (daemon.PullRequestGate{}).OpenForTask(ctx, w.task.ID)
	if err != nil {
		t.Fatalf("the gate refused after an opener was set: %v", err)
	}
	if len(w.sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request opened through the gate, got %+v", w.sim.PullRequests)
	}
	if status.Number != w.sim.PullRequests[0].Number {
		t.Errorf("status = %+v, want the pull request the gate just opened (#%d)",
			status, w.sim.PullRequests[0].Number)
	}
}

// pullRequestOpener's whole job is the conversion, so this asserts every
// converted field against the orchestrator.PullRequestStatus it came
// from, read back by calling OpenPullRequestForTask directly -- which is
// safe to do a second time, and is the point: it never opens a second
// pull request (pkg/orchestrator's own
// TestOpenPullRequestForTaskIsAdoptedByTheFinishPath), so the two calls
// describe one pull request from either side of the conversion.
func TestPullRequestOpenerConvertsEveryFieldOfTheOrchestratorsAnswer(t *testing.T) {
	t.Parallel()
	w := newPullRequestWorld(t, "t1")
	ctx := context.Background()
	// One completed check and one still running: the running one is the
	// nil Conclusion the conversion has to turn into an empty string
	// rather than dereferencing.
	w.sim.CheckRuns[w.branch] = []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPointer("failure")},
		{Name: "tests", Status: "in_progress"},
	}

	opener := &daemon.PullRequestOpener{Store: w.store, Client: w.client}
	got, err := opener.OpenForTask(ctx, w.task.ID)
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	want, err := orchestrator.OpenPullRequestForTask(ctx, w.store, w.client, w.task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask (reading back what the conversion was fed): %v", err)
	}
	if len(w.sim.PullRequests) != 1 {
		t.Fatalf("expected exactly one pull request across both calls, got %+v", w.sim.PullRequests)
	}

	if got.Number != want.PullRequest.Number {
		t.Errorf("Number = %d, want %d", got.Number, want.PullRequest.Number)
	}
	if got.URL != want.PullRequest.HTMLURL {
		t.Errorf("URL = %q, want the pull request's HTMLURL %q", got.URL, want.PullRequest.HTMLURL)
	}
	// Not just equal to each other: URL and Repo are both strings, and a
	// conversion that filled one from the other would satisfy an equality
	// check against a status built the same wrong way.
	if !strings.Contains(got.URL, "/pull/") {
		t.Errorf("URL = %q, want the pull request's own web URL", got.URL)
	}
	// Repo comes from the task, not from the status: it is the one field
	// with no counterpart on the orchestrator side.
	if got.Repo != w.task.Target.String() {
		t.Errorf("Repo = %q, want the task's own target %q", got.Repo, w.task.Target.String())
	}
	if got.ChecksAvailable != want.ChecksKnown {
		t.Errorf("ChecksAvailable = %v, want ChecksKnown (%v)", got.ChecksAvailable, want.ChecksKnown)
	}
	if !got.ChecksAvailable {
		t.Error("ChecksAvailable = false, want true -- this client can read check runs")
	}
	if got.ChecksError != want.ChecksError || got.ChecksError != "" {
		t.Errorf("ChecksError = %q, want the orchestrator's own (%q, empty here)",
			got.ChecksError, want.ChecksError)
	}

	if len(got.Checks) != len(want.Checks) {
		t.Fatalf("Checks = %+v, want one per orchestrator check (%+v)", got.Checks, want.Checks)
	}
	for i, check := range want.Checks {
		if got.Checks[i].Name != check.Name || got.Checks[i].Status != check.Status {
			t.Errorf("Checks[%d] = %+v, want %+v", i, got.Checks[i], check)
		}
		conclusion := ""
		if check.Conclusion != nil {
			conclusion = *check.Conclusion
		}
		if got.Checks[i].Conclusion != conclusion {
			t.Errorf("Checks[%d].Conclusion = %q, want %q", i, got.Checks[i].Conclusion, conclusion)
		}
	}
	// And the two cases the loop above would pass over silently if the
	// orchestrator had reported nothing at all.
	if got.Checks[0].Conclusion != "failure" {
		t.Errorf("Checks[0] = %+v, want the completed check's own conclusion", got.Checks[0])
	}
	if got.Checks[1].Conclusion != "" || got.Checks[1].Status != "in_progress" {
		t.Errorf("Checks[1] = %+v, want the still-running check with no conclusion", got.Checks[1])
	}
}

// A pull request that was opened and whose checks could not be read is
// the case ChecksError exists for: the number is real, and losing it
// because the read after it failed would cost the caller the one thing
// it cannot get back by retrying. Both halves of that answer have to
// survive the conversion -- the message, and ChecksAvailable false.
func TestPullRequestOpenerCarriesAFailedCheckReadWithoutLosingThePullRequest(t *testing.T) {
	t.Parallel()
	w := newPullRequestWorld(t, "t1")
	ctx := context.Background()
	opener := &daemon.PullRequestOpener{
		Store:  w.store,
		Client: github.NewClient(brokenCheckRuns{inner: w.sim}, nil),
	}

	got, err := opener.OpenForTask(ctx, w.task.ID)
	if err != nil {
		t.Fatalf("OpenForTask: %v -- an unreadable check must not fail the call that opened the pull request", err)
	}
	if len(w.sim.PullRequests) != 1 {
		t.Fatalf("expected one pull request, got %+v", w.sim.PullRequests)
	}
	if got.Number != w.sim.PullRequests[0].Number || got.URL == "" {
		t.Errorf("status = %+v, want the pull request that really was opened", got)
	}
	if got.ChecksError == "" {
		t.Error("ChecksError is empty, want the failed check read carried through")
	}
	if got.ChecksAvailable {
		t.Error("ChecksAvailable = true, want false when the checks could not be read")
	}
	if len(got.Checks) != 0 {
		t.Errorf("Checks = %+v, want none", got.Checks)
	}
}

// The store lookup exists so an id nothing was filed under is a sentence
// rather than a nil dereference on the task the store did not find --
// pkg/ui's own handler 404s first for a request that came in over HTTP,
// but the opener is called directly by the gate and has to hold on its
// own.
func TestPullRequestOpenerRefusesAnUnknownTask(t *testing.T) {
	t.Parallel()
	w := newPullRequestWorld(t, "t1")
	opener := &daemon.PullRequestOpener{Store: w.store, Client: w.client}

	_, err := opener.OpenForTask(context.Background(), "no-such-task")
	if err == nil {
		t.Fatal("expected an error for a task id nothing was filed under")
	}
	if !strings.Contains(err.Error(), "no-such-task") {
		t.Errorf("err = %q, want it to name the task that does not exist", err)
	}
	if len(w.sim.PullRequests) != 0 {
		t.Fatalf("expected no pull request, got %+v", w.sim.PullRequests)
	}
}

// The whole controller-side chain, in one process: an mcpserver's own
// daemonPullRequests -> ui.HTTPClient -> a real ui.Server ->
// pullRequestOpener -> orchestrator -> githubsim. Both conversions run
// here, back to back, which is the only place a disagreement between
// them (a field one side fills and the other drops) shows up as a wrong
// answer rather than as something a compiler would have caught.
func TestDaemonPullRequestsReportsWhatTheOpenerOpened(t *testing.T) {
	t.Parallel()
	w := newPullRequestWorld(t, "t1")
	ctx := context.Background()
	w.sim.CheckRuns[w.branch] = []github.CheckRun{
		{Name: "unit-tests", Status: "completed", Conclusion: strPointer("success")},
		{Name: "integration", Status: "queued"},
	}

	// The opener runs on the UI server's own request-handling goroutines
	// from here on, so the sim it drives is reached through syncedSim --
	// the same wrapper daemon_live_test.go introduced for exactly this
	// (a *githubsim.Sim carries no lock a test can read its fields
	// under).
	wire := &syncedSim{sim: w.sim}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{
		Actor:        ui.DefaultActor("tester"),
		Capabilities: ui.OfferedCapabilities(),
		PullRequests: &daemon.PullRequestOpener{Store: w.store, Client: github.NewClient(wire, nil)},
	}), w.store))
	t.Cleanup(srv.Close)

	report, err := daemon.DaemonPullRequests{
		Client: accessCodeClient(srv.URL), TaskID: w.task.ID,
	}.OpenPullRequest(ctx)
	if err != nil {
		t.Fatalf("OpenPullRequest: %v", err)
	}

	opened := wire.onlyPullRequest(t)
	if report.Number != opened.Number {
		t.Errorf("Number = %d, want the pull request GitHub actually has (#%d)", report.Number, opened.Number)
	}
	if report.URL != opened.HTMLURL {
		t.Errorf("URL = %q, want %q", report.URL, opened.HTMLURL)
	}
	if report.Repo != prWorldOwner+"/"+prWorldRepo {
		t.Errorf("Repo = %q, want %q", report.Repo, prWorldOwner+"/"+prWorldRepo)
	}
	if !report.ChecksAvailable {
		t.Error("ChecksAvailable = false, want true -- this deployment can read check runs")
	}
	if report.ChecksError != "" {
		t.Errorf("ChecksError = %q, want none", report.ChecksError)
	}
	want := []mcp.CheckReport{
		{Name: "unit-tests", Status: "completed", Conclusion: "success"},
		{Name: "integration", Status: "queued"},
	}
	if !slices.Equal(report.Checks, want) {
		t.Errorf("Checks = %+v, want %+v", report.Checks, want)
	}
	// What the agent reads is this report rendered by pkg/mcp, which
	// tests/e2e/mcpserver_open_pull_request_test.go drives through the
	// real tool against a real subprocess -- the half of the chain that
	// cannot be reached from in here.
}

// The other end of the same glue: closing a task that has a pull request
// open says so on the pull request itself, through the same
// pullRequestOpener and the same GitHub client (ui.Config.
// PullRequestComments). pkg/ui proves the note is written and pkg/model
// proves it is written once; what is only true here is that the ref
// pkg/ui hands over as "owner/name#123" reaches the right repo and the
// right number on GitHub, which no fake standing in for this conversion
// could have shown.
//
// Driven over real HTTP, like TestDaemonPullRequestsReportsWhatTheOpener
// Opened above, because `grain close` is an HTTPClient call in every
// deployment -- pkg/ui's own Close is never reached directly.
func TestClosingATaskSaysSoOnThePullRequestItOrphans(t *testing.T) {
	t.Parallel()
	w := newPullRequestWorld(t, "t1")
	ctx := context.Background()

	// The pull request a run of this task would have opened, linked onto
	// the task the way its own finish would leave it.
	status, err := orchestrator.OpenPullRequestForTask(ctx, w.store, w.client, w.task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}

	wire := &syncedSim{sim: w.sim}
	opener := &daemon.PullRequestOpener{Store: w.store, Client: github.NewClient(wire, nil)}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{
		Actor:               ui.DefaultActor("tester"),
		Capabilities:        ui.OfferedCapabilities(),
		PullRequestComments: opener,
	}), w.store))
	t.Cleanup(srv.Close)

	if err := accessCodeClient(srv.URL).Close(ctx, w.task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	wire.mu.Lock()
	posted := append([]github.Comment(nil), wire.sim.Comments[status.PullRequest.Number]...)
	wire.mu.Unlock()
	ref := prWorldOwner + "/" + prWorldRepo + "#" + strconv.Itoa(status.PullRequest.Number)
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "grain has stopped watching "+ref) {
		t.Fatalf("comments on the pull request = %+v, want grain's own note about %s", posted, ref)
	}

	comments, err := w.store.Comments(ctx, w.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, ref) {
		t.Fatalf("comments on the task = %+v, want the same note", comments)
	}
	if !strings.Contains(comments[0].Body, "left this note on the pull request itself too") {
		t.Fatalf("note = %q, want it to record that GitHub was told", comments[0].Body)
	}
}

// The same glue for the close itself: a task closed with
// close_pull_request set really does close that pull request on GitHub,
// through the same pullRequestOpener and the same client. pkg/ui proves
// the decision and pkg/github proves the call; what is only true here is
// that "owner/name#123" is parsed back into the repo and number the
// PATCH is aimed at -- and that what it aims at is the pull request and
// not the branch under it.
func TestClosingATaskWithTheOptionClosesThePullRequestOnGitHub(t *testing.T) {
	t.Parallel()
	w := newPullRequestWorld(t, "t1")
	ctx := context.Background()

	status, err := orchestrator.OpenPullRequestForTask(ctx, w.store, w.client, w.task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}

	wire := &syncedSim{sim: w.sim}
	opener := &daemon.PullRequestOpener{Store: w.store, Client: github.NewClient(wire, nil)}
	srv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{
		Actor:               ui.DefaultActor("tester"),
		Capabilities:        ui.OfferedCapabilities(),
		PullRequestComments: opener,
		PullRequestCloser:   opener,
	}), w.store))
	t.Cleanup(srv.Close)

	if err := accessCodeClient(srv.URL).Close(ctx, w.task.ID,
		ui.CloseOptions{ClosePullRequest: true}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	detail, err := w.client.GetPullRequest(prWorldOwner, prWorldRepo, status.PullRequest.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "closed" || detail.Merged {
		t.Fatalf("pull request state = %q, merged = %t, want closed and unmerged",
			detail.State, detail.Merged)
	}
	// The branch the agent pushed is still there, at the same commit:
	// nothing about closing a pull request throws its work away, which is
	// the promise the checkbox is offered on.
	head, err := w.client.GetBranchHead(prWorldOwner, prWorldRepo, w.branch)
	if err != nil || head == nil {
		t.Fatalf("branch head = %+v (%v), want the branch left exactly as it was", head, err)
	}

	ref := prWorldOwner + "/" + prWorldRepo + "#" + strconv.Itoa(status.PullRequest.Number)
	comments, err := w.store.Comments(ctx, w.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "grain has closed "+ref) {
		t.Fatalf("comments on the task = %+v, want the note saying %s was closed", comments, ref)
	}
}

// A close arriving before runDaemon has built a GitHub client cannot be
// told to wait -- the task is closed either way. The gate's refusal ends
// up quoted in the note left on the task instead, which is where somebody
// wondering why the pull request was never told can read it.
func TestClosingATaskBeforeTheGateHasAClientStillNotesTheOrphan(t *testing.T) {
	// Serial, for the reason the gate test above is: this one needs
	// livePullRequests to be empty for the length of the test, and the
	// test above fills it in.
	t.Cleanup(func() { daemon.LivePullRequests.Store(nil) })
	daemon.LivePullRequests.Store(nil)

	w := newPullRequestWorld(t, "t1")
	ctx := context.Background()
	if _, err := orchestrator.OpenPullRequestForTask(ctx, w.store, w.client, w.task); err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}

	srv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{
		Actor:               ui.DefaultActor("tester"),
		Capabilities:        ui.OfferedCapabilities(),
		PullRequestComments: daemon.PullRequestGate{},
	}), w.store))
	t.Cleanup(srv.Close)

	if err := accessCodeClient(srv.URL).Close(ctx, w.task.ID, ui.CloseOptions{}); err != nil {
		t.Fatalf("Close: %v -- a daemon with no GitHub client yet must still close a task", err)
	}
	comments, err := w.store.Comments(ctx, w.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "reconcile loop") {
		t.Fatalf("comments on the task = %+v, want the note to quote the gate's own refusal", comments)
	}
}

func strPointer(s string) *string { return &s }
