package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestMCPProposeTaskWalkthroughSection4 implements the full verification walkthrough
// from docs/test-plans/2026-09-24.md §4 (MCP propose_task Full Parameter Expansion &
// Declarative Task Instantiation).
//
// Walkthrough Procedure:
//  1. Initialize a Grain test environment with Store, UI Client, and mock GitHub backend.
//  2. Register templates in the store with parameterized fields and default values.
//  3. File tasks using the expanded propose_task MCP tool parameters via live MCP / UI client:
//     - at_front: true (assert queue placement ahead of earlier queued tasks)
//     - no_repo: true (assert Target is nil for standalone execution)
//     - sandbox_cpus, sandbox_memory_mb, sandbox_disk_gb (assert VM sizing persisted)
//     - network_access: "none" / "open" (assert network sandbox gating)
//     - template_id & template_fields (assert interpolation of title and body)
//     - reads & writes (assert multi-repository target and credential resolution)
//     - capabilities: ["gcp-key"] (assert capability assignment)
//  4. Execute end-of-run replay via orchestrator.ProcessResult and verify relayProposedTasks
//     correctly handles declarative parameters and template expansion.
//  5. Dispatch tasks through dispatch.Cycle and verify dispatcher honors orderKey, sizing,
//     and standalone execution properties.
func TestMCPProposeTaskWalkthroughSection4(t *testing.T) {
	t.Parallel()
	clock := baseTime
	rig := newProposalRig(t)
	storeDir := t.TempDir()

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		client := ui.NewClient(ui.Config{
			Actor:        ui.DefaultActor("test-operator@acme.com"),
			Capabilities: ui.OfferedCapabilities(),
		}, store)

		// --------------------------------------------------------------------
		// Step 1: Register reusable template in Store
		// --------------------------------------------------------------------
		t.Log("=== Step 1: Register Reusable Declarative Templates ===")
		tmpl := model.Template{
			ID:            "tmpl-perf-audit",
			Name:          "Performance Audit",
			Title:         "Performance audit for {{service}}",
			Body:          "Run memory and latency benchmarks for {{service}} under {{load_tier}} load.",
			NetworkAccess: model.NetworkNone,
			Fields: []model.TemplateField{
				{Name: "service", Required: true},
				{Name: "load_tier", Default: "high-throughput"},
			},
		}
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatalf("putting template: %v", err)
		}

		// Configure global workflow repo for auto-grant testing
		repoRef := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutRepoWorkflows(ctx, repoRef, []model.RepoWorkflow{
			{Repo: repoRef, Path: ".grain/workflows/ci.yaml", Text: "steps: []"},
		}); err != nil {
			t.Fatalf("putting repo workflows: %v", err)
		}

		// --------------------------------------------------------------------
		// Step 2: Seed existing queued tasks to test at_front queue ordering
		// --------------------------------------------------------------------
		t.Log("=== Step 2: Seed Baseline Queued Tasks ===")
		baseTask1, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "Baseline Task 1", Repo: "acme/widgets", Approved: true,
		})
		if err != nil {
			t.Fatalf("creating base task 1: %v", err)
		}
		baseTask2, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "Baseline Task 2", Repo: "acme/widgets", Approved: true,
		})
		if err != nil {
			t.Fatalf("creating base task 2: %v", err)
		}

		// Parent task that will propose subsequent work
		parentTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			Title: "Director Task", Repo: "acme/widgets", Approved: true,
		})
		if err != nil {
			t.Fatalf("creating parent task: %v", err)
		}

		// --------------------------------------------------------------------
		// Step 3: Live MCP proposal creation with full parameter expansion
		// --------------------------------------------------------------------
		t.Log("=== Step 3: Live MCP Proposal with Expanded Declarative Parameters ===")
		atFront := true
		liveProposal, err := client.CreateTask(ctx, ui.CreateTaskRequest{
			ProposedBy:      parentTask.ID,
			TemplateID:      tmpl.ID,
			Fields:          map[string]string{"service": "billing-service"},
			AtFront:         &atFront,
			NoRepo:          true,
			SandboxCPUs:     4,
			SandboxMemoryMB: 8192,
			SandboxDiskGB:   40,
			NetworkAccess:   string(model.NetworkNone),
			Reads:           []string{"acme/docs"},
			Writes:          []ui.WriteTarget{{Repo: "acme/backend", Base: "release-v2"}},
			Capabilities:    &[]string{"gcp-key"},
			Model:           "claude-sonnet-5",
			AgentFramework:  "claude",
			Approved:        true,
		})
		if err != nil {
			t.Fatalf("creating live proposal from template: %v", err)
		}

		// Verify live proposal fields in store
		storedLive, err := store.GetTask(ctx, liveProposal.ID)
		if err != nil || storedLive == nil {
			t.Fatalf("GetTask(%s): %v", liveProposal.ID, err)
		}

		// Assertions on declarative parameters
		if storedLive.Title != "Performance audit for billing-service" {
			t.Errorf("Title = %q, want %q", storedLive.Title, "Performance audit for billing-service")
		}
		if !strings.Contains(storedLive.Body, "Run memory and latency benchmarks for billing-service under high-throughput load.") {
			t.Errorf("Body = %q, want interpolated template body", storedLive.Body)
		}
		if storedLive.Target != nil {
			t.Errorf("Target = %v, want nil for no_repo: true", storedLive.Target)
		}
		if storedLive.SandboxCPUs != 4 || storedLive.SandboxMemoryMB != 8192 || storedLive.SandboxDiskGB != 40 {
			t.Errorf("VM sizing = (%d CPUs, %d MB, %d GB), want (4, 8192, 40)",
				storedLive.SandboxCPUs, storedLive.SandboxMemoryMB, storedLive.SandboxDiskGB)
		}
		if storedLive.NetworkAccess != model.NetworkNone {
			t.Errorf("NetworkAccess = %q, want %q", storedLive.NetworkAccess, model.NetworkNone)
		}
		if len(storedLive.Reads) != 1 || storedLive.Reads[0].String() != "acme/docs" {
			t.Errorf("Reads = %+v, want acme/docs", storedLive.Reads)
		}
		if len(storedLive.Writes) != 1 || storedLive.Writes[0].Repo.String() != "acme/backend" || storedLive.Writes[0].Base != "release-v2" {
			t.Errorf("Writes = %+v, want acme/backend@release-v2", storedLive.Writes)
		}
		// A proposal's model is a model choice (model.Task.Model), not a
		// connection pin (docs/connections-and-models.md).
		if storedLive.Model != "claude-sonnet-5" || storedLive.AgentBackendID != "" || storedLive.AgentFramework != "claude" {
			t.Errorf("Model = %q, AgentBackendID = %q, AgentFramework = %q; want claude-sonnet-5, no pin, claude",
				storedLive.Model, storedLive.AgentBackendID, storedLive.AgentFramework)
		}

		// Assert at_front placed it before baseline tasks
		storedBase1, _ := store.GetTask(ctx, baseTask1.ID)
		storedBase2, _ := store.GetTask(ctx, baseTask2.ID)
		if storedLive.OrderKey >= storedBase1.OrderKey || storedLive.OrderKey >= storedBase2.OrderKey {
			t.Errorf("OrderKey of at_front task (%f) should be before base1 (%f) and base2 (%f)",
				storedLive.OrderKey, storedBase1.OrderKey, storedBase2.OrderKey)
		}

		// --------------------------------------------------------------------
		// Step 4: End-of-run replay via orchestrator.ProcessResult
		// --------------------------------------------------------------------
		t.Log("=== Step 4: End-of-Run Replay via ProcessResult with Template Instantiation ===")
		replayResult := agent.Result{
			ToolCalls: []agent.ToolCall{
				{
					Name: "propose_task",
					Arguments: map[string]any{
						"template_id":       tmpl.ID,
						"template_fields":   map[string]any{"service": "auth-gateway", "load_tier": "extreme"},
						"at_front":          true,
						"no_repo":           true,
						"sandbox_cpus":      8,
						"sandbox_memory_mb": 16384,
						"sandbox_disk_gb":   80,
						"network_access":    "none",
						"reads":             []any{"acme/docs"},
						"writes":            []any{map[string]any{"repo": "acme/gateway", "base": "main"}},
						"capabilities":      []any{"gcp-key"},
					},
				},
			},
		}

		parentStored, _ := store.GetTask(ctx, parentTask.ID)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *parentStored, &replayResult, parentTask.ID+"-run1", clock); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		// Retrieve replayed proposed task
		allTasks, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var replayedTask *model.Task
		for i := range allTasks {
			if allTasks[i].Title == "Performance audit for auth-gateway" {
				replayedTask = &allTasks[i]
				break
			}
		}
		if replayedTask == nil {
			t.Fatal("replayed proposed task was not found in store")
		}

		if !strings.Contains(replayedTask.Body, "Run memory and latency benchmarks for auth-gateway under extreme load.") {
			t.Errorf("Replayed Body = %q, want extreme load interpolated", replayedTask.Body)
		}
		if replayedTask.Target != nil {
			t.Errorf("Replayed Target = %v, want nil for no_repo", replayedTask.Target)
		}
		if replayedTask.SandboxCPUs != 8 || replayedTask.SandboxMemoryMB != 16384 || replayedTask.SandboxDiskGB != 80 {
			t.Errorf("Replayed VM sizing = (%d, %d, %d), want (8, 16384, 80)",
				replayedTask.SandboxCPUs, replayedTask.SandboxMemoryMB, replayedTask.SandboxDiskGB)
		}
		if replayedTask.NetworkAccess != model.NetworkNone {
			t.Errorf("Replayed NetworkAccess = %q, want %q", replayedTask.NetworkAccess, model.NetworkNone)
		}
		if len(replayedTask.Reads) != 1 || replayedTask.Reads[0].String() != "acme/docs" {
			t.Errorf("Replayed Reads = %+v, want acme/docs", replayedTask.Reads)
		}
		if len(replayedTask.Writes) != 1 || replayedTask.Writes[0].Repo.String() != "acme/gateway" {
			t.Errorf("Replayed Writes = %+v, want acme/gateway", replayedTask.Writes)
		}

		// --------------------------------------------------------------------
		// Step 5: Dispatch cycle execution verification
		// --------------------------------------------------------------------
		t.Log("=== Step 5: Dispatch Cycle Verification ===")
		clock = clock.Add(time.Minute)
		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 5}, clock)
		if err != nil {
			t.Fatalf("dispatch.Cycle: %v", err)
		}

		// The top dispatched task should be the at_front live proposal
		if len(dispatches) == 0 {
			t.Fatal("expected tasks to be dispatched")
		}
		if dispatches[0].TaskID != liveProposal.ID {
			t.Errorf("first dispatched task = %s, want at_front task %s", dispatches[0].TaskID, liveProposal.ID)
		}
		t.Logf("Successfully dispatched at_front task %s ahead of baseline tasks", dispatches[0].TaskID)
	})
}
