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

// Deliberate compaction of a long-lived task's context
// (design/design/durable-tasks.design.md#DT-15).
//
// conversation.go bounds a thread by position: it keeps the oldest and the
// newest messages and elides the middle, whatever the middle said. That
// holds when nobody is watching, and it is still the backstop here. What
// this file adds is the deliberate version: a run (or a person) folds the
// thread up to a cursor into a fresh handover, and every later prompt
// carries that handover and only the comments after the cursor. The
// summarised comments stay on the task and in the UI; they only leave the
// prompt.
//
// Beside it, the current brief: a durable task's description as it stands
// now, which replaces the original description in prompts once somebody
// has written one, so a months-old task stops being briefed on work it
// finished in its first week.

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultAutoCompactBytes is the assembled-prompt size past which a
// durable task's run is asked to compact before anything else
// (Config.AutoCompactBytes). About 50 KB: under conversation.go's own
// 64 KB thread bound plus its handover allowance, so a durable task is
// compacted on purpose before the positional cut has to do it blindly.
const DefaultAutoCompactBytes = 50 * 1024

// autoCompactRequester is who an automatic compaction request is
// recorded as (model.TaskContext.CompactRequestedBy).
const autoCompactRequester = "grain (auto-compaction)"

// loadTaskContext reads task's context row and handover age into
// history. Advisory like the other history reads: a store that cannot
// answer leaves the prompt as it was before compaction existed.
func loadTaskContext(ctx context.Context, store *model.Store, task model.Task, history *History) {
	if store == nil {
		return
	}
	tc, err := store.TaskContext(ctx, task.ID)
	if err != nil {
		log.Printf("orchestrator: task %s: reading its context: %v", task.ID, err)
		return
	}
	history.Context = tc
	if task.Admission == model.AdmissionDurable {
		if age, err := store.TaskHandoverAge(ctx, task.ID); err == nil {
			history.HandoverAge = age
		}
	}
}

// applyTaskContext is what BuildPrompt sees of the task once its context
// is applied: the current brief in place of the description, and the
// conversation with every summarised comment taken out.
func applyTaskContext(task model.Task, history History) (model.Task, History) {
	if brief := strings.TrimSpace(history.Context.CurrentBrief); brief != "" {
		task.Body = brief + "\n\n" + currentBriefNote
	}
	if through := history.Context.CompactedThrough; through > 0 {
		history.Comments = commentsPastCursor(history.Comments, through)
	}
	return task, history
}

// commentsPastCursor is comments without the ones a compaction through
// through summarised, keeping the newest handover even when it is at or
// before the cursor: summarisedSection tells the run the summarised
// messages are in "the handover below", so it has to be.
func commentsPastCursor(comments []model.Comment, through int64) []model.Comment {
	kept := make([]model.Comment, 0, len(comments))
	var summary *model.Comment
	for i, c := range comments {
		switch {
		case c.ID > through:
			kept = append(kept, c)
		case c.Handover:
			summary = &comments[i]
		}
	}
	if summary == nil || slices.ContainsFunc(kept, func(c model.Comment) bool { return c.Handover }) {
		return kept
	}
	return append([]model.Comment{*summary}, kept...)
}

// dispatchComments is the part of task's conversation a dispatch reads:
// the comments after its compaction cursor, and the newest handover when
// that is not among them. A task's record keeps growing after it is
// compacted; what its dispatch reads grows only with what was said since
// (durable-tasks.design.md#DT-15). A task never compacted reads its
// whole thread, which conversation.go's positional cut bounds as before.
func dispatchComments(ctx context.Context, store *model.Store, taskID string) ([]model.Comment, error) {
	tc, err := store.TaskContext(ctx, taskID)
	if err != nil {
		return nil, err
	}
	comments, err := store.CommentsAfter(ctx, taskID, tc.CompactedThrough)
	if err != nil || tc.CompactedThrough <= 0 {
		return comments, err
	}
	if slices.ContainsFunc(comments, func(c model.Comment) bool { return c.Handover }) {
		return comments, nil
	}
	summary, err := store.LatestHandover(ctx, taskID)
	if err != nil || summary == nil {
		return comments, err
	}
	return append([]model.Comment{*summary}, comments...), nil
}

// currentBriefNote follows a current brief in the prompt, so a run knows
// the brief it is reading is not the one the task was filed with.
const currentBriefNote = "[grain] That is this task's current brief, which replaces the description it was " +
	"filed with. The original description is kept on the task and shown in grain's UI; " +
	"if the brief no longer says what this task is for, rewrite it with update_brief."

// summarisedSection says, where the task has been compacted, that the
// comments before the cursor are summarised by its handover rather than
// shown. "" for a task never compacted.
func summarisedSection(history History) string {
	tc := history.Context
	if tc.CompactedThrough <= 0 {
		return ""
	}
	when := ""
	if tc.CompactedAt != nil {
		when = " on " + tc.CompactedAt.UTC().Format("2006-01-02")
	}
	by := ""
	if tc.CompactedBy != "" {
		by = " by " + tc.CompactedBy
	}
	return fmt.Sprintf("[grain] This task's conversation was compacted%s%s: every message up to comment "+
		"%d is summarised by the handover below (version %d) and left out of this prompt. Those "+
		"messages are still on the task in grain's UI, collapsed under the handover; ask for any "+
		"of them you need.\n", when, by, tc.CompactedThrough, tc.HandoverVersion)
}

// contextUpkeepSection is what a durable task's run is asked to do about
// its own context before it finishes, or before it starts: compact when a
// person or the size threshold asked for it, refresh a stale handover.
// promptBytes is the size of the prompt assembled so far. "" for a task
// that owes none of it.
func contextUpkeepSection(task model.Task, history History, promptBytes int) string {
	if task.Admission != model.AdmissionDurable {
		return ""
	}
	var b strings.Builder
	threshold := history.AutoCompactBytes
	if threshold <= 0 {
		threshold = DefaultAutoCompactBytes
	}
	switch {
	case history.Context.CompactRequestedAt != nil:
		who := history.Context.CompactRequestedBy
		if who == "" {
			who = "someone"
		}
		fmt.Fprintf(&b, "[grain] %s asked for this task's context to be compacted. Before anything "+
			"else, call compact_context with a fresh handover that folds in the current handover and "+
			"everything settled in the conversation above; grain then leaves those messages out of "+
			"every later prompt. Then carry on with the run.\n", who)
	case promptBytes > threshold:
		fmt.Fprintf(&b, "[grain] This prompt is %s, past the %s at which a durable task compacts. "+
			"Before anything else, call compact_context with a fresh handover that folds in the "+
			"current handover and everything settled in the conversation above; grain then leaves "+
			"those messages out of every later prompt. Then carry on with the run.\n",
			model.PromptBytes(promptBytes), model.PromptBytes(threshold))
	}
	if age := history.HandoverAge; age.Stale() {
		fmt.Fprintf(&b, "[grain] This task's handover is stale: %d run(s) and %d comment(s) since it was "+
			"written or last confirmed. Before you finish, replace it (write_handover) or, if it is "+
			"still true, write it again unchanged to confirm it.\n", age.Runs, age.Comments)
	}
	return b.String()
}

// recordAutoCompaction records an automatic compaction request on a
// durable task whose assembled prompt crossed the threshold, so the task
// view says why its run was asked to compact. Best effort.
func recordAutoCompaction(ctx context.Context, store *model.Store, cfg Config, task model.Task, promptBytes int) {
	if store == nil || task.Admission != model.AdmissionDurable || promptBytes <= cfg.autoCompactBytes() {
		return
	}
	if err := store.RequestCompaction(ctx, task.ID, autoCompactRequester, cfg.now()); err != nil {
		log.Printf("orchestrator: task %s: recording auto-compaction: %v", task.ID, err)
	}
}

func (c Config) autoCompactBytes() int {
	if c.AutoCompactBytes > 0 {
		return c.AutoCompactBytes
	}
	return DefaultAutoCompactBytes
}

// relayCompaction applies a run's compact_context call: its handover
// becomes the task's newest, and the cursor moves to the last comment
// posted before the run started. Nothing at all for a run that made no
// such call.
func relayCompaction(ctx context.Context, store *model.Store, task model.Task,
	result *agent.Result, runID string, now time.Time) error {
	if result == nil {
		return nil
	}
	handover, ok := lastToolCallArg(result, "compact_context", "handover")
	if !ok || strings.TrimSpace(handover) == "" {
		return nil
	}
	cutoff := now
	if started, ok := store.RunStartedAt(ctx, runID); ok {
		cutoff = started
	}
	var through int64
	var before int64
	for {
		page, more, cerr := store.CommentsBefore(ctx, task.ID, before, 50)
		if cerr != nil {
			return fmt.Errorf("orchestrator: reading comments before compacting %s: %w", task.ID, cerr)
		}
		if id := model.LastCommentBefore(page, cutoff); id > through {
			through = id
		}
		if through > 0 || !more || len(page) == 0 {
			break
		}
		before = page[0].ID
	}
	if through <= 0 {
		// Nothing was said before this run: there is nothing to fold,
		// and a cursor of 0 would read as "everything" to CompactTask.
		return relayHandover(ctx, store, task, &agent.Result{ToolCalls: []agent.ToolCall{{
			Name: "write_handover", Arguments: map[string]any{"handover": handover}}}}, now)
	}
	now, err := answeringTime(ctx, store, task.ID, now)
	if err != nil {
		return fmt.Errorf("orchestrator: compacting %s: %w", task.ID, err)
	}
	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: task.ID}
	if _, err := store.CompactTask(ctx, task.ID, handover, through, model.Attribution{
		Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
		OnBehalfOf: &agentPrincipal,
	}, "task "+task.ID, now); err != nil {
		return fmt.Errorf("orchestrator: compacting %s: %w", task.ID, err)
	}
	return nil
}

// relayBrief applies a run's update_brief call to the task's current
// brief, recording the edit as the task's own agent's.
func relayBrief(ctx context.Context, store *model.Store, task model.Task, result *agent.Result, now time.Time) error {
	if result == nil {
		return nil
	}
	brief, ok := lastToolCallArg(result, "update_brief", "brief")
	if !ok || strings.TrimSpace(brief) == "" {
		return nil
	}
	reason, _ := lastToolCallArg(result, "update_brief", "reason")
	if err := store.SetTaskBrief(ctx, task.ID, brief, "agent:"+task.ID, reason, now); err != nil {
		return fmt.Errorf("orchestrator: updating the brief of %s: %w", task.ID, err)
	}
	return nil
}
