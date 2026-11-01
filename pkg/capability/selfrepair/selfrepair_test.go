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

package selfrepair

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, ctx
}

func putTask(t *testing.T, store *model.Store, ctx context.Context) string {
	t.Helper()
	id, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("NewTaskID: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{ID: id, Interactive: true}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	return id
}

func humanReply(t *testing.T, store *model.Store, ctx context.Context, taskID, body string) {
	t.Helper()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      body,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
}

// awaitConfirmWaiting blocks until Confirm has posted its question and
// marked the task pending -- the last thing it does before entering its
// polling loop, so once this returns Confirm is provably waiting on a
// reply rather than merely likely to be.
//
// Every test below that answers a Confirm waits here first instead of
// sleeping for as long as Confirm "should" need to get started. A sleep
// is not the same thing on a loaded runner (`go test -race ./...` runs
// this package alongside every other), and it fails in both directions:
// a reply written before Confirm posts its question carries a lower
// comment ID than the question and Confirm ignores it forever, and a
// reply written after Confirm's deadline has passed gets the timeout
// Confirm was designed to report instead of the answer the test is
// asserting about. Synchronising here removes both, because Confirm
// reads the comments on each tick before it ever looks at its own
// deadline: once the reply is in the store, no amount of scheduling
// delay can turn it back into a timeout. That is also why the timeouts
// these tests hand Confirm are long enough to be irrelevant -- what
// bounds them is the reply landing, not the clock, and
// TestConfirmDeniesOnTimeoutWithNoReply is where the deadline itself is
// under test.
func awaitConfirmWaiting(t *testing.T, store *model.Store, ctx context.Context, taskID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		obs, err := store.GetObservation(ctx, taskID)
		if err != nil {
			t.Fatalf("GetObservation: %v", err)
		}
		if obs != nil && obs.PendingQuestionCommentID != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Confirm never marked the task awaiting a reply -- it did not reach its polling loop")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConfirmApprovedRunsAfterAHumanApproves(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	done := make(chan struct{})
	var approved bool
	var reply string
	go func() {
		approved, reply, _ = Confirm(ctx, store, taskID, "run rm -rf /tmp/x?", 20*time.Millisecond, 30*time.Second)
		close(done)
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	humanReply(t, store, ctx, taskID, "approve, go ahead")

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Confirm did not return in time")
	}
	if !approved {
		t.Fatalf("approved = false, reply = %q, want true", reply)
	}
}

func TestConfirmDeniedAfterAHumanDenies(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	done := make(chan struct{})
	var approved bool
	var reply string
	go func() {
		approved, reply, _ = Confirm(ctx, store, taskID, "run rm -rf /tmp/x?", 20*time.Millisecond, 30*time.Second)
		close(done)
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	humanReply(t, store, ctx, taskID, "deny, that looks dangerous")

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Confirm did not return in time")
	}
	if approved {
		t.Fatal("approved = true, want false")
	}
	if !strings.Contains(reply, "dangerous") {
		t.Fatalf("reply = %q, want it to carry the human's own words", reply)
	}
}

func TestConfirmDeniesOnTimeoutWithNoReply(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	approved, reply, err := Confirm(ctx, store, taskID, "run something?", 10*time.Millisecond, 60*time.Millisecond)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if approved {
		t.Fatal("approved = true, want false on timeout")
	}
	if !strings.Contains(reply, "timeout") {
		t.Fatalf("reply = %q, want it to mention the timeout", reply)
	}

	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs != nil && obs.PendingQuestionCommentID != nil {
		t.Fatal("PendingQuestionCommentID left set after a timeout, want it cleared")
	}
}

func TestConfirmIgnoresNonHumanCommentsAndItsOwnQuestion(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	// The timeout is generous, unlike TestConfirmDeniesOnTimeoutWithNoReply's
	// own: what this test asserts is that Confirm keeps waiting through a
	// non-human comment, so a deadline anywhere near the wait below would
	// make a loaded machine report a timeout as a missed approval.
	done := make(chan struct{})
	var approved bool
	go func() {
		approved, _, _ = Confirm(ctx, store, taskID, "run something?", 10*time.Millisecond, 30*time.Second)
		close(done)
	}()

	// The agent's comment has to land after Confirm's own question to be
	// the thing under test: one posted before it is ignored for its
	// comment ID alone, and the test would pass without Confirm ever
	// having to read the author.
	awaitConfirmWaiting(t, store, ctx, taskID)
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: taskID}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body:      "approve -- said by the agent itself, not a human",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	select {
	case <-done:
		t.Fatal("Confirm returned before a human replied -- it must not trust a non-human comment")
	case <-time.After(150 * time.Millisecond):
	}
	humanReply(t, store, ctx, taskID, "approve")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Confirm did not return after the human's own approve")
	}
	if !approved {
		t.Fatal("approved = false after the human's own approve, want true")
	}
}

func TestHostCommandToolRunsTheCommandOnceApproved(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	tools := HostCommandTools(store, taskID, 10*time.Millisecond, 30*time.Second)
	if len(tools) != 1 || tools[0].Name != "run_host_command" {
		t.Fatalf("tools = %+v, want exactly one run_host_command tool", tools)
	}

	resultCh := make(chan struct {
		text    string
		isError bool
	}, 1)
	go func() {
		res := tools[0].Handler(ctx, map[string]any{"command": "echo hello-from-configuration-mode"})
		resultCh <- struct {
			text    string
			isError bool
		}{res.Text, res.IsError}
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	humanReply(t, store, ctx, taskID, "approve")

	select {
	case res := <-resultCh:
		if res.isError {
			t.Fatalf("run_host_command errored: %s", res.text)
		}
		if !strings.Contains(res.text, "hello-from-configuration-mode") {
			t.Fatalf("Text = %q, want it to contain the command's own stdout", res.text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run_host_command did not return in time")
	}
}

func TestHostCommandToolNeverRunsWhenDenied(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)

	tools := HostCommandTools(store, taskID, 10*time.Millisecond, 30*time.Second)

	resultCh := make(chan struct {
		text    string
		isError bool
	}, 1)
	go func() {
		res := tools[0].Handler(ctx, map[string]any{"command": "touch /tmp/should-never-exist-selfrepair-test"})
		resultCh <- struct {
			text    string
			isError bool
		}{res.Text, res.IsError}
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	humanReply(t, store, ctx, taskID, "deny")

	select {
	case res := <-resultCh:
		if !res.isError {
			t.Fatalf("expected an error result for a denied command, got: %s", res.text)
		}
		if !strings.Contains(res.text, "denied") {
			t.Fatalf("Text = %q, want it to say denied", res.text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run_host_command did not return in time")
	}
}

func TestDecideOnlyAcceptsExplicitApproval(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  bool
	}{
		{name: "approve", reply: "approve", want: true},
		{name: "approve with reason", reply: "  APPROVE, this is safe  ", want: true},
		{name: "yes", reply: "yes, proceed", want: true},
		{name: "denial wins", reply: "approve? no, deny this", want: false},
		{name: "question", reply: "can you explain why?", want: false},
		{name: "approval mentioned later", reply: "I might approve after a backup", want: false},
		{name: "empty", reply: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decide(tt.reply); got != tt.want {
				t.Fatalf("decide(%q) = %v, want %v", tt.reply, got, tt.want)
			}
		})
	}
}

func TestHostCommandToolRequiresACommand(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)
	tool := HostCommandTools(store, taskID, 10*time.Millisecond, time.Second)[0]

	for _, args := range []map[string]any{nil, {}, {"command": 42}, {"command": ""}} {
		result := tool.Handler(ctx, args)
		if !result.IsError || result.Text != "command is required" {
			t.Fatalf("Handler(%v) = %#v, want command-required error", args, result)
		}
	}
}

func TestHostCommandToolReportsACommandFailure(t *testing.T) {
	store, ctx := openStore(t)
	taskID := putTask(t, store, ctx)
	tool := HostCommandTools(store, taskID, 10*time.Millisecond, 30*time.Second)[0]

	resultCh := make(chan mcp.Result, 1)
	go func() {
		resultCh <- tool.Handler(ctx, map[string]any{"command": "printf failure-output >&2; exit 7"})
	}()

	awaitConfirmWaiting(t, store, ctx, taskID)
	humanReply(t, store, ctx, taskID, "approve")

	select {
	case result := <-resultCh:
		if !result.IsError {
			t.Fatalf("IsError = false, want true; Text = %q", result.Text)
		}
		if !strings.Contains(result.Text, "exit=7") || !strings.Contains(result.Text, "failure-output") {
			t.Fatalf("Text = %q, want exit code and stderr", result.Text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run_host_command did not return in time")
	}
}
