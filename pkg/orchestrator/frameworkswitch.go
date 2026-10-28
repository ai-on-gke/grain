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
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// frameworkSwitchNote is what an interactive task's conversation is told
// when its next run lands on a different agent framework than the
// session its previous run left: the model this turn is on and why, and
// that the earlier CLI session was not resumed. "" when there is nothing
// to say -- a task nobody is sitting with, no session to switch away
// from, or the same framework as before.
//
// Per-task session stores are each framework's own (pkg/agent/
// sessionstore, agy's resume, claude's session HOME), so a session one
// framework left cannot be resumed by another: the run starts fresh from
// grain's prompt and the conversation thread, as a new task would
// (model.ResumeTokenFor already refuses the other framework's token).
// This note is so that the change of voice is not a mystery.
func frameworkSwitchNote(task model.Task, obs *model.Observation, framework agent.Framework, runFramework string) string {
	if !task.Interactive && !model.NeverCompletes(task) {
		return ""
	}
	if obs == nil || obs.SuspendedSession == nil || obs.SuspendedSession.Token == "" {
		return ""
	}
	prev := model.NormalizeAgentFrameworkName(obs.SuspendedSession.Framework)
	runFramework = model.NormalizeAgentFrameworkName(runFramework)
	if prev == "" || runFramework == "" || prev == runFramework {
		return ""
	}
	on := runFramework
	if cm, ok := framework.(CredentialModel); ok && cm.CredentialModel() != "" {
		on = fmt.Sprintf("%s (%s)", cm.CredentialModel(), runFramework)
	}
	because := ""
	if ff, ok := framework.(FallbackFramework); ok && ff.FallbackReason() != "" {
		because = " because " + ff.FallbackReason()
	}
	return fmt.Sprintf("This turn ran on %s%s. The earlier %s session cannot be resumed on another framework, "+
		"so this run started fresh from the task and its conversation.", on, because, prev)
}

// noteFrameworkSwitch posts frameworkSwitchNote into task's conversation,
// logging rather than failing the run when the write does not land: a
// bookkeeping note must not be able to fail the work it describes.
func noteFrameworkSwitch(ctx context.Context, store *model.Store, task model.Task, obs *model.Observation,
	framework agent.Framework, runFramework string, now time.Time) {
	body := frameworkSwitchNote(task, obs, framework, runFramework)
	if body == "" {
		return
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		log.Printf("orchestrator: task %s: noting its switch of agent framework: %v", task.ID, err)
	}
}
