package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
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
