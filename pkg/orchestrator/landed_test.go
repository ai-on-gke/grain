package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// gke-labs/granule task 147, replayed against a real bare repository: a
// branch whose work a human squash-merged onto its base by hand, which
// gives every commit on the base a different SHA and leaves the branch
// "ahead" by commit for ever after. Each test below is one of the places
// grain used to read that as work still to be done.

// squashOnto squash-merges branch onto base in bare, the way GitHub's
// "Squash and merge" button does, and returns the squash commit.
func squashOnto(t *testing.T, bare, branch, base, message string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", "--branch", base, bare, "work")
	wd := filepath.Join(dir, "work")
	run(t, wd, "git", "config", "user.email", "human@example.com")
	run(t, wd, "git", "config", "user.name", "human")
	run(t, wd, "git", "fetch", "-q", "origin", branch)
	run(t, wd, "git", "merge", "-q", "--squash", "FETCH_HEAD")
	run(t, wd, "git", "commit", "-q", "-m", message)
	run(t, wd, "git", "push", "-q", "origin", base)
	return revParse(t, bare, base)
}

// mergeBaseInto merges base into branch in bare, the way a merge-queue
// repair or refresh does.
func mergeBaseInto(t *testing.T, bare, branch, base string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, "work")
	wd := filepath.Join(dir, "work")
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	run(t, wd, "git", "merge", "-q", "--no-edit", "origin/"+base)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

func revParse(t *testing.T, bare, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// landedBranch is task 147's branch: two commits of real work, squash-
// merged onto main, with main moving on afterwards. It returns the
// squash commit.
func landedBranch(t *testing.T, sim *githubsim.Sim, branch string) string {
	t.Helper()
	pushBranch(t, sim.BareRepo, branch)
	pushFileToBranch(t, sim.BareRepo, branch, "dns.go", "no workarounds", "Remove dns workarounds")
	pushFileToBranch(t, sim.BareRepo, branch, "dns_test.go", "tested", "Test the dns path")
	sha := squashOnto(t, sim.BareRepo, branch, "main", "Remove dns workarounds (#69)")
	pushFileToBranch(t, sim.BareRepo, "main", "OTHER.md", "unrelated", "Unrelated work on main")
	return sha
}

func linkPullRequest(t *testing.T, ctx context.Context, store *model.Store, task *model.Task, ref model.PullRequestRef) {
	t.Helper()
	task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: ref.String()})
	if err := store.PutTask(ctx, *task); err != nil {
		t.Fatal(err)
	}
}

func openPullRequestsFor(sim *githubsim.Sim, branch string) int {
	n := 0
	for _, pr := range sim.PullRequests {
		if pr.Head == branch && pr.State == "open" {
			n++
		}
	}
	return n
}

// The run's open_pull_request on task 147's branch, after the squash:
// nothing is opened, and the run and the task are both told where the
// work went -- by the task's own merged pull request when it has one.
func TestOpenPullRequestRefusesABranchSquashMergedOntoItsBase(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	squash := landedBranch(t, sim, branch)

	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number: 69, Head: branch, Base: "main", State: "closed", Merged: true, MergeCommitSHA: squash,
	})
	linkPullRequest(t, ctx, store, &task, model.PullRequestRef{Repo: repo, Number: 69})

	_, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err == nil {
		t.Fatal("OpenPullRequestForTask opened a pull request for work already on main")
	}
	want := "already landed on main as " + squash[:12] + " (acme/widgets#69); nothing to open"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to say %q", err, want)
	}
	if n := openPullRequestsFor(sim, branch); n != 0 {
		t.Fatalf("%d pull request(s) open for %s, want none", n, branch)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], want) {
		t.Fatalf("task comments = %q, want the finding recorded once", bodies)
	}
}

// The same branch with no merged pull request to point at: the content
// alone decides, and the commits main gained are named instead.
func TestOpenPullRequestRefusesASquashLandedBranchByContentAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	squash := landedBranch(t, sim, branch)

	_, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err == nil || !strings.Contains(err.Error(), "already landed on main") {
		t.Fatalf("error = %v, want the branch reported as already landed", err)
	}
	if !strings.Contains(err.Error(), squash[:12]) {
		t.Fatalf("error = %q, want it to name the squash commit %s", err, squash[:12])
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("pull requests = %+v, want none", sim.PullRequests)
	}
}

// Part of the branch is on main and part is not: the pull request opens
// as before, and says which part already landed.
func TestOpenPullRequestSaysWhichPartOfABranchAlreadyLanded(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	pushFileToBranch(t, sim.BareRepo, branch, "a.txt", "landed", "Change a")
	pushFileToBranch(t, sim.BareRepo, branch, "b.txt", "not landed", "Change b")
	pushFileToBranch(t, sim.BareRepo, "main", "a.txt", "landed", "Change a, picked by hand")

	status, err := orchestrator.OpenPullRequestForTask(ctx, store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v", err)
	}
	if n := openPullRequestsFor(sim, branch); n != 1 {
		t.Fatalf("%d pull request(s) open, want one", n)
	}
	for _, want := range []string{"Part of this branch already landed on main", "`a.txt`", "`b.txt`"} {
		if !strings.Contains(status.Note, want) {
			t.Errorf("note = %q, want it to contain %q", status.Note, want)
		}
		if !strings.Contains(sim.PullRequests[0].Body, want) {
			t.Errorf("pull request body does not say %q:\n%s", want, sim.PullRequests[0].Body)
		}
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "`a.txt`") {
		t.Fatalf("task comments = %q, want the partial landing recorded", bodies)
	}
}

// The automatic opening at the end of a run reaches the same finding and
// marks the task done instead of opening a duplicate.
func TestTheFinishPathMarksASquashLandedTaskDone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	landedBranch(t, sim, model.BranchName(task.ID))

	runID := "t1-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if len(sim.PullRequests) != 0 {
		t.Fatalf("pull requests = %+v, want none", sim.PullRequests)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want the task done", st)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) == 0 || !strings.Contains(bodies[len(bodies)-1], "already landed on main") {
		t.Fatalf("task comments = %q, want the finding recorded", bodies)
	}
}

// Task 147's second pull request: a repair merged main into a branch
// whose work had already been squash-merged, leaving an empty diff. The
// queue closes it, saying where the work landed, instead of merging an
// empty merge commit.
func TestARepairThatResolvesToAnEmptyDiffClosesThePullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFileToBranch(t, sim.BareRepo, branch, "dns.go", "no workarounds", "Remove dns workarounds")
	squash := squashOnto(t, sim.BareRepo, branch, "main", "Remove dns workarounds (#69)")
	mergeBaseInto(t, sim.BareRepo, branch, "main")

	repairedAt := baseTime
	doneAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, task.ID, doneAt, func(o *model.Observation) {
		o.MergeQueueRepairAt = &repairedAt
		o.CompletedAt = &doneAt
	}); err != nil {
		t.Fatal(err)
	}
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("success")}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	pr := sim.PullRequests[0]
	if pr.Merged || pr.State != "closed" {
		t.Fatalf("pull request state=%q merged=%v, want closed without merging", pr.State, pr.Merged)
	}
	if tip := revParse(t, sim.BareRepo, "main"); tip != squash {
		t.Fatalf("main moved to %s: something was merged", tip)
	}
	comments := sim.Comments[pr.Number]
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, squash[:12]) {
		t.Fatalf("pull request comments = %+v, want one naming %s", comments, squash[:12])
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want the task done", st)
	}
}

// The same empty diff reaching the merge arm with no repair behind it --
// a branch somebody else caught up by hand -- is closed there too.
func TestTheQueueNeverMergesAnEmptyDiff(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFileToBranch(t, sim.BareRepo, branch, "dns.go", "no workarounds", "Remove dns workarounds")
	squash := squashOnto(t, sim.BareRepo, branch, "main", "Remove dns workarounds (#69)")
	mergeBaseInto(t, sim.BareRepo, branch, "main")
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if pr := sim.PullRequests[0]; pr.Merged || pr.State != "closed" {
		t.Fatalf("pull request state=%q merged=%v, want closed without merging", pr.State, pr.Merged)
	}
	if tip := revParse(t, sim.BareRepo, "main"); tip != squash {
		t.Fatalf("main moved to %s: an empty merge commit landed", tip)
	}
}

// Close, reopen, and the pull request still open from the task's branch
// is the task's again -- linked, and said so on both sides.
func TestReopeningATaskReattachesItsOpenPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}

	closedAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, task.ID, closedAt, func(o *model.Observation) { o.ClosedAt = &closedAt }); err != nil {
		t.Fatal(err)
	}
	reopenedAt := baseTime.Add(2 * time.Minute)
	if err := store.ObserveField(ctx, task.ID, reopenedAt, func(o *model.Observation) { o.ClosedAt = nil }); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := orchestrator.ReattachPullRequest(ctx, store, client, *reopened, true, reopenedAt)
	if err != nil {
		t.Fatalf("ReattachPullRequest: %v", err)
	}
	want := model.PullRequestRef{Repo: repo, Number: pr.Number}
	if got == nil || *got != want {
		t.Fatalf("re-attached %v, want %s", got, want)
	}
	after, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, l := range after.Links {
		if l.Kind == model.LinkFixes && l.Target == want.String() {
			linked++
		}
	}
	if linked != 1 {
		t.Fatalf("links = %+v, want %s linked once", after.Links, want)
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 || !strings.Contains(bodies[0], want.String()) {
		t.Fatalf("task comments = %q, want one saying %s is attached again", bodies, want)
	}
	if comments := sim.Comments[pr.Number]; len(comments) != 1 {
		t.Fatalf("pull request comments = %+v, want one saying grain is watching it again", comments)
	}

	// A run starting on the task afterwards finds it already attached and
	// says nothing more.
	if _, err := orchestrator.ReattachPullRequest(ctx, store, client, *after, false, reopenedAt); err != nil {
		t.Fatal(err)
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 {
		t.Fatalf("task comments = %q, want still one", bodies)
	}
}

// A branch whose work was squash-merged onto main without main ever being
// merged back into the branch still has ChangedFiles > 0 in GitHub's
// three-dot diff; the queue still recognizes the blobs on main and closes
// the pull request instead of merging it.
func TestTheQueueClosesALandedBranchEvenWhenThreeDotDiffReportsChangedFiles(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFileToBranch(t, sim.BareRepo, branch, "dns.go", "no workarounds", "Remove dns workarounds")
	squash := squashOnto(t, sim.BareRepo, branch, "main", "Remove dns workarounds (#69)")
	pushFileToBranch(t, sim.BareRepo, "main", "OTHER.md", "unrelated", "Unrelated work on main")
	mainTip := revParse(t, sim.BareRepo, "main")

	sim.PullRequests[0].ChangedFiles = 1
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("success")}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	pr := sim.PullRequests[0]
	if pr.Merged || pr.State != "closed" {
		t.Fatalf("pull request state=%q merged=%v, want closed without merging", pr.State, pr.Merged)
	}
	if tip := revParse(t, sim.BareRepo, "main"); tip != mainTip {
		t.Fatalf("main moved to %s, want %s", tip, mainTip)
	}
	comments := sim.Comments[pr.Number]
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, squash[:7]) {
		t.Fatalf("pull request comments = %+v, want one naming %s", comments, squash[:7])
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want %q", st, model.StateClosed)
	}
}

// Replay of task 189 (bwsalmon/grain#4385): a port pull request targeting
// candidate is stuck in merge_failed / conflicted with ChangedFiles > 0, and
// candidate is subsequently fast-forwarded to main (which includes both the
// ported commit and later commits to the same file). SyncPullRequests closes
// the obsolete port pull request, marks the port task done, and clears its
// blocked merge-queue state.
func TestSyncPullRequestsClosesObsoletePortPRWhenBaseAdvances(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Cut candidate and the port branch from the initial main commit before
	// main advances, without pushBranch's synthetic empty commit.
	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "branch", "candidate", "main")
	portTask := filedTask(t, ctx, store, "t-port", repo)
	portBranch := model.BranchName(portTask.ID)
	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "branch", portBranch, "candidate")

	// Source task lands on main (PR #4381), followed by another commit to
	// the same file on main so the final blob on main/candidate differs
	// from the cherry-pick's blob on the port branch.
	srcTask := filedTask(t, ctx, store, "t-src", repo)
	pushFileToBranch(t, sim.BareRepo, "main", "app.go", "v1-fixed", "fix: patch badge (#4381)")
	srcSHA := revParse(t, sim.BareRepo, "main")
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number: 4381, Head: model.BranchName(srcTask.ID),
		Base: "main", State: "closed", Merged: true, MergeCommitSHA: srcSHA,
	})
	linkPullRequest(t, ctx, store, &srcTask, model.PullRequestRef{Repo: repo, Number: 4381})
	pushFileToBranch(t, sim.BareRepo, "main", "app.go", "v2-refactored-with-fix", "feat: rework layout")

	// Port task branches from pre-promotion candidate, cherry-picks the fix,
	// and gets stuck in merge_failed / conflicted with ChangedFiles > 0.
	portTask.Title = "Port acme/widgets#4381 to candidate"
	portTask.Branch = "candidate"
	portTask.AutoMerge = true
	portTask.Links = []model.Link{
		{Kind: model.LinkPortOf, Target: srcTask.ID},
		{Kind: model.LinkFixes, Target: "acme/widgets#4385"},
	}
	if err := store.PutTask(ctx, portTask); err != nil {
		t.Fatal(err)
	}
	pushFileToBranch(t, sim.BareRepo, portBranch, "app.go", "v1-fixed-on-candidate",
		"fix: patch badge\n\nBackported-from: "+srcSHA)
	mergeable := false
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number:       4385,
		Title:        portTask.Title,
		Head:         portBranch,
		Base:         "candidate",
		State:        "open",
		Mergeable:    &mergeable,
		ChangedFiles: 1,
	})
	doneAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, portTask.ID, doneAt, func(o *model.Observation) {
		o.CompletedAt = &doneAt
		o.MergeQueueState = "merge_failed"
		o.MergeQueueBlockedAt = &doneAt
		o.MergeQueueBlockedBase = "candidate"
	}); err != nil {
		t.Fatal(err)
	}

	// Fast-forward candidate to main (release promotion).
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", "--branch", "main", sim.BareRepo, "work")
	run(t, filepath.Join(dir, "work"), "git", "push", "-q", "origin", "HEAD:refs/heads/candidate")
	candidateTip := revParse(t, sim.BareRepo, "candidate")

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	var portPR githubsim.PullRequest
	for _, pr := range sim.PullRequests {
		if pr.Number == 4385 {
			portPR = pr
		}
	}
	if portPR.Merged || portPR.State != "closed" {
		t.Fatalf("port pull request state=%q merged=%v, want closed without merging", portPR.State, portPR.Merged)
	}
	if tip := revParse(t, sim.BareRepo, "candidate"); tip != candidateTip {
		t.Fatalf("candidate moved to %s, want %s", tip, candidateTip)
	}
	comments := sim.Comments[4385]
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "acme/widgets#4381") {
		t.Fatalf("pull request comments = %+v, want one naming acme/widgets#4381", comments)
	}
	st, err := store.State(ctx, portTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want %q", st, model.StateClosed)
	}
	obs, err := store.GetObservation(ctx, portTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueState != "" || obs.MergeQueueBlockedAt != nil {
		t.Fatalf("observation queue state=%q blockedAt=%v, want both cleared",
			obs.MergeQueueState, obs.MergeQueueBlockedAt)
	}
	states, err := store.MergeQueueStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := states[portTask.ID]; ok {
		t.Fatalf("MergeQueueStates[%s] = %+v, want omitted once closed", portTask.ID, got)
	}
}

// commitFilesToBranch commits files (path -> content) to branch in bare
// as one commit, the way pushFileToBranch does one file.
func commitFilesToBranch(t *testing.T, bare, branch string, files map[string]string, message string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, "work")
	wd := filepath.Join(dir, "work")
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(wd, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, wd, "git", "add", "-A")
	run(t, wd, "git", "commit", "-q", "-m", message)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// Task 189 (bwsalmon/grain#4385), in the shape the content check cannot
// answer: a port blocked against candidate, whose change is bigger than
// GitHub's compare will list, when candidate is synced with main in an RC
// cut that changes one of the ported files again. Its source task has no
// merged pull request the queue can find either. What still answers is
// the port's own footer: the commit it cherry-picks is on candidate now,
// so the pull request is closed as landed rather than left in
// merge_failed for ever.
func TestTheQueueClosesAPortWhoseCherryPickedCommitsAreAllOnItsBase(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "branch", "candidate", "main")
	portTask := filedTask(t, ctx, store, "t-port", repo)
	portBranch := model.BranchName(portTask.ID)
	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "branch", portBranch, "candidate")

	// The source: one commit on main touching more files than a compare
	// lists, and main changing one of them again afterwards.
	change := map[string]string{"app.go": "v1-fixed"}
	for i := 0; i < 310; i++ {
		change[fmt.Sprintf("gen%03d.txt", i)] = "generated"
	}
	srcTask := filedTask(t, ctx, store, "t-src", repo)
	commitFilesToBranch(t, sim.BareRepo, "main", change, "fix: regenerate everything (#4381)")
	srcSHA := revParse(t, sim.BareRepo, "main")
	pushFileToBranch(t, sim.BareRepo, "main", "app.go", "v2-refactored-with-fix", "feat: rework layout")

	portTask.Title = "Port acme/widgets#4381 to candidate"
	portTask.Branch = "candidate"
	portTask.AutoMerge = true
	portTask.Links = []model.Link{
		{Kind: model.LinkPortOf, Target: srcTask.ID},
		{Kind: model.LinkFixes, Target: "acme/widgets#4385"},
	}
	if err := store.PutTask(ctx, portTask); err != nil {
		t.Fatal(err)
	}
	commitFilesToBranch(t, sim.BareRepo, portBranch, change,
		"fix: regenerate everything\n\n(cherry picked from commit "+srcSHA+")")
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number:       4385,
		Title:        portTask.Title,
		Head:         portBranch,
		Base:         "candidate",
		State:        "open",
		ChangedFiles: len(change),
	})
	doneAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, portTask.ID, doneAt, func(o *model.Observation) {
		o.CompletedAt = &doneAt
		o.MergeQueueState = "merge_failed"
		o.MergeQueueBlockedAt = &doneAt
	}); err != nil {
		t.Fatal(err)
	}

	// The RC cut: candidate takes main.
	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "update-ref", "refs/heads/candidate",
		revParse(t, sim.BareRepo, "main"))
	candidateTip := revParse(t, sim.BareRepo, "candidate")

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	var portPR githubsim.PullRequest
	for _, pr := range sim.PullRequests {
		if pr.Number == 4385 {
			portPR = pr
		}
	}
	if portPR.Merged || portPR.State != "closed" {
		t.Fatalf("port pull request state=%q merged=%v, want closed without merging", portPR.State, portPR.Merged)
	}
	if tip := revParse(t, sim.BareRepo, "candidate"); tip != candidateTip {
		t.Fatalf("candidate moved to %s, want %s", tip, candidateTip)
	}
	comments := sim.Comments[4385]
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, srcSHA[:7]) {
		t.Fatalf("pull request comments = %+v, want one naming %s", comments, srcSHA[:7])
	}
	st, err := store.State(ctx, portTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want %q", st, model.StateClosed)
	}
}

// The other side of the same check: a port carrying a commit of its own
// that names no source -- a fix-up an agent wrote on top of the
// cherry-pick -- has work the base does not, and is left open.
func TestTheQueueKeepsAPortWithACommitOfItsOwn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "branch", "candidate", "main")
	portTask := filedTask(t, ctx, store, "t-port", repo)
	portBranch := model.BranchName(portTask.ID)
	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "branch", portBranch, "candidate")
	srcTask := filedTask(t, ctx, store, "t-src", repo)
	pushFileToBranch(t, sim.BareRepo, "main", "app.go", "v1-fixed", "fix: patch badge (#4381)")
	srcSHA := revParse(t, sim.BareRepo, "main")
	pushFileToBranch(t, sim.BareRepo, "main", "app.go", "v2-refactored-with-fix", "feat: rework layout")

	portTask.Branch = "candidate"
	portTask.Links = []model.Link{
		{Kind: model.LinkPortOf, Target: srcTask.ID},
		{Kind: model.LinkFixes, Target: "acme/widgets#4385"},
	}
	if err := store.PutTask(ctx, portTask); err != nil {
		t.Fatal(err)
	}
	pushFileToBranch(t, sim.BareRepo, portBranch, "app.go", "v1-fixed-on-candidate",
		"fix: patch badge\n\nBackported-from: "+srcSHA)
	pushFileToBranch(t, sim.BareRepo, portBranch, "extra.go", "candidate only", "fix: candidate-only follow-up")
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number: 4385, Head: portBranch, Base: "candidate", State: "open", ChangedFiles: 2,
	})
	doneAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, portTask.ID, doneAt, func(o *model.Observation) {
		o.CompletedAt = &doneAt
		o.MergeQueueState = "merge_failed"
		o.MergeQueueBlockedAt = &doneAt
	}); err != nil {
		t.Fatal(err)
	}
	run(t, sim.BareRepo, "git", "--git-dir", sim.BareRepo, "update-ref", "refs/heads/candidate",
		revParse(t, sim.BareRepo, "main"))

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	for _, pr := range sim.PullRequests {
		if pr.Number == 4385 && pr.State != "open" {
			t.Fatalf("port pull request state=%q, want it left open", pr.State)
		}
	}
}
