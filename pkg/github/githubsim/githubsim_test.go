package githubsim

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// initBareRepo creates the bare repository at path -- what the tests
// here clone and push to -- on branch, and returns the path.
//
// -b branch, not the default-named branch renamed after the fact: a bare
// repo's own HEAD symref is set once at init and a later push of a
// differently-named branch does not move it, which used to leave HEAD
// pointing at a branch that was never pushed at all -- harmless as long
// as nothing ever cloned this repo for real, which changed once
// mergeIntoBase started doing exactly that.
//
// The two config settings are what keep a push from outliving the test
// that made it: git runs auto-maintenance on the receiving side of a
// push, and since v2.47 it detaches, so the repacking goes on in the
// background while t.TempDir deletes the tree underneath it, and the
// deletion fails on files that land after RemoveAll has walked past them
// ("TempDir RemoveAll cleanup: unlinkat ...: directory not empty").
// Saying autoDetach=false does not turn the maintenance off, it runs it
// in the foreground, so the push that triggered it is what waits for it.
// pkg/cli/bare_remote_test.go's initBareRepo has the whole
// explanation, including why both keys are set and why nothing on a
// runner with git 2.39 can see any of this; this is that helper
// restated, because this codebase duplicates a small test helper across
// packages rather than sharing one (tests/e2e/harness_test.go's own
// comment on why).
func initBareRepo(t *testing.T, path, branch string) string {
	t.Helper()
	run(t, filepath.Dir(path), "git", "init", "--bare", "-q", "-b", branch, path)
	run(t, path, "git", "config", "maintenance.autoDetach", "false")
	run(t, path, "git", "config", "gc.autoDetach", "false")
	return path
}

// setupBareRepoWithBranch creates a real bare git repo (the same rig
// gitproxy/live_test.go builds) with one commit on branch -- what
// BranchExists checks against for real, rather than trusting a canned
// answer.
func setupBareRepoWithBranch(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	bare := initBareRepo(t, filepath.Join(dir, "repo.git"), branch)

	seed := filepath.Join(dir, "seed")
	run(t, dir, "git", "clone", "-q", bare, seed)
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	run(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "seed")
	run(t, seed, "git", "push", "-q", "origin", branch)
	return bare
}

// pushBranch pushes an empty commit on branch straight to bare, standing
// in for a real dispatch's own git push -- restated here (package-private,
// deliberately duplicated -- see tests/e2e/harness_test.go's own
// comment on why) from pkg/orchestrator's own helper of the same name,
// since a Sim test that merges a pull request now needs the head branch
// to actually exist for mergeIntoBase to merge.
func pushBranch(t *testing.T, bare, branch string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	run(t, wd, "git", "checkout", "-q", "-b", branch)
	// Named after the branch, so two branches pushed off the same base in
	// one test never share a commit sha. An empty commit is hashed out of
	// its base, its author, its message and a timestamp git only resolves
	// to the second, so a fixed message made both of them the same object.
	// That was harmless while nothing here resolved a branch to a sha;
	// Sim now resolves a branch name and a commit sha to one commit
	// through BareRepo and keys Sim.CheckRuns by what it resolved, so a
	// test seeding check runs for one branch would be seeding them for
	// every other branch it had pushed in the same second.
	// pkg/orchestrator's own pushBranch carries this line for the same
	// reason.
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", "agent commit on "+branch)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// pushAnotherCommit lands one more commit on a branch that already
// exists -- pushBranch above creates one, this one moves it, which is
// what a push arriving while grain is mid-cycle does.
func pushAnotherCommit(t *testing.T, bare, branch string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	// Named after the branch for pushBranch's reason above, which reaches
	// this helper too: distinct tips are what keep two of these commits
	// apart, and a merge the sim has just performed leaves the base
	// branch and the branch it merged sharing one tip.
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", "a later push to "+branch)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// The helper's own contract, checked rather than trusted: every test
// below that seeds Sim.CheckRuns for one branch depends on no other
// branch it pushed answering to the same sha, and back-to-back pushes are
// exactly the case a fixed commit message collapses into one object.
func TestPushBranchGivesEachBranchItsOwnSHA(t *testing.T) {
	sim, _ := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pushBranch(t, sim.BareRepo, "grain/task-2")

	first, second := sim.branchSHA("grain/task-1"), sim.branchSHA("grain/task-2")
	if first == "" || second == "" {
		t.Fatalf("both branches should be on the remote: %q, %q", first, second)
	}
	if first == second {
		t.Fatalf("two branches share a commit sha (%s): whichever one a test seeds check runs for, it seeds them for both", first)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// newSim wires a Sim in behind a real github.RESTClient -- the exact seam
// a live end-to-end test uses, and what proves RESTClient's own logic
// (path building, status handling, JSON field extraction) runs unmodified
// against Sim, the same as it would against github.FakeTransport or the
// real network.
func newSim(t *testing.T, branch string) (*Sim, *github.RESTClient) {
	bare := setupBareRepoWithBranch(t, branch)
	sim := New("acme", "widgets", bare, branch)
	client := github.NewClient(sim, nil)
	return sim, client
}

func TestSimListIssuesFiltersByLabel(t *testing.T) {
	sim, client := newSim(t, "main")
	sim.Issues[1] = &Issue{Title: "needs a fix", Body: "do the thing", Labels: map[string]struct{}{"grain-agent": {}}}
	sim.Issues[2] = &Issue{Title: "unrelated", Body: "nothing to see", Labels: map[string]struct{}{}}

	issues, err := client.ListIssues("acme", "widgets", "grain-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 1 || issues[0].Title != "needs a fix" {
		t.Fatalf("got %+v", issues)
	}
}

func TestSimGetIssueReturns404ForAnUnknownIssue(t *testing.T) {
	_, client := newSim(t, "main")
	if _, err := client.GetIssue("acme", "widgets", 99); err == nil {
		t.Fatal("expected an error for an unseeded issue")
	}
}

func TestSimAddAndRemoveLabelMutateTheSeededIssue(t *testing.T) {
	sim, client := newSim(t, "main")
	sim.Issues[1] = &Issue{Title: "t", Body: "b", Labels: map[string]struct{}{"grain-agent": {}}}

	if err := client.AddLabel("acme", "widgets", 1, "grain-agent-in-progress"); err != nil {
		t.Fatal(err)
	}
	issue, err := client.GetIssue("acme", "widgets", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !issue.HasLabel("grain-agent") || !issue.HasLabel("grain-agent-in-progress") {
		t.Fatalf("got labels %+v", issue.Labels)
	}

	if err := client.RemoveLabel("acme", "widgets", 1, "grain-agent"); err != nil {
		t.Fatal(err)
	}
	issue, err = client.GetIssue("acme", "widgets", 1)
	if err != nil {
		t.Fatal(err)
	}
	if issue.HasLabel("grain-agent") || !issue.HasLabel("grain-agent-in-progress") {
		t.Fatalf("got labels %+v", issue.Labels)
	}
}

func TestSimDefaultBranchReadsTheConfiguredDefault(t *testing.T) {
	_, client := newSim(t, "trunk")
	branch, err := client.DefaultBranch("acme", "widgets")
	if err != nil || branch != "trunk" {
		t.Fatalf("branch=%q err=%v", branch, err)
	}
}

func TestSimEmptyRepoAdoptsFirstPushedBranchAndAllowsUpdatingDefaultBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := initBareRepo(t, filepath.Join(t.TempDir(), "repo.git"), "main")
	sim := New("acme", "widgets", bare, "main")
	client := github.NewClient(sim, nil)

	pushBranch(t, bare, "grain/task-1")

	def, err := client.DefaultBranch("acme", "widgets")
	if err != nil || def != "grain/task-1" {
		t.Fatalf("DefaultBranch after first push to empty repo = %q, %v; want grain/task-1", def, err)
	}

	if err := client.SetDefaultBranch("acme", "widgets", "main"); err == nil {
		t.Fatal("SetDefaultBranch to non-existent branch succeeded; want 422")
	}

	head, err := client.GetBranchHead("acme", "widgets", "grain/task-1")
	if err != nil || head == nil {
		t.Fatalf("GetBranchHead: %+v, %v", head, err)
	}
	if err := client.CreateBranch("acme", "widgets", "main", head.SHA); err != nil {
		t.Fatalf("CreateBranch(main): %v", err)
	}
	if err := client.SetDefaultBranch("acme", "widgets", "main"); err != nil {
		t.Fatalf("SetDefaultBranch(main): %v", err)
	}
	def, err = client.DefaultBranch("acme", "widgets")
	if err != nil || def != "main" {
		t.Fatalf("DefaultBranch after SetDefaultBranch = %q, %v; want main", def, err)
	}
}

func TestSimBranchExistsChecksTheRealBareRepo(t *testing.T) {
	sim, client := newSim(t, "main")

	// A real commit landed on "main" by the setup above -- BranchExists
	// must answer true from the real repo, not from any bookkeeping Sim
	// itself keeps.
	exists, err := client.BranchExists("acme", "widgets", "main")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}

	exists, err = client.BranchExists("acme", "widgets", "grain/issue-1")
	if err != nil || exists {
		t.Fatalf("expected grain/issue-1 not to exist yet: exists=%v err=%v", exists, err)
	}

	// Push a real branch straight to the bare repo (standing in for an
	// agent's own push through a real git proxy, which githubsim itself
	// doesn't model) and confirm Sim now reports it as real too.
	seed := t.TempDir()
	run(t, seed, "git", "clone", "-q", sim.BareRepo, seed)
	run(t, seed, "git", "config", "user.email", "agent@example.com")
	run(t, seed, "git", "config", "user.name", "agent")
	run(t, seed, "git", "checkout", "-q", "-b", "grain/issue-1")
	run(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "fix")
	run(t, seed, "git", "push", "-q", "origin", "grain/issue-1")

	exists, err = client.BranchExists("acme", "widgets", "grain/issue-1")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}

func TestSimGetBranchHeadReadsTheRealCommit(t *testing.T) {
	sim, client := newSim(t, "main")

	head, err := client.GetBranchHead("acme", "widgets", "main")
	if err != nil || head == nil {
		t.Fatalf("head=%+v err=%v", head, err)
	}
	if head.SHA == "" {
		t.Fatal("expected a real sha, got empty")
	}
	if head.Message != "seed" {
		t.Fatalf("got message %q, want %q", head.Message, "seed")
	}

	shaOut, err := exec.Command("git", "--git-dir", sim.BareRepo, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(shaOut)); head.SHA != want {
		t.Fatalf("got sha %q, want %q (the real repo's own tip)", head.SHA, want)
	}

	missing, err := client.GetBranchHead("acme", "widgets", "no-such-branch")
	if err != nil || missing != nil {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
}

// ListBranches is what grain's UI checks a typed base branch against;
// Sim answers it from the bare repo's real refs, so a branch pushed
// after the Sim was built shows up in the next listing.
func TestSimListBranchesReadsTheRealRefs(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "release/1.0")

	names, err := client.ListBranches("acme", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names, ","), "main,release/1.0"; got != want {
		t.Fatalf("ListBranches = %q, want %q", got, want)
	}

	// A repo this Sim does not stand for is GitHub's own 404, not a
	// panic: a UI asks about whichever repo its form names.
	if _, err := client.ListBranches("acme", "gadgets"); err == nil {
		t.Fatal("ListBranches(acme/gadgets) = nil error, want GitHub's 404")
	}
}

// CreateBranch and UpdateBranch are release management's own
// (bwsalmon/agents#398) git-database calls; Sim answers both against the
// real bare repo the same way branchExists does, so a test proving the
// releases reconciler cuts and promotes for real needs no mock beneath
// them either.
func TestSimCreateBranchCreatesARealRef(t *testing.T) {
	sim, client := newSim(t, "main")
	head, err := client.GetBranchHead("acme", "widgets", "main")
	if err != nil || head == nil {
		t.Fatalf("head=%+v err=%v", head, err)
	}

	if err := client.CreateBranch("acme", "widgets", "release/3.1-rc1", head.SHA); err != nil {
		t.Fatal(err)
	}
	if !sim.branchExists("release/3.1-rc1") {
		t.Fatal("expected release/3.1-rc1 to exist in the real bare repo")
	}
	newHead, err := client.GetBranchHead("acme", "widgets", "release/3.1-rc1")
	if err != nil || newHead == nil || newHead.SHA != head.SHA {
		t.Fatalf("got %+v, want it pinned at main's own tip %q", newHead, head.SHA)
	}

	// Creating it again is a caller mistake, the same 422 real GitHub
	// answers with.
	if err := client.CreateBranch("acme", "widgets", "release/3.1-rc1", head.SHA); err == nil {
		t.Fatal("expected an error creating an already-existing branch")
	}
}

func TestSimUpdateBranchMovesARealRef(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "rc")
	before, err := client.GetBranchHead("acme", "widgets", "rc")
	if err != nil || before == nil {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	mainHead, err := client.GetBranchHead("acme", "widgets", "main")
	if err != nil || mainHead == nil {
		t.Fatalf("mainHead=%+v err=%v", mainHead, err)
	}
	if before.SHA == mainHead.SHA {
		t.Fatal("test setup: rc and main should not already share a tip")
	}

	if err := client.UpdateBranch("acme", "widgets", "rc", mainHead.SHA, true); err != nil {
		t.Fatal(err)
	}
	after, err := client.GetBranchHead("acme", "widgets", "rc")
	if err != nil || after == nil || after.SHA != mainHead.SHA {
		t.Fatalf("got %+v, want rc moved to main's own tip %q", after, mainHead.SHA)
	}

	if err := client.UpdateBranch("acme", "widgets", "no-such-branch", mainHead.SHA, true); err == nil {
		t.Fatal("expected an error moving a branch that doesn't exist")
	}
}

// pushFileToBranch lands one commit writing path on an existing branch,
// which is how a Sim test gives two branches something to genuinely
// conflict over -- pushBranch's own empty commits never can.
func pushFileToBranch(t *testing.T, bare, branch, path, content string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	if err := os.WriteFile(filepath.Join(wd, path), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wd, "git", "add", path)
	run(t, wd, "git", "commit", "-q", "-m", "a change to "+path)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// The merges endpoint the merge queue brings a stale pull request branch
// up to date with (orchestrator.refreshStaleHead). Its three answers are
// what the queue switches on, so Sim answers all three with real git
// rather than bookkeeping: a merge that says it landed has really moved
// the branch, an up-to-date branch is one git says is up to date, and a
// conflict is a merge git really refused.
func TestSimMergeBranchMergesForReal(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pushAnotherCommit(t, sim.BareRepo, "main")

	// Behind: main has moved since the branch was cut, so the merge lands
	// and the branch really contains main afterwards.
	got, err := client.MergeBranch("acme", "widgets", "grain/task-1", "main", "catch up")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Merged || got.SHA == "" {
		t.Fatalf("got %+v, want a merge commit", got)
	}
	head, err := client.GetBranchHead("acme", "widgets", "grain/task-1")
	if err != nil || head == nil || head.SHA != got.SHA {
		t.Fatalf("branch head = %+v, want the merge commit %q the endpoint reported", head, got.SHA)
	}
	if !strings.Contains(head.Message, "catch up") {
		t.Errorf("merge commit message = %q, want the caller's own", head.Message)
	}

	// Up to date: the very same call again, now that the branch contains
	// main, writes nothing at all.
	again, err := client.MergeBranch("acme", "widgets", "grain/task-1", "main", "catch up")
	if err != nil {
		t.Fatalf("an up-to-date branch is an answer, not a failure: %v", err)
	}
	if again.Merged {
		t.Fatalf("got %+v, want nothing merged into a branch that is already up to date", again)
	}
	after, err := client.GetBranchHead("acme", "widgets", "grain/task-1")
	if err != nil || after == nil || after.SHA != head.SHA {
		t.Fatalf("branch head = %+v, want it left at %q", after, head.SHA)
	}
}

func TestSimMergeBranchRefusesARealConflict(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pushFileToBranch(t, sim.BareRepo, "grain/task-1", "CONFIG.md", "setting: from-the-task")
	pushFileToBranch(t, sim.BareRepo, "main", "CONFIG.md", "setting: from-main")

	before, err := client.GetBranchHead("acme", "widgets", "grain/task-1")
	if err != nil || before == nil {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	_, err = client.MergeBranch("acme", "widgets", "grain/task-1", "main", "catch up")
	if !github.IsMergeConflict(err) {
		t.Fatalf("got %v, want a conflict", err)
	}
	if files := github.ConflictFiles(err); len(files) != 1 || files[0] != "CONFIG.md" {
		t.Fatalf("ConflictFiles = %v, want [CONFIG.md]", files)
	}
	after, err := client.GetBranchHead("acme", "widgets", "grain/task-1")
	if err != nil || after == nil || after.SHA != before.SHA {
		t.Fatalf("branch head = %+v, want it untouched at %q by a merge that conflicted", after, before.SHA)
	}
}

// A branch in another repository -- a fork pull request's head -- is a
// 404 here, the same as one that is simply gone, and the caller's cue to
// leave that pull request alone rather than to resolve anything.
func TestSimMergeBranchAnswers404ForAMissingBranch(t *testing.T) {
	_, client := newSim(t, "main")
	_, err := client.MergeBranch("acme", "widgets", "no-such-branch", "main", "")
	var ghErr *github.Error
	if !errors.As(err, &ghErr) || ghErr.Status != 404 {
		t.Fatalf("got %v, want a 404", err)
	}
}

func TestSimCreatePullRequestRecordsThePR(t *testing.T) {
	sim, client := newSim(t, "main")

	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "grain: fix #1", "please review")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 9000 {
		t.Fatalf("got %+v", pr)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("got %+v", sim.PullRequests)
	}
	recorded := sim.PullRequests[0]
	if recorded.Head != "grain/issue-1" || recorded.Base != "main" || recorded.Title != "grain: fix #1" {
		t.Fatalf("got %+v", recorded)
	}

	// A second PR gets a distinct, incrementing number -- v1's own mock
	// numbers pull requests the same way, and nothing here needs a real
	// GitHub-shaped id since this repo never sees a real GitHub.
	pr2, err := client.CreatePullRequest("acme", "widgets", "grain/issue-2", "main", "grain: fix #2", "")
	if err != nil {
		t.Fatal(err)
	}
	if pr2.Number != 9001 {
		t.Fatalf("got %+v", pr2)
	}
}

// Real GitHub refuses a pull request whose base is not a branch, every
// time, and that refusal is worth having here: a task whose base merged
// and was deleted between being filed and being dispatched could never be
// finished, because this one call was declined on every attempt -- and a
// sim that accepted any base at all is why nothing ever caught it.
// orchestrator.pullRequestBase is what answers it now.
func TestSimRefusesAPullRequestAgainstABaseThatIsNotABranch(t *testing.T) {
	_, client := newSim(t, "main")

	_, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "gone", "grain: fix #1", "")
	var ghErr *github.Error
	if !errors.As(err, &ghErr) || ghErr.Status != 422 {
		t.Fatalf("got %v, want a 422 for a base branch that does not exist", err)
	}
	if !strings.Contains(string(ghErr.Body), "base") {
		t.Errorf("body = %q, want GitHub's own complaint about the base", ghErr.Body)
	}
}

// The sibling refusal, and the same argument for having it: real GitHub
// declines a pull request whose head carries nothing its base does not
// already have, every time, so a run that pushed a branch with no commits
// on it could never be finished either -- refused, offered again,
// refused identically. github.IsNoCommitsBetween reads the message this
// answers with, and orchestrator.noteEmptyBranch is what does something
// about it.
func TestSimRefusesAPullRequestWhoseHeadAddsNothingToItsBase(t *testing.T) {
	sim, client := newSim(t, "main")
	// A branch pushed at the base's own tip: no commit of its own, which
	// is what a run that reverted its work or never committed leaves.
	run(t, t.TempDir(), "git", "--git-dir", sim.BareRepo,
		"branch", "grain/task-1", "refs/heads/main")

	_, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "grain: task 1", "")
	var ghErr *github.Error
	if !errors.As(err, &ghErr) || ghErr.Status != 422 {
		t.Fatalf("got %v, want a 422 for a head with no commits over its base", err)
	}
	if !strings.Contains(string(ghErr.Body), "No commits between main and grain/task-1") {
		t.Errorf("body = %q, want GitHub's own words about it", ghErr.Body)
	}
	if !github.IsNoCommitsBetween(err) {
		t.Errorf("IsNoCommitsBetween(%v) = false, want the predicate to recognise this body", err)
	}
	if len(sim.PullRequests) != 0 {
		t.Errorf("pull requests = %+v, want none opened", sim.PullRequests)
	}
}

// And a branch that is genuinely ahead is still opened -- the refusal
// above must not be a blanket one, since every ordinary finish in this
// package's tests goes through the same call.
func TestSimOpensAPullRequestForABranchThatIsAhead(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")

	if _, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "grain: task 1", ""); err != nil {
		t.Fatalf("CreatePullRequest for a branch with a commit on it: %v", err)
	}
	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want the one just opened", sim.PullRequests)
	}
}

func TestSimPanicsOnAnUnhandledRequest(t *testing.T) {
	sim, _ := newSim(t, "main")
	sim.Issues[1] = &Issue{Title: "t", Body: "b", Labels: map[string]struct{}{}}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic for an endpoint Sim doesn't answer")
		}
		if !strings.Contains(r.(string), "unhandled request") {
			t.Fatalf("got panic %v", r)
		}
	}()
	// Asking for a reviewer on a pull request (POST .../pulls/{number}/
	// requested_reviewers) is real GitHub API surface neither Sim nor
	// github.Client implements -- nothing in this project ever requests
	// one -- so this is called directly against Sim rather than through
	// client, which has no method for it. A test exercising an endpoint
	// this double doesn't yet answer for should fail loudly, the same as
	// v1's own RealGitHubMock raising AssertionError.
	sim.Request("POST", "/repos/acme/widgets/pulls/1/requested_reviewers", nil,
		[]byte(`{"reviewers":["someone"]}`))
}

// Closing a pull request without merging it, which is what a human
// closing a task and asking for its pull request to go with it does
// (github.RESTClient.ClosePullRequest). The state moves and nothing else
// does: the branch is still there, at the same commit, and the pull
// request is closed rather than merged -- which is the whole distinction
// the offer to a human rests on.
func TestSimClosesAPullRequestWithoutMergingIt(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/issue-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	head, err := client.GetBranchHead("acme", "widgets", "grain/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := client.GetBranchHead("acme", "widgets", "main")
	if err != nil {
		t.Fatal(err)
	}

	if err := client.ClosePullRequest("acme", "widgets", pr.Number); err != nil {
		t.Fatalf("ClosePullRequest: %v", err)
	}

	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "closed" || detail.Merged {
		t.Fatalf("state = %q, merged = %t, want closed and unmerged", detail.State, detail.Merged)
	}
	// Both branches exactly where they were: the work is still on the
	// head branch, and none of it reached the base. That is what makes
	// this reversible with a click, and what the human ticking the box
	// was promised.
	after, err := client.GetBranchHead("acme", "widgets", "grain/issue-1")
	if err != nil || after.SHA != head.SHA {
		t.Fatalf("head branch = %+v (%v), want the commits left exactly where they were", after, err)
	}
	baseAfter, err := client.GetBranchHead("acme", "widgets", "main")
	if err != nil || baseAfter.SHA != base.SHA {
		t.Fatalf("base branch = %+v (%v), want it untouched", baseAfter, err)
	}
}

// A pull request number nothing opened is a 404, not a silent success --
// a caller that closed nothing must be able to tell.
func TestSimRefusesToCloseAPullRequestThatIsNotThere(t *testing.T) {
	_, client := newSim(t, "main")

	err := client.ClosePullRequest("acme", "widgets", 9999)
	var ghErr *github.Error
	if !errors.As(err, &ghErr) || ghErr.Status != 404 {
		t.Fatalf("got %v, want a 404 for a pull request that does not exist", err)
	}
}

func TestSimListReviewCommentsReadsSeededComments(t *testing.T) {
	sim, client := newSim(t, "main")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "")
	if err != nil {
		t.Fatal(err)
	}

	comments, err := client.ListReviewComments("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Fatalf("expected no review comments before seeding, got %+v", comments)
	}

	line := 42
	sim.ReviewComments[pr.Number] = []github.ReviewComment{
		{ID: 1, User: "alice", Body: "nit: rename this", Path: "main.go", Line: &line},
	}
	comments, err = client.ListReviewComments("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].User != "alice" || comments[0].Path != "main.go" ||
		comments[0].Line == nil || *comments[0].Line != 42 {
		t.Fatalf("got %+v", comments)
	}
}

func TestSimCreateReviewRecordsADraftReviewNotVisibleThroughListReviewComments(t *testing.T) {
	sim, client := newSim(t, "main")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/issue-1", "main", "t", "")
	if err != nil {
		t.Fatal(err)
	}

	id, err := client.CreateReview("acme", "widgets", pr.Number, "looks good overall", []github.NewReviewComment{
		{Path: "main.go", Line: 10, Body: "consider a guard clause here"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected a non-zero review id")
	}
	if len(sim.Reviews) != 1 {
		t.Fatalf("got %+v", sim.Reviews)
	}
	recorded := sim.Reviews[0]
	if recorded.Number != pr.Number || recorded.Body != "looks good overall" || len(recorded.Comments) != 1 ||
		recorded.Comments[0].Path != "main.go" || recorded.Comments[0].Line != 10 {
		t.Fatalf("got %+v", recorded)
	}

	// A draft review's own comments aren't visible through the ordinary
	// review-comments endpoint until a human submits it -- see Review's
	// doc comment on why CreateReview never writes to ReviewComments.
	comments, err := client.ListReviewComments("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Fatalf("expected a draft review's comments to stay invisible, got %+v", comments)
	}

	// A second review gets a distinct, incrementing id.
	id2, err := client.CreateReview("acme", "widgets", pr.Number, "one more pass", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id {
		t.Fatalf("expected a distinct review id, got %d twice", id)
	}
}

func TestSimPanicsWhenAskedAboutARepoItIsNotConfiguredFor(t *testing.T) {
	_, client := newSim(t, "main")
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for a repo Sim isn't configured for")
		}
	}()
	client.ListIssues("someone-else", "other-repo", "grain-agent")
}

func TestSimRecordsEveryCall(t *testing.T) {
	sim, client := newSim(t, "main")
	sim.Issues[1] = &Issue{Title: "t", Body: "b", Labels: map[string]struct{}{}}

	if _, err := client.GetIssue("acme", "widgets", 1); err != nil {
		t.Fatal(err)
	}
	if len(sim.Calls) != 1 || sim.Calls[0].Method != "GET" || sim.Calls[0].Path != "/repos/acme/widgets/issues/1" {
		t.Fatalf("got %+v", sim.Calls)
	}
}

// pushCommit lands one commit carrying message on branch, creating the
// branch if it is not there yet -- what a run's own git push does, and
// the only way a test can say what a commit message says.
func pushCommit(t *testing.T, bare, branch, message string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	if err := exec.Command("git", "-C", wd, "checkout", "-q", branch).Run(); err != nil {
		run(t, wd, "git", "checkout", "-q", "-b", branch)
	}
	run(t, wd, "git", "commit", "-q", "--allow-empty", "-m", message)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// Compare is answered against the real repository, so what comes back is
// the commits an agent really pushed, in git's own order, with the base
// branch's own history left out.
func TestSimCompareCommitsReturnsTheBranchesOwnCommits(t *testing.T) {
	sim, client := newSim(t, "main")
	pushCommit(t, sim.BareRepo, "grain/task-1", "Add the parser\n\nWhy it was needed.")
	pushCommit(t, sim.BareRepo, "grain/task-1", "Fix the vet warning")

	commits, err := client.CompareCommits("acme", "widgets", "main", "grain/task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %+v", commits)
	}
	if commits[0].Message != "Add the parser\n\nWhy it was needed." {
		t.Fatalf("expected the oldest commit first, got %+v", commits)
	}
	if commits[1].Message != "Fix the vet warning" {
		t.Fatalf("got %+v", commits[1])
	}
	if commits[0].Merge || commits[1].Merge {
		t.Fatalf("neither commit is a merge, got %+v", commits)
	}
	if commits[0].SHA == "" || commits[0].SHA == commits[1].SHA {
		t.Fatalf("expected two distinct shas, got %+v", commits)
	}
}

func TestSimCompareCommitsIs404ForAMissingRef(t *testing.T) {
	_, client := newSim(t, "main")
	if _, err := client.CompareCommits("acme", "widgets", "main", "grain/never-pushed"); err == nil {
		t.Fatal("expected an error for a branch that does not exist")
	}
}

// pushFile writes path on branch and pushes it, for the tests that care
// about what a branch changed rather than only that it changed
// something. Creates the branch if it isn't there yet, like pushCommit
// above.
func pushFile(t *testing.T, bare, branch, path, content string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	if err := exec.Command("git", "-C", wd, "checkout", "-q", branch).Run(); err != nil {
		run(t, wd, "git", "checkout", "-q", "-b", branch)
	}
	full := filepath.Join(wd, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wd, "git", "add", "-A")
	run(t, wd, "git", "commit", "-q", "-m", "write "+path+" on "+branch)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// The files a pull request touches are read off the real repository, for
// the reason its commits are: the merge queue decides whether to send an
// agent at a broken build on this answer, and a canned one would let a
// test pass on a workflow edit nobody made.
func TestSimListPullRequestFilesReadsTheRealDiff(t *testing.T) {
	sim, client := newSim(t, "main")
	pushFile(t, sim.BareRepo, "grain/task-1", ".github/workflows/ci.yml", "on: push\n")
	pushFile(t, sim.BareRepo, "grain/task-1", "pkg/thing/thing.go", "package thing\n")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "a title", "a body")
	if err != nil {
		t.Fatal(err)
	}

	files, err := client.ListPullRequestFiles("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != ".github/workflows/ci.yml" || files[1] != "pkg/thing/thing.go" {
		t.Fatalf("got %+v", files)
	}
}

// A renamed file is listed the way GitHub lists it -- under its new path,
// with the old one in previous_filename -- so that a move out of a
// directory reads as touching that directory (the security hold's rename
// case). Before this, the sim listed only the new path, and a rename out
// of pkg/relay/ read as touching nothing on the trust boundary.
func TestSimListPullRequestFilesListsARenameUnderBothPaths(t *testing.T) {
	sim, client := newSim(t, "main")
	pushFileToBranch(t, sim.BareRepo, "main", "relay.go", "package relay")
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", sim.BareRepo, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	run(t, wd, "git", "checkout", "-q", "-b", "grain/task-1")
	run(t, wd, "git", "mv", "relay.go", "moved.go")
	run(t, wd, "git", "commit", "-q", "-m", "move relay.go")
	run(t, wd, "git", "push", "-q", "origin", "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "a title", "a body")
	if err != nil {
		t.Fatal(err)
	}

	files, err := client.ListPullRequestFiles("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != "moved.go" || files[1] != "relay.go" {
		t.Fatalf("got %+v, want the new path and the old one", files)
	}
}

// A branch that changed nothing but its history -- every merge-queue test
// that pushes an empty commit is this shape -- touches no files, and has
// to read as an empty list rather than as an error or a missing answer.
func TestSimListPullRequestFilesIsEmptyForABranchThatTouchedNothing(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "a title", "a body")
	if err != nil {
		t.Fatal(err)
	}

	files, err := client.ListPullRequestFiles("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("an empty commit touches no files, got %+v", files)
	}
}

// Three dots, not two: a base branch that has moved on since the pull
// request was opened is not this pull request touching whatever landed
// there.
func TestSimListPullRequestFilesIgnoresWhatLandedOnTheBaseSince(t *testing.T) {
	sim, client := newSim(t, "main")
	pushFile(t, sim.BareRepo, "grain/task-1", "pkg/thing/thing.go", "package thing\n")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "a title", "a body")
	if err != nil {
		t.Fatal(err)
	}
	pushFile(t, sim.BareRepo, "main", "docs/somebody_elses_change.md", "not this pull request's\n")

	files, err := client.ListPullRequestFiles("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "pkg/thing/thing.go" {
		t.Fatalf("got %+v", files)
	}
}

func TestSimListPullRequestFilesIs404ForAnUnknownPullRequest(t *testing.T) {
	_, client := newSim(t, "main")
	if _, err := client.ListPullRequestFiles("acme", "widgets", 4242); err == nil {
		t.Fatal("expected an error for a pull request that does not exist")
	}
}

func TestSimUpdatePullRequestBodyRewritesItInPlace(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "a title", "opened early")
	if err != nil {
		t.Fatal(err)
	}

	if err := client.UpdatePullRequestBody("acme", "widgets", pr.Number, "the finished change"); err != nil {
		t.Fatal(err)
	}
	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Body != "the finished change" {
		t.Fatalf("got %q", detail.Body)
	}
	// The title it was never sent is the title it still has.
	if detail.Title != "a title" {
		t.Fatalf("got %q", detail.Title)
	}
}

func TestSimUpdatePullRequestTitleRenamesItInPlace(t *testing.T) {
	sim, client := newSim(t, "main")
	pushBranch(t, sim.BareRepo, "grain/task-1")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "wip", "opened early")
	if err != nil {
		t.Fatal(err)
	}

	if err := client.UpdatePullRequestTitle("acme", "widgets", pr.Number, "Fix the off-by-one"); err != nil {
		t.Fatal(err)
	}
	detail, err := client.GetPullRequest("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "Fix the off-by-one" {
		t.Fatalf("got %q", detail.Title)
	}
	// The body it was never sent is the body it still has.
	if detail.Body != "opened early" {
		t.Fatalf("got %q", detail.Body)
	}
}

func TestSimUpdatePullRequestTitleIs404ForAnUnknownPullRequest(t *testing.T) {
	_, client := newSim(t, "main")
	if err := client.UpdatePullRequestTitle("acme", "widgets", 4242, "x"); err == nil {
		t.Fatal("expected an error for a pull request that does not exist")
	}
}

func TestSimUpdatePullRequestBodyIs404ForAnUnknownPullRequest(t *testing.T) {
	_, client := newSim(t, "main")
	if err := client.UpdatePullRequestBody("acme", "widgets", 4242, "x"); err == nil {
		t.Fatal("expected an error for a pull request that does not exist")
	}
}

// A pull request's own commits are read off the real repository while it
// is open, and stay what they were once it merges: by then its base
// contains its head and the head branch may be gone, but GitHub still
// answers with the change's commits, which is when the merge queue asks
// (orchestrator.decidePort).
func TestSimListPullRequestCommitsOutlivesTheMerge(t *testing.T) {
	sim, client := newSim(t, "main")
	pushFile(t, sim.BareRepo, "grain/task-1", "a.txt", "a\n")
	pushFile(t, sim.BareRepo, "grain/task-1", "b.txt", "b\n")
	pr, err := client.CreatePullRequest("acme", "widgets", "grain/task-1", "main", "a title", "a body")
	if err != nil {
		t.Fatal(err)
	}

	open, err := client.ListPullRequestCommits("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 || open[0].SHA == "" || open[0].Merge || open[0].Message == "" {
		t.Fatalf("open pull request's commits = %+v, want its 2 own", open)
	}

	if err := client.MergePullRequest("acme", "widgets", pr.Number, ""); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteBranch("acme", "widgets", "grain/task-1"); err != nil {
		t.Fatal(err)
	}
	merged, err := client.ListPullRequestCommits("acme", "widgets", pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 || merged[0].SHA != open[0].SHA || merged[1].SHA != open[1].SHA {
		t.Fatalf("merged pull request's commits = %+v, want %+v", merged, open)
	}
}

// PublishFiles writes real objects: the commit is parentless, lives on
// the ref it was given and nowhere else, carries the files byte for
// byte, and is the same commit when the same files are published again.
func TestSimPublishFilesWritesARealCommitOffEveryBranch(t *testing.T) {
	sim, client := newSim(t, "main")
	png := []byte("\x89PNG\r\n\x1a\nnot really")
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	files := []github.PublishedFile{{Path: "contact-sheet.png", Content: png}}
	ref := "refs/grain-images/grain/task-7/3"

	sha, err := client.PublishFiles("acme", "widgets", ref, "Images for task 7", at, files)
	if err != nil {
		t.Fatal(err)
	}
	if got := sim.refSHA(ref); got != sha {
		t.Fatalf("%s = %q, want the published commit %q", ref, got, sha)
	}
	out, err := exec.Command("git", "--git-dir", sim.BareRepo, "cat-file", "blob", sha+":contact-sheet.png").Output()
	if err != nil || string(out) != string(png) {
		t.Fatalf("published file = %q (%v), want %q", out, err, png)
	}
	if out, _ := exec.Command("git", "--git-dir", sim.BareRepo, "rev-list", "--count", sha).Output(); strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("published commit has history %q, want it parentless", out)
	}
	if branches, _ := sim.branchNames(); len(branches) != 1 {
		t.Fatalf("branches = %v, want publishing to have added none", branches)
	}

	again, err := client.PublishFiles("acme", "widgets", ref, "Images for task 7", at, files)
	if err != nil || again != sha {
		t.Fatalf("publishing again = %q, %v; want the same commit %q", again, err, sha)
	}

	if _, err := client.PublishFiles("acme", "widgets", "refs/heads/sneaky", "x", at, files); err == nil {
		t.Fatal("expected publishing onto a branch to be refused")
	}
}
