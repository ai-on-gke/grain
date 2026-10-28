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
// (docs/design/durable-tasks.md#DT-2, Continuity), and relays what it
// wrote. Best effort by design: a session too full to take even this, or
// a framework that keeps no session, leaves the handover whatever the
// run wrote last, and nothing here can fail the run being stopped.
func lastTurnForHandover(ctx context.Context, store *model.Store, cfg Config, framework agent.Framework,
	runCfg agent.RunConfig, task model.Task, session string) {
	if framework == nil || session == "" {
		return
	}
	turnCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contextHandoverTimeout)
	defer cancel()
	last := runCfg
	last.Prompt, last.SuspendedPrompt, last.ResumeSession = contextHandoverPrompt, contextHandoverPrompt, session
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
		log.Printf("orchestrator: task %s: relaying the handover written after its context ran out: %v", task.ID, err)
	}
}
