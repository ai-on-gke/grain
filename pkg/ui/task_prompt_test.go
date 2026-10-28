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

package ui_test

// GET /api/tasks/{id}/prompt is the API surface behind the UI's "show
// the full prompt" button (grain/task-91): the whole text
// orchestrator.RunDispatch handed the agent, recorded per run by
// Store.SetRunPrompt. These cover the route's status codes and JSON
// shape together with what Client.TaskPrompt picks out of the store,
// since the interesting part is which attempt's prompt a task with
// several answers with.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// promptResponse mirrors ui.TaskPrompt's wire shape, decoded here rather
// than imported so a rename of the JSON keys shows up as a failing test
// instead of silently still compiling.
type promptResponse struct {
	Prompt  string `json:"prompt"`
	Attempt int    `json:"attempt"`
}

// startRun records one attempt at taskID with the prompt its agent was
// given -- the pair RunDispatch writes (StartRun, then SetRunPrompt just
// before the agent's first turn). prompt "" is an attempt that never
// reached its agent, which records none.
func startRun(t *testing.T, store *model.Store, ctx context.Context, taskID, runID string, attempt int, prompt string) {
	t.Helper()
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: taskID, Sandbox: "s" + runID,
		Attempt: attempt, StartedAt: baseTime.Add(time.Duration(attempt) * time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if prompt == "" {
		return
	}
	if err := store.SetRunPrompt(ctx, runID, prompt); err != nil {
		t.Fatal(err)
	}
}

func TestGetTaskPromptReturnsWhatTheAgentWasGiven(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	task := create(t, client, ctx)

	const prompt = "Fix the thing\n\ndetails\n\nWork in acme/widgets. Push your change to a new branch named \"grain/task-1\"."
	startRun(t, client.Store, ctx, task.ID, "r1", 1, prompt)

	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/prompt", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[promptResponse](t, rec)
	if got.Prompt != prompt {
		t.Fatalf("prompt = %q, want %q", got.Prompt, prompt)
	}
	if got.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1 -- the attempt the prompt belongs to", got.Attempt)
	}
}

// A redispatched task's prompt grows with its conversation, so the
// newest attempt's is the one worth showing: it is what this task looks
// like to an agent now.
func TestGetTaskPromptPrefersTheMostRecentAttempt(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	task := create(t, client, ctx)

	startRun(t, client.Store, ctx, task.ID, "r1", 1, "the first prompt")
	if err := client.Store.FinishRun(ctx, "r1", baseTime.Add(time.Minute), "failed", ""); err != nil {
		t.Fatal(err)
	}
	startRun(t, client.Store, ctx, task.ID, "r2", 2, "the second prompt, with the conversation so far")

	got := decode[promptResponse](t, do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/prompt", ""))
	if got.Prompt != "the second prompt, with the conversation so far" || got.Attempt != 2 {
		t.Fatalf("prompt = %+v, want attempt 2's", got)
	}
}

// An attempt that died in setup (a checkout that would not clone, a
// capability that would not mint) never reached its agent and so
// recorded no prompt. That must not hide the prompt an earlier attempt
// really was given.
func TestGetTaskPromptSkipsAnAttemptThatNeverReachedItsAgent(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	task := create(t, client, ctx)

	startRun(t, client.Store, ctx, task.ID, "r1", 1, "the prompt attempt 1 was given")
	if err := client.Store.FinishRun(ctx, "r1", baseTime.Add(time.Minute), "failed", ""); err != nil {
		t.Fatal(err)
	}
	startRun(t, client.Store, ctx, task.ID, "r2", 2, "")

	got := decode[promptResponse](t, do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/prompt", ""))
	if got.Prompt != "the prompt attempt 1 was given" || got.Attempt != 1 {
		t.Fatalf("prompt = %+v, want attempt 1's, which is the only one there is", got)
	}
}

// A task nothing has been dispatched for is a 200 with an empty prompt,
// not a 404: the task exists, and "nothing has run yet" is an answer the
// frontend renders rather than an error it reports.
func TestGetTaskPromptOfATaskThatHasNeverRunIsEmpty(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	task := create(t, client, context.Background())

	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/prompt", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[promptResponse](t, rec)
	if got.Prompt != "" || got.Attempt != 0 {
		t.Fatalf("prompt = %+v, want nothing recorded", got)
	}
}

func TestGetTaskPromptOfAnUnknownTaskIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/tasks/nosuchtask/prompt", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The prompt's own size travels with it, which is the point of
// measuring it at all: a task's prompt grows with its conversation and
// the growth is invisible until somebody puts a number beside it
// (model.PromptSize, grain/task-268). Bytes and tokens on the prompt
// route, and the same pair on every attempt in the task's detail, so a
// reader can see attempt 10's prompt against attempt 1's without
// fetching ten prompts.
func TestTaskPromptAndAttemptsCarryTheirMeasuredSize(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	task := create(t, client, ctx)

	small := strings.Repeat("a", 4000)
	startRun(t, client.Store, ctx, task.ID, "r1", 1, small)
	if err := client.Store.FinishRun(ctx, "r1", baseTime.Add(time.Minute), "failed", ""); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("b", 40000)
	startRun(t, client.Store, ctx, task.ID, "r2", 2, large)

	var got struct {
		Attempt       int `json:"attempt"`
		Bytes         int `json:"bytes"`
		Tokens        int `json:"tokens"`
		ContextTokens int `json:"contextTokens"`
	}
	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/prompt", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the prompt response: %v", err)
	}
	if got.Bytes != len(large) || got.Tokens != len(large)/model.PromptBytesPerToken {
		t.Errorf("prompt measured %d bytes/%d tokens, want %d/%d",
			got.Bytes, got.Tokens, len(large), len(large)/model.PromptBytesPerToken)
	}
	if got.ContextTokens != model.PromptContextTokens {
		t.Errorf("context window = %d, want %d -- a share is useless without the window it is of",
			got.ContextTokens, model.PromptContextTokens)
	}

	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reading the task's detail: %v", err)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(detail.Attempts))
	}
	if detail.Attempts[0].PromptBytes != len(small) || detail.Attempts[1].PromptBytes != len(large) {
		t.Errorf("attempt sizes = %d then %d, want %d then %d -- the growth is the whole point",
			detail.Attempts[0].PromptBytes, detail.Attempts[1].PromptBytes, len(small), len(large))
	}
}

// An attempt that never reached its agent recorded no prompt, and reads
// as unmeasured rather than as a prompt of nought bytes -- the two are
// different facts, and a zero would average into any report as the
// second.
func TestAnAttemptWithNoPromptIsReportedUnmeasured(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	ctx := context.Background()
	task := create(t, client, ctx)

	startRun(t, client.Store, ctx, task.ID, "r1", 1, "")

	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reading the task's detail: %v", err)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].PromptBytes != 0 || detail.Attempts[0].PromptTokens != 0 {
		t.Errorf("attempts = %+v, want one attempt with no measurement", detail.Attempts)
	}
}
