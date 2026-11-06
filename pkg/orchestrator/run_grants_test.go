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

// Which of its task's grants RunDispatch tells a run it holds, and where
// grain's own source is -- the facts a subprocess Framework turns into
// its forked mcpserver's -grant/-grain-src-dir (agent.GrantArgs). Unlike
// Config.GrantTools next door, which hands a run in-process tools no CLI
// framework can consume, this is what actually reaches a dispatched
// agent, so it is worth a test of its own.
package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/capability/deploymenthealth"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type staticCapabilityServers map[string]*protocol.Server

func (s staticCapabilityServers) Lookup(id string) (*protocol.Server, error) {
	if srv, ok := s[id]; ok {
		return srv, nil
	}
	return nil, os.ErrNotExist
}

// grantCapturingFramework records what one run was told about its own
// grants, then finishes the way toolCapturingFramework does.
type grantCapturingFramework struct {
	mu          sync.Mutex
	grants      []string
	sourceDir   string
	sandboxRoot string
	prompt      string
	onRun       func(agent.RunConfig)
}

func (f *grantCapturingFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.grants, f.sourceDir, f.sandboxRoot, f.prompt = cfg.Grants, cfg.GrainSourceDir, cfg.SandboxRoot, cfg.Prompt
	onRun := f.onRun
	f.mu.Unlock()
	if onRun != nil {
		onRun(cfg)
	}
	return toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}}), nil
}

func (f *grantCapturingFramework) seen() ([]string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants, f.sourceDir
}

func runWithGrants(t *testing.T, grants []model.Grant, sourceDir string) ([]string, string) {
	t.Helper()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Grants = grants
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	framework := &grantCapturingFramework{}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:        orchestrator.StaticFramework(framework),
		Config:           orchestrator.Config{GrainSourceDir: sourceDir},
		CapabilityPasses: orchestrator.NewCapabilityPasses(),
		MaxWorkers:       1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	return framework.seen()
}

func TestRunCycleTellsASelfDebugTaskItHoldsTheGrant(t *testing.T) {
	t.Parallel()
	grants := []model.Grant{{Capability: "self-debug", Via: model.GrantByLabel}}
	held, sourceDir := runWithGrants(t, grants, "/usr/local/share/grain/src")
	if !slices.Contains(held, "self-debug") {
		t.Errorf("RunConfig.Grants = %v, want the self-debug grant among them", held)
	}
	if sourceDir != "/usr/local/share/grain/src" {
		t.Errorf("RunConfig.GrainSourceDir = %q, want the deployment's own source checkout", sourceDir)
	}
}

// Bootstrap playbooks are served by the config-map server's built-in
// bootstrap map: they place the embedded markdown files on HostSandboxes,
// add the prompt guidance and grain playbooks bullet, and still refuse
// operator-configured config-map entries on unisolated host sandboxes.
func TestRunCycleMaterializesBootstrapPlaybooksOnHostSandboxes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t-boot", repo)
	task.Grants = []model.Grant{{Capability: model.BootstrapPlaybooksGrant, Via: model.GrantByGrain}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	extraRoot := t.TempDir()
	extraDir := filepath.Join(extraRoot, "operator-map")
	if err := os.MkdirAll(extraDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extraDir, "SKILL.md"), []byte("# Operator skill\nnope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := intree.NewConfigMap(intree.ConfigMapConfig{Dir: extraRoot})
	if err != nil {
		t.Fatal(err)
	}
	cmSrv, err := protocol.NewServer(ctx, model.ConfigMapCapabilityServerID, cm.InProcessCaller())
	if err != nil {
		t.Fatal(err)
	}
	lookup := staticCapabilityServers{model.ConfigMapCapabilityServerID: cmSrv}

	framework := &grantCapturingFramework{
		onRun: func(cfg agent.RunConfig) {
			if raw, err := os.ReadFile(filepath.Join(cfg.SandboxRoot, ".config-map", "bootstrap", "gcp-capabilities.md")); err != nil || len(raw) == 0 {
				t.Errorf("reading placed bootstrap playbook in HostSandboxes: err=%v len=%d", err, len(raw))
			}
		},
	}
	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: sandboxes,
		Framework:         orchestrator.StaticFramework(framework),
		CapabilityServers: lookup,
		CapabilityPasses:  orchestrator.NewCapabilityPasses(),
		MaxWorkers:        1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/home/debian/.config-map/bootstrap",
		"run_host_command",
		"`grain playbooks [<name>]`",
	} {
		if !strings.Contains(framework.prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, framework.prompt)
		}
	}

	// Operator-configured config-map entries remain refused on HostSandboxes.
	refused := filedTask(t, ctx, store, "t-op-map", repo)
	refused.Grants = []model.Grant{{
		Capability: `https://grain.dev/capabilities/config-map#config-map={"config_maps":["operator-map"]}`,
		Via:        model.GrantByLabel,
	}}
	if err := store.PutTask(ctx, refused); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(1)); err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, refused.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Errorf("operator config-map task state on HostSandboxes = %q, want %q", state, model.StateAwaitingReply)
	}
}

// grain-ci reaches the forked mcpserver as a grant name so it replaces
// the GitHub-backed wait_for_checks tool with the task-scoped Grain CI
// implementation. Without this handoff the prompt tells the agent to start
// Grain CI, but the identically named GitHub tool remains registered.
func TestRunCycleTellsAGrainCITaskItHoldsTheGrant(t *testing.T) {
	t.Parallel()
	grants := []model.Grant{{Capability: "grain-ci", Via: model.GrantByLabel}}
	held, _ := runWithGrants(t, grants, "/usr/local/share/grain/src")
	if !slices.Contains(held, "grain-ci") {
		t.Errorf("RunConfig.Grants = %v, want the grain-ci grant among them", held)
	}
}

func TestRunCycleTellsADBAccessTaskItHoldsTheGrant(t *testing.T) {
	t.Parallel()
	grants := []model.Grant{
		{Capability: "db-read", Via: model.GrantByLabel},
		{Capability: "db-write", Via: model.GrantByLabel},
	}
	held, _ := runWithGrants(t, grants, "/usr/local/share/grain/src")
	if !slices.Contains(held, "db-read") {
		t.Errorf("RunConfig.Grants = %v, want the db-read grant among them", held)
	}
	if !slices.Contains(held, "db-write") {
		t.Errorf("RunConfig.Grants = %v, want the db-write grant among them", held)
	}
}

// Deployment-health is also a tool-only grant. It must cross this process
// boundary or the capability editor changes the task while the Director's
// forked MCP server still cannot serve either diagnostic tool.
func TestRunCycleTellsADeploymentHealthTaskItHoldsTheGrant(t *testing.T) {
	t.Parallel()
	grants := []model.Grant{{Capability: model.GrainHealthGrant, Via: model.GrantByGrain}}
	held, _ := runWithGrants(t, grants, "/usr/local/share/grain/src")
	if !slices.Contains(held, model.GrainHealthGrant) {
		t.Errorf("RunConfig.Grants = %v, want the %s grant among them", held, model.GrainHealthGrant)
	}
}

// Read-issues is also a tool-only grant. It must reach the subprocess
// Framework's Grants so the forked mcpserver starts with -grant read-issues
// and registers the list_issues and read_issue tools.
func TestRunCycleTellsAReadIssuesTaskItHoldsTheGrant(t *testing.T) {
	t.Parallel()
	grants := []model.Grant{{Capability: readissues.CapabilityName, Via: model.GrantByLabel}}
	held, _ := runWithGrants(t, grants, "/usr/local/share/grain/src")
	if !slices.Contains(held, readissues.CapabilityName) {
		t.Errorf("RunConfig.Grants = %v, want the %s grant among them", held, readissues.CapabilityName)
	}
}

// Every tool-granting capability must be forwarded to the subprocess mcpserver
// via RunConfig.Grants when attached to a task. If a new tool grant is added to
// the system without adding it to toolGrants in pkg/orchestrator/run.go, this
// test fails.
func TestRunCycleForwardsAllKnownToolGrants(t *testing.T) {
	t.Parallel()
	allToolGrants := []string{
		model.SelfDebugCapability,
		model.GrainSourceGrant,
		model.APIGrantPrefix + "self-debug",
		grainlog.CapabilityName,
		model.GrainJournalGrant,
		deploymenthealth.CapabilityName,
		model.GrainHealthGrant,
		model.GrainSQLReadGrant,
		model.GrainSQLWriteGrant,
		grainci.Grant,
		readissues.CapabilityName,
	}

	for _, grant := range allToolGrants {
		t.Run(grant, func(t *testing.T) {
			t.Parallel()
			grants := []model.Grant{{Capability: grant, Via: model.GrantByLabel}}
			held, _ := runWithGrants(t, grants, "/usr/local/share/grain/src")
			if !slices.Contains(held, grant) {
				t.Errorf("RunConfig.Grants = %v, want %s among them; is %s missing from toolGrants in pkg/orchestrator/run.go?", held, grant, grant)
			}
		})
	}
}

// Two tool-only grants on one task: both reach the same run at once.
func TestRunCycleCarriesEveryToolGrantATaskHolds(t *testing.T) {
	t.Parallel()
	grants := []model.Grant{
		{Capability: readissues.CapabilityName, Via: model.GrantByLabel},
		{Capability: "self-debug", Via: model.GrantByLabel},
	}
	held, _ := runWithGrants(t, grants, "/usr/local/share/grain/src")
	for _, want := range []string{"self-debug", readissues.CapabilityName} {
		if !slices.Contains(held, want) {
			t.Errorf("RunConfig.Grants = %v, want %s among them", held, want)
		}
	}
}

// Only the grants that decide a tool roster travel. A capability that
// mints credentials is placed in the sandbox by its own Materialize, and
// has no business being named in a subprocess's arguments.
func TestRunCycleCarriesOnlyToolGrants(t *testing.T) {
	t.Parallel()
	grants := []model.Grant{{Capability: "gcp-key", Via: model.GrantByLabel}}
	held, _ := runWithGrants(t, grants, "/usr/local/share/grain/src")
	if len(held) != 0 {
		t.Errorf("RunConfig.Grants = %v, want none for a task holding no tool-granting capability", held)
	}
}

// Not gated on Interactive, unlike Config.GrantTools: that gate exists
// because selfrepair's tools need a human watching the chat, and nothing
// either of these grants offers asks anyone anything. filedTask's own
// task is not interactive, so the tests above already prove this; this
// one proves the other half -- a task without a grant is told nothing,
// including where grain's source is.
func TestRunCycleTellsAnOrdinaryTaskNothingAboutItsGrants(t *testing.T) {
	t.Parallel()
	held, sourceDir := runWithGrants(t, nil, "/usr/local/share/grain/src")
	if len(held) != 0 {
		t.Errorf("RunConfig.Grants = %v, want none for a task holding no grant", held)
	}
	if sourceDir != "" {
		t.Errorf("RunConfig.GrainSourceDir = %q, want it withheld from a task without the grant", sourceDir)
	}
}

// A capability registered from outside core as a run grant travels to the
// run like the core ones above, and only while it is registered. Not
// parallel: the registration is process-wide (capabilitytest.Register).
func TestRunCycleForwardsARegisteredRunGrant(t *testing.T) {
	grants := []model.Grant{{Capability: capabilitytest.ID, Via: model.GrantByLabel}}
	if held, _ := runWithGrants(t, grants, ""); slices.Contains(held, capabilitytest.ID) {
		t.Fatalf("RunConfig.Grants = %v before %s is registered, want it absent", held, capabilitytest.ID)
	}
	capabilitytest.Register(t)
	if held, _ := runWithGrants(t, grants, ""); !slices.Contains(held, capabilitytest.ID) {
		t.Errorf("RunConfig.Grants = %v, want the registered %s among them", held, capabilitytest.ID)
	}
}

// A method contributed from outside core (capability.Register) as a run
// grant travels to the run whether the task holds its recorded grant or the
// bare name it retires, and the in-process server it is on admits it
// without any capability server being attached.
func TestRunCycleForwardsARegisteredMethodGrant(t *testing.T) {
	capabilitytest.RegisterMethod(t)
	for _, grant := range []string{capabilitytest.MethodGrant, capabilitytest.MethodAlias} {
		held, _ := runWithGrants(t, []model.Grant{{Capability: grant, Via: model.GrantByLabel}}, "")
		if !slices.Contains(held, grant) {
			t.Errorf("a task holding %s: RunConfig.Grants = %v, want it among them", grant, held)
		}
	}
}
