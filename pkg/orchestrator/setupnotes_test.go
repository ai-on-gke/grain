// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator_test

// grain narrating a run's own setup: the stretch between a dispatch
// becoming durable and the agent's first turn -- a sandbox built, a repo
// cloned, a setup command run -- which is the one part of a run's life
// nothing could describe, because there was no agent yet to describe it.
//
// The phrases land in task_run.activity, the same column update_status
// writes, so what these tests assert on is what a person watching the
// task list actually sees (ui.Task.Activity), read at the moment each
// piece of setup is happening rather than after the fact.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// seenNote is the task's live synopsis as it stood at one moment of a
// run's setup, beside what was happening at that moment.
type seenNote struct {
	during string // the first line of the command that was running
	note   string
	grains bool // model.RunActivity.BySetup: grain's phrase, not the run's
}

// watchNotes wraps a sandbox's run_command tool so that every command
// grain runs during setup -- the clone, the repo's setup command --
// records what the task's row said while it was running. Reading it
// afterwards would prove nothing: each phrase is replaced by the next
// and the last is cleared at the handover, so the only place a setup
// synopsis can be observed is from inside the work it describes.
func watchNotes(t *testing.T, store *model.Store, ctx context.Context, taskID string, tools []mcp.Tool) ([]mcp.Tool, func() []seenNote) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenNote
	out := append([]mcp.Tool(nil), tools...)
	for i, tool := range out {
		if tool.Name != "run_command" {
			continue
		}
		inner := tool.Handler
		out[i].Handler = func(ctx context.Context, args map[string]any) mcp.Result {
			command, _ := args["command"].(string)
			activity, err := store.TaskActivityOf(ctx, taskID)
			if err != nil {
				t.Errorf("reading %s's synopsis mid-setup: %v", taskID, err)
			}
			note := seenNote{during: firstLine(command)}
			if activity != nil {
				note.note, note.grains = activity.Note, activity.BySetup
			}
			mu.Lock()
			seen = append(seen, note)
			mu.Unlock()
			return inner(ctx, args)
		}
	}
	return out, func() []seenNote {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenNote(nil), seen...)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// bareRemote seeds base/owner/name.git with one commit on main, so
// CloneURL(base, repo) is a path a real clone can be driven against --
// the same shape orchestrator's internal checkout tests use, with a
// directory standing in for the deployment's git proxy.
func bareRemote(t *testing.T, base string, repo model.RepoRef) {
	t.Helper()
	bare := initBareRepo(t, filepath.Join(base, repo.Owner, repo.Name+".git"), "main")

	seed := t.TempDir()
	run(t, seed, "git", "clone", "--quiet", bare, "work")
	work := filepath.Join(seed, "work")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", "README.md")
	run(t, work, "git", "-c", "user.email=t@localhost", "-c", "user.name=t", "commit", "--quiet", "-m", "seed")
	run(t, work, "git", "push", "--quiet", "origin", "main")
}

// The clone and the setup command each say so while they are happening,
// in grain's own name -- and the row is clean again by the time the agent
// gets its first turn, because from that moment on anything standing
// there would read as something the run said about itself.
func TestRunDispatchSaysWhatItIsDoingBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	dispatchTask(t, ctx, store, "t1")
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, SetupCommand: "echo installed > DEPS",
	}); err != nil {
		t.Fatal(err)
	}
	remoteBase := t.TempDir()
	bareRemote(t, remoteBase, repo)

	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	root := t.TempDir()
	tools, notes := watchNotes(t, store, ctx, "t1", mcp.NewSandboxTools(root))

	var agentSaw *model.RunActivity
	fw := agentFunc(func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		agentSaw, err = store.TaskActivityOf(ctx, "t1")
		if err != nil {
			t.Errorf("reading the synopsis at the agent's first turn: %v", err)
		}
		return pushed(), nil
	})
	cfg := orchestrator.Config{GitRemoteBase: remoteBase}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, tools, root, "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	seen := notes()
	if len(seen) < 2 {
		t.Fatalf("setup ran %d commands, want at least the clone and the setup command: %+v", len(seen), seen)
	}
	if got := seen[0]; got.note != "cloning acme/widgets" || !got.grains {
		t.Errorf("during %q the task said %+v, want grain's own \"cloning acme/widgets\"", got.during, got)
	}
	if got := seen[1]; got.note != "running the repo's setup command" || !got.grains {
		t.Errorf("during %q the task said %+v, want grain's own setup-command phrase", got.during, got)
	}

	// The handover: grain stops narrating, and takes its last phrase with
	// it rather than leaving "running the repo's setup command" standing
	// over an agent that is doing something else entirely.
	if agentSaw != nil {
		t.Errorf("the agent's first turn found %+v on the row, want nothing until it says something itself", agentSaw)
	}
}

// watchingSandboxes reads the task's synopsis at the moment its sandbox
// is being built, which is the only moment that phrase is standing --
// and optionally refuses to build one, to see what a run that never
// reached its agent ends up carrying.
type watchingSandboxes struct {
	orchestrator.Sandboxes
	store  *model.Store
	taskID string
	refuse error

	mu   sync.Mutex
	seen *model.RunActivity
}

func (s *watchingSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	activity, err := s.store.TaskActivityOf(ctx, s.taskID)
	s.mu.Lock()
	s.seen = activity
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if s.refuse != nil {
		return nil, s.refuse
	}
	return s.Sandboxes.Acquire(ctx, name, shape)
}

func (s *watchingSandboxes) sawDuringAcquire() *model.RunActivity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

// The first phrase of all, and the one this is most worth doing for: on
// a granule deployment Acquire is a VM boot measured in minutes, during
// which the task read 'running' with nothing beside it.
func TestRunCycleSaysTheSandboxIsBeingBuilt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	sandboxes := &watchingSandboxes{
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()), store: store, taskID: "t1",
	}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	got := sandboxes.sawDuringAcquire()
	if got == nil || got.Note != "building a sandbox" {
		t.Fatalf("synopsis while the sandbox was being built = %+v, want grain saying so", got)
	}
	if !got.BySetup {
		t.Error("BySetup = false, want true: no agent exists yet to have said this")
	}
	if got.At == nil {
		t.Error("At = nil, want when it was said -- a phrase with no age cannot be read")
	}
}

// A run whose sandbox never came up finishes "setup-failed", and the
// phrase it broke on stays on the finished row. Nothing renders it as
// current (only live runs are read back), so it contradicts nothing --
// and beside a detail that says the sandbox could not be prepared, "what
// grain was doing when it gave up" is the half the detail cannot carry.
func TestASetupFailureKeepsTheLastPhraseWithoutShowingItAsCurrent(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	sandboxes := &watchingSandboxes{
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()), store: store, taskID: "t1",
		refuse: errInjected,
	}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil {
		t.Fatal("RunCycle reported no error for a sandbox that could not be built")
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = (%v, %v), want the one finished run", runs, err)
	}
	if runs[0].Outcome != "setup-failed" {
		t.Fatalf("outcome = %q, want setup-failed", runs[0].Outcome)
	}
	if runs[0].Activity != "building a sandbox" {
		t.Errorf("finished run's Activity = %q, want the phrase it broke on", runs[0].Activity)
	}
	if !strings.Contains(runs[0].Detail, "sandbox could not be prepared") {
		t.Errorf("detail = %q, want it to say the sandbox could not be prepared", runs[0].Detail)
	}

	// The row keeps it; nothing reads it back as something happening now.
	if live, err := store.TaskActivityOf(ctx, "t1"); err != nil || live != nil {
		t.Errorf("TaskActivityOf after the run failed = (%+v, %v), want nothing live", live, err)
	}
}

// A repo with no setup command has nothing to say about one: the phrase
// belongs to work that is actually happening, so a run that skips that
// step must not be left claiming to be doing it.
func TestRunDispatchSaysNothingAboutASetupCommandThatDoesNotExist(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	dispatchTask(t, ctx, store, "t1")
	remoteBase := t.TempDir()
	bareRemote(t, remoteBase, repo)

	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	root := t.TempDir()
	tools, notes := watchNotes(t, store, ctx, "t1", mcp.NewSandboxTools(root))
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) { return pushed(), nil })
	cfg := orchestrator.Config{GitRemoteBase: remoteBase}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, tools, root, "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	for _, got := range notes() {
		if strings.Contains(got.note, "setup command") {
			t.Errorf("during %q the task said %q, but this repo configures no setup command", got.during, got.note)
		}
	}
}
