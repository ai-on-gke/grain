package daemon

import (
	"context"
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

// diffTestRepo is a simulated forge over a real bare repository, and a
// checkout of it to commit from.
type diffTestRepo struct {
	sim  *githubsim.Sim
	seed string
}

func newDiffTestRepo(t *testing.T) *diffTestRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare := filepath.Join(t.TempDir(), "widgets.git")
	gitT(t, "", "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(t.TempDir(), "seed")
	gitT(t, "", "clone", "-q", bare, seed)
	gitT(t, seed, "config", "user.email", "s@example.com")
	gitT(t, seed, "config", "user.name", "s")
	gitT(t, seed, "commit", "-q", "--allow-empty", "-m", "seed")
	gitT(t, seed, "push", "-q", "origin", "main")
	sim := githubsim.New("acme", "widgets", bare, "main")
	sim.PullRequestBase = githubsim.UniquePullRequestBase()
	return &diffTestRepo{sim: sim, seed: seed}
}

// commit writes path on branch (cut from main the first time) and pushes it.
func (r *diffTestRepo) commit(t *testing.T, branch, path, content string) {
	t.Helper()
	if err := exec.Command("git", "-C", r.seed, "checkout", "-q", branch).Run(); err != nil {
		gitT(t, r.seed, "checkout", "-q", "-b", branch, "main")
	}
	full := filepath.Join(r.seed, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, r.seed, "add", "-A")
	gitT(t, r.seed, "commit", "-q", "-m", "write "+path)
	gitT(t, r.seed, "push", "-q", "origin", branch)
}

// The wrapper the daemon puts around its forge client has to pass the raw
// diffs through: the security hold finds them by type assertion, and a
// wrapper without them made every carried clearance fail closed into a
// fresh review (tasks 447, 439, 441 and 424 -- two and three reviews each
// of one change).
func TestStateRepoPullRequestClientServesRawDiffs(t *testing.T) {
	repo := newDiffTestRepo(t)
	repo.commit(t, "feature", "README.md", "hello\n")
	var client github.Client = &StateRepoPullRequestClient{Client: github.NewClient(repo.sim, nil)}

	differ, ok := client.(github.DiffReader)
	if !ok {
		t.Fatal("StateRepoPullRequestClient hides github.DiffReader from the security hold")
	}
	diff, err := differ.CompareDiff("acme", "widgets", "main", "feature")
	if err != nil {
		t.Fatalf("CompareDiff: %v", err)
	}
	if !strings.Contains(diff, "diff --git a/README.md b/README.md") || !strings.Contains(diff, "+hello") {
		t.Fatalf("CompareDiff did not come through from the forge: %q", diff)
	}
}

// End to end through the wrapper: a change cleared by a review, whose head
// then moves on in a file off the list, keeps its clearance and merges
// without a second review.
func TestSecurityHoldCarriesAClearanceThroughStateRepoPullRequestClient(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	repo := newDiffTestRepo(t)
	client := &StateRepoPullRequestClient{Client: github.NewClient(repo.sim, nil), Store: store}
	at := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "touch the server", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human}, Target: &target, Binding: model.BindingDirective,
		AutoMerge: true,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	repo.commit(t, branch, "pkg/ui/server.go", "changed\n")
	result := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "pushed"}}}
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", at); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	reviews := func() []string {
		t.Helper()
		tasks, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range tasks {
			if strings.HasPrefix(r.Title, "Security review: ") {
				out = append(out, r.ID)
			}
		}
		return out
	}
	filed := reviews()
	if len(filed) != 1 {
		t.Fatalf("filed %d security reviews of a trust-boundary change, want 1", len(filed))
	}
	done := at.Add(30 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: filed[0], CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	repo.commit(t, branch, "docs/notes.md", "a docs fix merged back\n")
	mergeable := true
	for i := range repo.sim.PullRequests {
		repo.sim.PullRequests[i].Mergeable = &mergeable
	}
	success := "success"
	repo.sim.CheckRuns[branch] = []github.CheckRun{{Name: "tests", Status: "completed", Conclusion: &success}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, at.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if got := reviews(); len(got) != 1 {
		t.Fatalf("filed %d security reviews, want only the first: the clearance was not carried", len(got))
	}
	merged := false
	for _, pr := range repo.sim.PullRequests {
		if pr.Head == branch {
			merged = pr.Merged
		}
	}
	if !merged {
		got, _ := store.GetTask(ctx, task.ID)
		t.Fatalf("a reviewed change whose head moved off the list did not merge: links=%+v", got.Links)
	}
}

// The controller's own configuration repository has no forge to ask, so
// its diffs come off the local checkout -- in the form the forge's raw
// diff takes, which is what the security hold parses.
func TestStateRepoPullRequestClientDiffsTheLocalConfigRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer db.Close()
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("opening state repo: %v", err)
	}
	if err := loadStateRepo(ctx, dataDir, repo, db, false); err != nil {
		t.Fatalf("loading state repo: %v", err)
	}
	manager := NewStateManager(dataDir, db, repo, OpenSecrets(dataDir), nil, nil)
	client := &StateRepoPullRequestClient{Manager: manager, Store: store}
	if !client.isLocal("grain", "config") {
		t.Fatal("expected grain/config to be answered locally")
	}

	dir, base := repo.Dir(), repo.Branch()
	work := filepath.Join(t.TempDir(), "work")
	gitT(t, "", "clone", "-q", "--branch", base, dir, work)
	gitT(t, work, "checkout", "-q", "-b", "edit")
	if err := os.WriteFile(filepath.Join(work, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, work, "add", "note.txt")
	gitT(t, work, "-c", "user.email=s@example.com", "-c", "user.name=s", "commit", "-q", "-m", "note")
	gitT(t, work, "push", "-q", "origin", "edit")

	diff, err := client.CompareDiff("grain", "config", base, "edit")
	if err != nil {
		t.Fatalf("CompareDiff: %v", err)
	}
	if !strings.Contains(diff, "diff --git a/note.txt b/note.txt") || !strings.HasSuffix(diff, "+hello\n") {
		t.Fatalf("local CompareDiff = %q", diff)
	}
	if _, err := client.CompareDiff("grain", "config", "--output=/tmp/x", "edit"); err == nil {
		t.Fatal("CompareDiff took an option for a revision")
	}
}
