package daemon

import (
	"context"
	"os/exec"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// grain/task-473: the config repository's branches are its own
// checkout's, read with git rather than asked of GitHub.
func TestStateManagerBranchesListsTheLocalCheckout(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("opening the config repository: %v", err)
	}
	for _, args := range [][]string{
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "first"},
		{"branch", "grain/049f1e09/task-465"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo.Dir()}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	manager := NewStateManager(dataDir, nil, repo, OpenSecrets(dataDir), nil, nil)
	names, err := manager.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{repo.Branch(), "grain/049f1e09/task-465"} {
		if !slices.Contains(names, want) {
			t.Errorf("Branches() = %v, want %q among them", names, want)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("Branches() = %v, want sorted", names)
	}

	// And the gate the UI is handed routes grain/config to it.
	got, ok, err := repoBranchGate{local: manager}.RepoBranches(ctx, model.ConfigRepo())
	if !ok || err != nil || !slices.Equal(got, names) {
		t.Fatalf("gate = %v, %t, %v; want %v", got, ok, err, names)
	}
}

type gitBranchesStub struct {
	asked []string
	ok    bool
}

func (s *gitBranchesStub) GitBranches(owner, repo string) ([]string, bool, error) {
	s.asked = append(s.asked, owner+"/"+repo)
	if !s.ok {
		return nil, false, nil
	}
	return []string{"main"}, true, nil
}

// The config repository is never handed to the git-host lister (its
// remote, as far as the credentials know, is GitHub's), and every other
// repository is: a plain git host answers there, and a GitHub one says
// ok false so the UI asks GitHub.
func TestRepoBranchGateRoutesByRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := &gitBranchesStub{}
	gate := repoBranchGate{remote: remote}
	if _, ok, err := gate.RepoBranches(ctx, model.ConfigRepo()); !ok || err == nil {
		t.Errorf("config repo with no local source = ok %t, err %v; want ok and an error", ok, err)
	}
	if len(remote.asked) != 0 {
		t.Errorf("config repo asked of the git-host lister: %v", remote.asked)
	}
	if _, ok, _ := gate.RepoBranches(ctx, model.RepoRef{Owner: "acme", Name: "widgets"}); ok {
		t.Error("a GitHub repository was answered locally")
	}
	remote.ok = true
	plain := model.RepoRef{Host: "git.example.com", Owner: "walk", Name: "demo"}
	if names, ok, err := gate.RepoBranches(ctx, plain); !ok || err != nil || len(names) != 1 {
		t.Errorf("plain git repo = %v, %t, %v", names, ok, err)
	}
	if got := remote.asked[len(remote.asked)-1]; got != "git.example.com/walk/demo" {
		t.Errorf("asked about %q, want the host-qualified owner", got)
	}
}
