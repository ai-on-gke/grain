package orchestrator

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// earlySession records the conversation a live run is in the moment its
// framework names it (agent.RunConfig.OnResumableSession), rather than
// only once framework.Run returns.
//
// The run it is for is the one that never gets as far as RunDispatch's
// own write: a daemon stopped mid-turn for an upgrade or restart cancels
// every live run, and if the drain is cut off -- or the process is
// killed outright -- the session read off agent.Result is never written,
// so the next dispatch starts the task from nothing even though its
// conversation is sitting in the session store. Writing it as soon as it
// is known means the next dispatch resumes it whichever way the run
// ends (RecoverOrphanedRuns leaves model.SuspendedSession alone).
//
// RunDispatch's own write at the end still has the last word, as it
// always did: finish stops this from writing anything further, so a late
// report can never land on top of the run's own answer.
type earlySession struct {
	store     *model.Store
	taskID    string
	runID     string
	framework string
	now       func() time.Time
	// ctx is the run's parent context with its cancellation dropped: a
	// report arriving as the daemon begins to stop is the one most worth
	// keeping.
	ctx context.Context

	mu       sync.Mutex
	finished bool
	recorded string
}

// record is the RunConfig.OnResumableSession callback.
func (e *earlySession) record(token string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.finished || token == "" || token == e.recorded {
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, earlySessionWriteTimeout)
	defer cancel()
	session := &model.SuspendedSession{Framework: e.framework, Token: token, RunID: e.runID, At: e.now()}
	if err := e.store.SetSuspendedSession(ctx, e.taskID, session, e.now()); err != nil {
		// Logged and not retried: RunDispatch's write at the end is the
		// ordinary route, and this one only matters if that never runs.
		log.Printf("orchestrator: run %s: recording the session it is in as soon as it was named: %v", e.runID, err)
		return
	}
	e.recorded = token
}

// finish stops any further early write and reports the token the last
// successful one recorded, "" if none did.
func (e *earlySession) finish() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.finished = true
	return e.recorded
}

// earlySessionWriteTimeout bounds one early write, which runs on the
// goroutine copying the agent's output: a store that is slow to answer
// must not stall the run's own stream for long.
const earlySessionWriteTimeout = 10 * time.Second
