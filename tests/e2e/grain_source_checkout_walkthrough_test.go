// The read-only copy of grain's own source a self-debug task gets at
// grain-src/ (orchestrator's placeGrainSource): the deployment's baked-in
// copy, never a clone. Driven through a real orchestrator.RunDispatch with
// a real gitproxy in front of real bare upstream repos and pkg/mcp's real
// exec-backed sandbox tools: what the agent finds in its sandbox, that
// the proxy still will not serve it bwsalmon/grain, and what the prompt
// said.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/capability/selfdebug"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// grainSourceRun is what one dispatched task's run left behind: its
// sandbox, the prompt it was started with, and what its one scripted
// run_command printed.
type grainSourceRun struct {
	root, prompt, output string
}

// grainSourceProbe is the agent's whole turn: everything the walkthrough
// records about grain-src/, from inside the sandbox, in one command --
// including a fetch of bwsalmon/grain straight through the proxy, which
// no self-debug run may make now that nothing is cloned for it.
func grainSourceProbe(proxyURL string) string {
	return "ls grain-src; echo head=$(git -C grain-src log -1 --format=%H); " +
		"echo commits=$(git -C grain-src rev-list --count HEAD); " +
		"git -C grain-src push origin HEAD:refs/heads/probe 2>&1; echo push-exit=$?; " +
		"git ls-remote " + proxyURL + "/bwsalmon/grain.git 2>&1; echo fetch-exit=$?"
}

func dispatchForGrainSource(t *testing.T, w *world, id, srcDir string, grants []model.Grant, reads []model.RepoRef, at time.Time) grainSourceRun {
	t.Helper()
	tk := fileIssue(w, id, human("alice"), model.RepoRef{Owner: "acme", Name: "widgets"})
	tk.Grants, tk.Reads = grants, reads
	if err := w.store.PutTask(w.ctx, tk); err != nil {
		t.Fatal(err)
	}
	ds, err := dispatch.Cycle(w.ctx, w.store, model.Limits{Workers: 1}, at)
	if err != nil || len(ds) != 1 || ds[0].TaskID != id {
		t.Fatalf("Cycle for %s: %v, %+v", id, err, ds)
	}
	full, err := w.store.GetTask(w.ctx, id)
	if err != nil || full == nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	root := w.prepareSandbox(ds[0])
	rec := &promptRecorder{task: id, script: []antigravity.Step{
		toolCall("run_command", map[string]any{"command": grainSourceProbe(w.proxyURL)}),
		finalText("done"),
	}}
	fw, _ := rec.framework(w.ctx, orchestrator.AgentChoice{})
	result, err := orchestrator.RunDispatch(w.ctx, w.store, fw, orchestrator.Config{GitRemoteBase: w.proxyURL, GrainSourceDir: srcDir},
		*full, ds[0], mcp.NewSandboxTools(root), root, "", nil, at.Add(time.Minute))
	// The task starts whatever became of grain-src: it is a convenience,
	// and a run that cannot have it is told so instead.
	if err != nil {
		t.Fatalf("RunDispatch(%s): %v", id, err)
	}
	prompts := rec.prompts()
	if len(prompts) != 1 {
		t.Fatalf("%s ran %d times, want once", id, len(prompts))
	}
	return grainSourceRun{root: root, prompt: prompts[0], output: toolOutput(result)}
}

// bakeGrainSource stages a baked copy the way the image build does
// (scripts/export-source.sh) from a repository of four commits, keeping
// three of them, and returns the copy and its HEAD.
func bakeGrainSource(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	run := func(env []string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(nil, "init", "-q")
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("grain "+strconv.Itoa(i)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run(nil, "add", "README.md")
		date := "@" + strconv.Itoa(1700000000+i) + " +0000"
		run([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date},
			"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "commit "+strconv.Itoa(i))
	}
	dest := filepath.Join(t.TempDir(), "src")
	script, err := filepath.Abs("../../scripts/export-source.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, repo, dest)
	cmd.Env = append(os.Environ(), "GRAIN_SOURCE_HISTORY_DEPTH=3", "GRAIN_SOURCE_SECRET_SCAN=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("export-source.sh: %v\n%s", err, out)
	}
	return dest, run(nil, "rev-parse", "HEAD")
}

func TestGrainSourceCheckoutWalkthrough(t *testing.T) {
	t.Parallel()
	grain := model.GrainSourceRepo()
	selfDebug := []model.Grant{{Capability: selfdebug.CapabilityName}}
	newGrainWorld := func(t *testing.T) *world {
		t.Helper()
		w := newWorld(t)
		w.newRepo("acme", "widgets")
		w.newRepo("bwsalmon", "grain")
		return w
	}

	t.Run("self-debug with a baked copy", func(t *testing.T) {
		t.Parallel()
		w := newGrainWorld(t)
		src, head := bakeGrainSource(t)
		run := dispatchForGrainSource(t, w, "iss-src-baked", src, selfDebug, nil, baseTime)
		if _, err := os.Stat(filepath.Join(run.root, orchestrator.GrainSourceDir, "README.md")); err != nil {
			t.Fatalf("grain-src was not placed: %v\nagent saw:\n%s", err, run.output)
		}
		if !strings.Contains(run.output, "head="+head) || !strings.Contains(run.output, "commits=3") {
			t.Errorf("git log in grain-src is not the baked %s with 3 commits:\n%s", head, run.output)
		}
		if strings.Contains(run.output, "push-exit=0") {
			t.Errorf("a push from grain-src succeeded:\n%s", run.output)
		}
		if !strings.Contains(run.output, "not in scope for this sandbox") || strings.Contains(run.output, "fetch-exit=0") {
			t.Errorf("a self-debug run fetched bwsalmon/grain through the proxy:\n%s", run.output)
		}
		for _, want := range []string{head, "last 3 commits"} {
			if !strings.Contains(run.prompt, want) {
				t.Errorf("prompt does not say %q:%s", want, grainSourcePromptSection(run.prompt))
			}
		}
		t.Logf("prompt:%s", grainSourcePromptSection(run.prompt))
	})

	t.Run("self-debug without a baked copy", func(t *testing.T) {
		t.Parallel()
		w := newGrainWorld(t)
		run := dispatchForGrainSource(t, w, "iss-src-none", "", selfDebug, nil, baseTime)
		if _, err := os.Lstat(filepath.Join(run.root, orchestrator.GrainSourceDir)); !os.IsNotExist(err) {
			t.Fatalf("grain-src exists with no baked copy (err=%v)", err)
		}
		if !strings.Contains(run.prompt, "this deployment has no baked-in copy of grain's source") {
			t.Errorf("prompt does not say why there is no grain-src:%s", grainSourcePromptSection(run.prompt))
		}
	})

	t.Run("reads bwsalmon/grain without self-debug", func(t *testing.T) {
		t.Parallel()
		w := newGrainWorld(t)
		src, _ := bakeGrainSource(t)
		run := dispatchForGrainSource(t, w, "iss-src-reads", src, nil, []model.RepoRef{grain}, baseTime)
		if _, err := os.Lstat(filepath.Join(run.root, orchestrator.GrainSourceDir)); !os.IsNotExist(err) {
			t.Fatalf("grain-src placed for a task without self-debug (err=%v)", err)
		}
		if section := grainSourcePromptSection(run.prompt); strings.Contains(section, "baked-in") {
			t.Errorf("prompt describes a grain-src the task does not have:%s", section)
		}
	})
}

// grainSourcePromptSection is the paragraph of prompt that is about
// grain-src, for the walkthrough's log.
func grainSourcePromptSection(prompt string) string {
	for _, para := range strings.Split(prompt, "\n\n") {
		if strings.Contains(para, orchestrator.GrainSourceDir) {
			return "\n" + para
		}
	}
	return ""
}
