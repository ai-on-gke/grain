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

package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

func durableTask(id string) model.Task {
	tk := stdTask(id)
	tk.Admission = model.AdmissionDurable
	tk.Body = "Stage 1: build the hub."
	return tk
}

func humanSays(ctx context.Context, t *testing.T, store *model.Store, taskID, body string, at time.Time) int64 {
	t.Helper()
	id, err := store.AddComment(ctx, model.Comment{TaskID: taskID, Body: body, CreatedAt: at,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// promptFor builds the prompt a dispatch of tk would get now.
func promptFor(ctx context.Context, t *testing.T, store *model.Store, tk model.Task) string {
	t.Helper()
	comments, err := dispatchComments(ctx, store, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := History{Comments: comments}
	loadTaskContext(ctx, store, tk, &h)
	return BuildPrompt(tk, "", "", false, 0, h, nil)
}

// A compact_context call moves the cursor to the last comment posted
// before the run started, and every later prompt carries the handover
// and only the comments after the cursor -- while the summarised
// comments stay on the task, so a deep link to one still resolves.
func TestCompactionMovesTheCursorAndLaterPromptsLeaveSummarisedCommentsOut(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := durableTask("hub")
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	old := humanSays(ctx, t, store, tk.ID, "use the blue database for stage 1", standingNow.Add(-2*time.Hour))
	if err := store.StartRun(ctx, model.Run{ID: "hub-1", TaskID: tk.ID, Attempt: 1, StartedAt: standingNow.Add(-time.Hour)},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// Said while the run was working: not seen by it, so not folded away.
	humanSays(ctx, t, store, tk.ID, "also rename the bucket", standingNow.Add(-30*time.Minute))

	result := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "compact_context",
		Arguments: map[string]any{"handover": "Stage 1 is done on the blue database."}}}}
	if err := relayCompaction(ctx, store, tk, result, "hub-1", standingNow); err != nil {
		t.Fatal(err)
	}
	tc, err := store.TaskContext(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tc.CompactedThrough != old || tc.HandoverVersion != 1 || tc.CompactedBy != "task "+tk.ID {
		t.Fatalf("context after compaction = %+v, want the cursor at comment %d, version 1", tc, old)
	}

	prompt := promptFor(ctx, t, store, tk)
	if strings.Contains(prompt, "use the blue database for stage 1") {
		t.Errorf("a summarised comment is still in the prompt:\n%s", prompt)
	}
	for _, want := range []string{"Stage 1 is done on the blue database.", "also rename the bucket",
		fmt.Sprintf("every message up to comment %d is summarised", old)} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}

	comments, err := store.Comments(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range comments {
		found = found || c.ID == old
	}
	if !found {
		t.Errorf("summarised comment %d is gone from the record; deep links to it must still resolve", old)
	}
}

// The cursor never moves backwards: a later compaction through an older
// comment keeps the newer cursor.
func TestCompactionCursorNeverMovesBackwards(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := durableTask("hub")
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	first := humanSays(ctx, t, store, tk.ID, "one", standingNow.Add(-2*time.Hour))
	second := humanSays(ctx, t, store, tk.ID, "two", standingNow.Add(-time.Hour))
	by := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	if _, err := store.CompactTask(ctx, tk.ID, "through two", second, by, "human alice", standingNow); err != nil {
		t.Fatal(err)
	}
	tc, err := store.CompactTask(ctx, tk.ID, "through one", first, by, "human alice", standingNow)
	if err != nil {
		t.Fatal(err)
	}
	if tc.CompactedThrough != second || tc.HandoverVersion != 2 {
		t.Errorf("context = %+v, want the cursor kept at %d and version 2", tc, second)
	}
}

// A current brief replaces the original description in the prompt; the
// original stays on the task, and the edit is recorded with who and why.
func TestCurrentBriefReplacesTheDescriptionAndKeepsTheOriginal(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := durableTask("hub")
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	result := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "update_brief",
		Arguments: map[string]any{"brief": "Maintain the hub: keep main green.", "reason": "stage 1 is finished"}}}}
	if err := relayBrief(ctx, store, tk, result, standingNow); err != nil {
		t.Fatal(err)
	}
	prompt := promptFor(ctx, t, store, tk)
	if strings.Contains(prompt, "Stage 1: build the hub.") {
		t.Errorf("the original description is still in the prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Maintain the hub: keep main green.") || !strings.Contains(prompt, currentBriefNote) {
		t.Errorf("prompt does not carry the current brief and its note:\n%s", prompt)
	}
	stored, err := store.GetTask(ctx, tk.ID)
	if err != nil || stored == nil || stored.Body != "Stage 1: build the hub." {
		t.Errorf("the original description = %v (err %v), want it kept", stored, err)
	}
	edits, err := store.TaskBriefEdits(ctx, tk.ID)
	if err != nil || len(edits) != 1 || edits[0].By != "agent:"+tk.ID || edits[0].Reason != "stage 1 is finished" {
		t.Errorf("brief edits = %+v (err %v), want one by the task's agent with its reason", edits, err)
	}
}

// A handover past HandoverStaleRuns runs is flagged as stale in a durable
// task's prompt, and writing the same handover again confirms it and
// clears the warning.
func TestStaleHandoverWarnsUntilConfirmed(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := durableTask("hub")
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	handover := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "write_handover",
		Arguments: map[string]any{"handover": "the hub runs on the blue database"}}}}
	if err := relayHandover(ctx, store, tk, handover, standingNow.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < model.HandoverStaleRuns; i++ {
		if err := store.StartRun(ctx, model.Run{ID: fmt.Sprintf("hub-%d", i+1), TaskID: tk.ID, Attempt: i + 1,
			StartedAt: standingNow.Add(time.Duration(i-model.HandoverStaleRuns) * time.Hour)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, fmt.Sprintf("hub-%d", i+1), standingNow, "succeeded", ""); err != nil {
			t.Fatal(err)
		}
	}
	if prompt := promptFor(ctx, t, store, tk); !strings.Contains(prompt, "handover is stale") {
		t.Errorf("a handover %d runs old is not flagged as stale:\n%s", model.HandoverStaleRuns, prompt)
	}
	if err := relayHandover(ctx, store, tk, handover, standingNow); err != nil {
		t.Fatal(err)
	}
	if prompt := promptFor(ctx, t, store, tk); strings.Contains(prompt, "handover is stale") {
		t.Errorf("a confirmed handover is still flagged as stale:\n%s", prompt)
	}
	if tc, _ := store.TaskContext(ctx, tk.ID); tc.HandoverVersion != 1 {
		t.Errorf("handover version = %d after one handover and one confirmation, want 1", tc.HandoverVersion)
	}
}

// A durable task's prompt past the auto-compaction threshold asks the
// run to compact first; an ordinary task's never does, and neither does
// a durable one under it.
func TestAutoCompactionThreshold(t *testing.T) {
	durable := durableTask("hub")
	h := History{AutoCompactBytes: 1000}
	if got := contextUpkeepSection(durable, h, 999); got != "" {
		t.Errorf("under the threshold, upkeep = %q, want none", got)
	}
	if got := contextUpkeepSection(durable, h, 1001); !strings.Contains(got, "call compact_context") {
		t.Errorf("over the threshold, upkeep = %q, want a request to compact", got)
	}
	if got := contextUpkeepSection(stdTask("plain"), h, 1<<20); got != "" {
		t.Errorf("an ordinary task's upkeep = %q, want none", got)
	}

	store, ctx := openTestStore(t)
	if err := store.PutTask(ctx, durable); err != nil {
		t.Fatal(err)
	}
	cfg := Config{AutoCompactBytes: 1000, Now: func() time.Time { return standingNow }}
	recordAutoCompaction(ctx, store, cfg, durable, 999)
	if tc, _ := store.TaskContext(ctx, durable.ID); tc.CompactRequestedAt != nil {
		t.Fatalf("a prompt under the threshold recorded a compaction request: %+v", tc)
	}
	recordAutoCompaction(ctx, store, cfg, durable, 1001)
	tc, _ := store.TaskContext(ctx, durable.ID)
	if tc.CompactRequestedAt == nil || tc.CompactRequestedBy != autoCompactRequester {
		t.Fatalf("a prompt over the threshold left context %+v, want an auto-compaction request", tc)
	}
	if prompt := promptFor(ctx, t, store, durable); !strings.Contains(prompt, autoCompactRequester+" asked for this task's context to be compacted") {
		t.Errorf("the next prompt does not carry the request:\n%s", prompt)
	}
}

// A durable run that parks without writing or confirming its handover
// owes one; a run that wrote one, an ordinary task and a console session
// do not.
func TestDurableParkOwesAHandover(t *testing.T) {
	durable := durableTask("hub")
	parks := &agent.Result{ToolCalls: []agent.ToolCall{{Name: "resume_later", Arguments: map[string]any{"reason": "wait"}}}}
	if !owesHandover(durable, parks, "") {
		t.Error("a durable run parking without a handover does not owe one")
	}
	wrote := &agent.Result{ToolCalls: append(parks.ToolCalls, agent.ToolCall{Name: "write_handover",
		Arguments: map[string]any{"handover": "x"}})}
	if owesHandover(durable, wrote, "") {
		t.Error("a durable run that wrote its handover still owes one")
	}
	if owesHandover(stdTask("plain"), parks, "") {
		t.Error("an ordinary task owes a handover")
	}
	if owesHandover(durable, &agent.Result{}, "") {
		t.Error("a durable run that neither parked nor suspended owes a handover")
	}
	if !owesHandover(durable, &agent.Result{}, model.SuspendRequested) {
		t.Error("a durable run that suspended without a handover does not owe one")
	}
}

// A dispatch of a compacted task reads the comments after its cursor and
// the handover that summarises the rest, never the summarised comments
// themselves: what it reads grows with what was said since the
// compaction, not with the whole record (durable-tasks.design.md#DT-15).
func TestDispatchReadsOnlyTheCommentsAfterTheCursor(t *testing.T) {
	store, ctx := openTestStore(t)
	tk := durableTask("hub")
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	var summarised []int64
	for i := 0; i < 5; i++ {
		summarised = append(summarised, humanSays(ctx, t, store, tk.ID, fmt.Sprintf("old %d", i), standingNow.Add(-time.Hour)))
	}
	if _, err := store.CompactTask(ctx, tk.ID, "the old five, summarised", 0,
		model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}, "alice", standingNow); err != nil {
		t.Fatal(err)
	}
	later := humanSays(ctx, t, store, tk.ID, "new", standingNow.Add(time.Minute))

	got, err := dispatchComments(ctx, store, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Handover || got[0].Body != "the old five, summarised" || got[1].ID != later {
		t.Fatalf("dispatch read %+v, want the handover then comment %d", got, later)
	}
	for _, c := range got {
		if slices.Contains(summarised, c.ID) {
			t.Errorf("dispatch read summarised comment %d", c.ID)
		}
	}

	// A task never compacted still reads its whole thread.
	plain := durableTask("plain")
	if err := store.PutTask(ctx, plain); err != nil {
		t.Fatal(err)
	}
	humanSays(ctx, t, store, plain.ID, "one", standingNow)
	humanSays(ctx, t, store, plain.ID, "two", standingNow)
	if all, err := dispatchComments(ctx, store, plain.ID); err != nil || len(all) != 2 {
		t.Fatalf("uncompacted dispatch read %d comments (%v), want both", len(all), err)
	}
}

// The prompt says the summarised messages are in "the handover below",
// so the newest handover survives the cut even when it is at or before
// the cursor, and only when no handover is after it.
func TestCommentsPastCursorKeepsTheHandoverThatSummarisesThem(t *testing.T) {
	thread := []model.Comment{
		{ID: 1, Body: "old"},
		{ID: 2, Body: "older summary", Handover: true},
		{ID: 3, Body: "summary", Handover: true},
		{ID: 4, Body: "summarised too"},
		{ID: 5, Body: "new"},
	}
	got := commentsPastCursor(thread, 4)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 5 {
		t.Fatalf("past cursor 4 = %+v, want the handover (3) then comment 5", got)
	}
	thread = append(thread, model.Comment{ID: 6, Body: "newer summary", Handover: true})
	got = commentsPastCursor(thread, 4)
	if len(got) != 2 || got[0].ID != 5 || got[1].ID != 6 {
		t.Fatalf("past cursor 4 with a later handover = %+v, want comments 5 and 6 only", got)
	}
}
