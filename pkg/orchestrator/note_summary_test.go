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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The #4256 note: a closing note whose opening sentence is a report and
// whose action is halfway down it.
const actionNote = "I didn't change any code. These release failures aren't caused by a bug.\n\n" +
	"Only someone with admin on the repository can set the variable."

// A closing note's summary goes the whole way: the comment_on_issue call
// the tool accepted, the comment ProcessResult stores, and the line the
// task list, the task pane and the inbox show of the task resting on it.
// A note without one is shown by none of its own words, never by its
// opening sentence.
func TestAClosingNotesSummaryReachesTheTaskRowAndTheInbox(t *testing.T) {
	t.Parallel()
	const summary = "Set GRAIN_MERGE_LOGINS on acme/widgets to unblock image publishing"
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"given", map[string]any{"comment": actionNote, "summary": summary}, summary},
		{"missing", map[string]any{"comment": "## Report\n\nEverything is healthy."}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The tool accepts the call as the run made it.
			var comment mcp.Tool
			for _, tool := range mcp.NewMockTools(&mcp.MockSink{}, nil) {
				if tool.Name == "comment_on_issue" {
					comment = tool
				}
			}
			if res := comment.Handler(context.Background(), tc.args); res.IsError {
				t.Fatalf("comment_on_issue refused %v: %s", tc.args, res.Text)
			}

			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
			result := toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: tc.args})
			if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}
			comments, err := store.Comments(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(comments) != 1 || comments[0].Summary != tc.want {
				t.Fatalf("comments = %+v, want one note summarized %q", comments, tc.want)
			}
			if st, _ := store.State(ctx, task.ID); st != model.StateAwaitingReply {
				t.Fatalf("state = %q, want awaiting_reply", st)
			}

			uc := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store)
			list, err := uc.ListTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var row *ui.Task
			for i := range list {
				if list[i].ID == task.ID {
					row = &list[i]
				}
			}
			if row == nil {
				t.Fatalf("ListTasks has no %s", task.ID)
			}
			if row.NoteSummary != tc.want || row.QuestionSummary != "" {
				t.Errorf("list row: NoteSummary %q QuestionSummary %q, want %q and none", row.NoteSummary, row.QuestionSummary, tc.want)
			}
			one, err := uc.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if one.NoteSummary != tc.want {
				t.Errorf("Client.Task NoteSummary = %q, want %q", one.NoteSummary, tc.want)
			}
			detail, err := uc.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.NoteSummary != tc.want {
				t.Errorf("GetTask NoteSummary = %q, want %q", detail.NoteSummary, tc.want)
			}
			if tc.want == "" && strings.Contains(detail.NoteSummary+row.NoteSummary, "Report") {
				t.Errorf("a note without a summary was shown by its own opening")
			}
		})
	}
}

// A person's reply after the note is the task's newest word: the note's
// summary no longer stands for the task.
func TestANoteSummaryIsNotShownPastAReply(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	result := toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{
		"comment": actionNote, "summary": "Set the variable",
	}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got, _ := store.NoteSummary(ctx, task.ID); got != "Set the variable" {
		t.Fatalf("NoteSummary = %q, want the note's", got)
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body: "done, try again", CreatedAt: baseTime.Add(1),
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.NoteSummary(ctx, task.ID); got != "" {
		t.Fatalf("NoteSummary after a reply = %q, want none", got)
	}
	if all, _ := store.NoteSummaries(ctx); all[task.ID] != "" {
		t.Fatalf("NoteSummaries after a reply = %q, want none", all[task.ID])
	}
}

// Every run is told the rule, and the two task bodies most likely to end
// on something only a person can do -- a base-branch repair and a release
// triage -- repeat it.
func TestEveryPromptSaysAnActionIsAQuestionNotANote(t *testing.T) {
	t.Parallel()
	task := model.Task{ID: "t1", Intent: model.IntentImplement, Title: "x", Body: "y",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"}}
	prompt := orchestrator.BuildPrompt(task, "", "", false, 0, orchestrator.History{}, nil)
	for _, want := range []string{
		model.PersonActionInstruction,
		"end with ask_question, not comment_on_issue",
		"Its `summary` names the action (\"Set GRAIN_MERGE_LOGINS on ai-on-gke/grain to unblock image publishing\")",
		"numbered checklist of the exact steps, each with its UI path and the CLI command",
		"Use comment_on_issue only for a report nobody has to act on",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not say %q", want)
		}
	}
	if !strings.Contains(model.RepairNeedsAPersonReminder, model.PersonActionInstruction) {
		t.Errorf("RepairNeedsAPersonReminder does not carry the rule")
	}
}
