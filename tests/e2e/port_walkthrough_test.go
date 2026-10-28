// TestPortWalkthroughPairedBranches is docs/test-plans/2026-10-04.md §2,
// step 1 and step 3: automatic ports between paired branches, driven
// through real orchestrator.RunCycle cycles against a real githubsim and
// a real git server.
//
// pkg/orchestrator/port_test.go proves the filing rules against a fake
// forge and a hand-built PullRequestDetail. Nothing before this let the
// real merge queue merge a pull request onto a branch the repository
// pairs -- with `port-branches` read from the repository's own
// .grain/settings.yml by the real declaration sync -- and then followed
// the port task it files: its fields, its dispatch, its own merge onto
// the other branch, and a restart in between.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// bareDeclarations is orchestrator.DeclarationSource read straight off
// the stand-in GitHub's bare repository: cmd/grain's
// repoDeclarationSource clones over the network and runs the same
// repodecl.Load on the checkout, which is the part that matters here.
type bareDeclarations struct {
	t    *testing.T
	bare string
}

func (d bareDeclarations) Head(ctx context.Context, repo model.RepoRef) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--git-dir", d.bare, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out)), err
}

func (d bareDeclarations) Read(ctx context.Context, repo model.RepoRef) (model.RepoDeclarations, string, error) {
	dir := d.t.TempDir()
	if out, err := exec.CommandContext(ctx, "git", "clone", "-q", "--depth", "1", d.bare, dir).CombinedOutput(); err != nil {
		return model.RepoDeclarations{}, "", fmt.Errorf("%v: %s", err, out)
	}
	commit, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return model.RepoDeclarations{}, "", err
	}
	declared, err := repodecl.Load(os.DirFS(dir), repo)
	return declared, strings.TrimSpace(string(commit)), err
}

// greenCI is the grain CI run every port carries the grant for
// (newPortTask adds grain-ci): it answers every run green at once. It is
// a stub of the CI executor -- which runs jobs in sandboxes this harness
// does not have -- and not of the merge queue's reading of the verdict,
// which is real.
type greenCI struct {
	ci.NopRunner
	mu   sync.Mutex
	runs map[string]ci.Run
}

func (c *greenCI) Start(ctx context.Context, req ci.Request) (ci.Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	run := ci.Run{
		ID: fmt.Sprintf("run-%d", len(c.runs)+1), Repo: req.Repo, Ref: req.Ref, Commit: req.Commit,
		Event: req.Event, StartedAt: &now, FinishedAt: &now, Conclusion: ci.ConclusionSuccess,
	}
	c.runs[run.ID] = run
	return run, nil
}

func (c *greenCI) Get(id string) (ci.Run, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run, ok := c.runs[id]
	return run, ok
}

func (c *greenCI) Cancel(id, reason string, by ci.CancelSource) bool { return false }

// portAgent is the scripted stand-in for a live model session. It looks
// its task up by RunConfig.TaskID and does what that task's prompt asks:
// a port task follows port.go's portBody recipe (find the merge, then
// `git cherry-pick -x` each commit with a Backported-from footer), and
// any other task commits a file of its own on its base, with whatever
// commit message the test gave its title.
type portAgent struct {
	t      *testing.T
	remote string
	mu     sync.Mutex
	store  *model.Store
	// messages is the commit message each non-port task's agent writes,
	// keyed by task title; empty means "agent commit for <id>".
	messages map[string]string
	// ran is every task id the agent was run for, in order.
	ran []string
}

var (
	portTitleRe  = regexp.MustCompile(`^Port [^#]+#(\d+) to (\S+):`)
	portCherryRe = regexp.MustCompile("`(git cherry -v [^`]+)`")
)

func (a *portAgent) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	a.mu.Lock()
	store := a.store
	a.ran = append(a.ran, cfg.TaskID)
	a.mu.Unlock()
	task, err := store.GetTask(ctx, cfg.TaskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("portAgent: reading %s: %v", cfg.TaskID, err)
	}
	branch := string(model.BranchName(task.ID))
	base := task.Base
	if base == "" {
		base = "main"
	}
	prefix := "git clone -q " + a.remote + " work && cd work && " +
		"git config user.email agent@example.com && git config user.name agent && " +
		"git checkout -q " + base + " && git checkout -q -b " + branch + " && "
	if strings.HasPrefix(task.Title, "propose:") {
		// A run that only proposes: the mocked propose_task records the
		// call, and the close-out relays it (relayProposedTasks).
		return antigravity.NewForTest(antigravity.Steps(
			toolCall("propose_task", map[string]any{
				"title":   strings.TrimSpace(strings.TrimPrefix(task.Title, "propose:")),
				"body":    "a change that belongs on main only",
				"no_port": true,
			}),
			finalText("proposed it"),
		)).Run(ctx, cfg)
	}
	var cmd string
	if m := portTitleRe.FindStringSubmatch(task.Title); m != nil {
		// Steps 1 to 3 of portBody, using the `git cherry` command the
		// body itself gives, so a recipe that stops working fails here.
		cm := portCherryRe.FindStringSubmatch(task.Body)
		if cm == nil {
			return nil, fmt.Errorf("portAgent: %s's body names no `git cherry -v` command:\n%s", task.ID, task.Body)
		}
		cherry := strings.ReplaceAll(cm[1], "<merge>", "$M")
		cmd = prefix +
			"M=$(git log --first-parent --merges --format='%H %s' origin/" + sourceBranchOf(task) +
			" | grep -F 'pull request #" + m[1] + " ' | head -1 | cut -d' ' -f1) && test -n \"$M\" && " +
			"for c in $(" + cherry + " | awk '$1 == \"+\" {print $2}'); do " +
			"git cherry-pick -x $c >/dev/null && " +
			"git commit -q --amend -m \"$(git log -1 --format=%B)\n\nBackported-from: $c\"; done && " +
			"git push -q origin " + branch
	} else {
		msg := a.messages[task.Title]
		if msg == "" {
			msg = "agent commit for " + task.ID
		}
		file := "CHANGE-" + strings.ReplaceAll(task.ID, "/", "-") + ".md"
		if strings.HasPrefix(task.Title, "backmerge:") {
			// The agent merges the other paired branch into its own
			// first, the way grain/task-1869's did on 10-04.
			prefix += "git merge -q --no-ff origin/candidate -m 'Merge candidate into " + branch + "' && "
		}
		cmd = prefix + "echo 'change for " + task.ID + "' > " + file + " && git add " + file + " && " +
			"git commit -q -F - <<'EOF'\n" + msg + "\nEOF\n" +
			" git push -q origin " + branch
	}
	return antigravity.NewForTest(antigravity.Steps(
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed "+branch),
	)).Run(ctx, cfg)
}

// sourceBranchOf reads the branch a port is porting from out of its own
// body ("merged onto `main`"), the way the agent reading it would.
func sourceBranchOf(port *model.Task) string {
	m := regexp.MustCompile("merged onto `([^`]+)`").FindStringSubmatch(port.Body)
	if m == nil {
		return "main"
	}
	return m[1]
}

func TestPortWalkthroughPairedBranches(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	const owner, repoName = "acme", "widgets"
	repo := model.RepoRef{Owner: owner, Name: repoName}

	// The repository: main, with .grain/settings.yml declaring the pair,
	// and candidate cut from it -- this repository's own arrangement.
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	mustWriteFile(t, filepath.Join(seed, "NOTES.md"), "seed\n")
	mustWriteFile(t, filepath.Join(seed, ".grain", "settings.yml"), "port-branches: [main, candidate]\n")
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "main", "main:candidate")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	client := github.NewClient(sim, nil)

	pa := &portAgent{t: t, remote: remote, messages: map[string]string{}}
	deps := orchestrator.Deps{
		Client: client, Sandboxes: credentialed(t, remote), MaxWorkers: 1,
		Declarations: bareDeclarations{t: t, bare: bare},
		CI:           &greenCI{runs: map[string]ci.Run{}},
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			return pa, nil
		},
	}
	storeDir := t.TempDir()
	at := baseTime
	// cycle is one daemon cycle on a freshly opened store: every call is
	// a restart as far as anything held in memory is concerned.
	cycle := func(check func(*model.Store, context.Context)) {
		t.Helper()
		at = at.Add(10 * time.Minute)
		withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
			pa.mu.Lock()
			pa.store = store
			pa.mu.Unlock()
			deps.Store = store
			if err := orchestrator.RunCycle(ctx, deps, at); err != nil {
				t.Fatalf("RunCycle at %s: %v", at, err)
			}
			if check != nil {
				check(store, ctx)
			}
		})
	}
	read := func(fn func(*model.Store, context.Context)) {
		t.Helper()
		withStore(t, storeDir, fn)
	}
	// markOpenClean stands in for GitHub settling every open pull
	// request's mergeability (auto_merge_test.go's markPullRequestClean).
	markOpenClean := func() {
		for _, pr := range sim.openPullRequests() {
			sim.markPullRequestClean(pr.Number)
		}
	}
	// land drives a task from queued to its pull request merged: one
	// cycle dispatches it and opens the pull request, then cycles merge
	// it once it reads clean.
	land := func(id string, beforeMerge func()) {
		t.Helper()
		cycle(nil)
		if beforeMerge != nil {
			beforeMerge()
		}
		for i := 0; i < 4; i++ {
			markOpenClean()
			var st model.State
			cycle(func(store *model.Store, ctx context.Context) {
				var err error
				if st, err = store.State(ctx, id); err != nil {
					t.Fatal(err)
				}
			})
			if st == model.StateClosed {
				return
			}
		}
		read(func(store *model.Store, ctx context.Context) {
			st, _ := store.State(ctx, id)
			task, _ := store.GetTask(ctx, id)
			t.Fatalf("%s never merged: state %q, task %+v, pull requests %+v", id, st, task, sim.sim.PullRequests)
		})
	}
	portsOf := func(store *model.Store, ctx context.Context, source string) []model.Task {
		t.Helper()
		all, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Task
		for _, task := range all {
			if slices.Contains(task.Links, model.Link{Kind: model.LinkPortOf, Target: source}) {
				out = append(out, task)
			}
		}
		return out
	}
	allPorts := func(store *model.Store, ctx context.Context) []model.Task {
		t.Helper()
		all, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Task
		for _, task := range all {
			if slices.ContainsFunc(task.Links, func(l model.Link) bool { return l.Kind == model.LinkPortOf }) {
				out = append(out, task)
			}
		}
		return out
	}
	file := func(title string, mutate func(*model.Task)) string {
		t.Helper()
		var id string
		read(func(store *model.Store, ctx context.Context) {
			var err error
			if id, err = store.NewTaskID(ctx); err != nil {
				t.Fatal(err)
			}
			orderKey, err := store.OrderKeyForNewTask(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			now := at
			task := model.Task{
				ID: id, Title: title, Body: "body of " + title,
				Intent:   model.IntentImplement,
				Origin:   model.Origin{Attribution: model.Attribution{Actor: human("alice")}, Reason: model.ReasonDirect},
				Approval: &model.Attribution{Actor: human("alice")}, ApprovedAt: &now,
				Target: &repo, Base: "main", AutoMerge: true,
				OrderKey: orderKey, CreatedAt: &now,
				Grants: []model.Grant{{Capability: "release-notes", Via: model.GrantByLabel}},
			}
			if mutate != nil {
				mutate(&task)
			}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
		})
		return id
	}

	read(func(store *model.Store, ctx context.Context) {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, Declarations: true}); err != nil {
			t.Fatal(err)
		}
	})
	// The declaration sync reads port-branches off main.
	cycle(func(store *model.Store, ctx context.Context) {
		rc, err := store.GetRepoConfig(ctx, repo)
		if err != nil || rc == nil {
			t.Fatalf("repo config: %v %v", rc, err)
		}
		if got := rc.PortTargets("main"); !slices.Equal(got, []string{"candidate"}) {
			t.Fatalf("PortTargets(main) after the declaration sync = %v, want [candidate] (config %+v)", got, rc)
		}
	})

	// --- Step 1: a merge onto main files exactly one port onto candidate.
	source := file("add a widget", nil)
	land(source, nil)
	var port model.Task
	read(func(store *model.Store, ctx context.Context) {
		ports := portsOf(store, ctx, source)
		if len(ports) != 1 {
			t.Fatalf("ports of %s after its merge onto main = %d, want 1: %+v", source, len(ports), ports)
		}
		port = ports[0]
		src, _ := store.GetTask(ctx, source)
		if port.Base != "candidate" {
			t.Errorf("port base = %q, want candidate", port.Base)
		}
		if !port.AutoMerge {
			t.Errorf("port AutoMerge = %v, want true like its source", port.AutoMerge)
		}
		if port.Approval == nil || port.ApprovedAt == nil {
			t.Errorf("port is not approved: %+v", port)
		}
		for _, g := range src.Grants {
			if !slices.ContainsFunc(port.Grants, func(pg model.Grant) bool { return pg.Capability == g.Capability }) {
				t.Errorf("port grants %+v lack the source's %q", port.Grants, g.Capability)
			}
		}
		if !slices.Contains(src.Links, model.Link{Kind: model.LinkPortedBy, Target: port.ID}) {
			t.Errorf("source links %+v lack ported-by %s", src.Links, port.ID)
		}
		if !strings.HasPrefix(port.Title, "Port acme/widgets#") || !strings.Contains(port.Title, " to candidate: add a widget") {
			t.Errorf("port title = %q", port.Title)
		}
		for _, want := range []string{"merged onto `main`", "git cherry-pick -x", "Backported-from", "body of add a widget"} {
			if !strings.Contains(port.Body, want) {
				t.Errorf("port body lacks %q:\n%s", want, port.Body)
			}
		}
	})

	// --- Restart: more cycles on a reopened store file nothing more. The
	// port itself is dispatched and lands on candidate along the way.
	land(port.ID, nil)
	read(func(store *model.Store, ctx context.Context) {
		if n := len(portsOf(store, ctx, source)); n != 1 {
			t.Fatalf("ports of %s after a restart = %d, want still 1", source, n)
		}
		// --- The port's own merge onto candidate is not ported back.
		if n := len(portsOf(store, ctx, port.ID)); n != 0 {
			t.Fatalf("the port %s's merge onto candidate filed %d ports back to main", port.ID, n)
		}
	})
	if !strings.Contains(runOutput(t, upstream, "git", "--git-dir", bare, "log", "--format=%B", "candidate"), "Backported-from: ") {
		t.Fatalf("candidate has no Backported-from commit after the port merged")
	}
	runOutput(t, upstream, "git", "--git-dir", bare, "cat-file", "-e",
		"candidate:CHANGE-"+strings.ReplaceAll(source, "/", "-")+".md")

	// --- Each skip rule, end to end: nothing filed.
	noPort := file("main-only feature", func(task *model.Task) { task.NoPort = true })
	land(noPort, nil)

	footer := file("main-only footer", nil)
	pa.messages["main-only footer"] = "feat: main only\n\nThis belongs on main.\n\nPort: no"

	land(footer, nil)

	// A hand-made port: a task with no port-of link whose commit says
	// it is one.
	handPort := file("hand port", nil)
	pa.messages["hand port"] = "fix: carried over by hand\n\nBackported-from: 0123456789abcdef0123456789abcdef01234567"
	land(handPort, nil)

	// A head the target already contains: the branch is merged onto
	// candidate before its pull request merges onto main.
	already := file("already on candidate", nil)
	land(already, func() {
		dir := t.TempDir()
		run(t, dir, "git", "clone", "-q", bare, "m")
		wd := filepath.Join(dir, "m")
		run(t, wd, "git", "config", "user.email", "x@example.com")
		run(t, wd, "git", "config", "user.name", "x")
		run(t, wd, "git", "checkout", "-q", "candidate")
		run(t, wd, "git", "merge", "-q", "--no-ff", "origin/"+string(model.BranchName(already)), "-m", "carry onto candidate")
		run(t, wd, "git", "push", "-q", "origin", "candidate")
	})

	// --- A head that merged candidate in before landing on main: the port
	// carries only the pull request's own commit, not candidate's, which
	// the body's `git cherry` leaves out (cherry-picking them again would
	// stop on an empty pick). Candidate carries a candidate-only
	// `Port: no` commit first, which the merge brings onto the head: it is
	// candidate's own opt-out, not the change's, and the change is still
	// ported (grain/task-1890).
	{
		dir := t.TempDir()
		run(t, dir, "git", "clone", "-q", "--branch", "candidate", bare, "c")
		wd := filepath.Join(dir, "c")
		run(t, wd, "git", "config", "user.email", "x@example.com")
		run(t, wd, "git", "config", "user.name", "x")
		mustWriteFile(t, filepath.Join(wd, "RELEASE.md"), "candidate only\n")
		run(t, wd, "git", "add", "RELEASE.md")
		run(t, wd, "git", "commit", "-q", "-m", "fix(release): candidate only\n\nPort: no")
		run(t, wd, "git", "push", "-q", "origin", "candidate")
	}
	backMerged := file("backmerge: change built on candidate", nil)
	land(backMerged, nil)
	var backPort model.Task
	read(func(store *model.Store, ctx context.Context) {
		ports := portsOf(store, ctx, backMerged)
		if len(ports) != 1 {
			t.Fatalf("ports of the back-merged %s = %d, want 1", backMerged, len(ports))
		}
		backPort = ports[0]
	})
	candidateBefore := strings.TrimSpace(runOutput(t, upstream, "git", "--git-dir", bare, "rev-parse", "candidate"))
	land(backPort.ID, nil)
	picked := strings.Fields(runOutput(t, upstream, "git", "--git-dir", bare, "log", "--no-merges", "--format=%s",
		candidateBefore+"..candidate"))
	if got := strings.Join(picked, " "); got != "agent commit for "+backMerged {
		t.Fatalf("the back-merged change's port put %q on candidate, want only its own commit", got)
	}

	// --- Step 3: propose_task with no_port, relayed at the close-out, files
	// a NoPort child; approved and landed on main, it is not ported.
	proposer := file("propose: relayed main-only change", nil)
	cycle(nil)
	var child string
	read(func(store *model.Store, ctx context.Context) {
		all, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range all {
			if slices.Contains(task.Links, model.Link{Kind: model.LinkProposedBy, Target: proposer}) {
				if child != "" {
					t.Fatalf("%s's run filed more than one proposal", proposer)
				}
				child = task.ID
				if !task.NoPort {
					t.Errorf("relayed propose_task{no_port: true} filed %s with NoPort false", task.ID)
				}
			}
		}
		if child == "" {
			t.Fatalf("%s's propose_task was not relayed", proposer)
		}
		if err := store.UpdateTask(ctx, child, func(task *model.Task) error {
			now := at
			task.Approval = &model.Attribution{Actor: human("alice")}
			task.ApprovedAt = &now
			task.AutoMerge = true
			task.Base = "main"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	land(child, nil)

	read(func(store *model.Store, ctx context.Context) {
		for _, id := range []string{noPort, footer, handPort, already, child} {
			if ports := portsOf(store, ctx, id); len(ports) != 0 {
				task, _ := store.GetTask(ctx, id)
				t.Errorf("%s (%q) filed %d port(s), want none: %+v", id, task.Title, len(ports), ports)
			}
		}
		if n := len(allPorts(store, ctx)); n != 2 {
			t.Errorf("port tasks in the store = %d, want the two ported changes' only", n)
		}
	})

	// One more restart cycle: still nothing new.
	cycle(func(store *model.Store, ctx context.Context) {
		if n := len(allPorts(store, ctx)); n != 2 {
			t.Errorf("port tasks after a final restart = %d, want 2", n)
		}
	})
}
