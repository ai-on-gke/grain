package orchestrator

import (
	"context"
	"log"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// continueStoppedRun is the stop rule's fallback (agent.DecideStop) for a
// framework whose CLI does not apply it from inside the run -- antigravity,
// codex, a custom command; claude has its own Stop hook and says so
// (agent.StopRuleFramework). It reads the run that has just ended -- its
// final message and its tool calls -- and, where the hook would have sent
// the agent back, resumes the same CLI session once with the same nudge,
// the way the frameworks' own retry loops resume a session after a usage
// limit, before the run is allowed to end.
//
// Once only, and only for a run that finished cleanly: a run that came
// back with an error has an ending of its own for RunDispatch to
// classify, and the continuation itself is never sent back again. A run
// that leaves no resumable session (custom, which keeps none, or a
// deployment with no session store) is left as it ended: there is no
// conversation to send a nudge into, and starting the task again from its
// prompt to say one sentence would cost far more than the turn it saves.
//
// Nothing the agent wrote decides anything here beyond whether one more
// turn is spent: the continuation runs under the same RunConfig -- the
// same sandbox, tools, grants and deadline -- with only ResumeSession and
// ContinuePrompt set, and ContinuePrompt is one of grain's own constants.
//
// The continuation's own failure does not fail the run. One that never
// started is logged and the first ending stands; one that ran and then
// failed (a usage limit, the wall clock) is merged and returned with its
// error, since its work is already done and RunDispatch reads a partial
// Result beside an error for exactly that.
func continueStoppedRun(ctx context.Context, framework agent.Framework, cfg agent.RunConfig, result *agent.Result, runErr error) (*agent.Result, error) {
	if runErr != nil || result == nil {
		return result, runErr
	}
	if f, ok := framework.(agent.StopRuleFramework); ok && f.EnforcesStopRule() {
		return result, runErr
	}
	decision := agent.DecideStop(cfg.Interactive, result.FinalText, result.ToolCalls)
	if !decision.Block || result.ResumableSession == "" || ctx.Err() != nil {
		return result, runErr
	}
	block := agent.StopBlock{Via: agent.StopViaResume, Reason: decision.Reason}
	cfg.ResumeSession = result.ResumableSession
	cfg.ContinuePrompt = decision.Reason
	next, err := framework.Run(ctx, cfg)
	if next == nil {
		if err != nil {
			log.Printf("orchestrator: run %s: resuming the session for the stop rule: %v", cfg.RunID, err)
		}
		return agent.ContinueResult(result, nil, block), nil
	}
	return agent.ContinueResult(result, next, block), err
}
