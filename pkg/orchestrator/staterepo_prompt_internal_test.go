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

// What a run is told when the repo it has been handed is grain's own
// state (pkg/staterepo) rather than source code: that the files are a
// database dump, which of them are settings, and which are grain's own
// record of what it did.
//
// Internal, like checkout_internal_test.go and for the same reason: the
// fact this turns on is one prepareCheckout reads out of the cloned tree,
// and there is nothing exported to reach either half through that would
// not also be running an agent.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// seedStateRemote makes the remote look like a state repository: the
// tables/ directory and the schema-version stamp staterepo.Export
// writes, which together are what prepareCheckout recognises.
func seedStateRemote(t *testing.T, base string, repo model.RepoRef) {
	t.Helper()
	seed := seedRemote(t, base, repo)
	if err := os.MkdirAll(filepath.Join(seed, "tables"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "tables", "task_template.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "schema-version"), []byte("16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "--all", ".")
	git(t, seed, "commit", "--quiet", "-m", "state")
	git(t, seed, "push", "--quiet", "origin", "main")
}

func TestPrepareCheckoutRecognisesAStateRepository(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "grain-state"}
	seedStateRemote(t, remoteBase, repo)

	root := t.TempDir()
	task := model.Task{ID: "t1", Target: &repo}
	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(root), remoteBase, task, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if !prepared.StateRepo {
		t.Fatal("a checkout holding tables/ and schema-version was not recognised as grain's state")
	}
	if prepared.Dir != CheckoutDir {
		t.Fatalf("prepareCheckout returned %q, want %q", prepared.Dir, CheckoutDir)
	}
}

// The probe must not change what an ordinary repo's dispatch looks like,
// and above all must not fail one: it runs under `set -e` in the same
// script as the clone.
func TestPrepareCheckoutLeavesAnOrdinaryRepoAlone(t *testing.T) {
	t.Parallel()
	remoteBase := t.TempDir()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedRemote(t, remoteBase, repo)

	root := t.TempDir()
	task := model.Task{ID: "t1", Target: &repo}
	prepared, err := prepareCheckout(context.Background(), mcp.NewSandboxTools(root), remoteBase, task, "", setupNotes{})
	if err != nil {
		t.Fatalf("prepareCheckout: %v", err)
	}
	if prepared.StateRepo {
		t.Fatal("a repo with no dump in it was taken for grain's state")
	}
	if got, want := git(t, filepath.Join(root, CheckoutDir), "rev-parse", "--abbrev-ref", "HEAD"),
		model.BranchName("t1"); got != want {
		t.Fatalf("checked-out branch = %q, want %q", got, want)
	}
}

func TestStateRepoSectionIsSilentForAnOrdinaryCheckout(t *testing.T) {
	t.Parallel()
	if got := stateRepoSection(checkout{Dir: CheckoutDir}, ""); got != "" {
		t.Fatalf("an ordinary checkout got a state-repository section: %q", got)
	}
}

// Every fact the section is for: the layout, which tables are settings,
// which of grain's own tables are not in the export at all, and the
// check that keeps an unloadable dump out of the deployment.
func TestStateRepoSectionSaysWhatTheLayoutIs(t *testing.T) {
	t.Parallel()
	got := stateRepoSection(checkout{Dir: CheckoutDir, Root: "/home/debian", StateRepo: true}, "")
	for _, want := range []string{
		"tables/<name>.json",
		"one object per row",
		"declared order",
		"repo_config",
		"grain_config",
		"task_run",
		"task_observation",
		"state check /state",
		"secrets.enc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the state-repository section never mentions %q:\n%s", want, got)
		}
	}
	// Named from the list grain itself acts on, so the prompt cannot
	// call a table settings that Apply will not import.
	for _, table := range staterepo.SettingsTables {
		if !strings.Contains(got, table) {
			t.Errorf("the section does not name the settings table %q:\n%s", table, got)
		}
	}
}

// The operational tables are named for one reason only: to say they are
// not here. staterepo.ExportConfig writes the settings tables and the
// memory ones and deletes every other file under tables/, so a run told
// that task_run is "grain's own record, leave it alone" goes looking for
// a file that does not exist -- and a run that wants one is being sent
// to the wrong place entirely, since those rows are only reachable
// through the self-debug task tools.
func TestStateRepoSectionSaysTheRecordIsNotExported(t *testing.T) {
	t.Parallel()
	got := stateRepoSection(checkout{Dir: CheckoutDir, Root: "/home/debian", StateRepo: true}, "")
	for _, want := range []string{
		"not exported",
		"tables/task.json",
		"list_grain_tasks",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the state-repository section never mentions %q, so a run is left to "+
				"discover the missing half for itself:\n%s", want, got)
		}
	}
	// The memory tables are the half of the export that is neither
	// settings nor absent, and a run editing one should know grain will
	// take it.
	if !strings.Contains(got, "tables/memory.json") {
		t.Errorf("the section does not say the memory tables are exported:\n%s", got)
	}
}

// The check the section tells a run to make has to be one the sandbox
// can actually run: there is no `grain` binary in one, so a prompt that
// spells the check as `grain state check .` costs every run a turn on
// `command not found` before it finds the image (grain/task-277).
func TestStateRepoSectionNamesACheckTheSandboxCanRun(t *testing.T) {
	t.Parallel()
	image := "ghcr.io/bwsalmon/grain/grain:sha-abc123"
	got := stateRepoSection(checkout{Dir: CheckoutDir, Root: "/home/debian", StateRepo: true}, image)
	want := "docker run --rm -v /home/debian/work:/state:ro " + image + " state check /state"
	if !strings.Contains(got, want) {
		t.Errorf("the section does not spell the check as %q:\n%s", want, got)
	}
	if strings.Contains(got, "`grain state check .`") {
		t.Errorf("the section still tells a run to invoke a binary no sandbox has:\n%s", got)
	}
}

// The image is the deployment's own, and a caller that named none still
// gets a command that runs -- the published tag CI keeps on main, which
// is the same fallback the state repository's own CI step takes.
func TestStateCheckCommandFallsBackToThePublishedImage(t *testing.T) {
	t.Parallel()
	got := stateCheckCommand(checkout{Dir: CheckoutDir, Root: "/home/debian", StateRepo: true}, "")
	if !strings.Contains(got, staterepo.DefaultCheckImage) {
		t.Errorf("a section with no image named left the command without one: %q", got)
	}
}

// A sandbox that could not say where its working directory is: docker
// resolves a bind mount against its own filesystem rather than against a
// shell's ".", so the command has to reach an absolute path some other
// way.
func TestStateCheckCommandMountsAnAbsolutePathWithoutTheSandboxRoot(t *testing.T) {
	t.Parallel()
	got := stateCheckCommand(checkout{Dir: CheckoutDir, StateRepo: true}, "grain:test")
	want := `cd work && docker run --rm -v "$PWD:/state:ro" grain:test state check /state`
	if got != want {
		t.Errorf("stateCheckCommand without a sandbox root = %q, want %q", got, want)
	}
}

// Config.CheckImage is a function so an adopt reaches the next run
// dispatched, and neither a nil one nor an empty answer may leave the
// command with a hole in it.
func TestConfigCheckImageReadsTheLiveAnswer(t *testing.T) {
	t.Parallel()
	var cfg Config
	if got := cfg.checkImage(); got != staterepo.DefaultCheckImage {
		t.Fatalf("a Config with no CheckImage answered %q, want %q", got, staterepo.DefaultCheckImage)
	}
	current := "ghcr.io/bwsalmon/grain/grain:sha-abc123"
	cfg.CheckImage = func() string { return current }
	if got := cfg.checkImage(); got != current {
		t.Fatalf("checkImage() = %q, want %q", got, current)
	}
	current = ""
	if got := cfg.checkImage(); got != staterepo.DefaultCheckImage {
		t.Fatalf("checkImage() with nothing configured = %q, want %q", got, staterepo.DefaultCheckImage)
	}
}

// The section reaches a real prompt, not just its own function: the
// checkout is the only thing that knows, and prepareCapabilities is
// where what it knows is turned into words.
func TestPrepareCapabilitiesCarriesTheStateRepoSection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	task := model.Task{ID: "t1", Title: "Change a template", Target: &model.RepoRef{Owner: "acme", Name: "grain-state"}}
	cc := model.CapabilityContext{Task: task}
	_, prompt, err := prepareCapabilities(ctx, nil, cc, nil, t.TempDir(), nil, nil, History{}, nil,
		checkout{Dir: CheckoutDir, Root: "/home/debian", StateRepo: true}, model.RepoRef{}, "", false, "", "", DefaultMaxRunRuntime)
	if err != nil {
		t.Fatalf("prepareCapabilities: %v", err)
	}
	if !strings.Contains(prompt, "grain's own state") {
		t.Fatalf("the dispatch prompt never says the repo is grain's state:\n%s", prompt)
	}
}

// Which grain's settings the checkout holds, which is the fact the tree
// itself cannot carry: a dump looks the same whoever exported it.
func TestSettingsRepoSectionSaysTheCheckoutIsThisDeploymentsSettings(t *testing.T) {
	t.Parallel()
	settings := model.RepoRef{Owner: "acme", Name: "grain-state"}
	target := settings
	got := settingsRepoSection(checkout{Dir: CheckoutDir, StateRepo: true}, &target, settings)
	for _, want := range []string{"acme/grain-state", "in this very repo", "the grain running this task"} {
		if !strings.Contains(got, want) {
			t.Errorf("the section never mentions %q:\n%s", want, got)
		}
	}
}

// The same repository named a different way round: GitHub does not
// distinguish the two, and a run working in this deployment's own
// settings must not be told they are somewhere else.
func TestSettingsRepoSectionMatchesTheTargetCaseInsensitively(t *testing.T) {
	t.Parallel()
	target := model.RepoRef{Owner: "Acme", Name: "Grain-State"}
	got := settingsRepoSection(checkout{Dir: CheckoutDir, StateRepo: true}, &target,
		model.RepoRef{Owner: "acme", Name: "grain-state"})
	if !strings.Contains(got, "in this very repo") {
		t.Fatalf("a differently-cased spelling of this deployment's own state was taken for another repo:\n%s", got)
	}
}

// An ordinary dispatch: not the settings repository, and told where the
// settings actually are so that a run that wants one changed knows both
// halves of it.
func TestSettingsRepoSectionNamesTheSettingsRepoElsewhere(t *testing.T) {
	t.Parallel()
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	got := settingsRepoSection(checkout{Dir: CheckoutDir}, &target,
		model.RepoRef{Owner: "acme", Name: "grain-state"})
	for _, want := range []string{"not in this repo", "acme/grain-state", "pull request against that repository"} {
		if !strings.Contains(got, want) {
			t.Errorf("the section never mentions %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "does look like a grain state repository") {
		t.Errorf("a checkout with no dump in it was described as one:\n%s", got)
	}
}

// The tree an agent is most likely to mistake for this deployment's own:
// a state repository that belongs to some other grain.
func TestSettingsRepoSectionWarnsAboutAnotherGrainsState(t *testing.T) {
	t.Parallel()
	target := model.RepoRef{Owner: "other", Name: "grain-state"}
	got := settingsRepoSection(checkout{Dir: CheckoutDir, StateRepo: true}, &target,
		model.RepoRef{Owner: "acme", Name: "grain-state"})
	if !strings.Contains(got, "not this deployment's") {
		t.Fatalf("another grain's dump was not distinguished from this deployment's own:\n%s", got)
	}
}

// A task with no repo at all still gets the answer -- it may well be
// the run being asked why this deployment is configured as it is -- but
// not the clause contrasting the settings with a checkout it has not
// been given.
func TestSettingsRepoSectionSuitsATaskWithNoRepo(t *testing.T) {
	t.Parallel()
	got := settingsRepoSection(checkout{}, nil, model.RepoRef{Owner: "acme", Name: "grain-state"})
	if !strings.Contains(got, "acme/grain-state") {
		t.Fatalf("a task with no repo was not told where the settings are:\n%s", got)
	}
	if strings.Contains(got, "not in this repo") {
		t.Fatalf("a task with no repo was told the settings are not in a repo it does not have:\n%s", got)
	}
}

// A deployment whose state is local-only, and a caller that wired no
// answer at all, are the same silence: there is no repository to name.
func TestSettingsRepoSectionIsSilentWithoutASettingsRepo(t *testing.T) {
	t.Parallel()
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	if got := settingsRepoSection(checkout{Dir: CheckoutDir}, &target, model.RepoRef{}); got != "" {
		t.Fatalf("a deployment with no state repository still named one: %q", got)
	}
	if got := settingsRepoSection(checkout{Dir: CheckoutDir, StateRepo: true}, nil, model.RepoRef{}); got != "" {
		t.Fatalf("a task with no target still got a settings section: %q", got)
	}
}

// The section reaches a real prompt, and reaches it from the one field a
// deployment sets: Config.StateRepo, read per dispatch.
func TestPrepareCapabilitiesNamesTheSettingsRepo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	task := model.Task{ID: "t1", Title: "Fix a widget", Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	cc := model.CapabilityContext{Task: task}
	settings := model.RepoRef{Owner: "acme", Name: "grain-state"}
	_, prompt, err := prepareCapabilities(ctx, nil, cc, nil, t.TempDir(), nil, nil, History{}, nil,
		checkout{Dir: CheckoutDir}, settings, "", false, "", "", DefaultMaxRunRuntime)
	if err != nil {
		t.Fatalf("prepareCapabilities: %v", err)
	}
	if !strings.Contains(prompt, "acme/grain-state") {
		t.Fatalf("the dispatch prompt never names this deployment's settings repo:\n%s", prompt)
	}
}

// Config.StateRepo is a function so that adopting a different repository
// mid-process reaches the next run dispatched, and nil is the caller
// that has none.
func TestConfigSettingsRepoReadsTheLiveAnswer(t *testing.T) {
	t.Parallel()
	var cfg Config
	if got := cfg.settingsRepo(); got != (model.RepoRef{}) {
		t.Fatalf("a Config with no StateRepo answered %v, want the zero RepoRef", got)
	}
	current := model.RepoRef{Owner: "acme", Name: "grain-state"}
	cfg.StateRepo = func() model.RepoRef { return current }
	if got := cfg.settingsRepo(); got != current {
		t.Fatalf("settingsRepo() = %v, want %v", got, current)
	}
	current = model.RepoRef{Owner: "acme", Name: "adopted-state"}
	if got := cfg.settingsRepo(); got != current {
		t.Fatalf("settingsRepo() after an adopt = %v, want %v", got, current)
	}
}
