package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

var selfDebugTask = model.Task{
	ID:     "task-selfdebug",
	Target: &model.RepoRef{Owner: "acme", Name: "proj"},
	Grants: []model.Grant{{Capability: model.SelfDebugCapability}},
}

// bakeSource stages a baked copy the way the image build does: a small
// repository of commits commits, exported by scripts/export-source.sh
// with depth commits of history. It returns the export and its HEAD.
func bakeSource(t *testing.T, commits, depth int) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	for i := 0; i < commits; i++ {
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("revision "+strconv.Itoa(i)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", "README.md")
		// A second apart, since the cut is by commit date.
		commit := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "commit "+strconv.Itoa(i))
		commit.Dir = repo
		date := "@" + strconv.Itoa(1700000000+i) + " +0000"
		commit.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_DATE="+date)
		if out, err := commit.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
	git(t, repo, "config", "remote.origin.url", "https://user:secret-token@example.com/acme/private.git")
	head := git(t, repo, "rev-parse", "HEAD")
	dest := filepath.Join(t.TempDir(), "src")
	script, err := filepath.Abs("../../scripts/export-source.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, repo, dest)
	cmd.Env = append(os.Environ(), "GRAIN_SOURCE_HISTORY_DEPTH="+strconv.Itoa(depth), "GRAIN_SOURCE_SECRET_SCAN=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}
	return dest, head
}

// With a baked copy, a self-debug run's grain-src/ is that copy, at the
// revision it was built from, with the shallow history it was baked
// with and nothing of the building machine's own .git -- and nothing
// about it touched the git proxy.
func TestGrainSourceIsTheBakedCopyWithItsShallowHistory(t *testing.T) {
	t.Parallel()
	src, head := bakeSource(t, 5, 3)
	root := t.TempDir()
	out := checkout{Root: root}
	placeGrainSource(context.Background(), selfDebugTask, src, root, nil, mcp.NewSandboxTools(root), &out)

	if out.GrainSource != grainSourceReady {
		t.Fatalf("GrainSource = %q (%s), want ready", out.GrainSource, out.GrainSourceWhy)
	}
	dir := filepath.Join(root, GrainSourceDir)
	if got := git(t, dir, "log", "-1", "--format=%H"); got != head {
		t.Errorf("git log -1 in grain-src = %s, want the build revision %s", got, head)
	}
	if got := git(t, dir, "rev-list", "--count", "HEAD"); got != "3" {
		t.Errorf("grain-src carries %s commits, want the 3 baked", got)
	}
	if got := git(t, dir, "for-each-ref"); got != "" {
		t.Errorf("grain-src carries refs %q, want none but a detached HEAD", got)
	}
	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "secret-token") || strings.Contains(string(config), "acme/private") {
		t.Errorf("grain-src's .git/config carries the building checkout's remote:\n%s", config)
	}
	if out.GrainSourceRevision != head || out.GrainSourceCommits != 3 {
		t.Errorf("checkout records %s with %d commits, want %s with 3", out.GrainSourceRevision, out.GrainSourceCommits, head)
	}
	if out.ProxyBase != "" {
		t.Errorf("placing grain-src set a proxy base %q: it must not go near the git proxy", out.ProxyBase)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed"), 0o644); err == nil && os.Geteuid() != 0 {
		t.Error("grain-src/README.md is writable, want it read-only")
	}

	prompt := grainSourceSection(out, selfDebugTask)
	for _, want := range []string{dir, head, "last 3 commits", "git blame"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not say %q:\n%s", want, prompt)
		}
	}
}

// A copy baked without history is still placed, and the prompt says
// git log will not work there.
func TestGrainSourceBakedWithoutHistory(t *testing.T) {
	t.Parallel()
	src, _ := bakeSource(t, 2, 0)
	root := t.TempDir()
	out := checkout{Root: root}
	placeGrainSource(context.Background(), selfDebugTask, src, root, nil, nil, &out)
	if out.GrainSource != grainSourceReady || out.GrainSourceCommits != 0 {
		t.Fatalf("GrainSource = %q with %d commits, want ready with none", out.GrainSource, out.GrainSourceCommits)
	}
	if _, err := os.Stat(filepath.Join(root, GrainSourceDir, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, GrainSourceDir, ".git")); !os.IsNotExist(err) {
		t.Errorf("grain-src/.git: %v, want none", err)
	}
	if prompt := grainSourceSection(out, selfDebugTask); !strings.Contains(prompt, "no git history") {
		t.Errorf("prompt does not say there is no history:\n%s", prompt)
	}
}

// A source tree that is an operator's own checkout (-upgrade-src-dir),
// rather than an export, has a .git that may carry anything: it is never
// handed to a sandbox.
func TestGrainSourceNeverPlacesARealCheckoutsGit(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	git(t, src, "init", "-q")
	git(t, src, "config", "credential.helper", "store --file /secret")
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	out := checkout{Root: root}
	placeGrainSource(context.Background(), selfDebugTask, src, root, nil, nil, &out)
	if out.GrainSource != grainSourceReady {
		t.Fatalf("GrainSource = %q (%s), want ready", out.GrainSource, out.GrainSourceWhy)
	}
	if _, err := os.Stat(filepath.Join(root, GrainSourceDir, "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, GrainSourceDir, ".git")); !os.IsNotExist(err) {
		t.Errorf("grain-src/.git: %v, want the checkout's own .git left behind", err)
	}
}

// A baked copy this process cannot write is linked rather than copied:
// nothing is paid per run, and the files' owner keeps it read-only.
func TestGrainSourceLinksATreeThisProcessCannotWrite(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root can write anything")
	}
	src, _ := bakeSource(t, 1, 1)
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o755) })
	root := t.TempDir()
	out := checkout{Root: root}
	placeGrainSource(context.Background(), selfDebugTask, src, root, nil, nil, &out)
	if out.GrainSource != grainSourceReady {
		t.Fatalf("GrainSource = %q (%s), want ready", out.GrainSource, out.GrainSourceWhy)
	}
	if target, err := os.Readlink(filepath.Join(root, GrainSourceDir)); err != nil || target != src {
		t.Errorf("grain-src links to %q (%v), want %s", target, err, src)
	}
}

// A sandbox this process can only run commands in has the archive
// streamed into it.
func TestGrainSourceIsStreamedIntoACommandOnlySandbox(t *testing.T) {
	t.Parallel()
	src, head := bakeSource(t, 3, 2)
	root := t.TempDir()
	out := checkout{Root: root}
	placeGrainSource(context.Background(), selfDebugTask, src, "", localCommander{}, nil, &out)
	if out.GrainSource != grainSourceReady {
		t.Fatalf("GrainSource = %q (%s), want ready", out.GrainSource, out.GrainSourceWhy)
	}
	if got := git(t, filepath.Join(root, GrainSourceDir), "log", "-1", "--format=%H"); got != head {
		t.Errorf("git log -1 = %s, want %s", got, head)
	}
}

type localCommander struct{ SandboxPlacer }

func (localCommander) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String() + err.Error(), 1
	}
	return stdout.String(), stderr.String(), 0
}

// Without a baked copy only grain-src/ is skipped: the run goes ahead,
// and is told why there is nothing there.
func TestGrainSourceWithoutABakedCopyIsSkippedNotRefused(t *testing.T) {
	t.Parallel()
	for _, srcDir := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		root := t.TempDir()
		out := checkout{Root: root}
		placeGrainSource(context.Background(), selfDebugTask, srcDir, root, nil, nil, &out)
		if out.GrainSource != grainSourceNotBaked {
			t.Fatalf("%q: GrainSource = %q, want %q", srcDir, out.GrainSource, grainSourceNotBaked)
		}
		if _, err := os.Lstat(filepath.Join(root, GrainSourceDir)); !os.IsNotExist(err) {
			t.Errorf("%q: grain-src: %v, want none", srcDir, err)
		}
		if prompt := grainSourceSection(out, selfDebugTask); !strings.Contains(prompt, "this deployment has no baked-in copy of grain's source") {
			t.Errorf("%q: prompt does not say why:\n%s", srcDir, prompt)
		}
	}
}

// Only self-debug is given the copy. A task that reads bwsalmon/grain
// clones it like any other read; nothing is placed for it here.
func TestGrainSourceAbsentWithoutSelfDebug(t *testing.T) {
	t.Parallel()
	src, _ := bakeSource(t, 1, 1)
	for _, task := range []model.Task{
		{ID: "plain", Target: selfDebugTask.Target},
		{ID: "reads", Target: selfDebugTask.Target, Reads: []model.RepoRef{model.GrainSourceRepo()}},
	} {
		root := t.TempDir()
		out := checkout{Root: root}
		placeGrainSource(context.Background(), task, src, root, nil, nil, &out)
		if out.GrainSource != grainSourceAbsent {
			t.Errorf("%s: GrainSource = %q, want absent", task.ID, out.GrainSource)
		}
		if _, err := os.Lstat(filepath.Join(root, GrainSourceDir)); !os.IsNotExist(err) {
			t.Errorf("%s: grain-src: %v, want none", task.ID, err)
		}
		if prompt := grainSourceSection(out, task); prompt != "" {
			t.Errorf("%s: prompt = %q, want nothing", task.ID, prompt)
		}
	}
}

func TestGrainSourceUnavailableIsExplainedInThePrompt(t *testing.T) {
	t.Parallel()
	src, _ := bakeSource(t, 1, 1)
	out := checkout{Root: "/sandbox"}
	placeGrainSource(context.Background(), selfDebugTask, src, "", nil, nil, &out)
	if out.GrainSource != grainSourceUnavailable {
		t.Fatalf("GrainSource = %q, want unavailable", out.GrainSource)
	}
	prompt := grainSourceSection(out, selfDebugTask)
	if !strings.Contains(prompt, "/sandbox/grain-src") || !strings.Contains(prompt, "placing this deployment's baked-in copy of grain's source there failed") {
		t.Errorf("prompt = %q", prompt)
	}
}
