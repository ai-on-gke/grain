package orchestrator_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/trustboundary"
)

// pairedSim is a repository whose default branch is landed and whose
// other paired branch starts at the same commit, with the two declared
// as ports of one another.
func pairedSim(t *testing.T, ctx context.Context, store *model.Store, landed, other string) (*githubsim.Sim, *github.RESTClient, model.RepoRef) {
	t.Helper()
	sim, client := newSim(t, "acme", "widgets", landed)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	createBranchAt(t, sim.BareRepo, landed, other)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, PortBranches: []string{"main", "candidate"}}); err != nil {
		t.Fatal(err)
	}
	return sim, client, repo
}

// createBranchAt points branch at whatever from points at.
func createBranchAt(t *testing.T, bare, from, branch string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", from, bare, wd)
	run(t, wd, "git", "push", "-q", "-f", "origin", "HEAD:refs/heads/"+branch)
}

// mergeQueued drives one green, mergeable queue cycle and requires the
// task to have closed on it.
func mergeQueued(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client *github.RESTClient, task model.Task, branch string) {
	t.Helper()
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "tests", Status: "completed", Conclusion: strPtr("success")}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: the pull request should have merged", st)
	}
}

// portSkipNotes is every comment on task saying why grain did not port
// it (notePortSkips).
func portSkipNotes(t *testing.T, ctx context.Context, store *model.Store, task string) []string {
	t.Helper()
	comments, err := store.Comments(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range comments {
		if strings.Contains(c.Body, "but grain did not port it there") {
			out = append(out, c.Body)
		}
	}
	return out
}

// pullRequestOf is the pull request task fixes.
func pullRequestOf(t *testing.T, task model.Task) model.PullRequestRef {
	t.Helper()
	for _, l := range task.Links {
		if l.Kind == model.LinkFixes {
			ref, err := model.ParsePullRequestRef(l.Target)
			if err != nil {
				t.Fatal(err)
			}
			return ref
		}
	}
	t.Fatalf("%s links no pull request: %+v", task.ID, task.Links)
	return model.PullRequestRef{}
}

// portsOf is every task filed as a port of source.
func portsOf(t *testing.T, ctx context.Context, store *model.Store, source string) []model.Task {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, task := range tasks {
		if slices.Contains(task.Links, model.Link{Kind: model.LinkPortOf, Target: source}) {
			out = append(out, task)
		}
	}
	return out
}

// A merge onto either paired branch files one approved port onto the
// other, in the same team and with the same capabilities, linked to its
// source both ways.
func TestAMergeOnAPairedBranchFilesAPortToTheOther(t *testing.T) {
	for _, tc := range []struct{ landed, other string }{
		{landed: "candidate", other: "main"},
		{landed: "main", other: "candidate"},
	} {
		t.Run(tc.landed+" to "+tc.other, func(t *testing.T) {
			store, ctx := openStore(t)
			sim, client, repo := pairedSim(t, ctx, store, tc.landed, tc.other)
			task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
			task.Grants = []model.Grant{{Capability: "gcp-key", Via: model.GrantByLabel}}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}

			mergeQueued(t, ctx, store, sim, client, task, branch)

			ports := portsOf(t, ctx, store, task.ID)
			if len(ports) != 1 {
				t.Fatalf("filed %d ports, want 1", len(ports))
			}
			port := ports[0]
			if port.Base != tc.other {
				t.Errorf("port base = %q, want %q", port.Base, tc.other)
			}
			if port.Approval == nil || port.ApprovedAt == nil {
				t.Error("port was filed unapproved: it carries work a human already approved")
			}
			if port.AutoMerge != task.AutoMerge {
				t.Errorf("port auto-merge = %v, want the source's %v", port.AutoMerge, task.AutoMerge)
			}
			if !slices.ContainsFunc(port.Grants, func(g model.Grant) bool { return g.Capability == "gcp-key" }) {
				t.Errorf("port grants = %+v, want the source's gcp-key carried over", port.Grants)
			}
			for _, want := range []string{"cherry-pick -x", "Backported-from:", "git cherry -v origin/" + tc.other + " <merge>^2 <merge>^1",
				"do not merge `" + tc.landed + "` into your branch", "ask_question"} {
				if !strings.Contains(port.Body, want) {
					t.Errorf("port body does not say %q:\n%s", want, port.Body)
				}
			}
			st, err := store.State(ctx, port.ID)
			if err != nil {
				t.Fatal(err)
			}
			if st != model.StateQueued {
				t.Errorf("port state = %q, want queued", st)
			}
			source, err := store.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(source.Links, model.Link{Kind: model.LinkPortedBy, Target: port.ID}) {
				t.Errorf("source links = %+v, want ported-by %s", source.Links, port.ID)
			}
		})
	}
}

// A port's own merge is not ported back: that is the ping-pong this
// whole mechanism would otherwise be.
func TestAPortsOwnMergeIsNotPortedBack(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task.Links = append(task.Links, model.Link{Kind: model.LinkPortOf, Target: "t0"})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	mergeQueued(t, ctx, store, sim, client, task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
		t.Fatalf("a port's merge filed %d ports of its own, want none", len(ports))
	}
	// Every port lands this way: saying so on each would be noise.
	if notes := portSkipNotes(t, ctx, store, task.ID); len(notes) != 0 {
		t.Errorf("a port's merge noted %q, want nothing said", notes)
	}
}

// queuedTaskWithCommits is queuedTaskWithPullRequest with the branch's
// commits saying what a test needs them to: a port decision is made on
// the pull request's own commits, so that is where a footer has to be.
// setup, if not nil, runs on the pushed branch before the pull request is
// opened.
func queuedTaskWithCommits(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client *github.RESTClient, id string, repo model.RepoRef, setup func(branch string), messages ...string) (model.Task, string) {

	t.Helper()
	task := filedTask(t, ctx, store, id, repo)
	task.AutoMerge = true
	task.CreatedAt = &baseTime
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	for _, message := range messages {
		pushCommitSaying(t, sim.BareRepo, branch, message)
	}
	if setup != nil {
		setup(branch)
	}
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}
	return task, branch
}

// mergeBranchInto merges other into branch, the way an agent brings a
// branch it depends on into its own.
func mergeBranchInto(t *testing.T, bare, branch, other string) {
	t.Helper()
	dir := t.TempDir()
	wd := filepath.Join(dir, "work")
	run(t, dir, "git", "clone", "-q", "--branch", branch, bare, wd)
	run(t, wd, "git", "config", "user.email", "agent@example.com")
	run(t, wd, "git", "config", "user.name", "agent")
	run(t, wd, "git", "merge", "-q", "--no-ff", "-m", "Merge origin/"+other+" into "+branch, "origin/"+other)
	run(t, wd, "git", "push", "-q", "origin", branch)
}

// A pull request whose commits say they are cherry-picks is a port too,
// whoever filed the task it came from.
func TestAChangeCarryingBackportFootersIsNotPorted(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "candidate", "main")
	task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil,
		"fix: the thing\n\nBackported-from: 0123456789abcdef",
		"fix: the thing's test\n\n(cherry picked from commit fedcba9876543210)")

	mergeQueued(t, ctx, store, sim, client, task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
		t.Fatalf("a backported change filed %d ports, want none", len(ports))
	}
}

// A change that is only partly a port -- one cherry-picked commit beside
// one of its own -- is ported: the port task drops what the target
// already has, and the commit of its own still belongs there.
func TestAChangeOnlyPartlyAPortIsPorted(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "candidate", "main")
	task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil,
		"fix: the dependency\n\nBackported-from: 0123456789abcdef",
		"fix: the change itself\n\nWhat it does.")

	mergeQueued(t, ctx, store, sim, client, task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 1 {
		t.Fatalf("a change with a commit of its own beside a port filed %d ports, want 1", len(ports))
	}
}

// A footer naming a commit the merged-onto branch already has is not a
// port onto it: it is the other branch's port of that commit, carried
// back by a head that merged the other branch in (grain/task-1869's pull
// request, 10-04), and the change landing with it is still ported.
func TestAFooterNamingACommitTheBaseHasDoesNotStopThePort(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	out, err := exec.Command("git", "--git-dir", sim.BareRepo, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	onMain := strings.TrimSpace(string(out))
	task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil,
		"fix(ui): candidate's port of a main fix\n\nBackported-from: "+onMain)

	mergeQueued(t, ctx, store, sim, client, task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 1 {
		t.Fatalf("a change whose footer names a commit main already has filed %d ports, want 1", len(ports))
	}
}

// A `Port: no` the target branch already has is that branch's own
// opt-out, brought in by a head that merged the target in -- not the
// change's. The change is still ported. It was not while the opt-out was
// read from the description, which carried every commit's `Port: no`.
func TestAPortNoTheTargetAlreadyHasDoesNotStopThePort(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	pushCommitSaying(t, sim.BareRepo, "candidate", "fix(release): candidate only\n\nPort: no")
	task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo,
		func(branch string) { mergeBranchInto(t, sim.BareRepo, branch, "candidate") },
		"fix: the change this pull request is for\n\nWhat it does.")

	mergeQueued(t, ctx, store, sim, client, task, branch)

	ports := portsOf(t, ctx, store, task.ID)
	if len(ports) != 1 {
		t.Fatalf("a change whose head merged in candidate's Port: no filed %d ports, want 1", len(ports))
	}
	if ports[0].Base != "candidate" {
		t.Errorf("port base = %q, want candidate", ports[0].Base)
	}
}

// The description is not what a port is decided on: a `Port: no` written
// into it by hand, on none of the change's commits, does not opt out.
func TestAPortNoOnlyInTheDescriptionDoesNotStopThePort(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil,
		"fix: the change\n\nWhat it does.")
	for i := range sim.PullRequests {
		sim.PullRequests[i].Body = "fix: the change\n\nPort: no\n"
	}

	mergeQueued(t, ctx, store, sim, client, task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 1 {
		t.Fatalf("a change whose commits never said Port: no filed %d ports, want 1", len(ports))
	}
}

// A `Port: no` footer on a commit other than the one grain's pull
// request description is built around still opts the change out: the
// decision reads every one of the change's commits. The description
// carries it too (describeCommits), for whoever reads it.
func TestPortNoOnALaterCommitStillOptsOut(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	task.CreatedAt = &baseTime
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushCommitSaying(t, sim.BareRepo, branch, "feat: a main-only feature\n\nWhat it does, at length.")
	pushCommitSaying(t, sim.BareRepo, branch, "chore: keep it on main\n\nPort: no")
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}
	if body := prByHead(t, sim, branch).Body; !strings.Contains(body, "\nPort: no") {
		t.Fatalf("description lost the second commit's Port: no:\n%s", body)
	}

	mergeQueued(t, ctx, store, sim, client, task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
		t.Fatalf("a change with Port: no on its second commit filed %d ports, want none", len(ports))
	}
}

// Opting out works from either end: the task's own NoPort, or a
// `Port: no` footer on one of the change's commits.
func TestAnOptedOutChangeIsNotPorted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noPort  bool
		message string
		why     string
	}{
		{name: "task field", noPort: true, message: "fix(release): candidate-only",
			why: "the task is marked not to be ported"},
		{name: "commit footer", message: "fix(release): candidate-only\n\nPort: no\n",
			why: "the change carries a `Port: no` footer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			sim, client, repo := pairedSim(t, ctx, store, "candidate", "main")
			task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil, tc.message)
			task.NoPort = tc.noPort
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}

			mergeQueued(t, ctx, store, sim, client, task, branch)

			if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
				t.Fatalf("an opted-out change filed %d ports, want none", len(ports))
			}
			// The task says why, once.
			want := "Merged " + pullRequestOf(t, task).String() + " onto `candidate`, which `acme/widgets` keeps paired with `main`, " +
				"but grain did not port it there: " + tc.why + "."
			if notes := portSkipNotes(t, ctx, store, task.ID); !slices.Equal(notes, []string{want}) {
				t.Errorf("noted %q, want %q", notes, want)
			}
		})
	}
}

// No second port of the same source onto the same branch, and none of a
// change the target branch already has.
func TestDuplicatePortsAreNotFiled(t *testing.T) {
	t.Run("already filed", func(t *testing.T) {
		store, ctx := openStore(t)
		sim, client, repo := pairedSim(t, ctx, store, "candidate", "main")
		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
		earlier := filedTask(t, ctx, store, "t0", repo)
		earlier.Tags = []string{orchestrator.PortTag(repo, task.ID, "main")}
		if err := store.PutTask(ctx, earlier); err != nil {
			t.Fatal(err)
		}

		mergeQueued(t, ctx, store, sim, client, task, branch)

		if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
			t.Fatalf("filed %d more ports beside the one already open, want none", len(ports))
		}
	})
	t.Run("already on the target", func(t *testing.T) {
		store, ctx := openStore(t)
		sim, client, repo := pairedSim(t, ctx, store, "candidate", "main")
		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
		createBranchAt(t, sim.BareRepo, branch, "main")

		mergeQueued(t, ctx, store, sim, client, task, branch)

		if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
			t.Fatalf("filed %d ports of a change main already has, want none", len(ports))
		}
		if notes := portSkipNotes(t, ctx, store, task.ID); len(notes) != 0 {
			t.Errorf("noted %q about a change main already has, want nothing said", notes)
		}
	})
}

// A repository that pairs nothing ports nothing, which is every
// repository until it says otherwise.
func TestAnUnpairedRepositoryFilesNoPorts(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	mergeQueued(t, ctx, store, sim, client, task, branch)

	if ports := portsOf(t, ctx, store, task.ID); len(ports) != 0 {
		t.Fatalf("an unpaired repository filed %d ports, want none", len(ports))
	}
}

// A repository that declares `port-types: {main->candidate: [fix,
// revert]}` ports only fixes and reverts from main to candidate -- the
// rest reaches candidate at its next cut -- while every merge onto
// candidate is still ported to main, and the opt-in and opt-out footers
// keep working.
func TestPortTypesNarrowOneDirection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		landed   string
		messages []string
		want     int
	}{
		{name: "feat on main", landed: "main", messages: []string{"feat: a new thing\n\nWhat it does."}},
		{name: "fix on main", landed: "main", messages: []string{"fix: the bug\n\nWhat it was."}, want: 1},
		{name: "scoped breaking fix on main", landed: "main", messages: []string{"fix(port)!: the bug"}, want: 1},
		{name: "revert on main", landed: "main", messages: []string{"revert: feat: a new thing"}, want: 1},
		{name: "git revert on main", landed: "main", messages: []string{"Revert \"feat: a new thing\"\n\nThis reverts commit 0123456."}, want: 1},
		{name: "feat with Port: yes on main", landed: "main", messages: []string{"feat: the thing candidate needs\n\nPort: yes"}, want: 1},
		{name: "fix with Port: no on main", landed: "main", messages: []string{"fix: main only\n\nPort: no"}},
		{name: "refactor and docs on main", landed: "main", messages: []string{"refactor: tidy", "docs: say so"}},
		{name: "feat and fix on main", landed: "main", messages: []string{"feat: a new thing", "fix: the bug it exposed"}, want: 1},
		{name: "feat on candidate", landed: "candidate", messages: []string{"feat: a candidate-side change"}, want: 1},
		{name: "chore on candidate", landed: "candidate", messages: []string{"chore: release tweak"}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			other := "candidate"
			if tc.landed == "candidate" {
				other = "main"
			}
			sim, client, repo := pairedSim(t, ctx, store, tc.landed, other)
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo,
				DeclaredPortBranches: []string{"main", "candidate"},
				DeclaredPortTypes:    map[string][]string{"main->candidate": {"fix", "revert"}}}); err != nil {
				t.Fatal(err)
			}
			task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil, tc.messages...)

			mergeQueued(t, ctx, store, sim, client, task, branch)

			ports := portsOf(t, ctx, store, task.ID)
			if len(ports) != tc.want {
				t.Fatalf("filed %d ports, want %d", len(ports), tc.want)
			}
			if tc.want == 1 && ports[0].Base != other {
				t.Errorf("port base = %q, want %q", ports[0].Base, other)
			}
			// A change the filter left off candidate says so on the task.
			if notes := portSkipNotes(t, ctx, store, task.ID); (len(notes) == 1) != (tc.want == 0) {
				t.Errorf("noted %q, want one note exactly when the change is not ported", notes)
			}
		})
	}
}

// A revert is ported under `revert` only when the target may have what it
// reverts: the revert of a feature the filter kept off candidate has
// nothing there to undo (task 3137's finding F3), while the revert of one
// candidate has -- by a cut or by a port -- still goes across.
func TestARevertOfAChangeTheTargetLacksIsNotPorted(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup lands the reverted change on main (and candidate, if it
		// should have it) and returns the revert's commit message.
		setup func(t *testing.T, bare string) string
		want  int
	}{
		{name: "git revert of a feature candidate lacks", setup: func(t *testing.T, bare string) string {
			pushCommitSaying(t, bare, "main", "feat: main only")
			return "Revert \"feat: main only\"\n\nThis reverts commit " + headOf(t, bare, "main") + "."
		}},
		{name: "grain's revert of a feature candidate lacks", setup: func(t *testing.T, bare string) string {
			pushCommitSaying(t, bare, "main", "feat: main only")
			return "revert: commit 01234567\n\nWhy.\n\nReverts-Commit: " + headOf(t, bare, "main")
		}},
		{name: "revert of a merge candidate lacks", setup: func(t *testing.T, bare string) string {
			createBranchAt(t, bare, "main", "feature")
			pushCommitSaying(t, bare, "feature", "feat: main only")
			mergeBranchInto(t, bare, "main", "feature")
			return "revert: commit 01234567\n\nReverts-Commit: " + headOf(t, bare, "main")
		}, want: 0},
		{name: "revert of a feature candidate was cut with", setup: func(t *testing.T, bare string) string {
			pushCommitSaying(t, bare, "main", "feat: cut to candidate")
			createBranchAt(t, bare, "main", "candidate")
			return "Revert \"feat: cut to candidate\"\n\nThis reverts commit " + headOf(t, bare, "main") + "."
		}, want: 1},
		{name: "revert of a fix ported to candidate", setup: func(t *testing.T, bare string) string {
			pushCommitSaying(t, bare, "main", "fix: ported")
			fix := headOf(t, bare, "main")
			pushCommitSaying(t, bare, "candidate", "fix: ported\n\nBackported-from: "+fix)
			return "Revert \"fix: ported\"\n\nThis reverts commit " + fix + "."
		}, want: 1},
		{name: "revert of a merge whose commit was ported", setup: func(t *testing.T, bare string) string {
			createBranchAt(t, bare, "main", "feature")
			pushCommitSaying(t, bare, "feature", "fix: ported")
			fix := headOf(t, bare, "feature")
			mergeBranchInto(t, bare, "main", "feature")
			pushCommitSaying(t, bare, "candidate", "fix: ported\n\n(cherry picked from commit "+fix+")")
			return "revert: commit 01234567\n\nReverts-Commit: " + headOf(t, bare, "main")
		}, want: 1},
		{name: "revert of a commit GitHub cannot resolve", setup: func(t *testing.T, bare string) string {
			return "Revert \"feat: gone\"\n\nThis reverts commit " + strings.Repeat("ab", 20) + "."
		}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
			if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo,
				DeclaredPortBranches: []string{"main", "candidate"},
				DeclaredPortTypes:    map[string][]string{"main->candidate": {"fix", "revert"}}}); err != nil {
				t.Fatal(err)
			}
			message := tc.setup(t, sim.BareRepo)
			task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil, message)

			mergeQueued(t, ctx, store, sim, client, task, branch)

			if ports := portsOf(t, ctx, store, task.ID); len(ports) != tc.want {
				t.Fatalf("filed %d ports, want %d", len(ports), tc.want)
			}
			notes := portSkipNotes(t, ctx, store, task.ID)
			if tc.want == 0 && (len(notes) != 1 || !strings.Contains(notes[0], "there is nothing on `candidate` to revert")) {
				t.Errorf("noted %q, want the revert named as having nothing on candidate to undo", notes)
			}
			if tc.want == 1 && len(notes) != 0 {
				t.Errorf("noted %q about a ported revert, want nothing said", notes)
			}
		})
	}
}

// The note on a change the port-types filter left off its paired branch
// is said once per merge: a close-out that runs filePorts again for the
// same merge does not repeat it.
func TestAPortSkipIsNotedOnce(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo,
		DeclaredPortBranches: []string{"main", "candidate"},
		DeclaredPortTypes:    map[string][]string{"main->candidate": {"fix", "revert"}}}); err != nil {
		t.Fatal(err)
	}
	task, branch := queuedTaskWithCommits(t, ctx, store, sim, client, "t1", repo, nil, "feat: a new thing")

	mergeQueued(t, ctx, store, sim, client, task, branch)

	ref := pullRequestOf(t, task)
	detail, err := client.GetPullRequest(repo.Owner, repo.Name, ref.Number)
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.FilePorts(ctx, store, client, task, ref, detail, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := "Merged " + ref.String() + " onto `main`, which `acme/widgets` keeps paired with `candidate`, " +
		"but grain did not port it there: not a fix/revert change and no `Port: yes` footer " +
		"(`main` ports only those types to `candidate`); it reaches `candidate` some other way, such as the next release cut."
	if notes := portSkipNotes(t, ctx, store, task.ID); !slices.Equal(notes, []string{want}) {
		t.Errorf("after the close-out ran twice, noted %q, want %q once", notes, want)
	}
}

func TestCommitType(t *testing.T) {
	for message, want := range map[string]string{
		"fix: the bug":                   "fix",
		"Fix(ui): the bug\n\nfeat: body": "fix",
		"fix!: breaking":                 "fix",
		"feat(port)!: breaking":          "feat",
		"revert: feat: x":                "revert",
		"Revert \"feat: x\"":             "revert",
		"Merge pull request #1":          "",
		"fixed the bug":                  "",
		"\nfix: not the summary line":    "",
	} {
		if got := orchestrator.CommitType(message); got != want {
			t.Errorf("commitType(%q) = %q, want %q", message, got, want)
		}
	}
}

// reviewedSource is an auto-merging change onto main touching
// pkg/ui/server.go, held for a security review that has finished clean
// and released it -- the change task 2791 was before its port.
func reviewedSource(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client *github.RESTClient, repo model.RepoRef) (model.Task, string, string) {

	t.Helper()
	task := filedTask(t, ctx, store, "t1", repo)
	task.AutoMerge = true
	task.CreatedAt = &baseTime
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "reviewed\n")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	if reviewID == "" {
		t.Fatal("the source was not held for a security review")
	}
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge {
		t.Fatalf("the clean review did not release the source: links=%+v", got.Links)
	}
	return got, branch, reviewID
}

// securityReviewsOf is every security review filed against task.
func securityReviewsOf(t *testing.T, ctx context.Context, store *model.Store, task string) []model.Task {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, r := range tasks {
		if strings.HasPrefix(r.Title, "Security review: ") && linkOf(r, model.LinkProposedBy) == task {
			out = append(out, r)
		}
	}
	return out
}

// requireInherited fails unless port carries source's clearance for its
// current head, merging itself, with no review of its own filed and its
// pull request told why exactly once.
func requireInherited(t *testing.T, ctx context.Context, store *model.Store, client *github.RESTClient,
	sim *githubsim.Sim, port model.Task, source, sourceReview string) model.Task {

	t.Helper()
	got := reloaded(t, ctx, store, port.ID)
	if !got.AutoMerge {
		t.Fatalf("the port lost auto-merge: links=%+v", got.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, port.ID); len(reviews) != 0 {
		t.Fatalf("filed %d security reviews of a port of a reviewed change, want 0", len(reviews))
	}
	if v := linkOf(got, model.LinkSecurityHold); v != "" {
		t.Fatalf("the port is on hold: links=%+v", got.Links)
	}
	head := headOf(t, sim.BareRepo, got.BranchName())
	if v := linkOf(got, model.LinkTrustBoundaryCleared); v != trustboundary.Version+"@"+head {
		t.Fatalf("cleared = %q, want it for the port's head %s", v, head)
	}
	if v := linkOf(got, model.LinkClearanceInheritedFrom); v != source {
		t.Fatalf("inherited from %q, want %s", v, source)
	}
	var notes []string
	for _, body := range prCommentBodies(t, client, got) {
		if strings.Contains(body, "is a port of task "+source) {
			notes = append(notes, body)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], sourceReview) || !strings.Contains(notes[0], "`pkg/ui/server.go`") {
		t.Fatalf("the pull request was not told once where its clearance came from: %q", notes)
	}
	return got
}

// A mechanical port of a change whose security review finished clean is
// the same patch on the trust boundary, so it inherits that clearance:
// it keeps auto-merge and files no second review (task 2937).
func TestMechanicalPortInheritsSecurityReviewClearance(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	source, branch, reviewID := reviewedSource(t, ctx, store, sim, client, repo)

	mergeWithPorter(t, ctx, store, sim, client, mechanicalPorter(t, sim), source, branch)

	ports := portsOf(t, ctx, store, source.ID)
	if len(ports) != 1 || !slices.Contains(ports[0].Tags, model.TagMechanicalPort) {
		t.Fatalf("want one mechanical port, got %+v", ports)
	}
	requireInherited(t, ctx, store, client, sim, ports[0], source.ID, reviewID)
}

// agentPort is the port the merge of source filed, after an agent's run
// cherry-picked content onto it at path and finished.
func agentPort(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client *github.RESTClient, source model.Task, path, content string) model.Task {

	t.Helper()
	ports := portsOf(t, ctx, store, source.ID)
	if len(ports) != 1 || slices.Contains(ports[0].Tags, model.TagMechanicalPort) {
		t.Fatalf("want one agent port, got %+v", ports)
	}
	port := ports[0]
	branch := port.BranchName()
	createBranchAt(t, sim.BareRepo, "candidate", branch)
	pushFile(t, sim.BareRepo, branch, path, content)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, port, result, port.ID+"-1", baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	return reloaded(t, ctx, store, port.ID)
}

// An agent's port whose trust-boundary patches are the source's -- a clean
// cherry-pick, onto a candidate that has moved on in files of its own --
// inherits the source's clearance the way a mechanical one does.
func TestCleanPortInheritsSecurityReviewClearance(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	source, branch, reviewID := reviewedSource(t, ctx, store, sim, client, repo)
	pushFile(t, sim.BareRepo, "candidate", "CHANGELOG.md", "only on candidate\n")
	mergeQueued(t, ctx, store, sim, client, source, branch)

	port := agentPort(t, ctx, store, sim, client, source, "pkg/ui/server.go", "reviewed\n")
	requireInherited(t, ctx, store, client, sim, port, source.ID, reviewID)
}

// A port whose trust-boundary patch is not the source's -- a conflict
// resolved by hand, an adaptation -- is something nobody has reviewed, and
// is held like any other change.
func TestAdaptedPortIsHeldForItsOwnSecurityReview(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	source, branch, _ := reviewedSource(t, ctx, store, sim, client, repo)
	mergeQueued(t, ctx, store, sim, client, source, branch)

	port := agentPort(t, ctx, store, sim, client, source, "pkg/ui/server.go", "reviewed, then adapted\n")
	if port.AutoMerge || linkOf(port, model.LinkClearanceInheritedFrom) != "" {
		t.Fatalf("an adapted port inherited its source's clearance: links=%+v", port.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, port.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews of an adapted port, want 1", len(reviews))
	}
}

// A port of a change that was never released by a review -- one a human
// pressed Submit on over its hold -- has no clearance to inherit.
func TestPortOfAnUnreviewedChangeIsHeld(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	source, branch, _ := reviewedSource(t, ctx, store, sim, client, repo)
	if err := store.UpdateTask(ctx, source.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkSecurityHold, Target: trustboundary.Version})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mergeQueued(t, ctx, store, sim, client, source, branch)

	port := agentPort(t, ctx, store, sim, client, source, "pkg/ui/server.go", "reviewed\n")
	if port.AutoMerge || len(securityReviewsOf(t, ctx, store, port.ID)) != 1 {
		t.Fatalf("a port of a change merged over its hold was not held: automerge=%v links=%+v", port.AutoMerge, port.Links)
	}
}

// headOf is branch's tip in bare, read straight off the repository.
func headOf(t *testing.T, bare, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		t.Fatalf("reading %s's head: %v", branch, err)
	}
	return strings.TrimSpace(string(out))
}

// A port held for a review of its own -- its first push adapted the
// patch -- whose next push brings it back to exactly the source's patch
// inherits the source's clearance from then on: auto-merge back, and the
// review that first push filed closed unread.
func TestHeldPortThatComesToMatchItsSourceInheritsItsClearance(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	source, branch, _ := reviewedSource(t, ctx, store, sim, client, repo)
	mergeQueued(t, ctx, store, sim, client, source, branch)
	port := agentPort(t, ctx, store, sim, client, source, "pkg/ui/server.go", "reviewed, then adapted\n")
	own := linkOf(port, model.LinkSecurityReviewTask)
	if port.AutoMerge || own == "" {
		t.Fatalf("an adapted port was not held: links=%+v", port.Links)
	}

	pushFile(t, sim.BareRepo, port.BranchName(), "pkg/ui/server.go", "reviewed\n")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, port, result, port.ID+"-2", baseTime.Add(3*time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, port.ID)
	if !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" || linkOf(got, model.LinkClearanceInheritedFrom) != source.ID {
		t.Fatalf("the port did not inherit once it matched: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if st, err := store.State(ctx, own); err != nil || st != model.StateClosed {
		t.Fatalf("its own review %s is %q (%v), want closed", own, st, err)
	}
	if reviews := securityReviewsOf(t, ctx, store, port.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want only the first", len(reviews))
	}
}
