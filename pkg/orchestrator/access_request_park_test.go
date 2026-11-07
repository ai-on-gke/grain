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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestProcessResultParksOnAnAccessRequest is request_access's half of
// the parking contract: the task parks as it does on a question, the
// conversation carries the run's reason and every item, and the request
// is recorded beside the park -- with its summary on the comment the
// inbox shows -- so the UI offers Grant and Deny rather than a reply box.
func TestProcessResultParksOnAnAccessRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	result := toolResult(agent.ToolCall{
		Name: "request_access",
		Arguments: map[string]any{
			"summary": "Grant write to acme/lib so the fix lands in both",
			"reason":  "the bug is in the shared library",
			"requests": []any{
				map[string]any{"kind": "write", "repo": "acme/lib", "base": "dev"},
				map[string]any{"kind": "grant", "capability": "gcp#gcp-key", "with": map[string]any{"permission_set": "reader"}},
			},
		},
	})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateAwaitingReply {
		t.Fatalf("state = %q (%v), want awaiting_reply", st, err)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("conversation = %+v, want the relayed request", comments)
	}
	for _, want := range []string{"the bug is in the shared library", "write acme/lib (branch dev)", "gcp#gcp-key", "Grant or deny"} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("relayed request does not say %q:\n%s", want, comments[0].Body)
		}
	}
	if comments[0].Summary != "Grant write to acme/lib so the fix lands in both" {
		t.Errorf("summary = %q, want the run's own", comments[0].Summary)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID == nil || *obs.PendingQuestionCommentID != comments[0].ID {
		t.Fatalf("pending question = %v, want the request's comment", obs.PendingQuestionCommentID)
	}
	if obs.PendingAccess == nil || len(obs.PendingAccess.Items) != 2 || obs.PendingAccess.Items[1].With["permission_set"] != "reader" {
		t.Fatalf("pending access = %+v, want both items with their arguments", obs.PendingAccess)
	}
	if obs.PendingSecret != "" {
		t.Errorf("pending secret = %q, want none: this is not a secret request", obs.PendingSecret)
	}
}

// TestPersistentSessionTurnLoopStopsOnAccessRequest verifies §6 item 3:
// both persistent-session turn loops -- the in-process continuation loop
// (continueSessionTurns, WaitForSessionReply: true) and a KeepRunning
// session across orchestrator cycles -- stop on request_access rather than
// spinning into another turn, because capabilities and repo reach are fixed
// per RunDispatch and only take effect once a person grants the request.
func TestPersistentSessionTurnLoopStopsOnAccessRequest(t *testing.T) {
	t.Parallel()

	t.Run("in-process continuation loop", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.SessionSandboxIdleTTL = 30 * time.Minute
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID:            model.DirectorSessionID,
				Title:         "Director",
				Capabilities:  []string{},
				NetworkAccess: model.NetworkNone,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		sbx := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
		ag := &recordingSessionAgent{
			onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
				if turn == 1 {
					// Queue a second human message so turn 2 runs in-process inside the same dispatch.
					go func() {
						time.Sleep(40 * time.Millisecond)
						_, _ = store.AddComment(ctx, model.Comment{
							TaskID:    director.ID,
							Author:    aliceSays,
							Body:      "please inspect acme/private-lib",
							CreatedAt: baseTime.Add(2 * time.Minute),
						})
					}()
					return &agent.Result{
						FinalText:        fmt.Sprintf("turn %d ack", turn),
						Transcript:       fmt.Sprintf("turn %d ack", turn),
						ResumableSession: "sess-access-park",
					}, nil
				}
				// Turn 2 calls request_access while ResumableSession is active;
				// continueSessionTurns must break immediately instead of waiting out
				// the 30s SessionSandboxIdleTTL or continuing in-process.
				return &agent.Result{
					FinalText:        "asking for access",
					Transcript:       "asking for access",
					ResumableSession: "sess-access-park",
					ToolCalls: []agent.ToolCall{{
						Name: "request_access",
						Arguments: map[string]any{
							"summary": "Read acme/private-lib to inspect the regression",
							"reason":  "Need read access to acme/private-lib.",
							"requests": []any{
								map[string]any{"kind": "read", "repo": "acme/private-lib"},
							},
						},
					}},
				}, nil
			},
		}

		deps := orchestrator.Deps{
			Store:      store,
			Sandboxes:  sbx,
			Framework:  orchestrator.StaticFramework(ag),
			MaxWorkers: 1,
			Config: orchestrator.Config{
				StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities: orchestrator.SessionTestRegistry(),
				// Long enough that waiting it out is unmistakable next to
				// the elapsed bound below, which has to leave room for a
				// -race build of the whole package running in parallel on
				// a loaded CI runner (two turns take ~2s there unloaded).
				SessionSandboxIdleTTL: 30 * time.Second,
				WaitForSessionReply:   true,
				CancelPollInterval:    20 * time.Millisecond,
			},
		}

		t1 := baseTime.Add(time.Minute)
		replyToSession(t, store, director.ID, t1)
		start := time.Now()
		if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
		if elapsed := time.Since(start); elapsed >= 15*time.Second {
			t.Fatalf("RunCycle took %v, want immediate break on request_access rather than waiting out SessionSandboxIdleTTL", elapsed)
		}
		if len(ag.configs) != 2 {
			t.Fatalf("framework.Run calls = %d, want 2 (turn 1 + turn 2 stopping on request_access)", len(ag.configs))
		}
		st, err := store.State(ctx, director.ID)
		if err != nil || st != model.StateAwaitingReply {
			t.Fatalf("state = %q (%v), want awaiting_reply", st, err)
		}
		obs, err := store.GetObservation(ctx, director.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.PendingAccess == nil || len(obs.PendingAccess.Items) != 1 || obs.PendingAccess.Items[0].Repo != "acme/private-lib" {
			t.Fatalf("PendingAccess = %+v, want read acme/private-lib", obs.PendingAccess)
		}
	})

	t.Run("keep-running session across cycles", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		settings(t, store, func(cfg *model.Config) {
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID:            model.DirectorSessionID,
				Title:         "Director",
				Capabilities:  []string{},
				NetworkAccess: model.NetworkNone,
				KeepRunning:   true,
			}}
		})
		runSessions(t, store, baseTime)
		director := sessionTasks(t, store)[0]

		ag := &recordingSessionAgent{
			onTurn: func(turn int, cfg agent.RunConfig) (*agent.Result, error) {
				return &agent.Result{
					FinalText:        "asking for access",
					Transcript:       "asking for access",
					ResumableSession: "sess-keep-running",
					ToolCalls: []agent.ToolCall{{
						Name: "request_access",
						Arguments: map[string]any{
							"summary": "Open network access for external check",
							"reason":  "Need outbound HTTPS.",
							"requests": []any{
								map[string]any{"kind": "network", "access": "open"},
							},
						},
					}},
				}, nil
			},
		}
		deps := orchestrator.Deps{
			Store:      store,
			Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
			Framework:  orchestrator.StaticFramework(ag),
			MaxWorkers: 1,
			Config: orchestrator.Config{
				StateRepo:    func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
				Capabilities: orchestrator.SessionTestRegistry(),
			},
		}

		t1 := baseTime.Add(time.Minute)
		replyToSession(t, store, director.ID, t1)
		if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Second)); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
		// Run two more cycles: KeepRunning must stay parked on PendingAccess, not spin!
		for i := 2; i <= 3; i++ {
			if err := orchestrator.RunCycle(ctx, deps, t1.Add(time.Duration(i)*time.Minute)); err != nil {
				t.Fatalf("cycle %d: %v", i, err)
			}
		}
		if len(ag.configs) != 1 {
			t.Fatalf("framework.Run calls across 3 cycles = %d, want 1 (parked on request_access, not spinning)", len(ag.configs))
		}
		if st, _ := store.State(ctx, director.ID); st != model.StateAwaitingReply {
			t.Fatalf("state = %q, want awaiting_reply", st)
		}
	})
}

// TestProcessResultTargetRepoWithNilGitHubClientCompletesCleanly guards
// salvagePushedBranch when deps.GitHub is nil (e.g., git-only or
// consolerestserver runs): finishing a repo-targeted task with comment_on_issue
// must mark the task completed rather than panicking on client.BranchExists.
func TestProcessResultTargetRepoWithNilGitHubClientCompletesCleanly(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := filedTask(t, ctx, store, "t-nil-gh", model.RepoRef{Owner: "acme", Name: "widgets"})
	result := toolResult(agent.ToolCall{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "Completed verification without GitHub API."},
	})
	if err := orchestrator.ProcessResult(ctx, store, nil, task, result, "t-nil-gh-1", baseTime); err != nil {
		t.Fatalf("ProcessResult with nil GitHub client: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.CompletedAt == nil {
		t.Fatalf("Observation = %+v, want CompletedAt set", obs)
	}
}
