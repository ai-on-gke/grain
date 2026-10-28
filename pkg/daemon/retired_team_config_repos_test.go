package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func teamConfigTestStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// grain/config is the only configuration repository. A per-team name
// (grain/config-<team>) is refused before git is run: there is no local
// copy of one to read, and cloning it from a forge's grain/ owner with
// the deployment's credential is what it must never become.
func TestCiWorkflowSourceRefusesTeamConfigRepoNames(t *testing.T) {
	data := t.TempDir()
	// A leftover checkout from a multi-team build is not read either.
	stray := filepath.Join(data, "team-state-repos", "default")
	if err := os.MkdirAll(filepath.Join(stray, ".grain", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, stray, "init", "-q", "-b", "main")
	src := CiWorkflowSource{
		ConfigRepoDir: StateRepoDir(data),
		// Nothing listens here: a refusal that dialled would still fail,
		// so the error's words are what is checked.
		GitHubHost:   "127.0.0.1:9",
		InsecureHTTP: true,
		TempDir:      t.TempDir(),
	}
	for _, repo := range []string{"grain/config-default", "Grain/Config-Alpha", "grain/config-.."} {
		_, _, err := src.Workflows(t.Context(), repo, "main")
		if err == nil || !strings.Contains(err.Error(), "only grain/config exists") {
			t.Errorf("Workflows(%s) = %v, want the refusal saying only grain/config exists", repo, err)
		}
		if _, err := src.OnBranch(t.Context(), repo, "main", strings.Repeat("a", 40)); err == nil || !strings.Contains(err.Error(), "only grain/config exists") {
			t.Errorf("OnBranch(%s) = %v, want the refusal saying only grain/config exists", repo, err)
		}
	}

	checks := StateCheckSandboxes{ConfigRepoDir: StateRepoDir(data), TempDir: t.TempDir()}
	if _, err := checks.Acquire(t.Context(), ci.SandboxRequest{Builtin: StateCheckBuiltin, Repo: "grain/config-default"}); err == nil {
		t.Error("the state check acquired a checkout of grain/config-default")
	}
	if got, _, err := (ConfigRepoCISource{}).Workflows(t.Context(), "grain/config-default", "main"); err != nil || len(got) != 0 {
		t.Errorf("ConfigRepoCISource gave grain/config-default %d workflow(s), %v; want none", len(got), err)
	}
}

// A data directory from a multi-team build may still hold
// team-state-repos/. A start says so once, opens grain/config as it
// always does, and leaves the directory where it is.
func TestAStrayTeamStateReposDirectoryDoesNotBreakStartup(t *testing.T) {
	data := t.TempDir()
	if got := RetiredTeamStateReposNotice(data); got != "" {
		t.Errorf("notice with no team-state-repos = %q, want none", got)
	}
	stray := filepath.Join(data, "team-state-repos")
	gitIn(t, t.TempDir(), "init", "-q", "-b", "main", filepath.Join(stray, "default"))
	if err := os.WriteFile(filepath.Join(stray, "not-a-repo"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	notice := RetiredTeamStateReposNotice(data)
	if !strings.Contains(notice, stray) || !strings.Contains(notice, "grain/config is the only configuration repository") {
		t.Errorf("notice = %q, want it to name %s and say grain/config is the only one", notice, stray)
	}

	store := teamConfigTestStore(t)
	repo, err := OpenStateRepo(t.Context(), data)
	if err != nil {
		t.Fatalf("OpenStateRepo with a stray team-state-repos: %v", err)
	}
	if err := loadStateRepo(t.Context(), data, repo, store.DB(), false); err != nil {
		t.Fatalf("loading grain/config with a stray team-state-repos: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stray, "default", ".git")); err != nil {
		t.Errorf("the stray team-state-repos was touched: %v", err)
	}
}
