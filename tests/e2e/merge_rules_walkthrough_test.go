package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The path-scoped merge-rules walkthrough: docs/test-plans/2026-10-05.md
// §2, on the path-trigger harness (real orchestrator, real git server,
// githubsim, a scripted agent, and the real declaration sync reading
// `.grain/merge-rules.yml` off the git server's main). Grain CI's executor
// is stood in for by rulesCI, which answers each run with the job
// conclusions a case scripts; everything that reads a verdict -- the
// queue's narrowing, rechecks, repairs, batching -- is the real code. The
// report is docs/test-runs/2026-10-05-merge-rules.md.

// declarationsExample is the `.grain/merge-rules.yml` example of
// docs/declarations.md, verbatim.
const declarationsExample = `# .grain/merge-rules.yml
defaults:                      # every file no rule matches, and the PR as a whole
  stall_timeout: 2h
rules:
  - id: docs
    description: Documentation only needs lint.
    paths: ["docs/**"]
    ci:
      grain: [lint]
  - id: ui
    paths: ["ui/**"]
    ci:
      grain: ["ui e2e"]
  - id: deploy
    paths: ["deploy/**"]
    ci:
      mode: both
      github: [build, cla/google]
    auto_merge: never
    stall_timeout: 30m
`

// rulesJobs is the suite every rulesCI run reports.
var rulesJobs = []string{"lint", "test", "ui e2e"}

// rulesCI is grain CI's executor answering at once: every run has the
// jobs in rulesJobs, green unless the run's branch is scripted to fail
// them, and never finished for a branch scripted to hang.
type rulesCI struct {
	ci.NopRunner
	mu     sync.Mutex
	runs   map[string]ci.Run
	fail   map[string][]string // branch -> job names that fail on it
	hang   map[string]bool     // branch -> its runs never finish
	starts []ci.Request
}

func newRulesCI() *rulesCI {
	return &rulesCI{runs: map[string]ci.Run{}, fail: map[string][]string{}, hang: map[string]bool{}}
}

func (c *rulesCI) Start(ctx context.Context, req ci.Request) (ci.Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts = append(c.starts, req)
	now := time.Now()
	branch := strings.TrimPrefix(req.Ref, "refs/heads/")
	run := ci.Run{ID: fmt.Sprintf("run-%d", len(c.runs)+1), Repo: req.Repo, Ref: req.Ref, Commit: req.Commit,
		Event: req.Event, StartedAt: &now}
	if c.hang[branch] {
		run.Conclusion = ci.ConclusionPending
		c.runs[run.ID] = run
		return run, nil
	}
	run.FinishedAt = &now
	run.Conclusion = ci.ConclusionSuccess
	for _, name := range rulesJobs {
		if len(req.Jobs) > 0 && !slices.Contains(req.Jobs, name) {
			continue
		}
		j := ci.JobRun{Workflow: "ci", WorkflowPath: ".grain/workflows/ci.yml", JobID: name, Name: name,
			Conclusion: ci.ConclusionSuccess}
		if slices.Contains(c.fail[branch], name) {
			j.Conclusion = ci.ConclusionFailure
			run.Conclusion = ci.ConclusionFailure
		}
		run.Jobs = append(run.Jobs, j)
	}
	c.runs[run.ID] = run
	return run, nil
}

func (c *rulesCI) Get(id string) (ci.Run, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run, ok := c.runs[id]
	return run, ok
}

func (c *rulesCI) Cancel(id, reason string, by ci.CancelSource) bool { return false }

func (c *rulesCI) script(branch string, failing ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail[branch] = failing
}

func (c *rulesCI) rechecksOf(branch string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.starts {
		if r.Recheck && strings.TrimPrefix(r.Ref, "refs/heads/") == branch {
			n++
		}
	}
	return n
}

// rulesWorld is a triggerWorld whose repository gates on grain CI and
// declares merge rules on main.
type rulesWorld struct {
	*triggerWorld
	ci *rulesCI
}

func newRulesWorld(t *testing.T, files map[string]string) *rulesWorld {
	t.Helper()
	w := &rulesWorld{triggerWorld: newTriggerWorld(t, nil), ci: newRulesCI()}
	if err := w.store.PutRepoConfig(w.ctx, model.RepoConfig{Repo: widgets, Declarations: true,
		CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	w.deps.CI = w.ci
	if len(files) > 0 {
		w.pushMain("declare merge rules", files)
	}
	w.sync()
	return w
}

// change files an auto-merging task whose run edits files, and runs
// the cycle that dispatches it and opens its pull request.
func (w *rulesWorld) change(title string, failing []string, files ...string) model.Task {
	w.t.Helper()
	task := w.create(title, true, nil)
	w.ci.script(model.BranchName(task.ID), failing...)
	var edit []string
	for _, f := range files {
		edit = append(edit, "mkdir -p $(dirname "+f+") && echo '"+title+"' >> "+f)
	}
	w.setScript(func(tk model.Task) []antigravity.Step {
		if tk.ID == task.ID {
			return w.changeScript(tk, strings.Join(edit, " && "))
		}
		return []antigravity.Step{finalText("no findings")}
	})
	// Ports filed by earlier merges can hold the workers for a cycle.
	for i := 0; i < 4 && !w.branchExists(model.BranchName(task.ID)); i++ {
		w.cycle()
	}
	if !w.branchExists(model.BranchName(task.ID)) {
		w.t.Fatalf("%s pushed no branch: %s", task.ID, w.comments(task.ID))
	}
	return w.task(task.ID)
}

// cycle is holdWorld's, with RunCycle's error logged rather than fatal:
// a base whose rules do not parse is one of the things under test.
func (w *rulesWorld) cycle() {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if err := orchestrator.RunCycle(w.ctx, w.deps, w.now); err != nil {
		w.t.Logf("RunCycle: %v", err)
	}
}

// settle runs up to n cycles with every open pull request reading clean
// on GitHub, stopping when done says so.
func (w *rulesWorld) settle(n int, done func() bool) bool {
	w.t.Helper()
	for i := 0; i < n; i++ {
		w.markAllClean()
		w.cycle()
		if done() {
			return true
		}
	}
	return false
}

func (w *rulesWorld) policy(id string) model.EffectiveMergePolicy {
	w.t.Helper()
	p, ok := model.MergePolicyOf(w.task(id))
	if !ok {
		w.t.Fatalf("%s has no recorded merge policy; links %+v; comments %s", id, w.task(id).Links, w.comments(id))
	}
	return p
}

func requirementLines(p model.EffectiveMergePolicy) string { return p.Describe() }

func (w *rulesWorld) setChecks(branch string, runs ...github.CheckRun) {
	w.sim.mu.Lock()
	defer w.sim.mu.Unlock()
	w.sim.sim.CheckRuns[branch] = runs
}

func TestMergeRulesWalkthroughDocsAndMixed(t *testing.T) {
	t.Parallel()
	w := newRulesWorld(t, map[string]string{".grain/merge-rules.yml": declarationsExample})
	if got := w.sync(); got == nil || got.LastError != "" {
		t.Fatalf("declaration sync: %+v", got)
	}

	// A docs-only change needs lint alone: test is red on its branch and
	// it still merges.
	docs := w.change("Docs only", []string{"test"}, "docs/guide.md")
	branch := model.BranchName(docs.ID)
	if !w.settle(8, func() bool { return w.merged(branch) }) {
		t.Fatalf("docs-only change did not merge on lint alone: state %s, policy %s, comments:\n%s",
			w.state(docs.ID), requirementLines(w.policy(docs.ID)), w.comments(docs.ID))
	}
	p := w.policy(docs.ID)
	t.Logf("docs-only policy:\n%s", p.Describe())
	if p.GrainFullSuite || !slices.Equal(p.GrainJobs, []string{"lint"}) || !slices.Equal(p.Matched, []string{"docs"}) {
		t.Fatalf("docs-only policy = %+v", p)
	}

	// docs/** + pkg/** falls back to the full suite: the same red test
	// keeps it out.
	mixed := w.change("Docs and code", []string{"test"}, "docs/guide.md", "pkg/widget/widget.go")
	mb := model.BranchName(mixed.ID)
	if w.settle(6, func() bool { return w.merged(mb) }) {
		t.Fatalf("mixed change merged with test red")
	}
	p = w.policy(mixed.ID)
	t.Logf("mixed policy:\n%s", p.Describe())
	if !p.GrainFullSuite {
		t.Fatalf("mixed change is not on the full suite: %+v", p)
	}
	if !strings.Contains(p.Describe(), "full suite") {
		t.Fatalf("mixed policy text does not say full suite:\n%s", p.Describe())
	}
}

func (w *rulesWorld) obs(id string) model.Observation {
	w.t.Helper()
	o, err := w.store.GetObservation(w.ctx, id)
	if err != nil || o == nil {
		w.t.Fatalf("observation of %s: %v", id, err)
	}
	return *o
}

func success() *string { s := "success"; return &s }

// Not t.Parallel: the stall_timeout half below is timed by forge's
// process-wide stall clock, keyed by pull request ref, and every test in
// this package opens acme/widgets#9000 onwards. A parallel test syncing
// its own #9001 restarts or forgets this one's clock, and the 30m stall
// then never comes due.
func TestMergeRulesWalkthroughDeploy(t *testing.T) {
	w := newRulesWorld(t, map[string]string{".grain/merge-rules.yml": declarationsExample})

	dep := w.change("Deploy change", nil, "deploy/app.yaml")
	branch := model.BranchName(dep.ID)
	w.settle(3, func() bool { return false })
	if w.merged(branch) {
		t.Fatal("a deploy/** change merged itself")
	}
	got := w.task(dep.ID)
	if got.AutoMerge {
		t.Fatalf("deploy change kept auto-merge; comments:\n%s", w.comments(dep.ID))
	}
	if c := w.comments(dep.ID); !strings.Contains(c, "`auto_merge: never` (from `deploy`)") {
		t.Fatalf("no note naming rule deploy:\n%s", c)
	}
	p := w.policy(dep.ID)
	t.Logf("deploy policy:\n%s", p.Describe())
	if p.Mode != model.CIGateBoth || !slices.Equal(p.GitHubChecks, []string{"build", "cla/google"}) ||
		p.StallTimeout != 30*time.Minute {
		t.Fatalf("deploy policy = %+v", p)
	}

	// Submit with only build reported: the queue row names cla/google.
	if err := w.ui.Submit(w.as("owner"), dep.ID); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	w.setChecks(branch, github.CheckRun{Name: "build", Status: "completed", Conclusion: success()})
	w.settle(3, func() bool { return w.merged(branch) })
	if w.merged(branch) {
		t.Fatal("merged with cla/google never reported")
	}
	note := w.obs(dep.ID).MergeQueueNote
	t.Logf("queue row with cla/google missing: %s", note)
	if strings.Contains(note, "no check reported against it at all") {
		t.Errorf("the row says no check was reported with build green beside it: %q", note)
	}
	if !strings.Contains(note, "`cla/google` (required by `deploy`)") {
		t.Fatalf("queue note does not name the missing check and its rule: %q", note)
	}

	w.setChecks(branch, github.CheckRun{Name: "build", Status: "completed", Conclusion: success()},
		github.CheckRun{Name: "cla/google", Status: "completed", Conclusion: success()})
	if !w.settle(4, func() bool { return w.merged(branch) }) {
		t.Fatalf("deploy change did not merge after Submit with both checks green: %s, note %q, comments:\n%s",
			w.state(dep.ID), w.obs(dep.ID).MergeQueueNote, w.comments(dep.ID))
	}

	// stall_timeout: 30m -- cla/google stays queued; the queue retries
	// once, then hands the change to a person.
	stall := w.change("Deploy stall", nil, "deploy/other.yaml")
	sb := model.BranchName(stall.ID)
	w.settle(2, func() bool { return false })
	if err := w.ui.Submit(w.as("owner"), stall.ID); err != nil {
		t.Fatal(err)
	}
	w.setChecks(sb, github.CheckRun{Name: "build", Status: "completed", Conclusion: success()},
		github.CheckRun{Name: "cla/google", Status: "queued"})
	for i := 0; i < 12 && w.obs(stall.ID).MergeQueueBlockedAt == nil; i++ {
		w.now = w.now.Add(9 * time.Minute)
		w.markAllClean()
		w.cycle()
	}
	o := w.obs(stall.ID)
	t.Logf("stalled: blocked %v, note %q", o.MergeQueueBlockedAt, o.MergeQueueNote)
	if o.MergeQueueBlockedAt == nil || w.merged(sb) {
		t.Fatalf("a deploy change stalled past 30m was not handed to a person: %+v\ncomments:\n%s", o, w.comments(stall.ID))
	}
	if c := w.comments(stall.ID); !strings.Contains(c, "30m") {
		t.Errorf("the stall hand-off does not say 30m:\n%s", c)
	}
	// F2: the wait names the GitHub check it is waiting on.
	if !strings.Contains(o.MergeQueueNote, "`cla/google`") {
		t.Errorf("the stall reason does not name the queued cla/google: %q", o.MergeQueueNote)
	}

	// A grain CI run that never finishes under the same 30m: a person
	// (the daemon no longer retries a stall on its own).
	hung := w.create("Deploy hung CI", true, nil)
	hb := model.BranchName(hung.ID)
	w.ci.mu.Lock()
	w.ci.hang[hb] = true
	w.ci.mu.Unlock()
	w.setScript(func(tk model.Task) []antigravity.Step {
		if tk.ID == hung.ID {
			return w.changeScript(tk, "mkdir -p deploy && echo hung > deploy/hung.yaml")
		}
		return []antigravity.Step{finalText("no findings")}
	})
	w.cycle()
	w.settle(2, func() bool { return false })
	if err := w.ui.Submit(w.as("owner"), hung.ID); err != nil {
		t.Fatal(err)
	}
	w.setChecks(hb, github.CheckRun{Name: "build", Status: "completed", Conclusion: success()},
		github.CheckRun{Name: "cla/google", Status: "completed", Conclusion: success()})
	for i := 0; i < 20 && w.obs(hung.ID).MergeQueueBlockedAt == nil; i++ {
		w.now = w.now.Add(9 * time.Minute)
		w.markAllClean()
		w.cycle()
	}
	o = w.obs(hung.ID)
	t.Logf("hung grain CI: blocked %v, note %q", o.MergeQueueBlockedAt, o.MergeQueueNote)
	if o.MergeQueueBlockedAt == nil || o.MergeQueueStallRetryAt != nil {
		t.Errorf("a hung grain CI run was not handed straight to a person: %+v\ncomments:\n%s",
			o, w.comments(hung.ID))
	}
}

// queueOptionRules is the docs/declarations.md example with a rule for
// each of grain/task-1660's queue options.
const queueOptionRules = declarationsExample + `  - id: risky
    paths: ["risky/**"]
    batching: alone
  - id: fragile
    paths: ["fragile/**"]
    repairs: 0
  - id: exact
    paths: ["exact/**"]
    flake_recheck: false
  - id: local
    paths: ["local/**"]
    port: false
`

func (w *rulesWorld) scriptEdits(edits map[string]string) {
	w.setScript(func(tk model.Task) []antigravity.Step {
		if f, ok := edits[tk.ID]; ok {
			return w.changeScript(tk, "mkdir -p $(dirname "+f+") && echo '"+tk.ID+"' >> "+f)
		}
		return []antigravity.Step{finalText("no findings")}
	})
}

func (w *rulesWorld) allTasks() []model.Task {
	w.t.Helper()
	all, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	return all
}

func TestMergeRulesWalkthroughQueueOptions(t *testing.T) {
	t.Parallel()
	w := newRulesWorld(t, map[string]string{
		".grain/merge-rules.yml": queueOptionRules,
		".grain/settings.yml":    "port-branches: [main, candidate]\n",
	})
	run(t, w.upstream, "git", "--git-dir", w.bare, "branch", "candidate", "main")

	t.Run("repairs 0", func(t *testing.T) {
		f := w.change("Fragile", []string{"test"}, "fragile/x.go")
		w.settle(6, func() bool { return w.obs(f.ID).MergeQueueBlockedAt != nil })
		o := w.obs(f.ID)
		t.Logf("repairs 0: blocked %v, note %q", o.MergeQueueBlockedAt, o.MergeQueueNote)
		if o.MergeQueueBlockedAt == nil {
			t.Fatalf("a red `repairs: 0` change was not handed to a person:\n%s", w.comments(f.ID))
		}
		if c := w.comments(f.ID); strings.Contains(c, "sent this task back to an agent") ||
			!strings.Contains(c, "has not sent it to an agent") {
			t.Fatalf("repairs 0 went to an agent, or the note does not say it did not:\n%s", c)
		}
		for _, tk := range w.allTasks() {
			if tk.Origin.Reason == model.ReasonFix && strings.Contains(tk.Body, model.BranchName(f.ID)) {
				t.Fatalf("a repair task was filed for %s: %s", f.ID, tk.ID)
			}
		}
	})

	t.Run("flake_recheck false", func(t *testing.T) {
		e := w.change("Exact", nil, "exact/x.go")
		eb := model.BranchName(e.ID)
		w.settle(3, func() bool { return false })
		// The policy is recorded once the pull request is classified; a
		// new head is what the flake rule judges.
		w.ci.script(eb, "test")
		w.setScript(func(tk model.Task) []antigravity.Step { return []antigravity.Step{finalText("no findings")} })
		dir := t.TempDir()
		run(t, dir, "git", "clone", "-q", "-b", eb, w.remote, "c")
		run(t, dir+"/c", "git", "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "new head")
		run(t, dir+"/c", "git", "push", "-q", "origin", eb)
		w.settle(6, func() bool { return w.obs(e.ID).MergeQueueBlockedAt != nil })
		if n := w.ci.rechecksOf(eb); n != 0 {
			t.Fatalf("a `flake_recheck: false` change was rechecked %d time(s)", n)
		}
		if w.merged(eb) {
			t.Fatal("merged on a red test")
		}
		t.Logf("flake_recheck false policy:\n%s", w.policy(e.ID).Describe())
	})

	t.Run("port false", func(t *testing.T) {
		l := w.change("Local only", nil, "local/x.go")
		lb := model.BranchName(l.ID)
		if !w.settle(8, func() bool { return w.merged(lb) }) {
			t.Fatalf("local change did not merge:\n%s", w.comments(l.ID))
		}
		w.settle(2, func() bool { return false })
		var ports, docsPorts int
		for _, tk := range w.allTasks() {
			if slices.Contains(tk.Links, model.Link{Kind: model.LinkPortOf, Target: l.ID}) {
				ports++
			}
			if slices.ContainsFunc(tk.Links, func(k model.Link) bool { return k.Kind == model.LinkPortOf }) {
				docsPorts++
			}
		}
		if ports != 0 {
			t.Fatalf("a `port: false` merge filed %d port task(s)", ports)
		}
		if docsPorts == 0 {
			t.Errorf("no port was filed for any merge here: is port-branches read at all?")
		}
		t.Logf("port false: comments:\n%s", w.comments(l.ID))
	})
}

func TestMergeRulesWalkthroughTheFileItself(t *testing.T) {
	t.Parallel()

	// Step 2: a pull request editing the rules is judged by the base's
	// copy and held under merge-rules for the repository's owners.
	t.Run("an edit to the rules", func(t *testing.T) {
		t.Parallel()
		w := newRulesWorld(t, map[string]string{".grain/merge-rules.yml": declarationsExample})
		loosened := strings.Replace(declarationsExample, "    auto_merge: never\n", "", 1)
		task := w.create("Loosen deploy", true, nil)
		w.setScript(func(tk model.Task) []antigravity.Step {
			if tk.ID == task.ID {
				return w.changeScript(tk, "echo "+base64.StdEncoding.EncodeToString([]byte(loosened))+
					" | base64 -d > .grain/merge-rules.yml && mkdir -p deploy && echo x > deploy/app.yaml")
			}
			return []antigravity.Step{finalText("no findings")}
		})
		w.cycle()
		got := w.task(task.ID)
		if got.AutoMerge {
			t.Fatalf("an edit to .grain/merge-rules.yml kept auto-merge: %s", w.comments(task.ID))
		}
		if c := w.comments(task.ID); !strings.Contains(c, "merge-rules") {
			t.Errorf("the hold note does not name merge-rules:\n%s", c)
		}
		w.notMergedWhileHeld(task, 3)
		p := w.policy(task.ID)
		t.Logf("policy of the rules edit:\n%s", p.Describe())
		if !p.AutoMergeNever || !slices.Contains(p.Matched, "deploy") {
			t.Fatalf("the edit was not judged by the base's rules: %+v", p)
		}
		if rules, _ := w.store.MergeRulesOf(w.ctx, widgets); rules == nil || rules.Rules[2].AutoMerge != model.AutoMergeNever {
			t.Fatalf("the rules in force changed with a pull request: %+v", rules)
		}
		if err := w.ui.Submit(w.as("owner"), task.ID); err != nil {
			t.Fatalf("owner's Submit: %v", err)
		}
		if !w.task(task.ID).AutoMerge {
			t.Fatalf("the owner's Submit did not release merge-rules: %+v", w.task(task.ID).Links)
		}
	})

	// Step 3: a base whose rules stop parsing holds every self-merging
	// change, keeps the last good rules, and a fix releases them.
	t.Run("a broken base", func(t *testing.T) {
		t.Parallel()
		w := newRulesWorld(t, map[string]string{".grain/merge-rules.yml": declarationsExample})
		good := w.sync().Commit
		w.pushMain("break the rules", map[string]string{".grain/merge-rules.yml": "rules:\n  - id: docs\n    paths: [docs/**\n"})
		sync := w.sync()
		t.Logf("recorded declaration error: %s", sync.LastError)
		if !strings.Contains(sync.LastError, ".grain/merge-rules.yml") || sync.Commit != good {
			t.Fatalf("sync of broken rules = %+v, want an error naming the file at the last good commit %s", sync, good)
		}
		if rules, _ := w.store.MergeRulesOf(w.ctx, widgets); rules == nil || len(rules.Rules) != 3 {
			t.Fatalf("the last good rules were dropped: %+v", rules)
		}
		docs := w.change("Docs while broken", nil, "docs/guide.md")
		db := model.BranchName(docs.ID)
		w.settle(4, func() bool { return w.merged(db) })
		if w.merged(db) {
			t.Fatal("a self-merging change merged while the base's rules do not parse")
		}
		note := w.obs(docs.ID).MergeQueueNote
		t.Logf("held while broken: autoMerge %v, row %q, comments:\n%s", w.task(docs.ID).AutoMerge, note, w.comments(docs.ID))
		if !strings.Contains(note+w.comments(docs.ID), ".grain/merge-rules.yml") {
			t.Errorf("the hold does not name .grain/merge-rules.yml")
		}
		// F7: the hold's category said it came from `.grain/triggers`
		// alone, with the error naming merge-rules.yml beside it.
		if c := w.comments(docs.ID); !strings.Contains(c, "**unparseable-triggers** (built in, from this repository's `.grain/triggers` and `.grain/merge-rules.yml`)") {
			t.Errorf("the broken-rules hold does not say it can come from the merge rules:\n%s", c)
		}
		w.pushMain("fix the rules", map[string]string{".grain/merge-rules.yml": declarationsExample})
		if s := w.sync(); s.LastError != "" {
			t.Fatalf("the fix did not sync: %+v", s)
		}
		// F6: the fix releases what the broken rules alone held. The
		// docs change touches nothing the fixed rules hold for an owner,
		// so it merges itself again, with no owner's Submit.
		if !w.settle(6, func() bool { return w.merged(db) }) {
			t.Fatalf("the base's fix did not release the held change: autoMerge %v, row %q, comments:\n%s",
				w.task(docs.ID).AutoMerge, w.obs(docs.ID).MergeQueueNote, w.comments(docs.ID))
		}
		if c := w.comments(docs.ID); !strings.Contains(c, "rules parse again") {
			t.Errorf("the release is not said on the change:\n%s", c)
		}
	})

	// Step 4: both files on the base: merge-rules.yml wins, with a
	// warning.
	t.Run("both files", func(t *testing.T) {
		t.Parallel()
		w := newRulesWorld(t, map[string]string{
			".grain/merge-rules.yml": declarationsExample,
			".grain/sensitive.yml":   "rules:\n  - id: everything\n    paths: [\"**\"]\n    auto_merge: never\n",
		})
		s := w.sync()
		rules, _ := w.store.MergeRulesOf(w.ctx, widgets)
		if rules == nil || rules.Path != model.MergeRulesPath || len(rules.Rules) != 3 {
			t.Fatalf("merge-rules.yml did not win: %+v (sync %+v)", rules, s)
		}
		warned := false
		for _, warning := range rules.Warnings {
			if strings.Contains(warning, ".grain/sensitive.yml") {
				warned = true
				t.Logf("warning: %s", warning)
			}
		}
		if !warned {
			t.Errorf("no warning about the ignored sensitive.yml: %+v", rules.Warnings)
		}
		// F5 (fixed by grain/task-2107): the warning is on the repo's
		// declarations status, which the Repos page reads.
		defaults, err := w.ui.RepoDefaults(w.as("owner"), widgets.String())
		if err != nil {
			t.Fatalf("RepoDefaults: %v", err)
		}
		if st := defaults.DeclarationsStatus; st == nil || st.MergeRules != model.MergeRulesPath ||
			!slices.ContainsFunc(st.Warnings, func(w string) bool { return strings.Contains(w, ".grain/sensitive.yml") }) {
			t.Errorf("declarations status %+v does not carry the sensitive.yml warning", st)
		}
		docs := w.change("Docs with both", nil, "docs/guide.md")
		if !w.settle(6, func() bool { return w.merged(model.BranchName(docs.ID)) }) {
			t.Fatalf("sensitive.yml's auto_merge: never applied although merge-rules.yml wins:\n%s", w.comments(docs.ID))
		}
	})
}

// Step 6: query_merge_queue, asked through a real `grain mcpserver` over
// stdio against a real ui.Server on the walkthrough's own store, lists
// the requirement lines under a pull request with rules.
func TestMergeRulesWalkthroughQueryMergeQueue(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not installed")
	}
	bin := buildGrainBinary(t)
	w := newRulesWorld(t, map[string]string{".grain/merge-rules.yml": declarationsExample})
	dep := w.change("Deploy change", nil, "deploy/app.yaml")
	w.setChecks(model.BranchName(dep.ID), github.CheckRun{Name: "build", Status: "completed", Conclusion: success()})
	if err := w.ui.Submit(w.as("owner"), dep.ID); err != nil {
		t.Fatal(err)
	}
	w.settle(3, func() bool { return false })
	// `auto_merge: never` withdraws a head's auto-merge once, Submit or
	// not, and the Submit after that is what puts it on the queue
	// (withdrawForMergePolicy).
	if err := w.ui.Submit(w.as("owner"), dep.ID); err != nil {
		t.Fatal(err)
	}
	w.settle(2, func() bool { return false })

	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities(),
	}), w.store))
	p := startMCPServer(t, bin, "-sandbox-root", t.TempDir(), "-server", daemonURL, "-task", dep.ID)
	res, err := p.CallTool(context.Background(), "query_merge_queue", map[string]any{"repo": "acme/widgets", "view": "full"})
	if err != nil || res.IsError {
		t.Fatalf("query_merge_queue: %v %q\nstderr:\n%s", err, res.Text(), p.stderr.String())
	}
	t.Logf("query_merge_queue:\n%s", res.Text())
	for _, want := range []string{
		"`auto_merge: never` -- from rule `deploy`",
		"`ci.github: cla/google` -- from rule `deploy`",
		"`stall_timeout: 30m` -- from rule `deploy`",
		"`cla/google` (required by `deploy`)",
	} {
		if !strings.Contains(res.Text(), want) {
			t.Errorf("query_merge_queue does not say %q", want)
		}
	}
}

// isMainBatchRef reports whether ref is a merge batch built for main,
// whichever deployment id (model.MergeBatchRef) the walkthrough's store
// drew.
func isMainBatchRef(ref string) bool {
	return strings.HasPrefix(ref, "grain-merge-queue/") && strings.HasSuffix(ref, "/main")
}
