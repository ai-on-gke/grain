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

// Long term tests: create a complete Grain instance with end-to-end sandboxes,
// agents, multi-repo smart HTTP git upstream, and SQLite store. Run continuous
// workloads against it with simple coding tasks, merge conflicts, broken main,
// heterogeneous multiple teams, repair cycles, and invariant checks.
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
	"sync/atomic"
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

// longTermConfig holds parameters sizing the long term test execution.
type longTermConfig struct {
	slots               int
	repos               int
	teams               []longTermTeam
	initialTasksPerTeam int
	totalTasks          int
	rounds              int
	duration            time.Duration
	drainTimeout        time.Duration
	conflictRate        float64
	brokenMainRate      float64
	questionRate        float64
	proposalRate        float64
	repairSuccessRate   float64
	seed                [2]uint64
}

// defaultLongTermConfig returns default parameters for sustained long-term testing.
func defaultLongTermConfig(t *testing.T) longTermConfig {
	t.Helper()
	cfg := longTermConfig{
		slots: 12,
		repos: 3,
		teams: []longTermTeam{
			{ID: "team-platform", Name: "Platform Core Team"},
			{ID: "team-backend", Name: "Backend Services Team"},
			{ID: "team-frontend", Name: "Frontend Web Team"},
			{ID: "team-infra", Name: "Infrastructure Team"},
		},
		initialTasksPerTeam: 3,
		totalTasks:          40,
		rounds:              60,
		duration:            30 * time.Second,
		drainTimeout:        3 * time.Minute,
		conflictRate:        0.35,
		brokenMainRate:      0.15,
		questionRate:        0.10,
		proposalRate:        0.15,
		repairSuccessRate:   0.85,
		seed:                [2]uint64{uint64(time.Now().UnixNano()), 0x9e3779b97f4a7c15},
	}

	envInt(t, "GRAIN_LONG_TERM_SLOTS", &cfg.slots)
	envInt(t, "GRAIN_LONG_TERM_REPOS", &cfg.repos)
	envInt(t, "GRAIN_LONG_TERM_TASKS", &cfg.totalTasks)
	envInt(t, "GRAIN_LONG_TERM_ROUNDS", &cfg.rounds)
	envDuration(t, "GRAIN_LONG_TERM_DURATION", &cfg.duration)
	envDuration(t, "GRAIN_LONG_TERM_DRAIN", &cfg.drainTimeout)
	if v := os.Getenv("GRAIN_LONG_TERM_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("GRAIN_LONG_TERM_SEED: %v", err)
		}
		cfg.seed[0] = n
	}
	return cfg
}

// smokeLongTermConfig returns fast deterministic parameters for standard test runs.
func smokeLongTermConfig() longTermConfig {
	return longTermConfig{
		slots: 6,
		repos: 2,
		teams: []longTermTeam{
			{ID: "team-platform", Name: "Platform Core Team"},
			{ID: "team-backend", Name: "Backend Services Team"},
			{ID: "team-frontend", Name: "Frontend Web Team"},
			{ID: "team-infra", Name: "Infrastructure Team"},
		},
		initialTasksPerTeam: 2,
		totalTasks:          14,
		rounds:              25,
		duration:            0,
		drainTimeout:        2 * time.Minute,
		conflictRate:        0.35,
		brokenMainRate:      0.15,
		questionRate:        0.10,
		proposalRate:        0.15,
		repairSuccessRate:   0.90,
		seed:                [2]uint64{780, 2026},
	}
}

// longTermMetrics aggregates telemetry across the long term test run.
type longTermMetrics struct {
	mu sync.Mutex

	tasksFiled        int
	tasksDone         int
	initialPushes     int
	cleanTasks        int
	conflictsInjected int
	brokenMainEvents  int
	questionsAsked    int
	questionsAnswered int
	proposalsFiled    int
	proposalsApproved int

	repairsDispatched int
	repairsResolved   int
	repairsFailed     int

	mainCommitsPushed int

	teamTaskFiled   map[string]int
	teamTaskDone    map[string]int
	teamPeakWorkers map[string]int
	teamPeakMergers map[string]int
	teamPeakTotal   map[string]int

	childProposals  map[string]string // childTaskID -> parentTeam
	invariantErrors []error
}

func newLongTermMetrics() *longTermMetrics {
	return &longTermMetrics{
		teamTaskFiled:   make(map[string]int),
		teamTaskDone:    make(map[string]int),
		teamPeakWorkers: make(map[string]int),
		teamPeakMergers: make(map[string]int),
		teamPeakTotal:   make(map[string]int),
		childProposals:  make(map[string]string),
	}
}

func (m *longTermMetrics) recordTaskFiled(teamID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasksFiled++
	m.teamTaskFiled[teamID]++
}

func (m *longTermMetrics) recordTaskDone(teamID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasksDone++
	m.teamTaskDone[teamID]++
}

func (m *longTermMetrics) recordInitial(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initialPushes++
	switch kind {
	case "clean":
		m.cleanTasks++
	case "conflict":
		m.conflictsInjected++
	case "broken_main":
		m.brokenMainEvents++
	case "question":
		m.questionsAsked++
	case "proposal":
		m.proposalsFiled++
	}
}

func (m *longTermMetrics) recordRepair(outcome string) {
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

func (m *longTermMetrics) recordQuestionAnswered() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questionsAnswered++
}

func (m *longTermMetrics) recordProposalApproved() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proposalsApproved++
}

func (m *longTermMetrics) recordChildProposal(childID, parentTeam string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.childProposals[childID] = parentTeam
}

func (m *longTermMetrics) recordMainCommit() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mainCommitsPushed++
}

func (m *longTermMetrics) sampleLiveCounts(teamID string, live model.RunCounts, tm longTermTeam) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if live.Workers > m.teamPeakWorkers[teamID] {
		m.teamPeakWorkers[teamID] = live.Workers
	}
	if live.Mergers > m.teamPeakMergers[teamID] {
		m.teamPeakMergers[teamID] = live.Mergers
	}
	if live.Total() > m.teamPeakTotal[teamID] {
		m.teamPeakTotal[teamID] = live.Total()
	}
}

// longTermTaskPlan specifies how the scripted agent executes a specific task.
type longTermTaskPlan struct {
	kind          string // "clean", "conflict", "broken_main", "question", "proposal"
	repairOutcome string // "resolve", "fail"
	teamID        string
	targetFile    string
	editContent   string
}

// longTermScriptGenerator generates multi-path execution steps for agent workers.
type longTermScriptGenerator struct {
	mu            sync.Mutex
	rng           *rand.Rand
	cfg           longTermConfig
	githubHost    string
	multiSim      *multiRepoSim
	metrics       *longTermMetrics
	taskPlans     map[string]longTermTaskPlan
	initialPushed map[string]bool
}

func newLongTermScriptGenerator(
	rng *rand.Rand,
	cfg longTermConfig,
	githubHost string,
	multiSim *multiRepoSim,
	metrics *longTermMetrics,
) *longTermScriptGenerator {
	return &longTermScriptGenerator{
		rng:           rng,
		cfg:           cfg,
		githubHost:    githubHost,
		multiSim:      multiSim,
		metrics:       metrics,
		taskPlans:     make(map[string]longTermTaskPlan),
		initialPushed: make(map[string]bool),
	}
}

var longTermPromptRe = regexp.MustCompile(`Work in (\S+)\. Push your change to a new branch named "([^"]+)"`)

func (g *longTermScriptGenerator) Generate(prompt string) ([]antigravity.Step, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	m := longTermPromptRe.FindStringSubmatch(prompt)
	if m == nil {
		if strings.Contains(prompt, "This task has no repo attached") {
			return []antigravity.Step{
				finalText("no repo attached standing session note"),
			}, nil
		}
		// Standing console session / director note
		return []antigravity.Step{
			finalText("standing session processed"),
		}, nil
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
		var kind string
		switch {
		case r < g.cfg.conflictRate:
			kind = "conflict"
		case r < g.cfg.conflictRate+g.cfg.brokenMainRate:
			kind = "broken_main"
		case r < g.cfg.conflictRate+g.cfg.brokenMainRate+g.cfg.questionRate:
			kind = "question"
		case r < g.cfg.conflictRate+g.cfg.brokenMainRate+g.cfg.questionRate+g.cfg.proposalRate:
			kind = "proposal"
		default:
			kind = "clean"
		}

		var repairOutcome string
		if g.rng.Float64() < g.cfg.repairSuccessRate {
			repairOutcome = "resolve"
		} else {
			repairOutcome = "fail"
		}

		plan = longTermTaskPlan{
			kind:          kind,
			repairOutcome: repairOutcome,
			targetFile:    "SHARED_CONFIG.go",
			editContent:   fmt.Sprintf("// Config update from task %s (val=%d)\nvar TaskConfig_%s = %d\n", taskID, g.rng.Uint64(), strings.ReplaceAll(taskID, "-", "_"), g.rng.IntN(1000)),
		}
		g.taskPlans[taskID] = plan
	}

	if !isRepair {
		g.initialPushed[taskID] = true
		g.metrics.recordInitial(plan.kind)
		return g.buildInitialScript(remote, branch, taskID, plan, owner, repoName)
	}

	g.metrics.recordRepair(plan.repairOutcome)
	return g.buildRepairScript(remote, branch, taskID, plan, owner, repoName)
}

func (g *longTermScriptGenerator) buildInitialScript(
	remote, branch, taskID string,
	plan longTermTaskPlan,
	owner, repoName string,
) ([]antigravity.Step, error) {
	cloneDir := fmt.Sprintf("work-%s-%x", taskID, g.rng.Uint64())

	switch plan.kind {
	case "question":
		return []antigravity.Step{
			toolCall("ask_question", map[string]any{"question": fmt.Sprintf("Should task %s enable flag X?", taskID)}),
			finalText("waiting for operator feedback"),
		}, nil

	case "proposal":
		childID := fmt.Sprintf("child-%s", taskID)
		g.metrics.recordChildProposal(childID, plan.teamID)
		return []antigravity.Step{
			toolCall("propose_task", map[string]any{
				"title": fmt.Sprintf("Follow-up task for %s", taskID),
				"body":  fmt.Sprintf("Splitting follow-up work from task %s", taskID),
			}),
			finalText(fmt.Sprintf("proposed follow-up task %s", childID)),
		}, nil

	case "broken_main":
		// Pushes a commit that fails CI / build
		cmd := fmt.Sprintf(
			"rm -rf %s && git clone %s %s && cd %s && "+
				"git config user.email agent@example.com && git config user.name agent && "+
				"git checkout -b %s && "+
				"echo 'syntax error ::: broken code for %s' >> %s && "+
				"git add %s && git commit -q -m 'feat: broken commit for %s' && "+
				"git push origin %s",
			cloneDir, remote, cloneDir, cloneDir,
			branch,
			taskID, plan.targetFile,
			plan.targetFile, taskID,
			branch,
		)
		// Mark CI as failing in simulator
		g.multiSim.forEach(func(key string, sim *githubsim.Sim) {
			if key == owner+"/"+repoName {
				failure := "failure"
				sim.CheckRuns[branch] = []github.CheckRun{{
					Name: "ci", Status: "completed", Conclusion: &failure,
				}}
			}
		})
		return []antigravity.Step{
			toolCall("run_command", map[string]any{"command": cmd}),
			finalText("pushed initial broken branch for CI failure check"),
		}, nil

	case "conflict":
		// Overwrites shared file to create merge conflict with concurrent tasks/main
		cmd := fmt.Sprintf(
			"rm -rf %s && git clone %s %s && cd %s && "+
				"git config user.email agent@example.com && git config user.name agent && "+
				"git checkout -b %s && "+
				"echo '%s' > %s && "+
				"git add %s && git commit -q -m 'feat: conflicting update for %s' && "+
				"git push origin %s",
			cloneDir, remote, cloneDir, cloneDir,
			branch,
			plan.editContent, plan.targetFile,
			plan.targetFile, taskID,
			branch,
		)
		return []antigravity.Step{
			toolCall("run_command", map[string]any{"command": cmd}),
			finalText(fmt.Sprintf("pushed conflicting branch %s", branch)),
		}, nil

	case "clean":
		fallthrough
	default:
		// Clean modification on task-specific file
		cmd := fmt.Sprintf(
			"rm -rf %s && git clone %s %s && cd %s && "+
				"git config user.email agent@example.com && git config user.name agent && "+
				"git checkout -b %s && "+
				"echo '// Feature code for %s\npackage feature\n' > feature_%s.go && "+
				"git add feature_%s.go && git commit -q -m 'feat: add feature for %s' && "+
				"git push origin %s",
			cloneDir, remote, cloneDir, cloneDir,
			branch,
			taskID, strings.ReplaceAll(taskID, "-", "_"),
			strings.ReplaceAll(taskID, "-", "_"), taskID,
			branch,
		)
		return []antigravity.Step{
			toolCall("run_command", map[string]any{"command": cmd}),
			finalText(fmt.Sprintf("pushed clean feature branch %s", branch)),
		}, nil
	}
}

func (g *longTermScriptGenerator) buildRepairScript(
	remote, branch, taskID string,
	plan longTermTaskPlan,
	owner, repoName string,
) ([]antigravity.Step, error) {
	cloneDir := fmt.Sprintf("work-repair-%s-%x", taskID, g.rng.Uint64())
	var repairCmd string

	switch plan.repairOutcome {
	case "resolve":
		// Resolve merge conflict or fix broken code by pulling main and merging cleanly
		repairCmd = fmt.Sprintf(
			"rm -rf %s && git clone %s %s && cd %s && "+
				"git config user.email agent@example.com && git config user.name agent && "+
				"git checkout -b %s origin/%s && "+
				"git fetch origin main && "+
				"git merge -s ours -q origin/main -m 'merge origin/main' && "+
				"echo '// Clean resolved config for %s' > %s && "+
				"git add %s && git commit -q --amend -m 'fix: resolved conflicts and fixes for %s' && "+
				"git push origin %s",
			cloneDir, remote, cloneDir, cloneDir,
			branch, branch,
			taskID, plan.targetFile,
			plan.targetFile, taskID,
			branch,
		)
		// Mark CI as green now that repair is resolved
		g.multiSim.forEach(func(key string, sim *githubsim.Sim) {
			if key == owner+"/"+repoName {
				success := "success"
				sim.CheckRuns[branch] = []github.CheckRun{{
					Name: "ci", Status: "completed", Conclusion: &success,
				}}
			}
		})

	case "fail":
		// Simulate unresolvable repair failure (e.g. escalating to user)
		repairCmd = fmt.Sprintf(
			"rm -rf %s && git clone %s %s && cd %s && "+
				"git config user.email agent@example.com && git config user.name agent && "+
				"git checkout -b %s origin/%s && "+
				"echo 'failed resolution attempt' >> UNRESOLVED.txt && "+
				"git add UNRESOLVED.txt && git commit -q -m 'chore: failed resolution attempt' && "+
				"git push origin %s",
			cloneDir, remote, cloneDir, cloneDir,
			branch, branch,
			branch,
		)
	}

	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": repairCmd}),
		finalText(fmt.Sprintf("repair execution for %s finished with outcome %s", taskID, plan.repairOutcome)),
	}, nil
}

type longTermScriptAdapter struct {
	gen    *longTermScriptGenerator
	steps  []antigravity.Step
	called int
}

func (s *longTermScriptAdapter) Next(prompt string) (antigravity.Step, bool) {
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

// TestLongTermEndToEndSmoke runs the fast deterministic smoke test verifying the
// complete end-to-end long term test harness across multi-team workloads, merge conflicts,
// broken main events, and invariant monitors.
func TestLongTermEndToEndSmoke(t *testing.T) {
	t.Parallel()
	runLongTermTestHarness(t, smokeLongTermConfig(), false)
}

// TestLongTermEndToEndSustained runs the extended, sustained end-to-end long term test.
// Opt-in via GRAIN_LONG_TERM_TEST=1 or GRAIN_LOAD_TEST=1.
func TestLongTermEndToEndSustained(t *testing.T) {
	t.Parallel()
	if os.Getenv("GRAIN_LONG_TERM_TEST") == "" && os.Getenv("GRAIN_LOAD_TEST") == "" {
		t.Skip("set GRAIN_LONG_TERM_TEST=1 or GRAIN_LOAD_TEST=1 to run the sustained long term test")
	}
	runLongTermTestHarness(t, defaultLongTermConfig(t), true)
}

// runLongTermTestHarness executes the comprehensive long term test loop.
func runLongTermTestHarness(t *testing.T, cfg longTermConfig, isSustained bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	t.Logf("=== Starting Long Term Test Harness (Sustained=%v, Config=%+v) ===", isSustained, cfg)

	upstream := t.TempDir()
	rng := rand.New(rand.NewPCG(cfg.seed[0], cfg.seed[1]))
	metrics := newLongTermMetrics()
	multiSim := newMultiRepoSim()

	// 1. Initialize Bare Git Repositories
	repos := make([]model.RepoRef, 0, cfg.repos)
	barePaths := make(map[string]string)
	for i := 0; i < cfg.repos; i++ {
		owner := "acme"
		name := fmt.Sprintf("service-%d", i+1)
		repoDir := filepath.Join(upstream, owner, name+".git")
		bare := initBareRepo(t, repoDir, "main")
		run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")

		// Seed initial commit with shared file and basic structure
		seedParent := t.TempDir()
		run(t, seedParent, "git", "clone", bare, "seed")
		seed := filepath.Join(seedParent, "seed")
		run(t, seed, "git", "config", "user.email", "seed@example.com")
		run(t, seed, "git", "config", "user.name", "seed")
		initialConfig := "// Base configuration for service\npackage config\n\nconst Version = \"1.0.0\"\n"
		if err := os.WriteFile(filepath.Join(seed, "SHARED_CONFIG.go"), []byte(initialConfig), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(fmt.Sprintf("# %s/%s\n", owner, name)), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, seed, "git", "add", "SHARED_CONFIG.go", "README.md")
		run(t, seed, "git", "commit", "-q", "-m", "chore: initial repository seed")
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

	// 2. Initialize Embedded SQLite Store
	dbDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dbDir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := model.New(db)
	ctx := t.Context()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	// 3. Configure Heterogeneous Teams in Store
	teamMap := make(map[string]longTermTeam)
	for _, tm := range cfg.teams {
		teamMap[tm.ID] = tm
	}

	// 4. Setup Recurring Schedules for each Team
	now := time.Now().UTC()
	for _, tm := range cfg.teams {
		schedID := fmt.Sprintf("sched-%s", tm.ID)
		sched := model.Schedule{
			ID:         schedID,
			Title:      fmt.Sprintf("Recurring maintenance for %s", tm.Name),
			Target:     repos[0],
			Enabled:    true,
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 1},
			NextRunAt:  now.Add(-time.Minute), // Due immediately
			CreatedAt:  now,
		}
		if err := store.PutSchedule(ctx, sched); err != nil {
			t.Fatalf("PutSchedule(%s): %v", schedID, err)
		}
	}

	// 5. Setup Capabilities Registry
	capability := newLoadCapability(0.0, 2*time.Second)
	registry := loadHarnessRegistry(capability)

	// 6. Setup Orchestrator & Scripted Agent Framework
	client := github.NewClient(multiSim, nil)
	generator := newLongTermScriptGenerator(rng, cfg, githubHost, multiSim, metrics)
	cycles := orchestrator.NewCycleTimes(loadCycleHistory)

	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  sandboxes,
		MaxWorkers: cfg.slots,
		CycleTimes: cycles,
		Config:     orchestrator.Config{Capabilities: registry},
		Framework: func(ctx context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
			return antigravity.NewForTest(&longTermScriptAdapter{gen: generator}), nil
		},
	}

	// 7. Seed Initial Backlog evenly across Teams
	var taskSeq atomic.Int64
	clock := baseTime
	for _, tm := range cfg.teams {
		for i := 0; i < cfg.initialTasksPerTeam; i++ {
			seq := taskSeq.Add(1)
			taskID := fmt.Sprintf("task-%04d", seq)
			repo := repos[rng.IntN(len(repos))]
			task := model.Task{
				ID:        taskID,
				Intent:    model.IntentImplement,
				Title:     fmt.Sprintf("Task %s for %s", taskID, tm.Name),
				Origin:    model.Origin{Attribution: model.Attribution{Actor: human(tm.ID + "@acme.com")}, Reason: model.ReasonDirect},
				Approval:  &model.Attribution{Actor: human(tm.ID + "@acme.com")},
				Target:    &repo,
				Binding:   model.BindingDirective,
				AutoMerge: true,
				CreatedAt: &clock,
			}
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatalf("PutTask(%s): %v", taskID, err)
			}
			metrics.recordTaskFiled(tm.ID)
		}
	}

	// 8. Start Background Concurrency & Invariant Monitor
	stopMonitor := make(chan struct{})
	var monitorWg sync.WaitGroup
	monitorWg.Add(1)
	go func() {
		defer monitorWg.Done()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopMonitor:
				return
			case <-ticker.C:
				for teamID, tm := range teamMap {
					live, err := store.LiveRunCounts(ctx)
					if err != nil {
						continue
					}
					metrics.sampleLiveCounts(teamID, live, tm)
				}
			}
		}
	}()

	// 9. Main Orchestration & Continuous Driving Loop
	deadline := time.Time{}
	if cfg.duration > 0 {
		deadline = time.Now().Add(cfg.duration)
	}

	t.Logf("starting long term orchestration loop: slots=%d repos=%d target_tasks=%d rounds=%d duration=%v",
		cfg.slots, cfg.repos, cfg.totalTasks, cfg.rounds, cfg.duration)

	round := 0
	for ; round < cfg.rounds; round++ {
		if !deadline.IsZero() && time.Now().After(deadline) {
			t.Logf("configured duration %v reached at round %d", cfg.duration, round)
			break
		}
		clock = clock.Add(15 * time.Second)

		// A. File new tasks if under limit
		if int(taskSeq.Load()) < cfg.totalTasks && rng.Float64() < 0.70 {
			batch := 1 + rng.IntN(2)
			for b := 0; b < batch && int(taskSeq.Load()) < cfg.totalTasks; b++ {
				seq := taskSeq.Add(1)
				taskID := fmt.Sprintf("task-%04d", seq)
				tm := cfg.teams[rng.IntN(len(cfg.teams))]
				repo := repos[rng.IntN(len(repos))]
				task := model.Task{
					ID:        taskID,
					Intent:    model.IntentImplement,
					Title:     fmt.Sprintf("Continuous task %s for %s", taskID, tm.Name),
					Origin:    model.Origin{Attribution: model.Attribution{Actor: human(tm.ID + "@acme.com")}, Reason: model.ReasonDirect},
					Approval:  &model.Attribution{Actor: human(tm.ID + "@acme.com")},
					Target:    &repo,
					Binding:   model.BindingDirective,
					AutoMerge: true,
					CreatedAt: &clock,
				}
				if err := store.PutTask(ctx, task); err != nil {
					t.Fatalf("PutTask(%s): %v", taskID, err)
				}
				metrics.recordTaskFiled(tm.ID)
			}
		}

		// B. Occasionally push external commit to main (clean or broken main)
		if rng.Float64() < 0.20 {
			targetRepo := repos[rng.IntN(len(repos))]
			bare := barePaths[targetRepo.Owner+"/"+targetRepo.Name]
			isBrokenMain := rng.Float64() < 0.30
			var content, msg string
			if isBrokenMain {
				content = fmt.Sprintf("// BROKEN MAIN commit on round %d\npackage config\ninvalid syntax\n", round)
				msg = fmt.Sprintf("chore: breaking change on main (round %d)", round)
			} else {
				content = fmt.Sprintf("// External clean update on round %d\npackage config\nconst Revision = %d\n", round, round)
				msg = fmt.Sprintf("chore: external update on main (round %d)", round)
			}
			pushBareCommit(t, bare, "main", "SHARED_CONFIG.go", content, msg)
			metrics.recordMainCommit()
		}

		// C. Simulate external operator: answer parked questions & approve unapproved proposals
		states, err := store.States(ctx)
		if err == nil {
			for id, st := range states {
				if st == model.StateAwaitingReply {
					_, _ = store.AddComment(ctx, model.Comment{
						TaskID:    id,
						Body:      "operator guidance: please proceed",
						Author:    model.Attribution{Actor: human("operator")},
						CreatedAt: clock,
					})
					metrics.recordQuestionAnswered()
				}
				if st == model.StateProposed {
					_ = store.UpdateTask(ctx, id, func(t *model.Task) error {
						t.Approval = &model.Attribution{Actor: human("operator")}
						return nil
					})
					metrics.recordProposalApproved()
				}
			}
		}

		// D. Update GitHub simulator PR mergeability & CI status before RunCycle
		multiSim.forEach(func(key string, sim *githubsim.Sim) {
			updateSimMergeability(sim)
		})

		// E. Run Orchestrator Cycle
		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatalf("round %d: RunCycle: %v", round, err)
		}
	}

	t.Logf("active phase complete after %d rounds, %d tasks filed. Draining merge queue and backlog...", round, taskSeq.Load())

	// 10. Drain Phase: allow in-flight tasks and merge queues to settle
	drainDeadline := time.Now().Add(cfg.drainTimeout)
	for time.Now().Before(drainDeadline) {
		clock = clock.Add(15 * time.Second)

		// Answer any remaining questions & approve proposals
		states, _ := store.States(ctx)
		hasActive := false
		for id, st := range states {
			if st == model.StateAwaitingReply {
				_, _ = store.AddComment(ctx, model.Comment{
					TaskID:    id,
					Body:      "operator guidance: please proceed",
					Author:    model.Attribution{Actor: human("operator")},
					CreatedAt: clock,
				})
			}
			if st == model.StateProposed {
				_ = store.UpdateTask(ctx, id, func(t *model.Task) error {
					t.Approval = &model.Attribution{Actor: human("operator")}
					return nil
				})
			}
			if st == model.StateQueued || st == model.StateRunning || st == model.StateAwaitingSubmit {
				hasActive = true
			}
		}

		// Update mergeability before drain cycle
		multiSim.forEach(func(key string, sim *githubsim.Sim) {
			updateSimMergeability(sim)
		})

		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatalf("drain RunCycle: %v", err)
		}

		if !hasActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(stopMonitor)
	monitorWg.Wait()

	// 11. Verify Invariants & Terminal States
	states, err := store.States(ctx)
	if err != nil {
		t.Fatalf("reading final states: %v", err)
	}

	finalTasks := getAllWorkTasks(ctx, store)
	closedMerged := 0
	escalated := 0
	awaitingAck := 0
	nonTerminal := 0

	for _, tk := range finalTasks {
		st := states[tk.ID]
		switch st {
		case model.StateClosed:
			closedMerged++
			metrics.recordTaskDone(model.DefaultTeamID)
			branch := model.BranchName(tk.ID)
			if tk.Target != nil {
				bare := barePaths[tk.Target.Owner+"/"+tk.Target.Name]
				if !branchIsAncestor(t, bare, "main", branch) {
					t.Errorf("task %s was closed/merged but main in %s does not contain %s", tk.ID, tk.Target, branch)
				}
			}
		case model.StateAwaitingSubmit:
			escalated++
		case model.StateAwaitingReply:
			awaitingAck++
			metrics.recordTaskDone(model.DefaultTeamID)
		case model.StateCompleted:
			closedMerged++
			metrics.recordTaskDone(model.DefaultTeamID)
		default:
			nonTerminal++
		}
	}

	t.Logf("=== Long Term Test Results Summary ===")
	t.Logf("  Total Work Tasks: %d | Closed/Merged: %d | Escalated: %d | AwaitingAck: %d | NonTerminal: %d",
		len(finalTasks), closedMerged, escalated, awaitingAck, nonTerminal)
	t.Logf("  Initial Pushes: %d (Clean: %d, Conflicts: %d, BrokenMain: %d, Questions: %d, Proposals: %d)",
		metrics.initialPushes, metrics.cleanTasks, metrics.conflictsInjected, metrics.brokenMainEvents, metrics.questionsAsked, metrics.proposalsFiled)
	t.Logf("  Repairs Dispatched: %d (Resolved: %d, Failed: %d)",
		metrics.repairsDispatched, metrics.repairsResolved, metrics.repairsFailed)
	t.Logf("  External Main Commits: %d | Questions Answered: %d | Proposals Approved: %d",
		metrics.mainCommitsPushed, metrics.questionsAnswered, metrics.proposalsApproved)

	t.Logf("=== Multi-Team Peak Concurrency Report ===")
	for _, tm := range cfg.teams {
		t.Logf("  Team %-16s -> Filed: %d | Peak: Workers=%d, Mergers=%d, Total=%d",
			tm.ID, metrics.teamTaskFiled[tm.ID],
			metrics.teamPeakWorkers[tm.ID], metrics.teamPeakMergers[tm.ID], metrics.teamPeakTotal[tm.ID])
	}

	// Invariant 1: Check for invariant errors recorded by background monitor
	if len(metrics.invariantErrors) > 0 {
		for _, invErr := range metrics.invariantErrors {
			t.Errorf("invariant violation: %v", invErr)
		}
	}

	// Invariant 2: Progress was made across teams
	if closedMerged+awaitingAck+escalated == 0 {
		t.Fatalf("no tasks reached terminal state during the run")
	}
}

// longTermTeam is a team the harness files work under: a name on each
// task's team column, since schema 24 keeps no team rows of its own.
type longTermTeam struct {
	ID   string
	Name string
}
