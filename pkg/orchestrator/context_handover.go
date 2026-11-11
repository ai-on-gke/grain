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
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ContextExhaustedDetail is the detail of a run suspended because its
// context was exhausted (model.SuspendContext).
const ContextExhaustedDetail = "the conversation no longer fit the model's context window; " +
	"the next run starts a fresh session from the conversation and the handover"

// contextHandoverTimeout and contextHandoverTurns bound the one last
// turn a run whose context was exhausted is given to write its handover.
const (
	contextHandoverTimeout = 10 * time.Minute
	contextHandoverTurns   = 5
)

// contextHandoverPrompt is that turn's whole prompt.
const contextHandoverPrompt = "[grain] This conversation has run out of context and this run is being " +
	"stopped. The next run on this task starts a fresh session from the task's conversation and " +
	"your handover, and nothing else from this session reaches it. Write this task's handover " +
	"now (write_handover): what you were doing, what is done and pushed, what is left, and " +
	"anything decided or ruled out. Then stop: take no other action.\n"

// lastTurnForHandover gives a run whose context was exhausted one last
// turn, in the session it was in, to write its handover
// (design/design/durable-tasks.design.md#DT-2, Continuity), and relays what it
// wrote. Best effort by design: a session too full to take even this, or
// a framework that keeps no session, leaves the handover whatever the
// run wrote last, and nothing here can fail the run being stopped.
func lastTurnForHandover(ctx context.Context, store *model.Store, cfg Config, framework agent.Framework,
	runCfg agent.RunConfig, task model.Task, session, prompt string) {
	if framework == nil || session == "" {
		return
	}
	turnCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contextHandoverTimeout)
	defer cancel()
	last := runCfg
	last.Prompt, last.SuspendedPrompt, last.ResumeSession = prompt, prompt, session
	last.MaxTurns = contextHandoverTurns
	last.OnResumableSession = nil
	// The run's own transcript is already complete; this turn writes none.
	last.TranscriptPath = ""
	result, err := framework.Run(turnCtx, last)
	if err != nil && result == nil {
		log.Printf("orchestrator: task %s: the last turn for its handover after its context ran out: %v", task.ID, err)
		return
	}
	if err := relayHandover(turnCtx, store, task, result, cfg.now()); err != nil {
		log.Printf("orchestrator: task %s: relaying the handover written in its last turn: %v", task.ID, err)
	}
}

// requiredHandoverPrompt is the last turn's prompt for a durable run that
// is about to park or suspend without having written or confirmed its
// handover (design/design/durable-tasks.design.md#DT-15).
const requiredHandoverPrompt = "[grain] This durable task is about to rest, and this run has not written " +
	"or confirmed its handover. The next run on this task starts from the task's conversation and " +
	"your handover. Write the handover now (write_handover): what you were doing, what is done and " +
	"pushed, what is left, and anything decided or ruled out. If the current handover is still " +
	"true, write it again unchanged to confirm it. Then stop: take no other action.\n"

// parkingTools are the calls that leave a task open and waiting, which is
// when a durable task owes its next run a handover.
var parkingTools = []string{"ask_question", "request_secret", "request_access", "resume_later", "wait_for_task"}

// owesHandover reports whether a durable run ending in a park or a
// suspension has neither written nor confirmed its handover
// (write_handover or compact_context), so it is given one last turn to.
// A context suspension is asked separately (contextHandoverPrompt).
func owesHandover(task model.Task, result *agent.Result, suspend model.SuspensionKind) bool {
	if task.Admission != model.AdmissionDurable || suspend == model.SuspendContext {
		return false
	}
	parks := suspend != ""
	if result != nil {
		for _, c := range result.ToolCalls {
			if c.IsError {
				continue
			}
			switch c.Name {
			case "write_handover", "compact_context":
				return false
			}
			for _, p := range parkingTools {
				if c.Name == p {
					parks = true
				}
			}
		}
	}
	return parks
}

// handoverWrittenSince reports whether taskID's handover was written or
// confirmed at or after since -- by the in-sandbox `grain handover write`,
// which reaches the store directly rather than through a tool call this
// run's result carries. A store that cannot answer reads as written, so a
// read failure costs no extra turn.
//
// A task whose goal is that a person closes it -- a console session
// (DT-7) -- reads as written too: every turn of it parks on the person's
// next message, and a handover turn after each one would double what a
// conversation costs. Its handover is still asked for where its thread is
// cut (conversation.go) and at a context suspension.
func handoverWrittenSince(ctx context.Context, store *model.Store, taskID string, since time.Time) bool {
	if store == nil {
		return true
	}
	if goal, err := store.TaskGoal(ctx, taskID); err != nil || (goal != nil && goal.Kind == model.GoalPersonCloses) {
		return true
	}
	age, err := store.TaskHandoverAge(ctx, taskID)
	if err != nil {
		return true
	}
	return age.Exists && !age.At.Before(since)
}
