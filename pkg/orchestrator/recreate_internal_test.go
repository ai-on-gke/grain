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

package orchestrator

// Internal, not the _test package most of this package's tests use, for
// the reason checkout_internal_test.go gives about prepareCheckout: what
// these tests are about is the registry and the restore steps a rebuild
// runs, and the only exported way to reach either is to dispatch a whole
// run through RunCycle with a real agent behind it.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// recreateStore opens a real embedded SQLite store, the same discipline
// every other package's tests hold to -- restoreAttachments reads one,
// and a fake standing in for it would be testing nothing.
func recreateStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	logToTest(t)
	store, _ := sqlitetest.Open(t)
	return store, context.Background()
}

// fakeSandbox is a Sandbox that records what was asked of it, for the
// steps that have nothing to do with a real filesystem. rebuild is
// deliberately a separate embedded type so a test can build one *without*
// a Rebuild method and check what that is answered with.
type fakeSandbox struct {
	name        string
	gitCalls    int
	gitRemote   string
	gitToken    string
	gitIdentity mcp.GitIdentity
	gitErr      error
	releases    int
	rebuilds    int
	rebuildErr  error
}

func (s *fakeSandbox) Name() string { return s.name }

func (s *fakeSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }

func (s *fakeSandbox) ConfigureGitCredentials(_ context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	s.gitCalls++
	s.gitRemote, s.gitToken, s.gitIdentity = remoteURL, token, identity
	return s.gitErr
}

func (s *fakeSandbox) Release(context.Context) error {
	s.releases++
	return nil
}

// rebuildableSandbox is fakeSandbox plus the one optional method the
// whole feature turns on.
type rebuildableSandbox struct{ *fakeSandbox }

func (s rebuildableSandbox) Rebuild(context.Context) error {
	s.rebuilds++
	return s.rebuildErr
}

// A rebuild is only worth anything if what comes back is a sandbox the
// run can carry on in, so this drives the real thing: a real host
// sandbox with a real clone in it, junk of the agent's own beside it,
// and a real git remote to clone again from.
func TestRecreateDestroysTheSandboxAndClonesTheRepoAgain(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedRemote(t, remoteBase, repo)

	sandboxes := NewHostSandboxes(t.TempDir())
	sandbox, err := sandboxes.Acquire(ctx, "12-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	root, err := sandbox.(*hostSandbox).Root()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := sandbox.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}

	task := model.Task{ID: "12", Title: "Do the thing", Target: &repo}
	if _, err := prepareCheckout(ctx, tools, remoteBase, task, "", setupNotes{}); err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	// What the agent broke: a file of its own beside the checkout, and
	// something inside it. Neither may survive.
	junk := filepath.Join(root, "wedged.lock")
	if err := os.WriteFile(junk, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, CheckoutDir, "half-written.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &sandboxRecreation{
		store: store, cfg: Config{GitRemoteBase: remoteBase}, task: task,
		sandbox: sandbox, tools: tools, sandboxRoot: root,
	}
	out, err := rec.recreate(ctx)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}

	if len(out.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", out.Warnings)
	}
	if out.Sandbox != "12-1" {
		t.Errorf("Sandbox = %q, want the run's own name, unchanged by the rebuild", out.Sandbox)
	}
	if out.CheckoutDir != CheckoutDir {
		t.Errorf("CheckoutDir = %q, want %q", out.CheckoutDir, CheckoutDir)
	}
	for _, path := range []string{junk, inside} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the rebuild (err = %v), want it destroyed", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, CheckoutDir, ".git")); err != nil {
		t.Fatalf("the repo was not cloned again: %v", err)
	}
	branch := git(t, filepath.Join(root, CheckoutDir), "rev-parse", "--abbrev-ref", "HEAD")
	if branch != model.BranchName(task.ID) {
		t.Errorf("checked out %q, want %q", branch, model.BranchName(task.ID))
	}
	if len(out.Restored) != 1 || !strings.Contains(out.Restored[0], "fresh clone") {
		t.Errorf("Restored = %v, want the clone named", out.Restored)
	}
}

// A configured remote is part of the checkout being restored, not only
// something the first dispatch uses. Recreating a non-GitHub run must not
// fall back to the deployment's GitHub proxy and restore a different repo.
func TestRecreateClonesAConfiguredNonGitHubRemoteAgain(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedRemote(t, remoteBase, repo)
	cloneURL := filepath.Join(remoteBase, repo.Owner, repo.Name+".git")
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CloneURL: cloneURL}); err != nil {
		t.Fatal(err)
	}

	sandboxes := NewHostSandboxes(t.TempDir())
	sandbox, err := sandboxes.Acquire(ctx, "12-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	root, err := sandbox.(*hostSandbox).Root()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := sandbox.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task := model.Task{ID: "12", Title: "Do the thing", Target: &repo}
	if _, err := prepareCheckoutFrom(ctx, tools, "", cloneURL, task, "", setupNotes{}); err != nil {
		t.Fatalf("prepareCheckoutFrom: %v", err)
	}
	work := filepath.Join(root, CheckoutDir)
	if err := os.WriteFile(filepath.Join(work, "pushed.txt"), []byte("survives recreation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", "pushed.txt")
	git(t, work, "commit", "-m", "pushed before recreation")
	git(t, work, "push", "origin", task.BranchName())

	rec := &sandboxRecreation{
		store: store,
		// If recreation ignores RepoConfig.CloneURL, this deliberately bad
		// proxy base makes the regression fail at the clone.
		cfg:  Config{GitRemoteBase: filepath.Join(t.TempDir(), "wrong-proxy")},
		task: task, sandbox: sandbox, tools: tools, sandboxRoot: root,
	}
	out, err := rec.recreate(ctx)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", out.Warnings)
	}
	contents, err := os.ReadFile(filepath.Join(root, CheckoutDir, "pushed.txt"))
	if err != nil {
		t.Fatalf("reading the pushed file from the restored checkout: %v", err)
	}
	if string(contents) != "survives recreation\n" {
		t.Fatalf("restored pushed.txt = %q", contents)
	}
}

// The git credential is the one piece of setup a run cannot do without
// and cannot recreate itself: without it the sandbox can neither fetch
// nor push, which is most of what a run is for.
func TestRecreatePutsTheGitCredentialsBack(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	sandbox := rebuildableSandbox{&fakeSandbox{name: "12-1"}}

	rec := &sandboxRecreation{
		store: store, cfg: Config{GitRemoteBase: "http://127.0.0.1:8421/git"},
		task: model.Task{ID: "12", Title: "Do the thing"}, sandbox: sandbox,
		mint: func(name string) (string, error) {
			if name != "12-1" {
				t.Errorf("minted for %q, want the sandbox's own name", name)
			}
			return "tok-1", nil
		},
	}
	out, err := rec.recreate(ctx)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}

	if sandbox.rebuilds != 1 {
		t.Errorf("rebuilds = %d, want 1", sandbox.rebuilds)
	}
	if sandbox.gitCalls != 1 || sandbox.gitToken != "tok-1" {
		t.Errorf("ConfigureGitCredentials called %d time(s) with token %q, want once with the minted one",
			sandbox.gitCalls, sandbox.gitToken)
	}
	if !strings.HasPrefix(sandbox.gitRemote, "http://127.0.0.1:8421/git") {
		t.Errorf("remote = %q, want it under the configured proxy base", sandbox.gitRemote)
	}
	if len(out.Restored) != 1 || !strings.Contains(out.Restored[0], "git credentials") {
		t.Errorf("Restored = %v, want the credentials named", out.Restored)
	}
	// A task with no repo has nothing to clone, and that is not a
	// failure to report -- it is what the run started with too.
	if out.CheckoutDir != "" || len(out.Warnings) != 0 {
		t.Errorf("out = %+v, want no checkout and no warnings for a task with no repo", out)
	}
}

// Once the old sandbox is gone, nothing that follows can make the call a
// failure: the caller most needs to know what it is now sitting in front
// of, and an error would hide both the empty sandbox and whatever else
// did get restored.
func TestRecreateReportsAFailedRestoreAsAWarningNotAnError(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	sandbox := rebuildableSandbox{&fakeSandbox{name: "12-1"}}

	rec := &sandboxRecreation{
		store: store, cfg: Config{GitRemoteBase: "http://127.0.0.1:8421/git"},
		task: model.Task{ID: "12"}, sandbox: sandbox,
		mint: func(string) (string, error) { return "", errors.New("the token file is unreadable") },
	}
	out, err := rec.recreate(ctx)
	if err != nil {
		t.Fatalf("recreate: %v, want the rebuild to stand despite the failed restore", err)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "the token file is unreadable") {
		t.Fatalf("Warnings = %v, want the mint failure reported", out.Warnings)
	}
	if !strings.Contains(out.Warnings[0], "git push") {
		t.Errorf("Warnings = %v, want it to say what the run can no longer do", out.Warnings)
	}
}

// A rebuild that failed is the one thing that does fail the call: there
// is no point telling a run what was restored into a sandbox that may
// not exist.
func TestRecreateFailsWhenTheRebuildDoes(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	sandbox := rebuildableSandbox{&fakeSandbox{name: "12-1", rebuildErr: errors.New("granule vm create failed")}}

	rec := &sandboxRecreation{store: store, task: model.Task{ID: "12"}, sandbox: sandbox}
	if _, err := rec.recreate(ctx); err == nil {
		t.Fatal("expected an error")
	} else if !strings.Contains(err.Error(), "granule vm create failed") {
		t.Fatalf("err = %v, want konturctl's own reason", err)
	}
	if sandbox.gitCalls != 0 {
		t.Errorf("ConfigureGitCredentials called %d time(s), want none after a failed rebuild", sandbox.gitCalls)
	}
}

// A Sandboxes implementation from outside this package need not offer a
// rebuild at all, and a run on one is told so rather than left with a
// tool that silently does nothing.
func TestRecreateRefusesASandboxThatCannotRebuildItself(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	rec := &sandboxRecreation{store: store, task: model.Task{ID: "12"}, sandbox: &fakeSandbox{name: "12-1"}}

	_, err := rec.recreate(ctx)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no way to destroy and rebuild itself") {
		t.Fatalf("err = %v, want it to say the sandbox offers no rebuild", err)
	}
}

// The registry is what the REST route resolves a task id through, so a
// task with no run registered -- one that has finished, or one this
// daemon never dispatched -- must answer that rather than rebuilding
// something.
func TestSandboxRecreationsFindsALiveRunAndForgetsAFinishedOne(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	sandbox := rebuildableSandbox{&fakeSandbox{name: "12-1"}}
	registry := NewSandboxRecreations()

	if _, err := registry.Recreate(ctx, "12"); !errors.Is(err, errNoLiveRun) {
		t.Fatalf("err = %v, want errNoLiveRun before anything registers", err)
	}

	stop := registry.register("12", &sandboxRecreation{
		store: store, task: model.Task{ID: "12"}, sandbox: sandbox,
	})
	if _, err := registry.Recreate(ctx, "12"); err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	if sandbox.rebuilds != 1 {
		t.Errorf("rebuilds = %d, want 1", sandbox.rebuilds)
	}

	stop()
	if _, err := registry.Recreate(ctx, "12"); !errors.Is(err, errNoLiveRun) {
		t.Fatalf("err = %v, want errNoLiveRun once the run has ended", err)
	}
	if sandbox.rebuilds != 1 {
		t.Errorf("rebuilds = %d, want the finished run's sandbox left alone", sandbox.rebuilds)
	}
}

// Every caller that wires no registry -- a test, a one-shot cycle,
// `grain demo` -- goes through the same code paths with a nil one, so
// none of them may panic.
func TestNilSandboxRecreationsIsUsable(t *testing.T) {
	t.Parallel()
	var registry *SandboxRecreations
	stop := registry.register("12", &sandboxRecreation{})
	registry.setMaterialized("12", []model.Materialized{{}})
	stop()
	if _, err := registry.Recreate(context.Background(), "12"); !errors.Is(err, errNoLiveRun) {
		t.Fatalf("err = %v, want errNoLiveRun from a nil registry", err)
	}
}

// setMaterialized is how the placements RunDispatch already minted reach
// the rebuild -- they are written back, never materialized a second
// time, which would mint a second credential behind the back of this
// run's single revoke.
func TestRecreateWritesAlreadyMintedPlacementsBack(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	root := t.TempDir()
	sandbox := rebuildableSandbox{&fakeSandbox{name: "12-1"}}
	registry := NewSandboxRecreations()

	stop := registry.register("12", &sandboxRecreation{
		store: store, task: model.Task{ID: "12"}, sandbox: sandbox, sandboxRoot: root,
	})
	defer stop()
	registry.setMaterialized("12", []model.Materialized{{
		Grant: model.Grant{Capability: "gcp-key"},
		Materialization: model.Materialization{Placements: []model.Placement{{
			Side: model.SideSandbox, Path: "/etc/grain/key.json", Content: "{}", Mode: "0600",
		}}},
	}})

	out, err := registry.Recreate(ctx, "12")
	if err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", out.Warnings)
	}
	placed := filepath.Join(root, "etc/grain/key.json")
	content, err := os.ReadFile(placed)
	if err != nil {
		t.Fatalf("the placement was not written back: %v", err)
	}
	if string(content) != "{}" {
		t.Errorf("%s = %q, want the already-minted content", placed, content)
	}
}

// A rebuild takes the whole sandbox with it -- the node_modules, the
// virtualenv, whatever `make deps` installed -- so the repo's setup
// command has to run again in the fresh checkout. Handing a run back a
// directory in exactly the state model.RepoConfig.SetupCommand exists to
// prevent is worse than never having run it: the run was told at turn 1
// that setup was done for it.
func TestRecreateRunsTheReposSetupCommandAgain(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedRemote(t, remoteBase, repo)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, SetupCommand: "echo installed > DEPS",
	}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	sandboxes := NewHostSandboxes(t.TempDir())
	sandbox, err := sandboxes.Acquire(ctx, "12-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	root, err := sandbox.(*hostSandbox).Root()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := sandbox.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}

	task := model.Task{ID: "12", Title: "Do the thing", Target: &repo}
	rec := &sandboxRecreation{
		store: store, cfg: Config{GitRemoteBase: remoteBase}, task: task,
		sandbox: sandbox, tools: tools, sandboxRoot: root,
	}
	out, err := rec.recreate(ctx)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", out.Warnings)
	}
	if _, err := os.Stat(filepath.Join(root, CheckoutDir, "DEPS")); err != nil {
		t.Fatalf("the setup command did not run in the rebuilt sandbox's checkout: %v", err)
	}
	var said bool
	for _, r := range out.Restored {
		said = said || strings.Contains(r, "setup command")
	}
	if !said {
		t.Errorf("Restored = %v, want the setup command named -- a run that is not told cannot know", out.Restored)
	}
}

// And a setup command that fails in the rebuilt sandbox is a warning,
// not a silence: the whole point of the answer this tool returns is an
// account of what the run is now sitting in front of.
func TestRecreateWarnsWhenTheSetupCommandFailsAgain(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedRemote(t, remoteBase, repo)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, SetupCommand: "echo no such package >&2; exit 4",
	}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	sandboxes := NewHostSandboxes(t.TempDir())
	sandbox, err := sandboxes.Acquire(ctx, "13-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	root, err := sandbox.(*hostSandbox).Root()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := sandbox.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}

	task := model.Task{ID: "13", Title: "Do the thing", Target: &repo}
	rec := &sandboxRecreation{
		store: store, cfg: Config{GitRemoteBase: remoteBase}, task: task,
		sandbox: sandbox, tools: tools, sandboxRoot: root,
	}
	out, err := rec.recreate(ctx)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	// The clone is still restored -- a failed setup is not a failed
	// re-clone, and the run has a checkout to work in either way.
	if out.CheckoutDir != CheckoutDir {
		t.Errorf("CheckoutDir = %q, want the checkout the run still has", out.CheckoutDir)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "no such package") {
		t.Errorf("Warnings = %v, want one naming what the setup command printed", out.Warnings)
	}
}
