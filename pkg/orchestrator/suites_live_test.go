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

package orchestrator_test

// TestLiveSuiteUntilCleanFindsAndFixesABugThenStops is bwsalmon/
// agents#642's own live confirmation: the issue asks specifically to
// "confirm that the 'run until no repo changes or new issues' pattern
// works for a standard bug finding workflow with real agents" before
// the feature is trusted. Every other test in this package proves
// SyncSuites' own pass-to-pass bookkeeping against a scripted agent
// (suites_test.go) or pure store state (this package's own
// suites_test.go in the orchestrator package); none of them can say
// whether a *real* model, given nothing but a suite's own prompt and a
// real git checkout, actually behaves the way that bookkeeping assumes
// -- finds and fixes a genuine bug on one pass, then correctly reports
// "nothing to do" on the next once that fix has landed, rather than
// hallucinating a change or looping forever. This is what checks that,
// gated on GEMINI_API_KEY exactly like this package's own live_test.go
// and pkg/agent/antigravity's own live test, so it costs nothing and
// runs nowhere (including CI) without a live key:
//
//	GEMINI_API_KEY=... go test ./pkg/orchestrator/... -run TestLiveSuiteUntilCleanFindsAndFixesABugThenStops -v -timeout 5m
//
// The scenario: a tiny real repo seeded with one genuine, findable bug
// (calc.py's add() subtracts instead of adding). A suite in
// SuiteUntilClean mode, one item, targets it:
//
// 1. Pass 1 dispatches a real agent against the buggy checkout. It is
// expected to find the bug, fix it, and push -- OutcomeOfPass reads
// that as PassChanged (a pull request was opened), so the run fires
// a second pass rather than stopping.
// 2. The test plays GitHub's part and actually merges that pull
// request into the branch the suite is running against (a real git
// merge over the same bare repo, githubsim.Sim.mergeIntoBase) --
// the run's own "stack against the source branch" is what makes
// this matter: pass 2 clones that same branch, so it only sees the
// fix if it actually landed there first.
// 3. Pass 2 dispatches a second real agent against the now-fixed
// checkout. It is expected to find no bug, leave a closing comment
// (the template's own instruction: comment_on_issue, not silence --
// see the comment below on why silence does not read as "clean"
// here), and push nothing -- OutcomeOfPass reads that as PassClean,
// and the run stops, succeeded, without needing MaxPasses.
//
// If a real agent cannot be trusted to behave this way, this test is
// where that shows up -- as a failure here, not as a support ticket
// after the feature ships.
import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// buggyCalc is calc.py with one deliberate, unambiguous bug: add
// subtracts. Small and single-purpose on purpose -- a real model has to
// actually read the file and reason about it to find this, but there is
// exactly one plausible fix, so a correct run has one obvious outcome to
// check for rather than a range of acceptable diffs.
const buggyCalc = `def add(a, b):
    """Return the sum of a and b."""
    return a - b


def multiply(a, b):
    """Return the product of a and b."""
    return a * b
`

const fixedAddLine = "return a + b"

func TestLiveSuiteUntilCleanFindsAndFixesABugThenStops(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set; skipping live suite integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	store, ctx := openStore(t)
	repoRef := model.RepoRef{Owner: "acme", Name: "calc"}

	dir := t.TempDir()
	bare := initBareRepo(t, filepath.Join(dir, "repo.git"), "main")
	seed := filepath.Join(dir, "seed")
	run(t, dir, "git", "clone", "-q", bare, seed)
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "calc.py"), []byte(buggyCalc), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "calc.py")
	run(t, seed, "git", "commit", "-q", "-m", "seed: calc.py")
	run(t, seed, "git", "push", "-q", "origin", "main")

	sim := githubsim.New(repoRef.Owner, repoRef.Name, bare, "main")
	client := github.NewClient(sim, nil)

	// This package's Deps carries no Config.GitRemoteBase (this test's
	// Deps sets none, credentialingSandboxes' own doc comment on why
	// live_test.go's scripted tests all leave it unset too), so
	// prepareCheckout (checkout.go) is a no-op here -- unlike a real
	// deployment, nothing clones the repo into the sandbox before the
	// agent's first turn. A scripted test doesn't need it to (its script
	// already has the bare repo's path baked in); a real agent does, or
	// it is left to guess where its work is -- discovered the hard way
	// running this test the first time: with no clone instructions at
	// all, a live model went looking with `find /` and settled on
	// whatever stale checkout it happened to turn up first, including
	// ones left over from an earlier, unrelated pass. Spelling out
	// exactly what to run, including a fresh `rm -rf work` every time, is
	// what makes pass 2 actually see pass 1's fix once it has landed,
	// rather than an accidental leftover.
	body := fmt.Sprintf(
		"The repository you are working on lives locally at the git path %[1]q (not a URL -- "+
			"pass it to `git clone` exactly as given). Start by running exactly this, once, as your "+
			"very first tool call: `rm -rf work && git clone %[1]q work && cd work`. Always begin from "+
			"that completely fresh clone -- never reuse, search the filesystem for (e.g. with `find`), "+
			"or otherwise rely on any other checkout that might already exist on this machine from an "+
			"earlier task; only this fresh clone reflects the repository's current, real content.\n\n"+
			"That repository contains exactly one source file, calc.py, at its root, with two small "+
			"functions. Read it and look for a genuine bug -- not a style nit, an actual incorrect "+
			"behaviour. If you find one: inside ./work, check out a new branch with the exact name "+
			"given to you elsewhere in this prompt, make the smallest correct fix, commit it, and push "+
			"it with `git push origin <that branch name>`. Do not add tests, comments, or anything else. "+
			"If you do not find any bug, leave the repository completely untouched -- do not create or "+
			"push any branch at all -- and call your comment_on_issue tool with a short note saying you "+
			"found none.",
		bare,
	)
	if err := store.PutTemplate(ctx, model.Template{
		ID:        "template-live-bugfinder",
		Name:      "Bug finder",
		Title:     "Find and fix a bug in this repository",
		Body:      body,
		AutoMerge: true,
		CreatedAt: baseTime,
	}); err != nil {
		t.Fatalf("put template: %v", err)
	}

	suite := model.Suite{
		Name:      "live bug sweep",
		Items:     []model.SuiteItem{{TemplateID: "template-live-bugfinder"}},
		Mode:      model.SuiteUntilClean,
		MaxPasses: 3,
		AutoMerge: true,
	}
	suiteRun, err := store.CreateSuiteRun(ctx, suite, repoRef, "main", baseTime)
	if err != nil {
		t.Fatalf("create suite run: %v", err)
	}

	// WithMaxTurns well above the default 20: this run's own sandbox
	// exploration (listing the checkout, reading calc.py, editing,
	// running git commands) costs several turns before the model ever
	// gets to decide anything, and a run that exceeds the cap counts as
	// one failed attempt rather than ending the test outright --
	// dispatchUntilSettled below tolerates a handful of those the same
	// way a real deployment's own retry-until-task_streak-caps already
	// does (model.MaxConsecutiveFailures) -- but a higher cap up front
	// means fewer of them are needed.
	framework := liveFramework(t, antigravity.WithMaxTurns(40))

	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: credentialingSandboxes{inner: sandboxes, t: t}, MaxWorkers: 1,
		Framework: orchestrator.StaticFramework(framework),
	}

	pass1TaskID := suiteRun.PassTasks(1)[0].TaskID

	clock := baseTime
	t.Log("pass 1: dispatching a real agent against the buggy checkout")
	clock = dispatchUntilSettled(t, ctx, store, deps, clock, pass1TaskID, 3)

	pass1Run, err := store.GetSuiteRun(ctx, suiteRun.ID)
	if err != nil || pass1Run == nil {
		t.Fatalf("suite run: (%+v, %v)", pass1Run, err)
	}
	pass1Tasks := pass1Run.PassTasks(1)
	if len(pass1Tasks) != 1 {
		t.Fatalf("pass 1 has %d tasks, want 1", len(pass1Tasks))
	}
	pass1 := pass1Tasks[0]
	t.Logf("pass 1 task %s: state=%s openedPullRequest=%v", pass1.TaskID, pass1.State, pass1.OpenedPullRequest)
	if pass1.State != model.StateCompleted {
		logLatestTranscript(t, store, pass1.TaskID)
		t.Fatalf("pass 1 task state = %q, want completed (see the run's own comments/PR for what the agent actually did)", pass1.State)
	}
	if !pass1.OpenedPullRequest {
		logLatestTranscript(t, store, pass1.TaskID)
		t.Fatalf("pass 1 opened no pull request -- the live agent did not find and fix the seeded bug in calc.py")
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("githubsim has %d pull requests, want 1", len(sim.PullRequests))
	}
	prNumber := sim.PullRequests[0].Number
	prBranch := sim.PullRequests[0].Head
	t.Logf("pass 1 opened PR #%d from %s", prNumber, prBranch)

	// Confirm the fix is real -- not just that *a* pull request was
	// opened, but that its branch actually contains the corrected line.
	// A live model could, in principle, push some other change entirely
	// and still trip OpenedPullRequest true; this is what catches that.
	fixCheckout := filepath.Join(t.TempDir(), "check-fix")
	run(t, t.TempDir(), "git", "clone", "-q", "-b", prBranch, bare, fixCheckout)
	fixed, err := os.ReadFile(filepath.Join(fixCheckout, "calc.py"))
	if err != nil {
		t.Fatalf("reading calc.py from the agent's own branch: %v", err)
	}
	if !strings.Contains(string(fixed), fixedAddLine) {
		t.Fatalf("calc.py on the agent's branch does not contain %q -- got:\n%s", fixedAddLine, fixed)
	}

	// Play GitHub's part: actually merge the pull request into the same
	// branch ("main") the suite run is stacked against -- a real git
	// merge over the bare repo, not just flipping a status flag, since
	// pass 2 below needs the fix to genuinely be there when it clones.
	t.Log("merging pass 1's pull request into main")
	if err := client.MergePullRequest(repoRef.Owner, repoRef.Name, prNumber, ""); err != nil {
		t.Fatalf("merging pass 1's pull request: %v", err)
	}
	merged, err := os.ReadFile(bareFileAt(t, bare, "main", "calc.py"))
	if err != nil {
		t.Fatalf("reading calc.py from main after the merge: %v", err)
	}
	if !strings.Contains(string(merged), fixedAddLine) {
		t.Fatalf("calc.py on main after the merge does not contain %q -- the merge did not actually land the fix", fixedAddLine)
	}

	clock = clock.Add(time.Minute)
	t.Log("cycle: syncing the merge and letting SyncSuites fire pass 2 if it has not already")
	if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
		t.Logf("RunCycle (sync after merge) returned an error (tolerated): %v", err)
	}

	afterMerge, err := store.GetSuiteRun(ctx, suiteRun.ID)
	if err != nil || afterMerge == nil {
		t.Fatalf("suite run: (%+v, %v)", afterMerge, err)
	}
	if afterMerge.CurrentPass() < 2 {
		t.Fatalf("current pass = %d after pass 1 opened a pull request, want pass 2 to have been fired", afterMerge.CurrentPass())
	}
	pass2TaskID := afterMerge.PassTasks(2)[0].TaskID

	clock = clock.Add(time.Minute)
	t.Log("pass 2: dispatching a real agent against the now-fixed checkout")
	clock = dispatchUntilSettled(t, ctx, store, deps, clock, pass2TaskID, 3)

	final, err := store.GetSuiteRun(ctx, suiteRun.ID)
	if err != nil || final == nil {
		t.Fatalf("suite run: (%+v, %v)", final, err)
	}
	pass2Tasks := final.PassTasks(2)
	if len(pass2Tasks) != 1 {
		t.Fatalf("pass 2 has %d tasks, want 1", len(pass2Tasks))
	}
	pass2 := pass2Tasks[0]
	t.Logf("pass 2 task %s: state=%s openedPullRequest=%v proposed=%v", pass2.TaskID, pass2.State, pass2.OpenedPullRequest, pass2.Proposed)
	if pass2.State != model.StateCompleted {
		logLatestTranscript(t, store, pass2.TaskID)
		t.Fatalf("pass 2 task state = %q, want completed -- the live agent did not settle (finding a false bug, "+
			"asking a question, or otherwise not reaching comment_on_issue/push) once the fix had already landed", pass2.State)
	}
	if pass2.OpenedPullRequest {
		logLatestTranscript(t, store, pass2.TaskID)
		t.Fatal("pass 2 opened a pull request -- the live agent found (or invented) another change on an already-fixed repo")
	}

	if final.Status != model.SuiteRunSucceeded {
		t.Fatalf("run status = %q after a clean second pass, want succeeded (error: %s)", final.Status, final.LastError)
	}
	if final.CurrentPass() != 2 {
		t.Fatalf("current pass = %d, want exactly 2 -- a third pass means the clean pass was not recognised as clean", final.CurrentPass())
	}
	t.Log("confirmed: SuiteUntilClean found and fixed a real bug on pass 1, then correctly stopped on a clean pass 2")
}

// bareFileAt reads path out of branch in the bare repo at bareDir, via a
// throwaway clone -- the simplest way to read one file's content off a
// bare repo without a git-cat-file invocation of its own.
func bareFileAt(t *testing.T, bareDir, branch, path string) string {
	t.Helper()
	dir := t.TempDir()
	checkout := filepath.Join(dir, "check")
	run(t, dir, "git", "clone", "-q", "-b", branch, bareDir, checkout)
	return filepath.Join(checkout, path)
}

// dispatchUntilSettled runs cycles against deps until taskID reaches a
// terminal state (completed, failed or closed) or attempts run out,
// tolerating a RunCycle error on any one attempt -- a live model
// occasionally exceeding antigravity.WithMaxTurns is exactly the kind of
// per-run failure model.MaxConsecutiveFailures (5) already exists to
// absorb in a real deployment, so this test affords the same handful of
// retries rather than failing outright on the first one, the same way a
// real daemon's own reconcile loop just ticks again.
func dispatchUntilSettled(t *testing.T, ctx context.Context, store *model.Store, deps orchestrator.Deps, clock time.Time, taskID string, attempts int) time.Time {
	t.Helper()
	for i := 0; i < attempts; i++ {
		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Logf("RunCycle attempt %d for %s returned an error (tolerated, retried): %v", i+1, taskID, err)
		}
		st, err := store.State(ctx, taskID)
		if err != nil {
			t.Fatalf("reading state of %s: %v", taskID, err)
		}
		if st == model.StateCompleted || st == model.StateFailed || st == model.StateClosed {
			return clock
		}
		clock = clock.Add(time.Minute)
	}
	return clock
}

// logLatestTranscript logs taskID's most recent attempt's own transcript
// -- diagnostic only, so a failure here (an assertion below this call)
// shows what the live model actually did, not just which state it ended
// up in.
func logLatestTranscript(t *testing.T, store *model.Store, taskID string) {
	t.Helper()
	ctx := context.Background()
	attempts, err := store.Attempts(ctx, taskID)
	if err != nil || attempts == 0 {
		return
	}
	transcript, found, err := store.RunTranscript(ctx, taskID, attempts)
	if err != nil || !found {
		return
	}
	t.Logf("task %s attempt %d transcript:\n%s", taskID, attempts, transcript)
}
