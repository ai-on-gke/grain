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

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/durable"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// packageCacheRecordingSandboxes wraps HostSandboxes and records every
// RunInSandbox command issued during package-cache setup so tests can verify
// both the no_repo gocacheprog build skip and warm-sandbox skip on turn 2+.
type packageCacheRecordingSandboxes struct {
	inner orchestrator.Sandboxes

	mu       sync.Mutex
	acquires int
	commands [][]string
}

func (s *packageCacheRecordingSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	s.mu.Lock()
	s.acquires++
	s.mu.Unlock()
	inner, err := s.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	return &packageCacheRecordingSandbox{Sandbox: inner, parent: s}, nil
}

type packageCacheRecordingSandbox struct {
	orchestrator.Sandbox
	parent *packageCacheRecordingSandboxes
}

func (s *packageCacheRecordingSandbox) Root() (string, error) {
	if r, ok := s.Sandbox.(interface{ Root() (string, error) }); ok {
		return r.Root()
	}
	return "", nil
}

func (s *packageCacheRecordingSandbox) ConfigureGitCredentials(ctx context.Context, remoteBase, token string, id mcp.GitIdentity) error {
	return s.Sandbox.ConfigureGitCredentials(ctx, remoteBase, token, id)
}

func (s *packageCacheRecordingSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	s.parent.mu.Lock()
	s.parent.commands = append(s.parent.commands, append([]string(nil), argv...))
	s.parent.mu.Unlock()
	return "", "", 0
}

func (s *packageCacheRecordingSandboxes) CommandCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.commands)
}

func (s *packageCacheRecordingSandboxes) Commands() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.commands))
	copy(out, s.commands)
	return out
}

func TestInteractiveInitialStartupLatencyAndBreakdown(t *testing.T) {
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.SessionSandboxIdleTTL = 30 * time.Minute
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID: model.DirectorSessionID, Title: "Director",
			Capabilities: []string{selfdebug.CapabilityName}, NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	// Stage a baked grain-src directory to exercise parallel prepareCheckoutWithReuse + placeGrainSource.
	grainSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(grainSrc, "README.md"), []byte("grain source\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sbx := &packageCacheRecordingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	var firstTurnAt time.Time
	ag := &recordingSessionAgent{
		onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
			for _, tool := range cfg.Tools {
				if tool.Name == "read_file" {
					_ = tool.Handler(ctx, map[string]any{"path": "grain-src/README.md"})
					break
				}
			}
			firstTurnAt = time.Now()
			return &agent.Result{
				FinalText:        fmt.Sprintf("answer %d", turn),
				Transcript:       fmt.Sprintf("answer %d", turn),
				ResumableSession: "sess-startup-1",
			}, nil
		},
	}
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  orchestrator.StaticFramework(ag),
		MaxWorkers: 2,
		Config: orchestrator.Config{
			StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
			Capabilities:          model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
			GrainSourceDir:        grainSrc,
			PackageCache:          func(context.Context) string { return "http://10.9.0.1:41234" },
			SessionSandboxIdleTTL: 30 * time.Minute,
		},
	}

	startWall := time.Now()
	t1 := startWall.UTC()
	replyToSession(t, store, director.ID, t1)
	if err := orchestrator.RunDispatchCycle(ctx, deps, t1.Add(10*time.Millisecond)); err != nil {
		t.Fatalf("RunDispatchCycle: %v", err)
	}
	startupElapsed := firstTurnAt.Sub(startWall)
	t.Logf("initial interactive startup (message -> RunDispatchCycle -> agent first tool call): %v", startupElapsed)
	if startupElapsed >= time.Second {
		t.Errorf("initial interactive startup in local sandbox took %v, want < 1s (< 45s production ceiling)", startupElapsed)
	}

	// Verify that no_repo interactive sessions pass GRAIN_SKIP_GOCACHEPROG_BUILD=1
	// so `go build` of grain-gocacheprog.go is skipped during ConfigurePackageCache.
	foundSkipEnv := false
	for _, argv := range sbx.Commands() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "GRAIN_SKIP_GOCACHEPROG_BUILD=1") {
			foundSkipEnv = true
			break
		}
	}
	if !foundSkipEnv {
		t.Errorf("expected GRAIN_SKIP_GOCACHEPROG_BUILD=1 in ConfigurePackageCache commands for no_repo session; got %v", sbx.Commands())
	}

	// Verify per-phase startup timestamps on the recorded Run.
	runs, err := store.Runs(ctx, director.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs(%s) = %v, %v; want 1 run", director.ID, runs, err)
	}
	r := runs[0]
	if r.QueuedAt == nil || r.SandboxAcquiredAt == nil || r.SandboxPreparedAt == nil || r.AgentStartedAt == nil || r.FirstTurnAt == nil {
		t.Fatalf("run startup timestamps incomplete: QueuedAt=%v SandboxAcquiredAt=%v SandboxPreparedAt=%v AgentStartedAt=%v FirstTurnAt=%v",
			r.QueuedAt, r.SandboxAcquiredAt, r.SandboxPreparedAt, r.AgentStartedAt, r.FirstTurnAt)
	}
	if r.StartedAt.Before(*r.QueuedAt) {
		t.Errorf("StartedAt (%v) < QueuedAt (%v)", r.StartedAt, *r.QueuedAt)
	}
	if r.SandboxAcquiredAt.Before(r.StartedAt) {
		t.Errorf("SandboxAcquiredAt (%v) < StartedAt (%v)", *r.SandboxAcquiredAt, r.StartedAt)
	}
	if r.SandboxPreparedAt.Before(*r.SandboxAcquiredAt) {
		t.Errorf("SandboxPreparedAt (%v) < SandboxAcquiredAt (%v)", *r.SandboxPreparedAt, *r.SandboxAcquiredAt)
	}
	if r.FirstTurnAt.Before(*r.AgentStartedAt) {
		t.Errorf("FirstTurnAt (%v) < AgentStartedAt (%v)", *r.FirstTurnAt, *r.AgentStartedAt)
	}

	// Verify metrics.Compute separates InteractiveStartup from BatchStartup.
	taskTimings, err := store.TaskTimings(ctx)
	if err != nil {
		t.Fatalf("store.TaskTimings: %v", err)
	}
	runTimings, err := store.RunTimings(ctx)
	if err != nil {
		t.Fatalf("store.RunTimings: %v", err)
	}
	rep := metrics.Compute(metrics.Input{
		Window: metrics.Window{Since: t1.Add(-time.Hour), Until: t1.Add(time.Hour)},
		Tasks:  taskTimings,
		Runs:   runTimings,
	})
	if rep.Latency.InteractiveStartup.StartedToFirstTurn.N != 1 {
		t.Errorf("InteractiveStartup.StartedToFirstTurn.N = %d, want 1", rep.Latency.InteractiveStartup.StartedToFirstTurn.N)
	}
	if rep.Latency.InteractiveStartup.QueuedToDispatched.N != 1 || rep.Latency.InteractiveStartup.SandboxAcquire.N != 1 ||
		rep.Latency.InteractiveStartup.PrepareSandbox.N != 1 || rep.Latency.InteractiveStartup.AgentLaunchToFirstTurn.N != 1 {
		t.Errorf("InteractiveStartup breakdown missing phases: %+v", rep.Latency.InteractiveStartup)
	}
	if rep.Latency.BatchStartup.StartedToFirstTurn.N != 0 {
		t.Errorf("BatchStartup.StartedToFirstTurn.N = %d, want 0 for interactive-only store", rep.Latency.BatchStartup.StartedToFirstTurn.N)
	}
}

func TestInteractiveSubsequentTurnLatencyAndWarmReuse(t *testing.T) {
	t.Run("across-run warm sandbox reuse skips PrepareSandbox and completes under 250ms", func(t *testing.T) {
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{selfdebug.CapabilityName}, NetworkAccess: model.NetworkOpen,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		fakeGrainBin := filepath.Join(t.TempDir(), "grain-sandbox")
		if err := os.WriteFile(fakeGrainBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		sbx := &packageCacheRecordingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
		var turnStartAt time.Time
		var turnFirstToolAt time.Time
		ag := &recordingSessionAgent{
			onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
				if len(cfg.Tools) > 0 {
					_ = cfg.Tools[0].Handler(ctx, map[string]any{"path": "."})
				}
				turnFirstToolAt = time.Now()
				return &agent.Result{
					FinalText:        fmt.Sprintf("answer %d", turn),
					Transcript:       fmt.Sprintf("answer %d", turn),
					ResumableSession: "sess-warm-1",
				}, nil
			},
		}
		deps := orchestrator.Deps{
			Store:      store,
			Sandboxes:  sbx,
			Framework:  orchestrator.StaticFramework(ag),
			MaxWorkers: 2,
			Config: orchestrator.Config{
				StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities:          model.NewCapabilityRegistry(durable.New(), selfdebug.New()),
				PackageCache:          func(context.Context) string { return "http://10.9.0.1:41234" },
				SandboxGrainBinary:    fakeGrainBin,
				SessionSandboxIdleTTL: 30 * time.Minute,
				WaitForSessionReply:   false,
			},
		}

		// Turn 1: initial cold acquire + prepare.
		t1 := baseTime.Add(time.Minute)
		replyToSession(t, store, director.ID, t1)
		if err := orchestrator.RunDispatchCycle(ctx, deps, t1.Add(time.Second)); err != nil {
			t.Fatalf("turn 1: %v", err)
		}
		cmdsAfterTurn1 := sbx.CommandCount()
		if cmdsAfterTurn1 == 0 {
			t.Fatal("expected ConfigurePackageCache to run on turn 1 cold sandbox")
		}
		binInSandbox := filepath.Join(ag.configs[0].SandboxRoot, strings.TrimPrefix(orchestrator.SandboxGrainPath, "/"))
		if _, err := os.Stat(binInSandbox); err != nil {
			t.Fatalf("Stat(%s) after turn 1: %v", binInSandbox, err)
		}
		// Remove binInSandbox after turn 1 to prove turn 2+ with SandboxReused=true
		// does not re-run placeSandboxGrain.
		if err := os.Remove(binInSandbox); err != nil {
			t.Fatalf("Remove(%s): %v", binInSandbox, err)
		}

		// Turns 2 and 3: warm sandbox reuse via RunDispatchCycle.
		for turn := 2; turn <= 3; turn++ {
			turnTime := t1.Add(time.Duration(turn) * time.Minute)
			turnStartAt = time.Now()
			replyToSession(t, store, director.ID, turnTime)
			if err := orchestrator.RunDispatchCycle(ctx, deps, turnTime.Add(time.Millisecond)); err != nil {
				t.Fatalf("turn %d: %v", turn, err)
			}
			elapsed := turnFirstToolAt.Sub(turnStartAt)
			t.Logf("across-run turn %d latency (message -> warm RunDispatchCycle -> agent tool call): %v", turn, elapsed)
			if elapsed >= time.Second {
				t.Errorf("turn %d warm-reuse latency = %v, want < 1s", turn, elapsed)
			}
			if !ag.configs[turn-1].SandboxReused {
				t.Errorf("turn %d SandboxReused = false, want true", turn)
			}
			if got := sbx.CommandCount(); got != cmdsAfterTurn1 {
				t.Errorf("turn %d ran %d extra ConfigurePackageCache commands on warm sandbox, want 0", turn, got-cmdsAfterTurn1)
			}
			if _, err := os.Stat(binInSandbox); !os.IsNotExist(err) {
				t.Errorf("turn %d re-placed %s on warm sandbox (err=%v)", turn, binInSandbox, err)
			}
		}
	})

	t.Run("in-process subsequent exchanges complete under 250ms", func(t *testing.T) {
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 2 * time.Second
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID: model.DirectorSessionID, Title: "Director",
				Capabilities: []string{}, NetworkAccess: model.NetworkOpen,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		sbx := orchestrator.NewHostSandboxes(t.TempDir())
		var msgSentAt [4]time.Time
		var turnReachedAt [4]time.Time
		ag := &recordingSessionAgent{
			onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
				turnReachedAt[turn] = time.Now()
				if turn < 3 {
					go func(nextTurn int) {
						time.Sleep(15 * time.Millisecond)
						msgSentAt[nextTurn] = time.Now()
						_, _ = store.AddComment(ctx, model.Comment{
							TaskID:    director.ID,
							Author:    aliceSays,
							Body:      fmt.Sprintf("follow-up %d", nextTurn),
							CreatedAt: baseTime.Add(time.Duration(nextTurn) * time.Minute),
						})
					}(turn + 1)
				}
				return &agent.Result{
					FinalText:        fmt.Sprintf("answer %d", turn),
					Transcript:       fmt.Sprintf("answer %d", turn),
					ResumableSession: "sess-inproc-1",
				}, nil
			},
		}
		deps := orchestrator.Deps{
			Store:      store,
			Sandboxes:  sbx,
			Framework:  orchestrator.StaticFramework(ag),
			MaxWorkers: 1,
			Config: orchestrator.Config{
				StateRepo:             func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities:          model.NewCapabilityRegistry(durable.New()),
				SessionSandboxIdleTTL: 300 * time.Millisecond,
				WaitForSessionReply:   true,
				CancelPollInterval:    5 * time.Millisecond,
			},
		}

		t1 := baseTime.Add(time.Minute)
		replyToSession(t, store, director.ID, t1)
		if err := orchestrator.RunDispatchCycle(ctx, deps, t1.Add(time.Second)); err != nil {
			t.Fatalf("RunDispatchCycle: %v", err)
		}
		for turn := 2; turn <= 3; turn++ {
			if turnReachedAt[turn].IsZero() {
				t.Fatalf("turn %d was never reached", turn)
			}
			d := turnReachedAt[turn].Sub(msgSentAt[turn])
			t.Logf("in-process turn %d latency (store.AddComment -> agent turn %d): %v", turn, turn, d)
			if d >= 250*time.Millisecond {
				t.Errorf("in-process turn %d latency = %v, want < 250ms (< 1s ceiling)", turn, d)
			}
		}
	})
}
