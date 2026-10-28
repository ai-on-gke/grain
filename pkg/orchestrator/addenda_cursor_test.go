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

// Whether RunDispatch tells a run's own forked mcpserver where the
// prompt stopped quoting the task's conversation
// (agent.RunConfig.AddendaAfter). That cursor is the whole of what keeps
// "a person typed something while the run was going" from meaning "read
// the prompt's own thread back to the run as breaking news" -- see
// pkg/mcp's addenda.go for the half that acts on it.
package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// cursorCapturingFramework records the addenda cursor the one Run call it
// saw was given, then completes the way toolCapturingFramework does -- a
// comment rather than a push, so ProcessResult has something to observe
// without a branch needing to exist.
type cursorCapturingFramework struct {
	mu   sync.Mutex
	seen bool
	at   int64
}

func (f *cursorCapturingFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.seen, f.at = true, cfg.AddendaAfter
	f.mu.Unlock()
	return toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}}), nil
}

func (f *cursorCapturingFramework) cursor(t *testing.T) int64 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.seen {
		t.Fatal("the framework was never run")
	}
	return f.at
}

func dispatchCapturingCursor(t *testing.T, ctx context.Context, store *model.Store, client *github.RESTClient) int64 {
	t.Helper()
	framework := &cursorCapturingFramework{}
	deps := orchestrator.Deps{
		Store: store, Client: client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(framework),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	return framework.cursor(t)
}

// A task whose conversation is empty passes no cursor at all: nothing is
// in front of the run yet, so everything posted on the task from here is
// new to it.
func TestADispatchWithNoConversationPassesNoAddendaCursor(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	if got := dispatchCapturingCursor(t, ctx, store, client); got != 0 {
		t.Errorf("AddendaAfter = %d, want 0 for a task nobody has commented on", got)
	}
}

// A task that has been talked about hands the run the last thing already
// quoted in its prompt, so the first tool result does not read the
// prompt's own conversation back to it.
func TestADispatchPassesTheLastCommentAlreadyInThePrompt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	var last int64
	for _, body := range []string{"can you look at the parser", "the nil deref, specifically"} {
		id, err := store.AddComment(ctx, model.Comment{
			TaskID:    task.ID,
			Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
			Body:      body,
			CreatedAt: baseTime.Add(-time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		last = id
	}

	if got := dispatchCapturingCursor(t, ctx, store, client); got != last {
		t.Errorf("AddendaAfter = %d, want %d -- the last comment the prompt quotes", got, last)
	}
}
