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

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// The three parts of docs/test-plans/2026-10-29.md §3 that task 1797 could
// not walk on a daemon with no agent backend: the config-repository
// human-Submit hold (settings/config-policy.yml), CI jobs judged as kind
// ci by the capability catalogue (settings/capabilities.yml), and the
// catalogue asked of a run's proposals at the end of the run
// (orchestrator/finish.go). Each runs on the real orchestrator or the
// daemon's own CI service, over a real git server and githubsim, with a
// scripted agent; the catalogue is imported the way a merged config change
// is (staterepo.ImportTables over the exported settings). The report is
// docs/test-runs/2026-10-29-capability-catalogue-config-policy.md.

// importCatalogue commits doc as settings/capabilities.yml over the
// store's own exported settings and imports them back, as the daemon
// applies a merged change to grain/config.
func importCatalogue(t *testing.T, store *model.Store, doc string) {
	t.Helper()
	ctx := context.Background()
	if cfg, err := store.GetConfig(ctx); err != nil {
		t.Fatal(err)
	} else if cfg == nil {
		if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, store.DB(), dir); err != nil {
		t.Fatalf("exporting the settings: %v", err)
	}
	if _, err := staterepo.ValidateCapabilitiesFile([]byte(doc)); err != nil {
		t.Fatalf("the walkthrough's catalogue does not validate: %v", err)
	}
	mustWriteFile(t, filepath.Join(dir, filepath.FromSlash(staterepo.CapabilitiesPath)), doc)
	if err := staterepo.ImportTables(ctx, store.DB(), dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing settings/capabilities.yml: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil || cfg.CapabilityPolicy.Catalogue == nil {
		t.Fatalf("the catalogue was not imported: %+v, %v", cfg, err)
	}
}

// commitToMain pushes files straight onto the repository's main, as an
// operator committing to grain/config does.
func (w *holdWorld) commitToMain(files map[string]string) {
	w.t.Helper()
	dir := filepath.Join(w.t.TempDir(), "operator")
	run(w.t, filepath.Dir(dir), "git", "clone", "-q", w.bare, dir)
	run(w.t, dir, "git", "config", "user.email", "operator@example.com")
	run(w.t, dir, "git", "config", "user.name", "operator")
	for path, body := range files {
		mustWriteFile(w.t, filepath.Join(dir, filepath.FromSlash(path)), body)
	}
	run(w.t, dir, "git", "add", "-A")
	run(w.t, dir, "git", "commit", "-q", "-m", "operator: settings")
	run(w.t, dir, "git", "push", "-q", "origin", "main")
}

// newConfigRepoWorld is a holdWorld whose repository is this deployment's
// state repository (model.Store.SettingsRepo, which the daemon sets to its
// state manager's answer), with settings/config-policy.yml on main opting
// memory/** in and, past the NeverAutoMergePaths ceiling, scripts/**.
func newConfigRepoWorld(t *testing.T) *holdWorld {
	w := newHoldWorld(t, false)
	w.store.SettingsRepo = func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "widgets"} }
	w.commitToMain(map[string]string{
		trustboundary.ConfigPolicyPath: "auto_merge_paths:\n  - memory/**\n  - scripts/**\n",
		"memory/README.md":             "# memory\n",
	})
	return w
}

func TestConfigPolicyHoldWalkthrough(t *testing.T) {
	t.Parallel()

	t.Run("an opted-in memory/ path merges on its own", func(t *testing.T) {
		t.Parallel()
		w := newConfigRepoWorld(t)
		task := w.create("remember a fact", true, nil)
		w.workThenReview(task.ID, "mkdir -p memory && echo 'a fact' > memory/x.md", nil)
		w.cycle()
		got := w.task(task.ID)
		if !got.AutoMerge || hasSecurityHoldLink(got) || linkTarget(got, model.LinkSecurityReviewTask) != "" {
			t.Fatalf("an opted-in path was held: state=%s automerge=%v links=%+v comments=%s",
				w.state(task.ID), got.AutoMerge, got.Links, w.comments(task.ID))
		}
		if !w.cycles(4, func() bool { w.markAllClean(); return w.merged(model.BranchName(task.ID)) }) {
			t.Fatalf("memory/x.md never merged: state=%s comments=%s", w.state(task.ID), w.comments(task.ID))
		}
		if cs := w.holdComments(); len(cs) != 0 {
			t.Errorf("an opted-in change got hold comments: %q", cs)
		}
		t.Logf("memory/x.md: merged, state %s", w.state(task.ID))
	})

	held := func(t *testing.T, title, edit string, paths ...string) {
		t.Helper()
		w := newConfigRepoWorld(t)
		task := w.create(title, true, nil)
		w.workThenReview(task.ID, edit, nil)
		w.cycle()
		st := w.state(task.ID)
		got := w.task(task.ID)
		if st != model.StateAwaitingSubmit || got.AutoMerge {
			t.Fatalf("state=%s automerge=%v, want awaiting_submit with auto-merge off; links=%+v comments=%s",
				st, got.AutoMerge, got.Links, w.comments(task.ID))
		}
		if hasSecurityHoldLink(got) {
			t.Errorf("a hold only a person's Submit releases carries a releasable hold link: %+v", got.Links)
		}
		comments := w.comments(task.ID)
		if !strings.Contains(comments, "state-privilege") {
			t.Errorf("the task's hold comment does not name state-privilege:\n%s", comments)
		}
		for _, p := range paths {
			if !strings.Contains(comments, "`"+p+"`") {
				t.Errorf("the task's hold comment does not name %s:\n%s", p, comments)
			}
		}
		// A clean review, if one was filed, does not release it either.
		w.notMergedWhileHeld(task, 6)
		if st := w.state(task.ID); st != model.StateAwaitingSubmit {
			t.Errorf("after six cycles the held change is %s, want still awaiting_submit", st)
		}
		if review := linkTarget(w.task(task.ID), model.LinkSecurityReviewTask); review != "" {
			t.Logf("security review %s: state %s", review, w.state(review))
		}
		t.Logf("held: state %s, comments:\n%s", w.state(task.ID), comments)
	}

	t.Run("an unlisted path waits for a person's Submit", func(t *testing.T) {
		t.Parallel()
		held(t, "add a note", "mkdir -p notes && echo 'a note' > notes/unlisted.md", "notes/unlisted.md")
	})

	t.Run("a listed path under NeverAutoMergePaths waits for a person's Submit", func(t *testing.T) {
		t.Parallel()
		held(t, "add a script", "mkdir -p scripts && echo 'echo hi' > scripts/x.sh", "scripts/x.sh")
	})

	t.Run("a branch that adds its own path to auto_merge_paths is not honoured", func(t *testing.T) {
		t.Parallel()
		held(t, "opt notes in and add one",
			"printf 'auto_merge_paths:\\n  - memory/**\\n  - scripts/**\\n  - notes/**\\n' > "+trustboundary.ConfigPolicyPath+
				" && mkdir -p notes && echo 'a note' > notes/x.md",
			trustboundary.ConfigPolicyPath, "notes/x.md")
	})
}

// walkCICapability is an in-process capability a CI job can hold: minted
// per job, with nothing placed in the sandbox.
type walkCICapability struct {
	model.BaseCapability
	id string
}

func (c *walkCICapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: c.id, Provision: model.ProvisionMint}
}
func (c *walkCICapability) Resolve(context.Context, model.CapabilityContext) (model.Resolution, error) {
	return model.Honoured(), nil
}
func (c *walkCICapability) Materialize(_ context.Context, cc model.CapabilityContext) (model.Materialization, error) {
	return model.Materialization{Lease: &model.Lease{Capability: c.id, Resource: cc.Run.ID}}, nil
}
func (c *walkCICapability) Revoke(context.Context, model.CapabilityContext, model.Lease) error {
	return nil
}

// TestCICatalogueWalkthrough is the daemon's own CI service
// (daemon.BuildCIService: CiRepoCapabilities and CiGrantReservations, the
// catalogue's two CI edges) running acme/widgets' workflow from its git
// server, with a local sandbox standing in for a machine.
func TestCICatalogueWalkthrough(t *testing.T) {
	t.Parallel()
	const workflow = "name: checks\non: push\njobs:\n  test:\n    steps:\n      - run: echo tested\n"
	const catalogue = `defaults:
  ci: [ci-default-key]
in_process:
  ci-default-key: {}
  task-only-key:
    restrictions: {kinds: [task]}
  other-repo-key:
    restrictions: {repos: [acme/other]}
`
	newWorld := func(t *testing.T, repoCaps []string) (*ci.Service, *localCISandboxes) {
		t.Helper()
		ctx := context.Background()
		db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		store := model.New(db)
		if err := store.Init(ctx); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		bare := filepath.Join(dir, "widgets.git")
		run(t, dir, "git", "init", "-q", "--bare", "-b", "main", bare)
		seed := filepath.Join(dir, "seed")
		run(t, dir, "git", "clone", "-q", bare, seed)
		run(t, seed, "git", "config", "user.email", "seed@example.com")
		run(t, seed, "git", "config", "user.name", "seed")
		run(t, seed, "git", "checkout", "-q", "-b", "main")
		mustWriteFile(t, filepath.Join(seed, ".grain", "workflows", "checks.yml"), workflow)
		run(t, seed, "git", "add", "-A")
		run(t, seed, "git", "commit", "-q", "-m", "workflow")
		run(t, seed, "git", "push", "-q", "origin", "main")
		if err := store.PutRepoConfig(ctx, model.RepoConfig{
			Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, CIMode: model.CIModeGrain, CICapabilities: repoCaps,
		}); err != nil {
			t.Fatal(err)
		}
		importCatalogue(t, store, catalogue)
		reg := model.NewCapabilityRegistry(&walkCICapability{id: "ci-default-key"},
			&walkCICapability{id: "task-only-key"}, &walkCICapability{id: "other-repo-key"})
		dataDir := t.TempDir()
		tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "sandbox-tokens.json"))
		limits := func() model.Limits { return model.Limits{} }
		service := daemon.BuildCIService(ctx, daemon.Config{DataDir: dataDir}, limits,
			func() ci.StoredRunRetention { return ci.StoredRunRetention{} },
			func() ci.StorageBounds { return ci.StorageBounds{} }, store,
			nil, nil, "", tokens, func() string { return "" }, nil,
			func() *model.CapabilityRegistry { return reg }, nil, nil, nil, nil, nil)
		sandboxes := &localCISandboxes{dir: t.TempDir()}
		service.Engine.Sandboxes = sandboxes
		service.Source = bareRepoCISource{bare: map[string]string{"acme/widgets": bare}}
		return service, sandboxes
	}
	settle := func(t *testing.T, service *ci.Service) ci.Run {
		t.Helper()
		started, err := service.Start(context.Background(), ci.Request{Repo: "acme/widgets", Ref: "main", Event: ci.EventPush})
		if err != nil {
			t.Fatalf("starting the run: %v", err)
		}
		w := &ciWorld{t: t, service: service}
		return w.settle(started.ID)
	}

	t.Run("defaults.ci joins the repository's CI capabilities", func(t *testing.T) {
		t.Parallel()
		service, sandboxes := newWorld(t, nil)
		got := settle(t, service)
		if got.Conclusion != ci.ConclusionSuccess {
			t.Fatalf("run = %s, want success: %+v", got.Conclusion, got.Jobs)
		}
		reqs := sandboxes.requests()
		if len(reqs) != 1 || strings.Join(reqs[0].Capabilities, ",") != "ci-default-key" {
			t.Fatalf("the job's sandbox requests = %+v, want one holding ci-default-key", reqs)
		}
		t.Logf("job %s held %v", reqs[0].Name, reqs[0].Capabilities)
	})

	for _, tc := range []struct{ name, capability, says string }{
		{"a grant kept to tasks is refused to a CI job", "task-only-key", "kinds: task"},
		{"a grant kept to another repository is refused to this repository's CI job", "other-repo-key", "repos: acme/other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, sandboxes := newWorld(t, []string{tc.capability})
			got := settle(t, service)
			if got.Conclusion == ci.ConclusionSuccess {
				t.Fatalf("a job holding %s ran: %+v", tc.capability, got.Jobs)
			}
			var errs []string
			for _, j := range got.Jobs {
				errs = append(errs, j.Error)
			}
			text := strings.Join(errs, "\n")
			if !strings.Contains(text, "the capability catalogue refuses this CI job") ||
				!strings.Contains(text, tc.capability) || !strings.Contains(text, tc.says) {
				t.Fatalf("job errors = %q, want the catalogue's refusal of %s (%s)", text, tc.capability, tc.says)
			}
			if reqs := sandboxes.requests(); len(reqs) != 0 {
				t.Fatalf("a sandbox was built for a refused job: %+v", reqs)
			}
			t.Logf("run %s: %s", got.Conclusion, text)
		})
	}
}

// TestCatalogueProposalReplayWalkthrough is the catalogue asked of a run's
// propose_task calls when the run ends (orchestrator/finish.go): from an
// auto-merging parent that holds both grants, so nothing about the
// proposals widens what it holds, a proposal asking for a grant the
// catalogue marks requires_person, and one asking for a grant the
// catalogue refuses it, are each filed for a person saying why; a plain
// one is queued.
func TestCatalogueProposalReplayWalkthrough(t *testing.T) {
	t.Parallel()
	w := newHoldWorld(t, false)
	parent := w.create("auto-merging parent", true, []string{"self-debug", "gcp-key"})
	if !holds(parent, "self-debug") || !holds(parent, "gcp-key") {
		t.Fatalf("the parent does not hold both grants: %+v", parent.Grants)
	}
	// The parent was filed from the template the catalogue keeps gcp-key
	// to; its proposals are filed from none.
	if err := w.store.UpdateTask(w.ctx, parent.ID, func(t *model.Task) error { t.TemplateID = "walk-ops"; return nil }); err != nil {
		t.Fatal(err)
	}
	importCatalogue(t, w.store, `in_process:
  self-debug:
    restrictions: {requires_person: true}
  gcp-key:
    restrictions: {templates: [walk-ops]}
`)
	propose := func(title string, caps ...string) antigravity.Step {
		list := make([]any, len(caps))
		for i, c := range caps {
			list[i] = c
		}
		return toolCall("propose_task", map[string]any{"title": title, "body": "split out", "capabilities": list})
	}
	w.setScript(func(task model.Task) []antigravity.Step {
		if task.ID != parent.ID {
			return []antigravity.Step{finalText("nothing to do")}
		}
		steps := w.changeScript(task, "echo 'typo' >> docs/guide.md")
		return append([]antigravity.Step{
			propose("needs self-debug", "self-debug"),
			propose("needs gcp-key", "gcp-key"),
			propose("needs nothing"),
		}, steps...)
	})
	w.cycle()
	if st := w.state(parent.ID); st == model.StateQueued {
		t.Fatalf("the parent was never dispatched (held? comments: %s)", w.comments(parent.ID))
	}

	byTitle := map[string]model.Task{}
	for _, p := range w.proposalsOf(parent.ID) {
		byTitle[p.Title] = p
	}
	if len(byTitle) != 3 {
		t.Fatalf("the run filed %d proposals, want 3: %+v", len(byTitle), byTitle)
	}
	for _, tc := range []struct {
		title, grant string
		state        model.State
		note         string
	}{
		{"needs self-debug", "self-debug", model.StateProposed, model.CatalogueRequiresPersonNote([]string{"self-debug"})},
		{"needs gcp-key", "gcp-key", model.StateProposed, "settings/capabilities.yml refuses what it asked for"},
		{"needs nothing", "", model.StateQueued, ""},
	} {
		p, ok := byTitle[tc.title]
		if !ok {
			t.Errorf("no proposal %q", tc.title)
			continue
		}
		st := w.state(p.ID)
		if st != tc.state {
			t.Errorf("%q: state %s, want %s; body=%q", tc.title, st, tc.state, p.Body)
		}
		if tc.note == "" {
			if strings.Contains(p.Body, "Filed for a person to approve") {
				t.Errorf("%q was filed for a person: %q", tc.title, p.Body)
			}
		} else if !strings.Contains(p.Body, "Filed for a person to approve") || !strings.Contains(p.Body, tc.note) {
			t.Errorf("%q's body does not say why it waits for a person (%q):\n%s", tc.title, tc.note, p.Body)
		}
		// The run did not ask for it to wait (hold_for_approval): grain
		// did, so it is not marked as the run's choice.
		if model.HeldForApproval(p) || strings.Contains(p.Body, "hold_for_approval") {
			t.Errorf("%q is marked as held by its proposer, which asked for no hold: tags=%v body=%q", tc.title, p.Tags, p.Body)
		}
		if tc.grant != "" && !holds(p, tc.grant) {
			t.Errorf("%q was filed without the grant it asked for: %+v", tc.title, p.Grants)
		}
		t.Logf("%q: %s\n%s", tc.title, st, p.Body)
	}
}
