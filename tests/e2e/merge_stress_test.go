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

// Package e2e provides end-to-end stress testing of the grain merge queue
// and orchestrator liveness under sustained, concurrent, and potentially
// conflicting edits across multiple tasks and repositories.
//
// TestMergeLivenessStressEndToEnd drives a deterministic, fast end-to-end
// simulation of multiple concurrent tasks making overlapping and conflicting
// changes to shared files across bare git repositories, verifying that:
//  1. Real git merge conflicts between task branches and main are accurately
//     detected.
//  2. Conflicted queue heads are automatically requeued for repair and
//     dispatched to agent workers.
//  3. Repaired branches merge cleanly into main when resolved.
//  4. Unresolvable repairs escalate to the operator without blocking the
//     remainder of the merge queue.
//  5. External commits landing on main while PRs are queued trigger head
//     refreshes or repairs as appropriate.
//  6. Invariants hold: no task gets stuck, no slots or sandboxes leak, and
//     all tasks reach terminal states.
//
// TestMergeStressSustained is the sustained, long-running counterpart designed
// for extended suites (such as nightly.yml and `make merge-stress`), capable
// of running for minutes or hours under continuous load to ensure merge queue
// liveness over time.
package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// mergeStressConfig holds every knob that sizes a merge liveness stress run.
type mergeStressConfig struct {
	slots        int
	repos        int
	totalTasks   int
	rounds       int
	duration     time.Duration
	drainTimeout time.Duration
	seed         [2]uint64
}

// mergeStressMetrics records statistics and coverage across the stress run.
type mergeStressMetrics struct {
	mu sync.Mutex

	initialPushes     int
	conflictsInjected int
	cleanEdits        int
	ciFailures        int

	repairsDispatched int
	repairsResolved   int
	repairsFailed     int

	mainCommitsPushed int

	cyclesDriven int
}

func (m *mergeStressMetrics) recordInitial(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initialPushes++
	switch kind {
	case "conflict":
		m.conflictsInjected++
	case "clean":
		m.cleanEdits++
	case "ci_fail":
		m.ciFailures++
	}
}

func (m *mergeStressMetrics) recordRepair(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repairsDispatched++
	switch outcome {
	case "resolve":
		m.repairsResolved++
	case "fail":
		m.repairsFailed++
	}
}

func (m *mergeStressMetrics) recordMainCommit() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mainCommitsPushed++
}

// multiRepoSim wraps multiple *githubsim.Sim instances behind a single
// github.Transport, routing requests by owner and repo extracted from the URL path.
type multiRepoSim struct {
	mu   sync.Mutex
	sims map[string]*githubsim.Sim // "owner/repo" -> Sim
}

func newMultiRepoSim() *multiRepoSim {
	return &multiRepoSim{sims: make(map[string]*githubsim.Sim)}
}

func (m *multiRepoSim) register(owner, repo string, sim *githubsim.Sim) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sims[owner+"/"+repo] = sim
}

var repoPathRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)`)

func (m *multiRepoSim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	m.mu.Lock()
	match := repoPathRe.FindStringSubmatch(path)
	if match == nil {
		m.mu.Unlock()
		return github.ApiResponse{Status: 404, Body: []byte(`{"message":"not found"}`)}, nil
	}
	key := match[1] + "/" + match[2]
	sim, ok := m.sims[key]
	m.mu.Unlock()
	if !ok {
		return github.ApiResponse{Status: 404, Body: []byte(`{"message":"repo not found"}`)}, nil
	}
	return sim.Request(method, path, headers, body)
}

func (m *multiRepoSim) forEach(fn func(key string, sim *githubsim.Sim)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, sim := range m.sims {
		fn(k, sim)
	}
}

// updateSimMergeability tests git mergeability for all open PRs in sim against
// the bare git repository and updates each PR's Mergeable boolean.
func updateSimMergeability(sim *githubsim.Sim) {
	for i := range sim.PullRequests {
		pr := &sim.PullRequests[i]
		if pr.State != "open" {
			continue
		}
		if !simContainsBranch(sim.BareRepo, pr.Head) || !simContainsBranch(sim.BareRepo, pr.Base) {
			continue
		}
		conflicted := simTestConflict(sim.BareRepo, pr.Base, pr.Head)
		mergeable := !conflicted
		pr.Mergeable = &mergeable
	}
}

func simContainsBranch(bareRepo, branch string) bool {
	cmd := exec.Command("git", "--git-dir", bareRepo, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func simTestConflict(bareRepo, base, head string) bool {
	dir, err := os.MkdirTemp("", "grain-merge-test-*")
	if err != nil {
		return true
	}
	defer os.RemoveAll(dir)
	wd := filepath.Join(dir, "work")
	if err := exec.Command("git", "clone", "-q", bareRepo, wd).Run(); err != nil {
		return true
	}
	_ = exec.Command("git", "-C", wd, "config", "user.email", "test@example.com").Run()
	_ = exec.Command("git", "-C", wd, "config", "user.name", "test").Run()
	_ = exec.Command("git", "-C", wd, "checkout", "-q", base).Run()
	_ = exec.Command("git", "-C", wd, "fetch", "-q", "origin", head).Run()
	mergeCmd := exec.Command("git", "-C", wd, "merge", "--no-commit", "--no-ff", "origin/"+head)
	err = mergeCmd.Run()
	return err != nil
}

// mergeStressScriptGenerator produces agent steps for initial dispatches
// and conflict repair dispatches.
type mergeStressScriptGenerator struct {
	mu            sync.Mutex
	rng           *rand.Rand
	githubHost    string
	multiSim      *multiRepoSim
	metrics       *mergeStressMetrics
	taskPlans     map[string]taskStressPlan // taskID -> plan
	initialPushed map[string]bool
}

type taskStressPlan struct {
	initialKind   string // "conflict", "clean", "ci_fail"
	repairOutcome string // "resolve", "fail"
	targetFile    string
	editContent   string
}

func newMergeStressScriptGenerator(rng *rand.Rand, githubHost string, multiSim *multiRepoSim, metrics *mergeStressMetrics) *mergeStressScriptGenerator {
	return &mergeStressScriptGenerator{
		rng:           rng,
		githubHost:    githubHost,
		multiSim:      multiSim,
		metrics:       metrics,
		taskPlans:     make(map[string]taskStressPlan),
		initialPushed: make(map[string]bool),
	}
}

var stressPromptRe = regexp.MustCompile(`Work in (\S+)\. Push your change to a new branch named "([^"]+)"`)

func (g *mergeStressScriptGenerator) Generate(prompt string) ([]antigravity.Step, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	m := stressPromptRe.FindStringSubmatch(prompt)
	if m == nil {
		if strings.Contains(prompt, "This task has no repo attached") {
			return []antigravity.Step{
				finalText("no repo attached standing session note"),
			}, nil
		}
		return nil, fmt.Errorf("merge stress generator: prompt missing repo/branch: %s", prompt)
	}

	repoFull, branch := m[1], m[2]
	parts := strings.Split(repoFull, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("unexpected repo format: %s", repoFull)
	}
	owner, repoName := parts[0], parts[1]
	taskID := strings.TrimPrefix(branch, "grain/task-")
	remote := fmt.Sprintf("http://%s/%s/%s.git", g.githubHost, owner, repoName)

	isRepair := g.initialPushed[taskID]

	plan, exists := g.taskPlans[taskID]
	if !exists {
		r := g.rng.Float64()
		var kind, repairOutcome string
		switch {
		case r < 0.65:
			kind = "conflict"
		case r < 0.85:
			kind = "clean"
		default:
			kind = "ci_fail"
		}

		if g.rng.Float64() < 0.80 {
			repairOutcome = "resolve"
		} else {
			repairOutcome = "fail"
		}

		plan = taskStressPlan{
			initialKind:   kind,
			repairOutcome: repairOutcome,
			targetFile:    "SHARED_FEATURE.md",
			editContent:   fmt.Sprintf("feature update from task %s (val=%d)", taskID, g.rng.Uint64()),
		}
		g.taskPlans[taskID] = plan
	}

	if !isRepair {
		g.initialPushed[taskID] = true
		g.metrics.recordInitial(plan.initialKind)
		return g.buildInitialScript(remote, branch, taskID, plan, owner, repoName)
	}

	g.metrics.recordRepair(plan.repairOutcome)
	return g.buildRepairScript(remote, branch, taskID, plan, owner, repoName)
}

func (g *mergeStressScriptGenerator) buildInitialScript(remote, branch, taskID string, plan taskStressPlan, owner, repoName string) ([]antigravity.Step, error) {
	cloneDir := fmt.Sprintf("work-%s-%x", taskID, g.rng.Uint64())
	var editCmd string

	switch plan.initialKind {
	case "conflict":
		editCmd = fmt.Sprintf("echo '%s' > %s", plan.editContent, plan.targetFile)
	case "clean":
		editCmd = fmt.Sprintf("echo 'clean content for %s' > TASK_%s.md", taskID, taskID)
	case "ci_fail":
		editCmd = fmt.Sprintf("echo 'ci fail change %s' > %s", taskID, plan.targetFile)
		g.multiSim.forEach(func(key string, sim *githubsim.Sim) {
			if key == owner+"/"+repoName {
				failure := "failure"
				sim.CheckRuns[branch] = []github.CheckRun{{
					Name: "ci", Status: "completed", Conclusion: &failure,
				}}
			}
		})
	}

	cmd := fmt.Sprintf("rm -rf %s && git clone %s %s && cd %s && "+
		"git config user.email agent@example.com && git config user.name agent && "+
		"git checkout -b %s && "+
		"%s && "+
		"git add . && git commit -q -m 'commit for %s' && "+
		"git push origin %s",
		cloneDir, remote, cloneDir, cloneDir,
		branch,
		editCmd,
		taskID,
		branch,
	)

	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText(fmt.Sprintf("initial push for %s", taskID)),
	}, nil
}

func (g *mergeStressScriptGenerator) buildRepairScript(remote, branch, taskID string, plan taskStressPlan, owner, repoName string) ([]antigravity.Step, error) {
	cloneDir := fmt.Sprintf("repair-%s-%x", taskID, g.rng.Uint64())
	var repairCmd string

	switch plan.repairOutcome {
	case "resolve":
		repairCmd = fmt.Sprintf(
			"rm -rf %s && git clone %s %s && cd %s && "+
				"git config user.email agent@example.com && git config user.name agent && "+
				"git checkout -b %s origin/%s && "+
				"git fetch origin main && "+
				"git merge origin/main -X ours -m 'resolved merge conflict for %s' && "+
				"git push origin %s",
			cloneDir, remote, cloneDir, cloneDir,
			branch, branch,
			taskID,
			branch,
		)
		g.multiSim.forEach(func(key string, sim *githubsim.Sim) {
			if key == owner+"/"+repoName {
				success := "success"
				sim.CheckRuns[branch] = []github.CheckRun{{
					Name: "ci", Status: "completed", Conclusion: &success,
				}}
			}
		})
	case "fail":
		repairCmd = fmt.Sprintf(
			"rm -rf %s && git clone %s %s && cd %s && "+
				"git config user.email agent@example.com && git config user.name agent && "+
				"git checkout -b %s origin/%s && "+
				"echo 'failed resolution attempt' >> ATTEMPT.md && "+
				"git add ATTEMPT.md && git commit -q -m 'attempt fix without resolve' && "+
				"git push origin %s",
			cloneDir, remote, cloneDir, cloneDir,
			branch, branch,
			branch,
		)
	}

	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": repairCmd}),
		finalText(fmt.Sprintf("repair execution for %s with outcome %s", taskID, plan.repairOutcome)),
	}, nil
}

type scriptAdapter struct {
	gen    *mergeStressScriptGenerator
	steps  []antigravity.Step
	called int
}

func (s *scriptAdapter) Next(prompt string) (antigravity.Step, bool) {
	if s.steps == nil {
		steps, err := s.gen.Generate(prompt)
		if err != nil {
			panic(err)
		}
		s.steps = steps
	}
	if s.called >= len(s.steps) {
		return antigravity.Step{}, false
	}
	step := s.steps[s.called]
	s.called++
	return step, true
}

func pushBareCommit(t *testing.T, barePath, branch, path, content, msg string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "clone", "-q", barePath, "work")
	wd := filepath.Join(dir, "work")
	run(t, wd, "git", "config", "user.email", "external@example.com")
	run(t, wd, "git", "config", "user.name", "external")
	run(t, wd, "git", "checkout", "-q", branch)
	if err := os.WriteFile(filepath.Join(wd, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wd, "git", "add", path)
	run(t, wd, "git", "commit", "-q", "-m", msg)
	run(t, wd, "git", "push", "origin", branch)
}

func getAllWorkTasks(ctx context.Context, store *model.Store) []model.Task {
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return nil
	}
	var out []model.Task
	for _, t := range tasks {
		if _, ok := model.ConsoleSessionIDOf(t); ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

// runMergeLivenessStressEngine is the core test driver for merge liveness stress testing.
func runMergeLivenessStressEngine(t *testing.T, cfg mergeStressConfig) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	upstream := t.TempDir()
	rng := rand.New(rand.NewPCG(cfg.seed[0], cfg.seed[1]))
	metrics := &mergeStressMetrics{}
	multiSim := newMultiRepoSim()

	repos := make([]model.RepoRef, 0, cfg.repos)
	barePaths := make(map[string]string)
	for i := 0; i < cfg.repos; i++ {
		owner := "acme"
		name := fmt.Sprintf("repo-%d", i+1)
		repoDir := filepath.Join(upstream, owner, name+".git")
		bare := initBareRepo(t, repoDir, "main")
		run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")

		// Seed initial commit
		seedParent := t.TempDir()
		run(t, seedParent, "git", "clone", bare, "seed")
		seed := filepath.Join(seedParent, "seed")
		run(t, seed, "git", "config", "user.email", "seed@example.com")
		run(t, seed, "git", "config", "user.name", "seed")
		if err := os.WriteFile(filepath.Join(seed, "SHARED_FEATURE.md"), []byte("base feature line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, seed, "git", "add", "SHARED_FEATURE.md")
		run(t, seed, "git", "commit", "-q", "-m", "initial commit")
		run(t, seed, "git", "push", "origin", "main")

		ref := model.RepoRef{Owner: owner, Name: name}
		repos = append(repos, ref)
		barePaths[owner+"/"+name] = bare

		sim := githubsim.New(owner, name, bare, "main")
		multiSim.register(owner, name, sim)
	}

	githubHost := githubHostServer(t, multiSim, upstream)
	remoteURL := "http://" + githubHost
	sandboxes := credentialed(t, remoteURL)

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := model.New(db)
	ctx := t.Context()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	client := github.NewClient(multiSim, nil)
	generator := newMergeStressScriptGenerator(rng, githubHost, multiSim, metrics)

	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  sandboxes,
		MaxWorkers: cfg.slots,
		Framework: func(ctx context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(&scriptAdapter{gen: generator}), nil
		},
	}

	clock := baseTime
	tasksCreated := 0
	deadline := time.Time{}
	if cfg.duration > 0 {
		deadline = time.Now().Add(cfg.duration)
	}

	t.Logf("starting merge liveness stress test: slots=%d repos=%d target_tasks=%d rounds=%d seed=%v duration=%v",
		cfg.slots, cfg.repos, cfg.totalTasks, cfg.rounds, cfg.seed, cfg.duration)

	round := 0
	for ; round < cfg.rounds; round++ {
		if !deadline.IsZero() && time.Now().After(deadline) {
			t.Logf("duration %v reached at round %d", cfg.duration, round)
			break
		}
		clock = clock.Add(15 * time.Second)

		// 1. File new tasks if under limit
		if tasksCreated < cfg.totalTasks && rng.Float64() < 0.75 {
			batch := 1 + rng.IntN(3)
			for b := 0; b < batch && tasksCreated < cfg.totalTasks; b++ {
				tasksCreated++
				taskID := fmt.Sprintf("stress-%04d", tasksCreated)
				repo := repos[rng.IntN(len(repos))]
				task := model.Task{
					ID:        taskID,
					Intent:    model.IntentImplement,
					Title:     fmt.Sprintf("Stress task %s", taskID),
					Origin:    model.Origin{Attribution: model.Attribution{Actor: human("stress-runner")}, Reason: model.ReasonDirect},
					Approval:  &model.Attribution{Actor: human("stress-runner")},
					Target:    &repo,
					Binding:   model.BindingDirective,
					AutoMerge: true,
					CreatedAt: &clock,
				}
				if err := store.PutTask(ctx, task); err != nil {
					t.Fatalf("PutTask(%s): %v", taskID, err)
				}
			}
		}

		// 2. Occasionally push an external direct commit to main on a random repo
		if rng.Float64() < 0.20 {
			targetRepo := repos[rng.IntN(len(repos))]
			bare := barePaths[targetRepo.Owner+"/"+targetRepo.Name]
			pushBareCommit(
				t, bare, "main",
				"SHARED_FEATURE.md",
				fmt.Sprintf("external change on main at round %d\n", round),
				fmt.Sprintf("external push at round %d", round),
			)
			metrics.recordMainCommit()
		}

		// 3. Update simulated mergeability for all open PRs in all repos
		multiSim.forEach(func(key string, sim *githubsim.Sim) {
			updateSimMergeability(sim)
		})

		// 4. Run Orchestrator Cycle
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := orchestrator.RunCycle(cctx, deps, clock); err != nil {
			cancel()
			t.Fatalf("round %d: RunCycle error: %v", round, err)
		}
		cancel()
		metrics.cyclesDriven++

		// 5. Invariant check after each cycle:
		// live run count must be 0 immediately after RunCycle returns
		liveRuns, err := store.LiveRunCount(ctx)
		if err != nil {
			t.Fatalf("round %d: LiveRunCount: %v", round, err)
		}
		if liveRuns != 0 {
			t.Fatalf("round %d: LiveRunCount = %d, want 0 after RunCycle returned", round, liveRuns)
		}

		// 6. Check for merge queue progress & records
		checkLivenessHealth(t, store, ctx, round)
	}

	t.Logf("active phase complete after %d rounds, %d tasks filed. Now draining merge queue...", round, tasksCreated)

	// --- Draining Phase ---
	drainClock := clock
	drainDeadline := time.Now().Add(cfg.drainTimeout)
	for {
		if time.Now().After(drainDeadline) {
			tasks := getAllWorkTasks(ctx, store)
			for _, task := range tasks {
				st, _ := store.State(ctx, task.ID)
				obs, _ := store.GetObservation(ctx, task.ID)
				t.Logf("drain timeout task: id=%s state=%s obs=%+v", task.ID, st, obs)
			}
			t.Fatalf("drain timeout (%v) exceeded while waiting for tasks to settle", cfg.drainTimeout)
		}
		drainClock = drainClock.Add(15 * time.Second)

		// Update mergeability before each drain cycle
		multiSim.forEach(func(key string, sim *githubsim.Sim) {
			updateSimMergeability(sim)
		})

		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := orchestrator.RunCycle(cctx, deps, drainClock); err != nil {
			cancel()
			t.Fatalf("drain RunCycle error: %v", err)
		}
		cancel()

		// Check if all work tasks have reached terminal state
		tasks := getAllWorkTasks(ctx, store)
		allSettled := true
		for _, task := range tasks {
			state, err := store.State(ctx, task.ID)
			if err != nil {
				t.Fatalf("State(%s): %v", task.ID, err)
			}
			obs, err := store.GetObservation(ctx, task.ID)
			if err != nil {
				t.Fatalf("GetObservation(%s): %v", task.ID, err)
			}
			if !isTaskSettled(state, obs) {
				allSettled = false
				break
			}
		}

		if allSettled {
			t.Logf("all %d tasks reached terminal states cleanly", len(tasks))
			break
		}
	}

	// --- Final Post-Drain Assertions ---
	assertFinalInvariants(t, store, ctx, barePaths, repos, metrics, tasksCreated)
}

// checkLivenessHealth asserts that no task in the store is in an invalid or stuck state.
func checkLivenessHealth(t *testing.T, store *model.Store, ctx context.Context, round int) {
	t.Helper()
	tasks := getAllWorkTasks(ctx, store)
	for _, task := range tasks {
		state, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatalf("round %d: State(%s): %v", round, task.ID, err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatalf("round %d: GetObservation(%s): %v", round, task.ID, err)
		}

		if state == model.StateCompleted {
			if obs == nil {
				t.Fatalf("round %d: task %s is completed but has no observation", round, task.ID)
			}
			if obs.MergeQueueBlockedAt != nil && obs.RepairInFlight() {
				t.Fatalf("round %d: task %s is marked blocked but still has repair in flight", round, task.ID)
			}
		}
	}
}

func isTaskSettled(state model.State, obs *model.Observation) bool {
	switch state {
	case model.StateClosed, model.StateAwaitingReply:
		return true
	case model.StateCompleted:
		// Terminal merge queue failure: blocked / merge_failed with no active repair
		return obs != nil && (obs.MergeQueueState == "merge_failed" || obs.MergeQueueBlockedAt != nil) && !obs.RepairInFlight()
	default:
		return false
	}
}

// assertFinalInvariants verifies terminal states, git branch reachability, and metrics.
func assertFinalInvariants(t *testing.T, store *model.Store, ctx context.Context, barePaths map[string]string, repos []model.RepoRef, metrics *mergeStressMetrics, totalTasks int) {
	t.Helper()

	tasks := getAllWorkTasks(ctx, store)
	if len(tasks) != totalTasks {
		t.Fatalf("expected %d total tasks, found %d in store", totalTasks, len(tasks))
	}

	closedCount := 0
	escalatedCount := 0
	ackCount := 0
	for _, task := range tasks {
		state, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatalf("State(%s): %v", task.ID, err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatalf("GetObservation(%s): %v", task.ID, err)
		}

		switch state {
		case model.StateClosed:
			closedCount++
			branch := model.BranchName(task.ID)
			if task.Target != nil {
				bare := barePaths[task.Target.Owner+"/"+task.Target.Name]
				if !branchIsAncestor(t, bare, "main", branch) {
					t.Errorf("task %s was closed/merged but main in %s does not contain %s", task.ID, task.Target, branch)
				}
			}
		case model.StateAwaitingReply:
			if obs != nil && obs.CompletedAt != nil {
				ackCount++
			} else {
				escalatedCount++
			}
		case model.StateCompleted:
			if obs != nil && (obs.MergeQueueState == "merge_failed" || obs.MergeQueueBlockedAt != nil) && !obs.RepairInFlight() {
				escalatedCount++
			} else {
				t.Errorf("task %s left in un-settled completed state: obs=%+v", task.ID, obs)
			}
		default:
			t.Errorf("task %s left in non-terminal state %q after draining", task.ID, state)
		}
	}

	t.Logf("merge liveness results: total_tasks=%d closed_merged=%d escalated=%d awaiting_ack=%d",
		totalTasks, closedCount, escalatedCount, ackCount)
	t.Logf("metrics: initial_pushes=%d (conflicts=%d clean=%d ci_fail=%d) repairs=%d (resolved=%d failed=%d) main_commits=%d cycles=%d",
		metrics.initialPushes, metrics.conflictsInjected, metrics.cleanEdits, metrics.ciFailures,
		metrics.repairsDispatched, metrics.repairsResolved, metrics.repairsFailed,
		metrics.mainCommitsPushed, metrics.cyclesDriven,
	)

	// Ensure git fsck passes on all bare repositories
	for _, repo := range repos {
		bare := barePaths[repo.Owner+"/"+repo.Name]
		cmd := exec.Command("git", "--git-dir", bare, "fsck", "--full")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fsck failed on %s: %s (%v)", bare, string(out), err)
		}
	}

	if closedCount == 0 {
		t.Error("no tasks were successfully merged into main")
	}
	if metrics.conflictsInjected == 0 {
		t.Error("no merge conflicts were injected during the run")
	}
	if metrics.repairsDispatched == 0 {
		t.Error("no repair dispatches were triggered during the run")
	}
}

// TestMergeLivenessStressEndToEnd is the deterministic, fast integration test
// that runs during normal `go test ./...` suites.
func TestMergeLivenessStressEndToEnd(t *testing.T) {
	t.Parallel()
	runMergeLivenessStressEngine(t, mergeStressConfig{
		slots:        4,
		repos:        2,
		totalTasks:   16,
		rounds:       30,
		duration:     0, // bounded by rounds
		drainTimeout: 90 * time.Second,
		seed:         [2]uint64{329, 2026},
	})
}

// TestMergeStressSustained is the long-running stress suite for nightly runs
// or manual invocation via `make merge-stress` or `GRAIN_MERGE_STRESS=1`.
func TestMergeStressSustained(t *testing.T) {
	t.Parallel()
	if os.Getenv("GRAIN_MERGE_STRESS") == "" && os.Getenv("GRAIN_LOAD_TEST") == "" {
		t.Skip("set GRAIN_MERGE_STRESS=1 to run the sustained merge liveness stress test -- see merge_stress_test.go")
	}

	cfg := mergeStressConfig{
		slots:        8,
		repos:        3,
		totalTasks:   60,
		rounds:       1000,
		duration:     20 * time.Minute,
		drainTimeout: 5 * time.Minute,
		seed:         [2]uint64{uint64(time.Now().UnixNano()), uint64(os.Getpid())},
	}

	if v := os.Getenv("GRAIN_MERGE_STRESS_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.slots = n
		}
	}
	if v := os.Getenv("GRAIN_MERGE_STRESS_REPOS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.repos = n
		}
	}
	if v := os.Getenv("GRAIN_MERGE_STRESS_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.totalTasks = n
		}
	}
	if v := os.Getenv("GRAIN_MERGE_STRESS_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.duration = d
		}
	}
	if v := os.Getenv("GRAIN_MERGE_STRESS_DRAIN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.drainTimeout = d
		}
	}
	if v := os.Getenv("GRAIN_MERGE_STRESS_SEED"); v != "" {
		parts := strings.Split(v, ",")
		if len(parts) == 2 {
			s1, err1 := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
			s2, err2 := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
			if err1 == nil && err2 == nil {
				cfg.seed = [2]uint64{s1, s2}
			}
		}
	}

	runMergeLivenessStressEngine(t, cfg)
}
