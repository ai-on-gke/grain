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
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Model failover (docs/design/durable-tasks.md#DT-2): a run suspended
// because its model refused it -- no capacity, a usage limit, a
// model-side failure -- records that model on its suspension
// (task_suspension.model_hint), and the task's next run is offered the
// rest of its rotation first (AgentChoice.AvoidModels). A usage limit
// already pauses the model it met on its connection, so the pool's own
// rotation finds another connection serving the same model if one is
// free; avoiding the model only reorders the default list, so a task
// whose every model has failed still runs on one once it is back.

// dispatchChoiceFor is choiceFor for a run about to start: the models
// task's latest runs failed over off are tried after the rest. Read
// failures are logged and leave the rotation as it is.
func (d Deps) dispatchChoiceFor(ctx context.Context, task model.Task) AgentChoice {
	choice := d.choiceFor(task)
	if d.Store == nil {
		return choice
	}
	if choice.Framework == "" && choice.BackendID == "" && choice.Model == "" {
		if obs, err := d.Store.GetObservation(ctx, task.ID); err == nil && obs != nil && obs.SuspendedSession != nil && obs.SuspendedSession.Token != "" {
			choice.PreferFramework = obs.SuspendedSession.Framework
		}
		if task.Interactive || choice.PreferFramework != "" {
			if runs, err := d.Store.Runs(ctx, task.ID); err == nil {
				for i := len(runs) - 1; i >= 0; i-- {
					r := runs[i]
					if r.AgentFramework == "" && r.Model == "" {
						continue
					}
					if choice.PreferFramework == "" && r.AgentFramework != "" {
						choice.PreferFramework = r.AgentFramework
					}
					if choice.PreferModel == "" && r.Model != "" && (choice.PreferFramework == "" || r.AgentFramework == choice.PreferFramework) {
						choice.PreferModel = r.Model
					}
					break
				}
			}
		}
	}
	avoid, err := d.Store.FailedOverModels(ctx, task.ID)
	if err != nil {
		log.Printf("orchestrator: task %s: reading the models it failed over off: %v", task.ID, err)
		return choice
	}
	choice.AvoidModels = avoid
	return choice
}

// noteFailedOverModels tells the task's conversation, once, when the run
// just suspended makes model.MaxFailoverModels different models in a
// row that have failed it -- the point where trying the next one is no
// longer obviously the answer. A task comment stands in for the
// operator notification DT-S6 adds. Best effort: it cannot fail the run.
func noteFailedOverModels(ctx context.Context, store *model.Store, task model.Task, now time.Time) {
	models, err := store.FailedOverModels(ctx, task.ID)
	if err != nil {
		log.Printf("orchestrator: task %s: counting the models that failed it: %v", task.ID, err)
		return
	}
	if len(models) != model.MaxFailoverModels {
		return
	}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: fmt.Sprintf("grain: %d different models have failed this task's runs in a row (%s). It "+
			"keeps being resumed on the next model of its rotation, but this may be the task rather "+
			"than the models: worth a look.", len(models), strings.Join(models, ", ")),
		CreatedAt: now,
	}); err != nil {
		log.Printf("orchestrator: task %s: saying that %d models failed it: %v", task.ID, len(models), err)
	}
}
