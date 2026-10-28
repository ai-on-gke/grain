package e2e

// docs/test-plans/2026-10-05.md §1, step 1 and 2: a three-level tree of
// work rooted at a GitHub issue, driven end to end through the real
// orchestrator (RunCycle: dispatch, finish, sync, the work-trees
// reconciler), real git, githubsim standing in for GitHub and the
// scripted antigravity agent standing in for a live model. The report is
// docs/test-runs/2026-10-05-work-origins-and-trees.md.
//
// The root here is a human-filed task with no origin rather than a chat:
// a chat is a console session RunCycle does not dispatch through this
// rig, and what makes a chat the root of the work it files is only that
// it has no GitHub origin of its own (originForNewTask in pkg/model), which this
// root shares.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// queuedFramework hands each dispatch the next script in line, so one
// cycle's single dispatch (MaxWorkers 1, one task approved at a time)
// runs the script the test queued for it.
type queuedFramework struct {
	mu      sync.Mutex
	scripts [][]antigravity.Step
}

func (q *queuedFramework) push(script []antigravity.Step) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.scripts = append(q.scripts, script)
}

func (q *queuedFramework) factory(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.scripts) == 0 {
		return antigravity.NewForTest(antigravity.Steps(finalText("nothing scripted"))), nil
	}
	next := q.scripts[0]
	q.scripts = q.scripts[1:]
	return antigravity.NewForTest(antigravity.Steps(next...)), nil
}

func proposeStep(title, origin string) antigravity.Step {
	args := map[string]any{"title": title, "body": "follow-up: " + title}
	if origin != "" {
		args["origin"] = origin
	}
	return toolCall("propose_task", args)
}

func (s *syncedSim) treeComments(number int) []github.Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []github.Comment
	for _, c := range s.sim.Comments[number] {
		if strings.Contains(c.Body, "<!-- grain:work-tree -->") {
			out = append(out, c)
		}
	}
	return out
}

func (s *syncedSim) pullRequestFor(branch string) (githubsim.PullRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range s.sim.PullRequests {
		if pr.Head == branch {
			return pr, true
		}
	}
	return githubsim.PullRequest{}, false
}

func (s *syncedSim) commentCalls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.sim.Calls {
		if c.Method == method && (strings.Contains(c.Path, "/issues/7/comments") || strings.Contains(c.Path, "/issues/comments/")) {
			n++
		}
	}
	return n
}

func TestWorkTreeWalkthroughThreeLevelsFromAnIssue(t *testing.T) {
	t.Parallel()
	const owner, repoName = "acme", "app"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")

	gs := githubsim.New(owner, repoName, bare, "main")
	gs.Issues[7] = &githubsim.Issue{Title: "Paginate the tasks API", Body: "it is slow", State: "open", Labels: map[string]struct{}{}}
	sim := &syncedSim{sim: gs}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"
	target := model.RepoRef{Owner: owner, Name: repoName}
	origin := model.WorkOrigin("acme/app#7")

	frameworks := &queuedFramework{}
	deps := orchestrator.Deps{
		Client: github.NewClient(sim, nil), Sandboxes: credentialed(t, remote), MaxWorkers: 1,
		Framework: frameworks.factory,
	}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}
	now := baseTime
	cycle := func(store *model.Store, ctx context.Context, what string) {
		t.Helper()
		// Past the work-trees reconciler's two-minute look per origin.
		now = now.Add(3 * time.Minute)
		if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
			t.Fatalf("RunCycle (%s): %v", what, err)
		}
	}
	approve := func(store *model.Store, ctx context.Context, id string, autoMerge bool) {
		t.Helper()
		if err := store.Approve(ctx, id, model.Attribution{Actor: human}, now); err != nil {
			t.Fatalf("approving %s: %v", id, err)
		}
		if autoMerge {
			if err := store.UpdateTask(ctx, id, func(tk *model.Task) error { tk.AutoMerge = autoMerge; return nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	childByTitle := func(store *model.Store, ctx context.Context, parent, title string) model.Task {
		t.Helper()
		kids, err := store.ChildTasks(ctx, parent)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range kids {
			if k.Title == title {
				return k
			}
		}
		t.Fatalf("task %s filed no %q; it filed %+v", parent, title, kids)
		return model.Task{}
	}

	withStore(t, t.TempDir(), func(store *model.Store, ctx context.Context) {
		deps.Store = store
		// The first cycle files the director's console session; file the
		// root after it so the two do not share an id.
		cycle(store, ctx, "settle the director")
		rootID, err := store.NewTaskID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		root := fileIssue(&world{t: t, ctx: ctx, store: store}, rootID, human, target)
		if root.WorkOrigin != "" {
			t.Fatalf("a human-filed task with no issue has origin %q, want none", root.WorkOrigin)
		}

		// The root's run files two follow-ups naming the issue, and the
		// three rejection paths of step 2: a malformed origin, and an
		// origin in a repository this deployment does not serve
		// (recorded, not reconciled: githubsim serves acme/app only).
		frameworks.push([]antigravity.Step{
			proposeStep("Add a cursor to the list endpoint", "acme/app#7"),
			proposeStep("Document the page size", "#7"),
			proposeStep("Malformed origin", "not a ref"),
			toolCall("comment_on_issue", map[string]any{"comment": "filed the work for #7"}),
			finalText("filed"),
		})
		cycle(store, ctx, "dispatch the root")

		cursor := childByTitle(store, ctx, root.ID, "Add a cursor to the list endpoint")
		docs := childByTitle(store, ctx, root.ID, "Document the page size")
		malformed := childByTitle(store, ctx, root.ID, "Malformed origin")
		for _, tk := range []model.Task{cursor, docs} {
			if tk.WorkOrigin != origin {
				t.Errorf("task %s (%s) origin = %q, want %q", tk.ID, tk.Title, tk.WorkOrigin, origin)
			}
		}
		// Step 2, malformed: filed, inheriting, and saying why.
		if malformed.WorkOrigin != model.TaskOrigin(root.ID) {
			t.Errorf("malformed origin: task origin = %q, want it to inherit task:1", malformed.WorkOrigin)
		}
		if !strings.Contains(malformed.Body, "could not use this proposal's origin") {
			t.Errorf("malformed origin: body says nothing about it: %q", malformed.Body)
		}

		// The cursor task runs, pushes, and files a grandchild that names
		// another issue: its tree already has a GitHub origin, so the
		// grandchild stays in #7's tree -- and is told so.
		approve(store, ctx, cursor.ID, true)
		frameworks.push(append([]antigravity.Step{proposeStep("Extract the cursor codec", "acme/app#99")},
			pushScriptOwnFile(remote, string(model.BranchName(cursor.ID)), cursor.ID)...))
		cycle(store, ctx, "dispatch the cursor task")
		codec := childByTitle(store, ctx, cursor.ID, "Extract the cursor codec")
		if codec.WorkOrigin != origin {
			t.Errorf("grandchild origin = %q, want %q (an origin is inherited at any depth)", codec.WorkOrigin, origin)
		}
		if !strings.Contains(codec.Body, "acme/app#99") {
			t.Errorf("grandchild naming acme/app#99 was re-homed to %s without a word in its body: %q", codec.WorkOrigin, codec.Body)
		}

		cursorPR, ok := sim.pullRequestFor(string(model.BranchName(cursor.ID)))
		if !ok {
			t.Fatalf("the cursor task opened no pull request")
		}
		if _, ok := cursorPR.Labels["origin/#7"]; !ok {
			t.Errorf("cursor PR labels = %v, want origin/#7", cursorPR.Labels)
		}
		for _, want := range []string{"<!-- grain:lineage -->", "Filed by: task " + root.ID, "Origin: [acme/app#7]", "task " + codec.ID + ": Extract the cursor codec"} {
			if !strings.Contains(cursorPR.Body, want) {
				t.Errorf("cursor PR lineage lacks %q:\n%s", want, cursorPR.Body)
			}
		}

		// One progress comment on #7.
		comments := sim.treeComments(7)
		if len(comments) != 1 {
			t.Fatalf("progress comments on #7 = %d, want 1: %+v", len(comments), comments)
		}
		first := comments[0].Body
		if !strings.Contains(first, "0 of 3 done") {
			t.Errorf("first progress comment: %q, want 0 of 3 done", first)
		}

		// A repository that refuses labels: the docs task's pull request
		// still opens and the tree is still reconciled.
		sim.mu.Lock()
		gs.RefuseLabels = 403
		sim.mu.Unlock()
		approve(store, ctx, docs.ID, false)
		frameworks.push(pushScriptOwnFile(remote, string(model.BranchName(docs.ID)), docs.ID))
		cycle(store, ctx, "dispatch the docs task")
		docsPR, ok := sim.pullRequestFor(string(model.BranchName(docs.ID)))
		if !ok {
			t.Fatalf("with labels refused, the docs task opened no pull request")
		}
		if len(docsPR.Labels) != 0 {
			t.Errorf("a refused label was applied: %v", docsPR.Labels)
		}
		sim.mu.Lock()
		gs.RefuseLabels = 0
		sim.mu.Unlock()

		// The cursor task merges through grain's own auto-merge.
		sim.markPullRequestClean(cursorPR.Number)
		cycle(store, ctx, "merge the cursor task")
		cycle(store, ctx, "reconcile after the merge")
		if got := sim.pullRequestState(cursorPR.Number); got != "closed" {
			t.Fatalf("cursor PR state = %q, want merged", got)
		}
		msg := runOutput(t, upstream, "git", "--git-dir", bare, "log", "-1", "--format=%B", "main")
		for _, want := range []string{"Origin: acme/app#7", "Filed-by: task " + root.ID} {
			if !strings.Contains(msg, want) {
				t.Errorf("merge commit lacks %q:\n%s", want, msg)
			}
		}

		comments = sim.treeComments(7)
		if len(comments) != 1 {
			t.Fatalf("after the merge, progress comments on #7 = %d, want still 1", len(comments))
		}
		if comments[0].Body == first {
			t.Errorf("the progress comment was never edited: %q", first)
		}
		if posts := sim.commentCalls("POST"); posts != 1 {
			t.Errorf("POSTs of a progress comment on #7 = %d, want 1 (then edits)", posts)
		}
		if edits := sim.commentCalls("PATCH"); edits == 0 {
			t.Errorf("the progress comment was never PATCHed")
		}

		// "n of m done" agrees with GET /api/tasks/{id}/tree for every
		// task in the tree -- the chat's own page included.
		uiClient := ui.NewClient(ui.Config{Actor: ui.DefaultActor("operator@example.com")}, store)
		tree, err := store.WorkTree(ctx, origin, nil)
		if err != nil || tree == nil {
			t.Fatalf("WorkTree(%s): %v", origin, err)
		}
		if !strings.Contains(comments[0].Body, "1 of 3 done") || tree.Done != 1 || tree.Total != 3 {
			t.Errorf("progress comment %q vs tree %d of %d, want 1 of 3", comments[0].Body, tree.Done, tree.Total)
		}
		for _, id := range []string{cursor.ID, docs.ID, codec.ID} {
			got, err := uiClient.WorkTree(ctx, id, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Done != 1 || got.Total != 3 {
				t.Errorf("GET /api/tasks/%s/tree = origin %s, %d of %d; the progress comment on #7 says 1 of 3",
					id, got.Origin, got.Done, got.Total)
			}
		}
		// The root's own page draws everything it filed, whatever tree
		// each went into: the three in #7's and the one that inherited
		// task:<root> when its origin was malformed.
		rootTree, err := uiClient.WorkTree(ctx, root.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if rootTree.Done != 1 || rootTree.Total != 4 {
			t.Errorf("GET /api/tasks/%s/tree = %d of %d, want 1 of 4 (everything it filed)", root.ID, rootTree.Done, rootTree.Total)
		}
	})
}
