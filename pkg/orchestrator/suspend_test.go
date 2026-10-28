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

// The task-shaped half of the suspended session (model.SuspendedSession):
// RunDispatch records the conversation each run leaves behind, and hands
// it to the next run of the same task so the agent carries on rather than
// working the task out from nothing.
//
// What a framework does with the token once it has it is
// agent/claude's own business and its own tests (that package's
// suspend_test.go). What these pin is the state on the task: when it is
// written, when it is cleared, and when it is deliberately left alone.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// claudeConfig is a deployment running the one framework whose sessions
// outlive a run, which is what makes a recorded session resumable at all
// (model.ResumeTokenFor compares the two).
func claudeConfig() orchestrator.Config {
	return orchestrator.Config{AgentFramework: model.AgentFrameworkClaude}
}

func observationOf(t *testing.T, ctx context.Context, store *model.Store, taskID string) *model.Observation {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatalf("reading %s's observation: %v", taskID, err)
	}
	return obs
}

// A run that names a session leaves it suspended on the task, whoever
// ended it and however: the point of the record is the run that was cut
// off mid-thought, and that run comes back as a partial result beside an
// error.
func TestRunDispatchSuspendsTheSessionAFailedRunLeftBehind(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{
				FinalText:        "half way through",
				ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
				ResumableSession: "sess-1",
			},
			errors.New("the agent CLI died")
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, claudeConfig(),
		*task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch err = nil, want the run's own failure")
	}

	obs := observationOf(t, ctx, store, "t1")
	if obs == nil || obs.SuspendedSession == nil {
		t.Fatalf("the task has no suspended session, so its next run starts from nothing: %+v", obs)
	}
	got := obs.SuspendedSession
	if got.Token != "sess-1" {
		t.Errorf("Token = %q, want sess-1", got.Token)
	}
	if got.Framework != model.AgentFrameworkClaude {
		t.Errorf("Framework = %q, want claude -- a token only means anything to the CLI that wrote it", got.Framework)
	}
	if got.RunID != "r1" {
		t.Errorf("RunID = %q, want r1 -- which attempt this conversation belongs to", got.RunID)
	}
	if got.At.IsZero() {
		t.Error("At is zero, so nothing says how old this conversation is")
	}
}

// And the next dispatch is handed it, which is the whole point: the
// agent picks the conversation up instead of starting the task again.
func TestRunDispatchResumesTheSessionTheLastRunSuspended(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if err := store.SetSuspendedSession(ctx, "t1", &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-1", RunID: "r1", At: baseTime,
	}, baseTime); err != nil {
		t.Fatalf("suspending a session: %v", err)
	}
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var resumed string
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		resumed = cfg.ResumeSession
		return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-1"}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, claudeConfig(),
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if resumed != "sess-1" {
		t.Errorf("the run was told to resume %q, want sess-1", resumed)
	}
}

// A session recorded by one framework is not handed to another. The
// token is an id in one CLI's own store, so a task whose framework has
// changed underneath it must start clean rather than spend an exec being
// refused.
func TestRunDispatchDoesNotResumeAnotherFrameworksSession(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if err := store.SetSuspendedSession(ctx, "t1", &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-1", RunID: "r1", At: baseTime,
	}, baseTime); err != nil {
		t.Fatalf("suspending a session: %v", err)
	}
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var resumed string
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		resumed = cfg.ResumeSession
		return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{{Name: "run_command"}}}, nil
	})
	cfg := orchestrator.Config{AgentFramework: model.AgentFrameworkCodex}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg,
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if resumed != "" {
		t.Errorf("a codex run was told to resume %q, which is a claude session id", resumed)
	}
}

// A run whose framework names no session clears whatever was there: the
// record is the conversation this task's *last* run ended in, and a
// token from two runs ago is one the framework has already declined to
// name again.
func TestRunDispatchClearsASessionTheLatestRunDidNotName(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if err := store.SetSuspendedSession(ctx, "t1", &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-1", RunID: "r1", At: baseTime,
	}, baseTime); err != nil {
		t.Fatalf("suspending a session: %v", err)
	}
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{{Name: "run_command"}}}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, claudeConfig(),
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if obs := observationOf(t, ctx, store, "t1"); obs != nil && obs.SuspendedSession != nil {
		t.Errorf("the task still carries %+v, which no run of it is in anymore", obs.SuspendedSession)
	}
}

// A run that never reached its agent says nothing about the session an
// earlier run left: a clone that would not clone has spoken to no model,
// and a clearing write from it would throw away hours of thinking over a
// transient git error.
func TestASetupFailureLeavesTheSuspendedSessionAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if err := store.SetSuspendedSession(ctx, "t1", &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-1", RunID: "r1", At: baseTime,
	}, baseTime); err != nil {
		t.Fatalf("suspending a session: %v", err)
	}
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		t.Error("the framework ran, so this is not the setup failure the test is about")
		return &agent.Result{}, nil
	})
	// A clone that cannot be made: the task has a target, the deployment
	// has a git proxy, and there is nothing here to run git with -- so
	// RunDispatch fails in preparation and never calls the framework.
	cfg := claudeConfig()
	cfg.GitRemoteBase = "http://127.0.0.1:1/git"
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg,
		*task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch err = nil, want the setup failure")
	}

	obs := observationOf(t, ctx, store, "t1")
	if obs == nil || obs.SuspendedSession == nil || obs.SuspendedSession.Token != "sess-1" {
		t.Errorf("suspended session = %+v, want sess-1 still there -- nothing in this run knew anything about it", obs)
	}
}

// When RunDispatch resumes a suspended session, it supplies a delta
// SuspendedPrompt containing the current task body, new comments, attempt
// history, checkout orientation, placed attachments, and capability prompt
// sections, while omitting standing deployment extensions and already-delivered
// comments (and keeping the full assembled prompt in Prompt for fallback).
func TestRunDispatchSendsDeltaSuspendedPromptOnResume(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")

	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "t1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "OLD-COMMENT-ALREADY-IN-SESSION",
		CreatedAt: baseTime,
	}); err != nil {
		t.Fatalf("adding initial comment: %v", err)
	}

	d1 := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d1, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}
	task.Body = "INITIAL-TASK-BODY-INSTRUCTIONS"

	cfg := claudeConfig()
	cfg.PromptExtension = "STANDING-DEPLOYMENT-EXTENSION"
	cfg.Capabilities = model.NewCapabilityRegistry(&fakeCapability{name: "extra-cap"})

	root1 := t.TempDir()
	fw1 := agentFunc(func(_ context.Context, rc agent.RunConfig) (*agent.Result, error) {
		if rc.SuspendedPrompt != "" {
			t.Errorf("first dispatch SuspendedPrompt = %q, want empty", rc.SuspendedPrompt)
		}
		return &agent.Result{
			FinalText:        "paused for reply",
			ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-1",
		}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw1, cfg,
		*task, d1, mcp.NewSandboxTools(root1), root1, "", nil, baseTime); err != nil {
		t.Fatalf("first RunDispatch: %v", err)
	}

	replyID, err := store.AddComment(ctx, model.Comment{
		TaskID:    "t1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "NEW-FOLLOW-UP-COMMENT-AFTER-SUSPEND",
		CreatedAt: baseTime.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("adding follow-up comment: %v", err)
	}
	if _, err := store.AddAttachment(ctx, model.Attachment{
		TaskID:      "t1",
		CommentID:   &replyID,
		Filename:    "reply-diag.png",
		ContentType: "image/png",
		Content:     []byte("PNGDATA"),
		Size:        7,
		CreatedAt:   baseTime.Add(time.Minute),
	}); err != nil {
		t.Fatalf("adding reply attachment: %v", err)
	}

	// Simulate an operator editing the task body and granting a capability while
	// the task is parked between dispatches.
	task.Body = "UPDATED-TASK-BODY-INSTRUCTIONS"
	task.Grants = []model.Grant{{Capability: "extra-cap", Via: model.GrantByLabel}}

	d2 := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d2, baseTime.Add(2*time.Minute))

	root2 := t.TempDir()
	var gotCfg agent.RunConfig
	fw2 := agentFunc(func(_ context.Context, rc agent.RunConfig) (*agent.Result, error) {
		gotCfg = rc
		return &agent.Result{
			FinalText:        "done",
			ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-1",
		}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw2, cfg,
		*task, d2, mcp.NewSandboxTools(root2), root2, "", nil, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("second RunDispatch: %v", err)
	}

	if gotCfg.ResumeSession != "sess-1" {
		t.Fatalf("ResumeSession = %q, want sess-1", gotCfg.ResumeSession)
	}
	if gotCfg.SuspendedPrompt == "" {
		t.Fatal("SuspendedPrompt is empty on resumed dispatch")
	}
	for _, want := range []string{
		"NEW-FOLLOW-UP-COMMENT-AFTER-SUSPEND",
		"UPDATED-TASK-BODY-INSTRUCTIONS",
		orchestrator.AttachmentsDir + "/1-reply-diag.png",
		"capability extra-cap is ready",
	} {
		if !strings.Contains(gotCfg.SuspendedPrompt, want) {
			t.Errorf("SuspendedPrompt missing %q:\n%s", want, gotCfg.SuspendedPrompt)
		}
		if !strings.Contains(gotCfg.Prompt, want) {
			t.Errorf("fallback Prompt missing %q:\n%s", want, gotCfg.Prompt)
		}
	}
	for _, unwanted := range []string{
		"OLD-COMMENT-ALREADY-IN-SESSION",
		"STANDING-DEPLOYMENT-EXTENSION",
	} {
		if strings.Contains(gotCfg.SuspendedPrompt, unwanted) {
			t.Errorf("SuspendedPrompt repeated %q that is already in the resumed session:\n%s", unwanted, gotCfg.SuspendedPrompt)
		}
		if !strings.Contains(gotCfg.Prompt, unwanted) {
			t.Errorf("fallback Prompt missing %q:\n%s", unwanted, gotCfg.Prompt)
		}
	}
}

// When the first dispatch started with zero comments on the task
// (priorDelivered == nil) and a comment was posted while that run was in flight
// (before SuspendedSession.At was stamped at the end of the run), the resumed
// dispatch must still include that comment in SuspendedPrompt.
func TestRunDispatchDeliversMidRunCommentsWhenFirstRunStartedWithNoComments(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")

	d1 := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d1, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	now := baseTime
	cfg := claudeConfig()
	cfg.Now = func() time.Time { return now }

	fw1 := agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
		// A comment arrives mid-run at baseTime+1m, while the run finishes and
		// stamps SuspendedSession.At at baseTime+5m.
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID:    "t1",
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			Body:      "MID-RUN-COMMENT-BEFORE-SUSPEND-AT",
			CreatedAt: baseTime.Add(time.Minute),
		}); err != nil {
			t.Errorf("adding mid-run comment: %v", err)
		}
		now = baseTime.Add(5 * time.Minute)
		return &agent.Result{
			FinalText:        "paused",
			ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-1",
		}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw1, cfg,
		*task, d1, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("first RunDispatch: %v", err)
	}

	d2 := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	now = baseTime.Add(6 * time.Minute)
	startRun(t, ctx, store, d2, now)

	var gotCfg agent.RunConfig
	fw2 := agentFunc(func(_ context.Context, rc agent.RunConfig) (*agent.Result, error) {
		gotCfg = rc
		return &agent.Result{
			FinalText:        "done",
			ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-1",
		}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw2, cfg,
		*task, d2, nil, t.TempDir(), "", nil, now); err != nil {
		t.Fatalf("second RunDispatch: %v", err)
	}

	if !strings.Contains(gotCfg.SuspendedPrompt, "MID-RUN-COMMENT-BEFORE-SUSPEND-AT") {
		t.Errorf("SuspendedPrompt omitted comment posted before SuspendedSession.At when priorDelivered was nil:\n%s",
			gotCfg.SuspendedPrompt)
	}
}

// If a resumed dispatch fails before completing its turn (for example, during
// capability setup before reaching the agent), DeliveredCommentID must not
// advance past comments that arrived since the previous successful turn, so the
// next resumed dispatch still receives them in SuspendedPrompt.
func TestRunDispatchDoesNotSkipCommentsAcrossFailedResume(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")

	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "t1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "INITIAL-DELIVERED-COMMENT",
		CreatedAt: baseTime,
	}); err != nil {
		t.Fatalf("adding initial comment: %v", err)
	}

	d1 := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d1, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw1 := agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{
			FinalText:        "paused",
			ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-1",
		}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw1, claudeConfig(),
		*task, d1, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("first RunDispatch: %v", err)
	}

	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    "t1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "COMMENT-BEFORE-FAILED-SETUP",
		CreatedAt: baseTime.Add(time.Minute),
	}); err != nil {
		t.Fatalf("adding comment before failed setup: %v", err)
	}

	// Attempt 2 fails during capability resolution before reaching the agent,
	// leaving "sess-1" intact on the task.
	d2 := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d2, baseTime.Add(2*time.Minute))
	taskWithRefused := *task
	taskWithRefused.Grants = []model.Grant{{Capability: "locked", Via: model.GrantByLabel}}
	refusingCfg := claudeConfig()
	refusingCfg.Capabilities = model.NewCapabilityRegistry(&fakeCapability{name: "locked", refuse: "not allowed"})
	if _, err := orchestrator.RunDispatch(ctx, store, fw1, refusingCfg,
		taskWithRefused, d2, nil, t.TempDir(), "", nil, baseTime.Add(2*time.Minute)); err == nil {
		t.Fatal("second RunDispatch succeeded, want capability refusal")
	}

	// Attempt 3 runs after the refused grant is removed and resumes "sess-1";
	// it must still receive COMMENT-BEFORE-FAILED-SETUP in SuspendedPrompt.
	d3 := dispatch.Dispatch{TaskID: "t1", RunID: "r3", Attempt: 3}
	startRun(t, ctx, store, d3, baseTime.Add(3*time.Minute))
	var gotCfg agent.RunConfig
	fw3 := agentFunc(func(_ context.Context, rc agent.RunConfig) (*agent.Result, error) {
		gotCfg = rc
		return &agent.Result{
			FinalText:        "done",
			ToolCalls:        []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-1",
		}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw3, claudeConfig(),
		*task, d3, nil, t.TempDir(), "", nil, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("third RunDispatch: %v", err)
	}

	if gotCfg.ResumeSession != "sess-1" {
		t.Fatalf("ResumeSession = %q, want sess-1", gotCfg.ResumeSession)
	}
	if !strings.Contains(gotCfg.SuspendedPrompt, "COMMENT-BEFORE-FAILED-SETUP") {
		t.Errorf("SuspendedPrompt dropped comment from failed setup dispatch:\n%s", gotCfg.SuspendedPrompt)
	}
	if strings.Contains(gotCfg.SuspendedPrompt, "INITIAL-DELIVERED-COMMENT") {
		t.Errorf("SuspendedPrompt repeated comment already delivered in attempt 1:\n%s", gotCfg.SuspendedPrompt)
	}
}

// fallbackFramework stands in for a run the daemon's pool built on a
// model further down the default list (daemon.BackendFramework): it
// names its framework, its model, and why it is not on the first.
type fallbackFramework struct {
	agentFunc
	framework, model, reason string
}

func (f fallbackFramework) CredentialFramework() string { return f.framework }
func (f fallbackFramework) CredentialModel() string     { return f.model }
func (f fallbackFramework) FallbackReason() string      { return f.reason }

// An interactive task whose last run left an agy session, and whose next
// lands on claude because the Gemini model is paused, starts fresh rather
// than handing claude agy's token, and its conversation says why.
func TestInteractiveRunOnAnotherFrameworkStartsFreshAndSaysWhy(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	if err := store.UpdateTask(ctx, "t1", func(task *model.Task) error {
		task.Interactive = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSuspendedSession(ctx, "t1", &model.SuspendedSession{
		Framework: model.AgentFrameworkAntigravity, Token: "agy-sess", RunID: "r1", At: baseTime,
	}, baseTime); err != nil {
		t.Fatalf("suspending a session: %v", err)
	}
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r2", Attempt: 2}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var resumed, suspendedPrompt, prompt string
	fw := fallbackFramework{
		agentFunc: func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			resumed, suspendedPrompt, prompt = cfg.ResumeSession, cfg.SuspendedPrompt, cfg.Prompt
			return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{{Name: "run_command"}},
				ResumableSession: "claude-sess"}, nil
		},
		framework: model.AgentFrameworkClaude, model: "claude-opus",
		reason: "gemini-3-pro is paused until 14:05 UTC",
	}
	// The deployment's own framework is agy; the run is on claude.
	cfg := orchestrator.Config{AgentFramework: model.AgentFrameworkAntigravity}
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	if resumed != "" || suspendedPrompt != "" {
		t.Errorf("a claude run was told to resume %q (delta prompt %d bytes), which is agy's session", resumed, len(suspendedPrompt))
	}
	if !strings.Contains(prompt, "details") {
		t.Errorf("the run was not given grain's full prompt: %q", prompt)
	}
	comments, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	var note string
	for _, c := range comments {
		if strings.Contains(c.Body, "This turn ran on") {
			note = c.Body
		}
	}
	if !strings.Contains(note, "claude-opus (claude) because gemini-3-pro is paused until 14:05 UTC") {
		t.Errorf("switch note = %q, want it naming the model and why", note)
	}
	if got := observationOf(t, ctx, store, "t1").SuspendedSession; got == nil || got.Framework != model.AgentFrameworkClaude || got.Token != "claude-sess" {
		t.Errorf("suspended session after the run = %+v, want claude's", got)
	}
}
