package ui_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// gateHealth is the base-branch half of GET /api/sandboxes, which is
// also what read_deployment_health hands an agent.
type gateHealth struct {
	BaseBranchGates []ui.BaseBranchGate `json:"baseBranchGates"`
}

func gateServer(t *testing.T) (*ui.Server, *model.Store) {
	t.Helper()
	client, store, _ := testClient(t)
	client.Config.Sandboxes = fakeSandboxHealth{}
	return ui.NewServerWithClient(client), store
}

func gatesOf(t *testing.T, srv *ui.Server) []ui.BaseBranchGate {
	t.Helper()
	return decode[gateHealth](t, do(t, srv, http.MethodGet, "/api/sandboxes", "")).BaseBranchGates
}

// A branch grain has been told to watch and will never enrol, because
// grain CI is not enabled for the repository. Since grain/task-461 this
// is the only settings-shaped way a base branch goes unjudged -- and it
// is as quiet as the release-switch version was: the reconciler says so
// once with sayOnce and never again, while every merge lands on a branch
// nothing tests.
func TestSandboxHealthSaysWhenNothingWillEverJudgeAWatchedBranch(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, WatchDefaultBranch: true,
	}); err != nil {
		t.Fatal(err)
	}
	got := gatesOf(t, srv)
	if len(got) != 1 {
		t.Fatalf("baseBranchGates = %+v, want the branch nothing will enrol", got)
	}
	gate := got[0]
	if gate.Judged || gate.Repo != repo.String() || gate.Reason != "grain-ci-off" {
		t.Fatalf("gate = %+v, want acme/widgets reported as unjudged for want of grain CI", gate)
	}
	for _, want := range []string{
		"Nothing is testing acme/widgets's default branch,",
		"merging onto it anyway",
		"No base-branch run has ever been started",
		`CI Mode to "Use Grain CI"`,
	} {
		if !strings.Contains(gate.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", gate.Detail, want)
		}
	}
	// docs/test-runs/2026-10-12-base-gate-warnings.md: the detail told
	// operators to attach grain-ci, which every picker refuses
	// (grainci.Refusal), and read "base branch its default branch".
	for _, unwanted := range []string{"Attaching the", "base branch its default branch"} {
		if strings.Contains(gate.Detail, unwanted) {
			t.Errorf("detail = %q, want it without %q", gate.Detail, unwanted)
		}
	}

	// Granting the capability is the repair, and it takes the finding
	// away: the next seeding cycle enrols the branch.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, WatchDefaultBranch: true,
		CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v once grain CI is enabled, want none", got)
	}
}

// The regression this reading must not become. Turning release workflows
// off used to mean "no base-branch run at all", and that is the incident
// this whole file was filed for -- but grain/task-458 and grain/task-461
// took the release switch out of the decision entirely, so a repository
// with releases off is now judged by its own push plan like every other.
//
// A warning drawn from the old condition would fire on a perfectly
// healthy repository, which is the way a surface stops being read.
func TestTheBaseBranchReadingDoesNotWarnAboutReleasesBeingOff(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	last := baseTime.Add(-time.Hour)
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: last, FinishedAt: &last, Conclusion: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v on a judged branch, want none", got)
	}

	// Releases off, no judging jobs named: the exact 2026-09-19 setting,
	// which now means "the whole push plan judges it" and is not a
	// finding.
	if err := store.PutRepoConfig(t.Context(), model.RepoConfig{
		Repo: repo, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v with releases off, want none: "+
			"the release switch has not decided this since grain/task-461", got)
	}
}

// The other half of the failure mode RepoConfig.BaseBranchCIJobs' doc
// comment names, which no switch is involved in: one run judges a branch
// at a time (grain/task-334), so a row naming a run that never concluded
// is a branch nothing will ever start another run for -- and from every
// other surface it looks exactly like a branch being tested right now.
func TestSandboxHealthSaysWhenABaseBranchRunNeverFinished(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Going for twenty minutes: a base-branch run is a release run and
	// takes the best part of an hour, so this is a run in progress and
	// not a finding.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: baseTime.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v on a run still going, want none", got)
	}

	// Five hours later it is not a run in progress, it is a row nothing
	// can refresh.
	started := baseTime.Add(-5 * time.Hour)
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: started, PendingTaskID: "task-9",
	}); err != nil {
		t.Fatal(err)
	}
	got := gatesOf(t, srv)
	if len(got) != 1 {
		t.Fatalf("baseBranchGates = %+v, want the stuck row reported", got)
	}
	gate := got[0]
	if gate.Reason != "run-unfinished" || !gate.RunInFlight || gate.RunID != "run-1" {
		t.Fatalf("gate = %+v, want run-1 reported as never having finished", gate)
	}
	// Judged is about the settings, not about this run: nothing here is
	// switched off, which is why the detail has to say what is wrong
	// rather than leave a reader looking for a switch.
	if !gate.Judged {
		t.Errorf("judged = false, want true: this repository's gate is on")
	}
	for _, want := range []string{
		"has recorded no verdict",
		"5h ago, and has not finished",
		"Run run-1 is the one to look at",
	} {
		if !strings.Contains(gate.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", gate.Detail, want)
		}
	}
}

// A merge that landed and nothing started a run for it: the catch-all,
// which reports the gap whatever the reason for it -- a wedged cycle, a
// paused pipeline, a daemon that is not running -- because the fact a
// reader needs first is that the queue is merging onto a commit nothing
// has judged.
func TestSandboxHealthSaysWhenAMergeHasWaitedForABaseBranchRun(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Half an hour with a merge waiting is a cycle that has not come
	// round yet, not a fault.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: baseTime.Add(-30 * time.Minute), FinishedAt: ptr(baseTime.Add(-5 * time.Minute)),
		Conclusion: "success", PendingTaskID: "task-9",
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v with a merge half an hour old, want none", got)
	}

	finished := baseTime.Add(-9 * time.Hour)
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: baseTime.Add(-10 * time.Hour), FinishedAt: &finished,
		Conclusion: "success", PendingTaskID: "task-9",
	}); err != nil {
		t.Fatal(err)
	}
	got := gatesOf(t, srv)
	if len(got) != 1 || got[0].Reason != "no-run-started" {
		t.Fatalf("baseBranchGates = %+v, want the waiting merge reported", got)
	}
	for _, want := range []string{
		"no base-branch run has been started for it",
		"10h ago, and merges have landed since with nothing judging them",
	} {
		if !strings.Contains(got[0].Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", got[0].Detail, want)
		}
	}
}

// A gate that runs and decides nothing: the shape grain/task-508 went
// looking for across this deployment's repositories. A repository none
// of whose `.grain/workflows` answer `push` gets a base-branch run made
// of no job at all, over in seconds, and nothing it concludes ever holds
// a merge -- so the queue goes on merging onto a branch whose gate has
// never read a line of it. It is reported as unjudged, because that is
// what the branch is.
func TestSandboxHealthSaysWhenABaseBranchRunJudgedNothing(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Twenty minutes old is grain still re-asking: an errored run is
	// retried a few times five minutes apart before grain gives up, and
	// reporting inside that window would alarm about a deployment that
	// is in the middle of sorting itself out.
	errored := "grain CI ran no job at all, so nothing judged this branch: " +
		"this repository declares no workflow, so grain CI ran nothing"
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: baseTime.Add(-20 * time.Minute), FinishedAt: ptr(baseTime.Add(-20 * time.Minute)),
		Conclusion: "errored", Detail: errored, Attempts: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v while grain is still re-asking, want none", got)
	}

	// Six hours later grain has stopped asking and the branch has never
	// been judged by anything.
	started := baseTime.Add(-6 * time.Hour)
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: started, FinishedAt: ptr(started), Conclusion: "errored",
		Detail: errored, Attempts: 4,
	}); err != nil {
		t.Fatal(err)
	}
	got := gatesOf(t, srv)
	if len(got) != 1 {
		t.Fatalf("baseBranchGates = %+v, want the gate that judged nothing reported", got)
	}
	gate := got[0]
	if gate.Judged || gate.Reason != "judged-nothing" || gate.RunID != "run-1" {
		t.Fatalf("gate = %+v, want acme/widgets main reported as judged by nothing", gate)
	}
	if gate.RunInFlight {
		t.Errorf("runInFlight = true, want false: this run finished")
	}
	for _, want := range []string{
		"finished without judging the tree",
		"merging onto it anyway",
		"6h ago",
		"grain CI ran no job at all",
		"answer no `push`",
	} {
		if !strings.Contains(gate.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", gate.Detail, want)
		}
	}

	// A run that did judge the tree takes the finding away, however long
	// ago it was: this reading is about gates that decide nothing, not
	// about branches nobody has pushed to.
	if err := store.PutBaseBranchCI(t.Context(), model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-2",
		StartedAt: started, FinishedAt: ptr(started), Conclusion: "success",
		Detail: "success: 7 job(s)",
	}); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v once a suite has judged the branch, want none", got)
	}
}

// The same finding where there was never a run to judge anything: grain
// asked grain CI for one and was refused, which is what a base-branch job
// list naming jobs no workflow declares any more gets
// (orchestrator.refuseBaseBranchCI, grain/task-509).
//
// The row carries a conclusion and no run id, and this reading has to
// reach it: before the refusal was written down there was no row to read
// at all, and the reconciler asked, was refused and logged it once a
// cycle for ever with the queue merging throughout.
func TestSandboxHealthSaysWhenGrainCouldNotEvenStartABaseBranchRun(t *testing.T) {
	t.Parallel()
	srv, store := gateServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	refused := "grain CI could not be asked for a run: " +
		`ci: selected job "go" does not exist in the matching workflows`
	// Inside the window grain is still re-asking in, this is a deployment
	// sorting itself out rather than a finding.
	at := baseTime.Add(-20 * time.Minute)
	row := model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", StartedAt: at, FinishedAt: &at,
		Conclusion: "errored", Detail: refused, Attempts: 2,
	}
	if err := store.PutBaseBranchCI(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if got := gatesOf(t, srv); len(got) != 0 {
		t.Fatalf("baseBranchGates = %+v while grain is still re-asking, want none", got)
	}

	at = baseTime.Add(-6 * time.Hour)
	row.StartedAt, row.FinishedAt, row.Attempts = at, &at, 4
	if err := store.PutBaseBranchCI(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	got := gatesOf(t, srv)
	if len(got) != 1 {
		t.Fatalf("baseBranchGates = %+v, want the gate grain could not start reported", got)
	}
	gate := got[0]
	if gate.Judged || gate.Reason != "judged-nothing" {
		t.Fatalf("gate = %+v, want acme/widgets main reported as judged by nothing", gate)
	}
	if gate.RunID != "" || gate.RunInFlight {
		t.Errorf("gate = %+v, want no run named: there was never one to name", gate)
	}
	for _, want := range []string{
		"could not start a base-branch run for acme/widgets main",
		"merging onto it anyway",
		"No base-branch run has ever been started for main",
		`selected job "go" does not exist`,
		"job list naming jobs that no longer exist",
	} {
		if !strings.Contains(gate.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", gate.Detail, want)
		}
	}
	// The sentence must not send a reader after a run that never
	// existed, which is what the finding for a run that did start says.
	if strings.Contains(gate.Detail, "finished without judging") {
		t.Errorf("detail = %q, want it to say the run was refused rather than that it finished",
			gate.Detail)
	}
}

// A gate that judged nothing outranks one that is merely late, so a
// repository with both reports the one a reader has to act on.
func TestTheReleasePaneRanksAGateThatJudgedNothingFirst(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	started := baseTime.Add(-6 * time.Hour)

	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "release-1.2", RunID: "run-1",
		StartedAt: baseTime.Add(-30 * time.Hour), FinishedAt: ptr(started),
		Conclusion: "success", PendingTaskID: "task-9",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "run-2",
		StartedAt: started, FinishedAt: ptr(started), Conclusion: "errored",
		Detail: "grain CI ran no job at all, so nothing judged this branch",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := client.RepoReleaseSettings(ctx, repo.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseBranchGate == nil || got.BaseBranchGate.Branch != "main" ||
		got.BaseBranchGate.Reason != "judged-nothing" {
		t.Fatalf("baseBranchGate = %+v, want main's gate that judged nothing", got.BaseBranchGate)
	}
}

// The repository's own release pane carries the same finding. That pane
// shows the settings, and a setting reads the same on the day it was
// chosen and on the second day of merging onto an untested tree; this is
// the consequence, and above all it is how long it has been true.
func TestTheReleasePaneSaysWhatHasBecomeOfTheBaseBranchCheck(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	srv := ui.NewServerWithClient(client)

	last := baseTime.Add(-33 * time.Hour)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: last, FinishedAt: &last, Conclusion: "success",
		PendingTaskID: "task-9",
	}); err != nil {
		t.Fatal(err)
	}

	got := decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet,
		"/api/repos/acme/widgets/release", ""))
	if got.BaseBranchGate == nil {
		t.Fatal("baseBranchGate = nil, want the pane to carry the finding")
	}
	if got.BaseBranchGate.Reason != "no-run-started" || got.BaseBranchGate.Branch != "main" {
		t.Fatalf("baseBranchGate = %+v, want main's unjudged merge", got.BaseBranchGate)
	}
	if !strings.Contains(got.BaseBranchGate.Detail, "33h ago") {
		t.Errorf("detail = %q, want it to say how long this has been true",
			got.BaseBranchGate.Detail)
	}

	// And once a run has judged the tip, the pane says nothing at all.
	judged := baseTime.Add(-10 * time.Minute)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "9f21ab0", RunID: "run-2",
		StartedAt: judged, FinishedAt: &judged, Conclusion: "success",
		MergedTaskID: "task-9",
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[ui.RepoReleaseSettings](t, do(t, srv, http.MethodGet,
		"/api/repos/acme/widgets/release", ""))
	if got.BaseBranchGate != nil {
		t.Fatalf("baseBranchGate = %+v on a judged branch, want nil", got.BaseBranchGate)
	}
}

// The inbox's own reading (grain/task-470). The two surfaces above are
// the System pane and one repository's settings, and neither is a place
// somebody goes without already suspecting something; the inbox is where
// this deployment says "somebody has to deal with this", and the rail
// counts what is on it in every view.
//
// So it needs a route of its own. The health response this reading rides
// on probes every live guest to answer, which is why nothing polls it --
// and a finding the app cannot afford to ask for is one the inbox cannot
// carry. This is three store reads and no probe.
func TestTheInboxCanReadTheBaseBranchGatesOnTheirOwn(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// A deployment whose gates are all working answers with an empty
	// list rather than with null: the inbox iterates it, and "nothing is
	// wrong" is the ordinary day.
	got := decode[ui.BaseBranchGates](t, do(t, srv, http.MethodGet,
		"/api/base-branch-gates", ""))
	if got.Gates == nil || len(got.Gates) != 0 {
		t.Fatalf("gates = %+v on a healthy deployment, want an empty list", got.Gates)
	}

	last := baseTime.Add(-33 * time.Hour)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "run-1",
		StartedAt: last, FinishedAt: &last, Conclusion: "success",
		PendingTaskID: "task-9",
	}); err != nil {
		t.Fatal(err)
	}
	got = decode[ui.BaseBranchGates](t, do(t, srv, http.MethodGet,
		"/api/base-branch-gates", ""))
	if len(got.Gates) != 1 {
		t.Fatalf("gates = %+v, want main's unjudged merge", got.Gates)
	}
	gate := got.Gates[0]
	if gate.Repo != repo.String() || gate.Branch != "main" ||
		gate.Reason != "no-run-started" {
		t.Fatalf("gate = %+v, want acme/widgets main reported with no run started", gate)
	}
	// The row's whole substance is the sentence, since there is nothing
	// here to press: it has to name the branch, say the queue is merging
	// onto it anyway, and say how long that has been true.
	for _, want := range []string{
		"acme/widgets main",
		"merging onto a commit nothing has judged",
		"33h ago",
	} {
		if !strings.Contains(gate.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", gate.Detail, want)
		}
	}
}
