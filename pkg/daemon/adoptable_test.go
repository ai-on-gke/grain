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

package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// A durable task is placed on a framework this daemon can adopt across
// its own restart (design/design/durable-tasks.design.md#DT-1): on a deployment
// whose default models are [gemini-x, claude-y] and whose claude runs in
// a run host, a durable run goes to claude-y first, an ordinary one keeps
// the list's order, and a pinned model or connection is left alone.
func TestDurableRunsPreferAnAdoptableFramework(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := crossFrameworkPool(t)
	live := DispatchConfig(ctx, pool.Store, pool.cfg)
	live.RunHost = &runhost.Host{}

	if got := live.adoptableFrameworks(ctx); !got[model.AgentFrameworkClaude] || got[model.AgentFrameworkAntigravity] {
		t.Fatalf("adoptableFrameworks = %v, want claude alone (its run host)", got)
	}

	pick := func(choice orchestrator.AgentChoice) (string, []string) {
		t.Helper()
		req, err := pool.resolve(ctx, live, choice)
		if err != nil {
			t.Fatalf("resolve(%+v): %v", choice, err)
		}
		pool.mu.Lock()
		defer pool.mu.Unlock()
		picked, candidates, _ := pool.selectCandidateLocked(live, req, time.Now())
		var ids []string
		for _, c := range candidates {
			ids = append(ids, c.ID)
		}
		return picked, ids
	}

	if m, ids := pick(orchestrator.AgentChoice{}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("ordinary run = %s on %v, want gemini-x on agy (the list's order)", m, ids)
	}
	if m, ids := pick(orchestrator.AgentChoice{Durable: true, Priority: true}); m != "claude-y" || !slices.Equal(ids, []string{"claude"}) {
		t.Errorf("durable run = %s on %v, want claude-y on claude (adoptable under the run host)", m, ids)
	}
	// Weaker than a pin, and weaker than failover (AvoidModels).
	if m, ids := pick(orchestrator.AgentChoice{Durable: true, Model: "gemini-x"}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("durable run pinned to gemini-x = %s on %v, want gemini-x on agy", m, ids)
	}
	if m, ids := pick(orchestrator.AgentChoice{Durable: true, AvoidModels: []string{"claude-y"}}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("durable run avoiding claude-y = %s on %v, want gemini-x on agy", m, ids)
	}
	// Still falls back to what cannot be adopted rather than waiting.
	now := time.Now()
	pool.Pause.Begin(now, orchestrator.PauseScope{Connection: "claude", Model: "claude-y", Team: model.DefaultTeamID},
		&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	if m, ids := pick(orchestrator.AgentChoice{Durable: true}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("durable run with claude-y paused = %s on %v, want gemini-x on agy (a restart suspension, not a wait)", m, ids)
	}

	// With nothing adoptable the preference is no preference at all.
	live.RunHost = nil
	pool.Pause.Lift()
	if m, _ := pick(orchestrator.AgentChoice{Durable: true}); m != "gemini-x" {
		t.Errorf("durable run with nothing adoptable = %s, want gemini-x (the list's order)", m)
	}
}

// The rotation with no default model: a durable run's candidates are
// narrowed to the adoptable connections when there are any.
func TestDurableRunsNarrowTheRotationToAdoptableConnections(t *testing.T) {
	t.Parallel()
	req := poolRequest{}
	both := []model.AgentBackend{
		{ID: "agy", Framework: model.AgentFrameworkAntigravity},
		{ID: "claude", Framework: model.AgentFrameworkClaude},
	}
	if got := req.adoptableOnly(both); len(got) != 2 {
		t.Errorf("ordinary request kept %v, want both", got)
	}
	req.preferAdoptable(map[string]bool{model.AgentFrameworkClaude: true})
	if got := req.adoptableOnly(both); len(got) != 1 || got[0].ID != "claude" {
		t.Errorf("durable request kept %v, want claude alone", got)
	}
	if got := req.adoptableOnly(both[:1]); len(got) != 1 || got[0].ID != "agy" {
		t.Errorf("durable request with no adoptable candidate kept %v, want agy rather than nothing", got)
	}
}

// An antigravity run adopted across a restart is read with antigravity's
// parser on a deployment that also has claude connections, names no
// framework of its own, and lists models on both in its default -- the
// v3.11.0 upgrade's six adopted agy runs, which the claude parser read
// ("claude: no result event found in output") and so lost with their
// transcripts. What the run is on comes back from its row (LiveRuns),
// not from the rotation, which for a durable task with claude under a
// run host would put it on claude.
func TestAnAdoptedAntigravityRunIsReadByAntigravitysParser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	agy := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"gemini-x", "opus-y"}}
	cl := model.AgentBackend{ID: "claude", Framework: model.AgentFrameworkClaude,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"claude-y"}}
	pool := providerDefaultPool(t, "gemini-x, claude-y, opus-y", agy, cl)
	pool.cfg.RunHost = &runhost.Host{}
	store := pool.Store

	task := model.Task{ID: "t", Intent: model.IntentImplement, Title: "t", Admission: model.AdmissionDurable,
		Filing:   model.Filing{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "p"}}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "p"}}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "t-1", TaskID: "t", Sandbox: "t-1", Attempt: 1,
		StartedAt: time.Now().UTC()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunModel(ctx, "t-1", "opus-y", model.AgentFrameworkAntigravity); err != nil {
		t.Fatal(err)
	}
	live, err := store.LiveRuns(ctx)
	if err != nil || len(live) != 1 {
		t.Fatalf("live runs = %+v (%v), want t-1", live, err)
	}
	if live[0].AgentFramework != model.AgentFrameworkAntigravity || live[0].Model != "opus-y" {
		t.Fatalf("live run = %+v, want it on opus-y on antigravity", live[0])
	}

	build := func(choice orchestrator.AgentChoice) *BackendFramework {
		t.Helper()
		built, err := pool.Build(ctx, choice)
		if err != nil {
			t.Fatalf("Build(%+v): %v", choice, err)
		}
		fw := built.(*BackendFramework)
		t.Cleanup(fw.Release)
		return fw
	}
	// What the rotation alone would give it: the trap the run fell in.
	if fw := build(orchestrator.ChoiceFor(task)); fw.FrameworkName != model.AgentFrameworkClaude {
		t.Fatalf("the task's own choice built %s, want claude (adoptable under the run host) -- "+
			"the precondition this test guards against", fw.FrameworkName)
	}
	fw := build(orchestrator.AdoptedChoice(orchestrator.ChoiceFor(task), live[0]))
	if fw.FrameworkName != model.AgentFrameworkAntigravity || fw.Id != "agy" || fw.CredentialModel() != "opus-y" {
		t.Fatalf("adopted run built %s on %s with %s, want antigravity on agy with opus-y",
			fw.FrameworkName, fw.Id, fw.CredentialModel())
	}

	stream := strings.Join([]string{
		`{"event":"init","init":{"cwd":"/w","tools":["mcp__grain-sandbox__run_command"],"permission_mode":"bypass"}}`,
		`{"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"tool","tool_name":"write_file","tool_info":{"name":"write_file","parameters":{"path":"ok.txt"}}}}`,
		`{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"write_file","tool_info":{"name":"write_file","output":"wrote ok.txt"}}}`,
		`{"event":"result","result":{"status":"SUCCESS","response":"done"}}`,
	}, "\n") + "\n"
	result, err := fw.ParseResult(stream, nil)
	if err != nil || result == nil {
		t.Fatalf("ParseResult = %+v, %v; want the agy stream read as the success it is", result, err)
	}
	if result.FinalText != "done" || len(result.ToolCalls) != 1 || !strings.Contains(result.Transcript, "write_file") {
		t.Fatalf("result = %+v, want agy's answer, its tool call and a transcript naming it", result)
	}
}
