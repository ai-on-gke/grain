package e2e

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/repodecl"
	"github.com/bwsalmon/grain/pkg/ui"
)

// The path-trigger walkthrough: docs/test-plans/2026-10-04.md §3, on the
// security-hold harness (real orchestrator, real git server, githubsim, a
// scripted agent) with the real declaration sync turned on: triggers are
// read by RunCycle's reconcileDeclarations off the git server's default
// branch through pkg/repodecl, exactly as a deployment reads them, and
// nothing is put in the store by hand. Only GitHub (githubsim) and the
// model vendor (a scripted antigravity run) are stood in for. The report
// is docs/test-runs/2026-10-04-path-triggers-sensitive-paths.md.

var widgets = model.RepoRef{Owner: "acme", Name: "widgets"}

// pathTriggerDeclarations is orchestrator.DeclarationSource over the harness's
// bare repository: cmd/grain's repoDeclarationSource without the
// credential ladder, which is the only part of it a test has no use for.
type pathTriggerDeclarations struct{ bare string }

func (d pathTriggerDeclarations) Head(ctx context.Context, _ model.RepoRef) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--git-dir", d.bare, "rev-parse", "refs/heads/main").Output()
	return strings.TrimSpace(string(out)), err
}

func (d pathTriggerDeclarations) Read(ctx context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error) {
	dir, err := os.MkdirTemp("", "grain-e2e-decl-")
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	defer os.RemoveAll(dir)
	if out, err := exec.CommandContext(ctx, "git", "clone", "-q", d.bare, dir).CombinedOutput(); err != nil {
		return model.RepoDeclarations{}, "", errors.New(string(out))
	}
	head, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	declared, err := repodecl.Load(os.DirFS(dir), repo)
	return declared, strings.TrimSpace(string(head)), err
}

// docsTrigger and docsTemplate are an ungated merge trigger and the
// repository template it files.
var docsTrigger = map[string]string{
	".grain/triggers/docs.yml":        "on: merge\npaths: [docs/x/**]\nper: folder\ntemplate: doc-review\n",
	".grain/templates/doc-review.yml": "name: Doc review\ntitle: \"Review {{group}}\"\nbody: |\n  Review {{paths}} from {{from_sha}} to {{to_sha}}.\n",
}

const paymentsTrigger = `on: pull_request
paths: [src/payments/**]
gate: true
sensitive: true
approvers: [team:default, alice]
human: required
`

// newTriggerWorld is a holdWorld whose repository declares files on its
// default branch, read by the real declaration sync. owner, alice, bob
// and carol are all people, so each is the deployment's owner: whoever
// holds the access code is, and a person's Submit releases every rule
// (model.Store.IsApprover). Only an agent's is refused.
//
// The trigger's team approver is the default team, the only one there
// is since schema 24 dropped the team table.
func newTriggerWorld(t *testing.T, files map[string]string) *triggerWorld {
	t.Helper()
	w := &triggerWorld{holdWorld: newHoldWorld(t, false)}
	cfg, err := w.store.GetConfig(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		d := model.DefaultConfig()
		cfg = &d
	}
	if err := w.store.PutConfig(w.ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if err := w.store.PutRepoConfig(w.ctx, model.RepoConfig{Repo: widgets, Declarations: true}); err != nil {
		t.Fatal(err)
	}
	w.deps.Declarations = pathTriggerDeclarations{bare: w.bare}
	if len(files) > 0 {
		w.pushMain("declare triggers", files)
	}
	w.sync()
	return w
}

type triggerWorld struct{ *holdWorld }

// pushMain commits files (an empty body deletes one) straight onto main,
// the way a merge lands there.
func (w *triggerWorld) pushMain(msg string, files map[string]string) string {
	w.t.Helper()
	dir := filepath.Join(w.t.TempDir(), "m")
	run(w.t, filepath.Dir(dir), "git", "clone", "-q", "-b", "main", w.remote, dir)
	run(w.t, dir, "git", "config", "user.email", "m@example.com")
	run(w.t, dir, "git", "config", "user.name", "m")
	for path, body := range files {
		if body == "" {
			run(w.t, dir, "git", "rm", "-q", path)
			continue
		}
		mustWriteFile(w.t, filepath.Join(dir, path), body)
	}
	run(w.t, dir, "git", "add", "-A")
	run(w.t, dir, "git", "commit", "-q", "-m", msg)
	run(w.t, dir, "git", "push", "-q", "origin", "main")
	return w.head("main")
}

// sync runs a cycle far enough after the last for the declaration
// sync's poll interval, and returns what the sync last recorded. The
// cycle's own error is logged rather than fatal: a declaration that does
// not read is one of the things under test.
func (w *triggerWorld) sync() *model.RepoDeclarationSync {
	w.t.Helper()
	w.now = w.now.Add(3 * time.Minute)
	if err := orchestrator.RunCycle(w.ctx, w.deps, w.now); err != nil {
		w.t.Logf("RunCycle: %v", err)
	}
	got, err := w.store.GetRepoDeclarationSync(w.ctx, widgets)
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

func (w *triggerWorld) as(user string) context.Context {
	return ui.WithPrincipal(w.ctx, model.Principal{Kind: model.PrincipalHuman, ID: user})
}

// triggered is every task a path trigger filed that is still open.
func (w *triggerWorld) triggered() []model.Task {
	w.t.Helper()
	all, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if !hasTagE2E(task, model.PathTriggerMarkerTag) {
			continue
		}
		if st := w.state(task.ID); st == model.StateClosed || st == model.StateCompleted {
			continue
		}
		out = append(out, task)
	}
	return out
}

func hasTagE2E(task model.Task, tag string) bool {
	for _, t := range task.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func linkTargets(task model.Task, kind model.LinkKind) []string {
	var out []string
	for _, l := range task.Links {
		if l.Kind == kind {
			out = append(out, l.Target)
		}
	}
	return out
}

func pendingRules(task model.Task) []string {
	var out []string
	for _, r := range model.PendingApprovals(task) {
		out = append(out, r.Rule+"="+strings.Join(r.Approvers, ","))
	}
	return out
}

// heldBySensitive runs subject, whose change edits src/payments, and checks
// the hold §3 step 1 describes.
func (w *triggerWorld) heldBySensitive(subject model.Task) model.Task {
	w.t.Helper()
	w.cycle()
	if st := w.state(subject.ID); st != model.StateAwaitingSubmit {
		w.t.Fatalf("%s state after its run = %q, want awaiting_submit (comments: %s)", subject.ID, st, w.comments(subject.ID))
	}
	got := w.task(subject.ID)
	if got.AutoMerge {
		w.t.Fatalf("a change to src/payments kept auto-merge: links %+v", got.Links)
	}
	if hold := linkTarget(got, model.LinkPathTriggerHold); hold != string(model.TriggerHumanRequired) {
		w.t.Fatalf("path-trigger-hold = %q, want %q; links %+v", hold, model.TriggerHumanRequired, got.Links)
	}
	gates := linkTargets(got, model.LinkPathTriggerTask)
	if len(gates) != 1 || !strings.HasPrefix(gates[0], "payments=") {
		w.t.Fatalf("path-trigger-task links = %v, want the payments trigger's one review", gates)
	}
	review := w.task(strings.TrimPrefix(gates[0], "payments="))
	if review.Origin.Reason != model.ReasonReview || review.Base != model.BranchName(subject.ID) ||
		!strings.Contains(review.Body, "`src/payments/x.go`") {
		w.t.Fatalf("the payments trigger's task is not a review stacked on the change: base=%s reason=%s body=%s",
			review.Base, review.Origin.Reason, review.Body)
	}
	if !strings.Contains(review.Body, "security") {
		w.t.Errorf("a sensitive trigger with no template did not file grain's security review:\n%s", review.Body)
	}
	if p := pendingRules(got); !slices.Contains(p, "trigger:payments=team:default,alice") {
		w.t.Fatalf("pending approvals = %v, want one for trigger:payments naming team:default and alice", p)
	}
	return got
}

func TestPathTriggersWalkthrough(t *testing.T) {
	t.Parallel()

	t.Run("a gated sensitive trigger holds the change for its named approvers, and a new head asks again", func(t *testing.T) {
		t.Parallel()
		w := newTriggerWorld(t, map[string]string{".grain/triggers/payments.yml": paymentsTrigger})
		rules, err := w.store.PathTriggersOf(w.ctx, widgets)
		if err != nil || len(rules) != 1 || rules[0].Trigger.Name != "payments" {
			t.Fatalf("the declaration sync did not store the payments trigger: %+v, %v", rules, err)
		}
		task := w.create("charge in cents", true, nil)
		w.workThenReview(task.ID, "mkdir -p src/payments && echo 'package payments' > src/payments/x.go", nil)
		if p := pendingRules(w.heldBySensitive(task)); len(p) != 1 {
			t.Fatalf("pending approvals = %v, want only trigger:payments", p)
		}

		// An agent approves nothing: a `human: required` hold waits for a
		// person.
		agent := ui.WithPrincipal(w.ctx, model.Principal{Kind: model.PrincipalAgent, ID: "run-1"})
		err = w.ui.Submit(agent, task.ID)
		var forbidden *ui.ForbiddenError
		if !errors.As(err, &forbidden) {
			t.Fatalf("an agent's Submit = %v, want a ForbiddenError", err)
		}
		for _, want := range []string{"is not one of the people who may release it", "`trigger:payments`",
			"a person holding the access code"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the agent's refusal does not say %q: %s", want, err)
			}
		}
		on := true
		if _, err := w.ui.UpdateTask(agent, task.ID, ui.UpdateTaskRequest{AutoMerge: &on}); !errors.As(err, &forbidden) {
			t.Fatalf("an agent turning auto-merge on = %v, want a ForbiddenError", err)
		}
		if got := w.task(task.ID); got.AutoMerge {
			t.Fatal("a refused Submit or edit turned auto-merge on")
		}
		w.notMergedWhileHeld(task, 3)

		// alice is named, and her Submit is the release.
		if err := w.ui.Submit(w.as("alice"), task.ID); err != nil {
			t.Fatalf("alice's Submit: %v", err)
		}
		got := w.task(task.ID)
		if !got.AutoMerge || len(model.PendingApprovals(got)) != 0 {
			t.Fatalf("alice's Submit did not release: automerge=%v pending=%v", got.AutoMerge, pendingRules(got))
		}
		if a := linkTargets(got, model.LinkApprovedBy); len(a) != 1 || a[0] != "trigger:payments=alice" {
			t.Errorf("approved-by = %v, want trigger:payments=alice", a)
		}

		// A new head is judged afresh: the approval is gone and the hold back.
		w.pushOutsideARun(model.BranchName(task.ID), "src/payments/y.go")
		w.cycle()
		w.cycle()
		got = w.task(task.ID)
		if got.AutoMerge || len(model.PendingApprovals(got)) != 1 || len(linkTargets(got, model.LinkApprovedBy)) != 0 {
			t.Fatalf("a new head kept the old approval: automerge=%v pending=%v approved=%v",
				got.AutoMerge, pendingRules(got), linkTargets(got, model.LinkApprovedBy))
		}
		if w.merged(model.BranchName(task.ID)) {
			t.Fatal("merged a head nobody approved")
		}
		// Any person is the owner, so carol's Submit releases it too,
		// though the rule names alice and the team.
		if err := w.ui.Submit(w.as("carol"), task.ID); err != nil {
			t.Fatalf("carol's Submit: %v", err)
		}
		if got := w.task(task.ID); !got.AutoMerge {
			t.Fatalf("a person's Submit did not release the hold: pending=%v", pendingRules(got))
		}
	})

	t.Run("a pull request editing the trigger is judged by the base's copy and held under path-triggers", func(t *testing.T) {
		t.Parallel()
		w := newTriggerWorld(t, map[string]string{".grain/triggers/payments.yml": paymentsTrigger})
		task := w.create("relax the payments trigger", true, nil)
		dropGate := strings.Replace(paymentsTrigger, "gate: true\n", "", 1)
		w.workThenReview(task.ID, "mkdir -p src/payments && echo 'package payments' > src/payments/x.go && "+
			"printf '"+dropGate+"' > .grain/triggers/payments.yml", nil)
		w.heldBySensitive(task)
		got := w.task(task.ID)
		if rules, _ := w.store.PathTriggersOf(w.ctx, widgets); len(rules) != 1 || !rules[0].Trigger.Gate {
			t.Fatalf("the rule in force changed with a pull request: %+v", rules)
		}
		reviewID := linkTarget(got, model.LinkSecurityReviewTask)
		if reviewID == "" || !strings.Contains(w.task(reviewID).Body, "path-triggers") {
			t.Errorf("no trust-boundary review naming path-triggers: links %+v", got.Links)
		}
		// The hold note names the category; the approval it asks for is
		// the repository owners', not the trigger's.
		if c := w.comments(task.ID); !strings.Contains(c, "path-triggers") {
			t.Errorf("the hold note on the task does not name path-triggers:\n%s", c)
		}
		w.notMergedWhileHeld(task, 3)
		// path-triggers asks the deployment's owner: an agent is not
		// one, and any person's Submit -- bob's -- is the release.
		var forbidden *ui.ForbiddenError
		agent := ui.WithPrincipal(w.ctx, model.Principal{Kind: model.PrincipalAgent, ID: "run-1"})
		if err := w.ui.Submit(agent, task.ID); !errors.As(err, &forbidden) {
			t.Fatalf("an agent's Submit = %v, want a ForbiddenError", err)
		}
		if err := w.ui.Submit(w.as("bob"), task.ID); err != nil {
			t.Fatalf("bob's Submit: %v", err)
		}
		if got := w.task(task.ID); !got.AutoMerge {
			t.Fatalf("bob's Submit did not release the edit to the triggers: pending=%v", pendingRules(got))
		}
	})

	t.Run("a malformed trigger fails the sync naming the file, and the last good rule keeps firing", func(t *testing.T) {
		t.Parallel()
		w := newTriggerWorld(t, docsTrigger)
		good := w.sync().Commit
		w.pushMain("break a trigger", map[string]string{".grain/triggers/broken.yml": "on: merge\npaths: [docs/**\n"})
		sync := w.sync()
		if sync == nil || sync.LastError == "" || !strings.Contains(sync.LastError, ".grain/triggers/broken.yml") {
			t.Fatalf("the sync of a malformed trigger = %+v, want an error naming .grain/triggers/broken.yml", sync)
		}
		t.Logf("recorded declaration error: %s", sync.LastError)
		if sync.Commit != good {
			t.Errorf("the sync moved its commit to the broken base: %s, want %s", sync.Commit, good)
		}
		rules, _ := w.store.PathTriggersOf(w.ctx, widgets)
		if len(rules) != 1 || rules[0].Trigger.Name != "docs" {
			t.Fatalf("the last good rules were not kept: %+v", rules)
		}
		w.pushMain("touch a doc", map[string]string{"docs/x/a/one.md": "one\n"})
		w.sync()
		if got := w.triggered(); len(got) != 1 || !strings.Contains(got[0].Body, "`docs/x/a/one.md`") {
			t.Fatalf("the last good trigger did not fire through a broken base: %d tasks %+v", len(got), got)
		}
	})

	t.Run("a burst of merges files one task per folder, and a restart neither re-fires nor skips", func(t *testing.T) {
		t.Parallel()
		w := newTriggerWorld(t, docsTrigger)
		start := w.head("main")
		var tasks []model.Task
		for _, f := range []string{"docs/x/a/one.md", "docs/x/a/two.md", "docs/x/b/one.md"} {
			task := w.create("write "+f, true, nil)
			tasks = append(tasks, task)
		}
		edits := map[string]string{
			tasks[0].ID: "mkdir -p docs/x/a && echo one > docs/x/a/one.md",
			tasks[1].ID: "mkdir -p docs/x/a && echo two > docs/x/a/two.md",
			tasks[2].ID: "mkdir -p docs/x/b && echo one > docs/x/b/one.md",
		}
		w.setScript(func(task model.Task) []antigravity.Step {
			if e, ok := edits[task.ID]; ok {
				return w.changeScript(task, e)
			}
			return []antigravity.Step{finalText("no findings")}
		})
		allMerged := func() bool {
			w.markAllClean()
			for _, task := range tasks {
				if !w.merged(model.BranchName(task.ID)) {
					return false
				}
			}
			return true
		}
		// Run all three to their pull requests first, unmergeable, so that
		// the merge queue lands them together once they read clean: the
		// burst the plan asks for.
		if !w.cycles(8, func() bool {
			return w.sim.pullRequestCount() == 3 && w.state(tasks[0].ID) != model.StateRunning &&
				w.state(tasks[1].ID) != model.StateRunning && w.state(tasks[2].ID) != model.StateRunning
		}) {
			t.Fatal("the three changes never all opened pull requests")
		}
		if got := w.triggered(); len(got) != 0 {
			t.Fatalf("filed %d task(s) before anything merged", len(got))
		}
		if w.head("main") != start {
			t.Fatal("something merged before the pull requests read clean")
		}
		// The queue lands them a cycle apart, so each folder's task covers
		// from the head before its first change landed -- and docs/x/a's,
		// which two merges touch, keeps that earliest one when the second
		// supersedes it.
		firstFrom := map[string]string{}
		merges := 0
		if !w.cycles(12, allMerged) {
			t.Fatal("the three changes never all merged")
		}
		for _, line := range strings.Split(strings.TrimSpace(runOutput(t, w.upstream, "git", "--git-dir", w.bare,
			"log", "--first-parent", "--reverse", "--format=%H", start+"..main")), "\n") {
			merges++
			for _, folder := range []string{"docs/x/a", "docs/x/b"} {
				if _, ok := firstFrom[folder]; ok {
					continue
				}
				files := runOutput(t, w.upstream, "git", "--git-dir", w.bare, "diff", "--name-only", line+"^", line)
				if strings.Contains(files, folder+"/") {
					firstFrom[folder] = strings.TrimSpace(runOutput(t, w.upstream, "git", "--git-dir", w.bare, "rev-parse", line+"^"))
				}
			}
		}
		t.Logf("%d commits landed on main for the three changes", merges)
		w.cycle()
		got := w.triggered()
		groups := map[string]model.Task{}
		for _, task := range got {
			for _, tag := range task.Tags {
				if g, ok := strings.CutPrefix(tag, "path-trigger:acme/widgets@main:docs:"); ok {
					groups[g] = task
				}
			}
		}
		if len(got) != 2 || groups["docs/x/a"].ID == "" || groups["docs/x/b"].ID == "" {
			t.Fatalf("want one open task each for docs/x/a and docs/x/b, got %d: %+v", len(got), got)
		}
		for g, task := range groups {
			if from := model.PathTriggerFrom(task.Tags); from != firstFrom[g] {
				t.Errorf("%s's task covers from %s, want the head before its first change, %s", g, from, firstFrom[g])
			}
		}
		if groups["docs/x/a"].ID != "" && linkTarget(groups["docs/x/a"], model.LinkSupersedes) == "" {
			t.Errorf("docs/x/a's task, fired twice, supersedes nothing: %+v", groups["docs/x/a"].Links)
		}
		if a := groups["docs/x/a"]; !strings.Contains(a.Body, "`docs/x/a/one.md`") || !strings.Contains(a.Body, "`docs/x/a/two.md`") {
			t.Errorf("docs/x/a's coalesced task does not list both files:\n%s", a.Body)
		}

		// Restart: a new process over the same database.
		cursor, _ := w.store.GetPathTriggerCursor(w.ctx, widgets, "main")
		w.restart()
		w.now = w.now.Add(5 * time.Minute)
		w.cycle()
		if again := w.triggered(); len(again) != 2 {
			t.Fatalf("a restart changed what is filed: %d open tasks, want 2: %+v", len(again), again)
		}
		if c, _ := w.store.GetPathTriggerCursor(w.ctx, widgets, "main"); c != cursor {
			t.Errorf("a restart moved the cursor from %s to %s", cursor, c)
		}

		// A fourth change to docs/x/a supersedes that folder's undispatched task.
		w.pushMain("a fourth", map[string]string{"docs/x/a/three.md": "three\n"})
		w.now = w.now.Add(2 * time.Minute)
		w.cycle()
		after := w.triggered()
		if len(after) != 2 {
			t.Fatalf("after a fourth merge: %d open tasks, want 2: %+v", len(after), after)
		}
		for _, task := range after {
			if task.ID == groups["docs/x/b"].ID {
				continue
			}
			if linkTarget(task, model.LinkSupersedes) != groups["docs/x/a"].ID {
				t.Errorf("the fourth merge's task %s does not supersede docs/x/a's %s: %+v", task.ID, groups["docs/x/a"].ID, task.Links)
			}
			if from := model.PathTriggerFrom(task.Tags); from != firstFrom["docs/x/a"] {
				t.Errorf("the superseding task covers from %s, want %s", from, firstFrom["docs/x/a"])
			}
			if !strings.Contains(task.Body, "`docs/x/a/three.md`") || !strings.Contains(task.Body, "`docs/x/a/one.md`") {
				t.Errorf("the superseding task does not cover the whole range:\n%s", task.Body)
			}
		}
	})

	t.Run("the design-doc planner files one task per doc folder", func(t *testing.T) {
		t.Parallel()
		// The trigger is turned off in this repository (docs/design-docs.md);
		// its body is kept in pkg/designdoc/testdata until it is restored.
		files := map[string]string{}
		for p, src := range map[string]string{
			".grain/triggers/design-docs.yml":     "pkg/designdoc/testdata/design-docs.yml",
			".grain/templates/design-planner.yml": ".grain/templates/design-planner.yml",
		} {
			body, err := os.ReadFile(filepath.Join("..", "..", src))
			if err != nil {
				t.Fatal(err)
			}
			files[p] = string(body)
		}
		w := newTriggerWorld(t, files)
		if sync := w.sync(); sync.LastError != "" {
			t.Fatalf("this repository's own design-docs declarations do not sync: %s", sync.LastError)
		}
		from := w.head("main")
		w.cycle()
		to := w.pushMain("two design docs", map[string]string{
			"docs/design/sample/README.md": "# Sample\n\n### Do a thing {#D-1}\n",
			"docs/design/other/README.md":  "# Other\n\n### Do another {#O-1}\n",
		})
		w.now = w.now.Add(2 * time.Minute)
		w.cycle()
		got := w.triggered()
		if len(got) != 2 {
			t.Fatalf("filed %d planner tasks, want one per folder: %+v", len(got), got)
		}
		placeholder := regexp.MustCompile(`\{\{`)
		titles := map[string]bool{}
		for _, task := range got {
			titles[task.Title] = true
			if placeholder.MatchString(task.Title + task.Body) {
				t.Errorf("%s's prompt has an unsubstituted variable:\n%s\n%s", task.ID, task.Title, task.Body)
			}
			if !strings.Contains(task.Body, from+".."+to) {
				t.Errorf("%s does not name the range %s..%s", task.ID, from, to)
			}
		}
		for _, want := range []string{"Plan design doc changes: docs/design/sample", "Plan design doc changes: docs/design/other"} {
			if !titles[want] {
				t.Errorf("no task titled %q among %v", want, titles)
			}
		}
	})
}

// restart is a new process over the same database: a new store handle
// (which is what the orchestrator's in-memory poll memory is keyed by)
// and new Deps around it.
func (w *triggerWorld) restart() {
	w.t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(w.dbDir))
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { db.Close() })
	w.store = model.New(db)
	if err := w.store.Init(w.ctx); err != nil {
		w.t.Fatal(err)
	}
	w.deps.Store = w.store
	w.ui = ui.NewClient(ui.Config{Actor: ui.DefaultActor("operator"), Capabilities: ui.OfferedCapabilities()}, w.store)
}
