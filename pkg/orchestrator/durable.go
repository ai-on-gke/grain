package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// durable.go is a durable task's run (docs/design/durable-tasks.md#DT-1,
// model.Task.IsDurable): no wall-clock deadline, no wait-credit cap, an
// idle watchdog in their place, and setup failures that suspend rather
// than count.

// errRunIdle is context.Cause's report, by identity, for a durable run
// its idle watchdog stopped (watchIdle). It ends as a suspension of kind
// model.SuspendIdle, and the task is resumed on the next cycle.
var errRunIdle = errors.New("orchestrator: durable run went idle")

// durableLeaseTerm is how long a durable run's capability leases are
// minted for (capabilityDeadline with no runtime): the longest any
// provider holds a lease to (gemini-key's and gcp-sets' 24h backstops).
// Its idle watchdog renews them before they run out.
const durableLeaseTerm = 24 * time.Hour

// durableDispatch gives a durable task's run no clock: maxRunRuntime is 0
// for it, which newRunClock, capabilityDeadline and the prompt each read
// as none, and runWaitCreditCap is 0, since with no deadline there is
// nothing to credit back. Applied after interactiveDispatch, so it wins
// over any longer budget a console or chat gave the run.
func durableDispatch(task model.Task, cfg Config) Config {
	if task.IsDurable() {
		cfg.durable = true
	}
	return cfg
}

// runWaitCreditCap is RunWaitCreditCap, and 0 for a durable run.
func (c Config) runWaitCreditCap() time.Duration {
	if c.durable {
		return 0
	}
	return RunWaitCreditCap
}

// runHostDeadline is the latest a run host may let this run live: its
// budget plus the most credit it can earn, or none (the zero time) for a
// durable run.
func runHostDeadline(cfg Config) time.Time {
	if cfg.durable {
		return time.Time{}
	}
	return cfg.now().Add(cfg.maxRunRuntime() + RunWaitCreditCap)
}

// idleDetail is the detail an idle suspension is recorded with.
func idleDetail(timeout time.Duration) string {
	return fmt.Sprintf("no transcript output, tool call or declared wait for %s", humanDuration(timeout))
}

// idleWatch is what watchIdle reads and calls for one durable run.
type idleWatch struct {
	store         *model.Store
	taskID, runID string
	// transcript is the run's live transcript file (agent.RunConfig's
	// TranscriptPath): it grows with the agent's output and with every
	// tool call starting and finishing. "" when the deployment keeps none.
	transcript string
	timeout    time.Duration
	// poll is how often to look; 0 is a sixth of timeout, at most five
	// minutes.
	poll   time.Duration
	cancel context.CancelCauseFunc
	// renew, if set, renews the run's capability leases to the time it is
	// given (Config.renewLeases).
	renew func(ctx context.Context, until time.Time) error
	// now is time.Now unless a test drives it.
	now func() time.Time
}

// idleWaitSlack is how much later than a declared wait's activity stamp
// the transcript has to change for the wait to count as returned: the
// call's own start is written to both at about the same moment.
const idleWaitSlack = 30 * time.Second

// watchIdle is the idle watchdog that replaces a durable run's deadline:
// it cancels the run with errRunIdle once w.timeout has passed with no
// transcript output, no tool call starting or finishing, and the run not
// blocked inside a declared wait (mcp.IsDeclaredWait: wait_for_checks,
// wait_for_task, a CI job, a chat wait for a person). A wedged CLI is
// caught without bounding a run that is still working. It also renews the
// run's leases (durableLeaseTerm) as they come due. The returned func
// stops it.
func watchIdle(ctx context.Context, w idleWatch) func() {
	if w.now == nil {
		w.now = time.Now
	}
	poll := w.poll
	if poll <= 0 {
		poll = min(w.timeout/6, 5*time.Minute)
	}
	ctx, stop := context.WithCancel(ctx)
	started := w.now()
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		last, renewed := started, started
		var size int64 = -1
		var mod time.Time
		var stamp time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			now := w.now()
			if w.transcript != "" {
				if fi, err := os.Stat(w.transcript); err == nil {
					if fi.Size() != size || !fi.ModTime().Equal(mod) {
						size, mod = fi.Size(), fi.ModTime()
						last = now
					}
				}
			}
			if w.store != nil {
				act, err := w.store.TaskActivityOf(ctx, w.taskID)
				if err == nil && act != nil && act.At != nil {
					if act.At.After(stamp) {
						stamp = *act.At
						last = now
					}
					// Still inside a declared wait: the activity says
					// one began, and the transcript has not moved since
					// to say it returned.
					if mcp.IsDeclaredWait(act.Note) && !mod.After(stamp.Add(idleWaitSlack)) {
						last = now
					}
				}
			}
			if w.renew != nil && now.Sub(renewed) >= durableLeaseTerm/4 {
				if err := w.renew(ctx, capabilityDeadline(now, 0)); err != nil {
					log.Printf("orchestrator: durable run %s: renewing its capability leases: %v", w.runID, err)
				} else {
					renewed = now
				}
			}
			if now.Sub(last) >= w.timeout {
				log.Printf("orchestrator: durable run %s of task %s: %s; suspending it",
					w.runID, w.taskID, idleDetail(w.timeout))
				w.cancel(errRunIdle)
				return
			}
		}
	}()
	return stop
}

// execTunnelAlert* are when noteExecTunnelRate raises its alert: this
// many runs, across the whole deployment, had the cluster's exec
// transport fail under them within this long.
const (
	execTunnelAlertRuns   = 3
	execTunnelAlertWindow = 5 * time.Minute
)

// ExecTunnelSubject is the subject of the deployment-level notification
// raised while sandboxes keep failing on the cluster's exec transport.
const ExecTunnelSubject = "cluster.exec-tunnel"

// noteExecTunnelRate raises ExecTunnelSubject once execTunnelAlertRuns
// runs -- of any task -- have had the cluster's exec transport fail
// under them within execTunnelAlertWindow of at: a setup that failed or
// was retried on it, or a run that reached its agent and then had
// sandbox tool calls fail on it past the exec runner's own retries
// (model.ExecTransportFailure, counted once per run however many of its
// calls failed). One such failure is weather that the retry outlasts;
// several in a few minutes is the control plane failing to reach its
// nodes, which no task can fix and an operator should know about. It is
// raised again (bumped, not duplicated) for as long as the rate stays
// high, and clears itself half an hour after the last raise.
//
// A run's tool-call failures are recorded when it finishes, since that
// is when its calls are seen, so a long run counts at its end rather
// than at the call. That is late for one run and on time for the many
// that a failing tunnel ends quickly.
//
// Called after the failing run is recorded, so it is among those counted.
// A failure to count or raise is logged rather than returned: the alert
// is a signal about the run's failure, not part of recording it.
func noteExecTunnelRate(ctx context.Context, store *model.Store, at time.Time) {
	since := at.Add(-execTunnelAlertWindow)
	details, err := store.SetupFailureDetailsSince(ctx, since)
	if err != nil {
		log.Printf("orchestrator: counting recent exec-tunnel setup failures: %v", err)
		return
	}
	setups := 0
	for _, d := range details {
		if kubectlgate.IsTransientTransport(d) {
			setups++
		}
	}
	midRun, err := store.ExecTransportFailuresSince(ctx, since)
	if err != nil {
		log.Printf("orchestrator: counting recent exec-tunnel tool-call failures: %v", err)
		return
	}
	calls := 0
	for _, f := range midRun {
		calls += f.Calls
	}
	if setups+len(midRun) < execTunnelAlertRuns {
		return
	}
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  ExecTunnelSubject,
		Severity: model.NotificationWarning,
		Title:    "The cluster's exec tunnel is failing",
		Body: fmt.Sprintf("In the last %s, %d sandboxes failed setup and %d running agents had tool calls fail "+
			"(%d calls) because the Kubernetes API server could not reach the node's kubelet (\"unable to upgrade "+
			"connection: error dialing backend\", or a dial to the API server itself). grain retries setups "+
			"automatically, but while this lasts runs start late and agent tool calls fail. Check the control "+
			"plane's konnectivity agents and the nodes' kubelets; `grain failures` counts them over a longer window.",
			execTunnelAlertWindow, setups, len(midRun), calls),
		RaisedBy:   "automation:orchestrator",
		RaisedAt:   at,
		ClearsWhen: &model.NotificationClear{Kind: model.ClearsOnTTL, TTL: "30m"},
	}); err != nil {
		log.Printf("orchestrator: raising %s: %v", ExecTunnelSubject, err)
	}
}

// transientSetupRetries is how long each automatic retry of a setup that
// failed on the cluster's exec transport waits (finishSetupFailed): long
// enough apart that a tunnel which drops for a minute or two is outlasted,
// short enough that a person waiting on a chat turn is answered by the
// retry rather than asked to say it again.
var transientSetupRetries = []time.Duration{10 * time.Second, 30 * time.Second, 90 * time.Second}

// finishSetupFailed finishes runID, whose setup failed, the way its task
// calls for: an ordinary task's as model.SetupFailedOutcome, a counted
// failure; a durable task's as a suspension of kind model.SuspendSetup,
// never a strike, held for dispatch.DurableSetupBackoff. The
// model.DurableSetupFailingAfter'th setup suspension in a row raises
// model.DurableSetupFailingSubject -- logged until operator notifications
// exist (docs/design/durable-tasks.md#DT-5, DT-S6).
//
// Any task's setup that failed on the path to its sandbox's container
// (kubectlgate.IsTransientTransport: the cluster's exec tunnel failing,
// an API server that could not be reached) is suspended rather than
// failed for its first len(transientSetupRetries) tries in a row, each
// held for the next of those waits: the cluster's weather, not the
// task's, and a suspension keeps the Run now request and priority
// standing the run started with (DT-3). Only the try after them ends
// setup-failed.
func finishSetupFailed(ctx context.Context, store *model.Store, taskID, runID string, at time.Time, detail string) error {
	durable, err := store.IsDurable(ctx, taskID)
	if err == nil && !durable && kubectlgate.IsTransientTransport(detail) {
		n, cerr := store.SuspensionsInARow(ctx, taskID, model.SuspendSetup)
		if cerr != nil {
			log.Printf("orchestrator: task %s: counting its setup retries: %v", taskID, cerr)
		} else if n < len(transientSetupRetries) {
			wait := transientSetupRetries[n]
			resumeAt := at.Add(wait)
			log.Printf("orchestrator: run %s of task %s: setup failed on the cluster's exec transport; "+
				"retrying in %s (%d of %d): %s", runID, taskID, wait, n+1, len(transientSetupRetries), detail)
			defer noteExecTunnelRate(ctx, store, at)
			return store.Suspend(ctx, model.Suspension{RunID: runID, Kind: model.SuspendSetup,
				Detail:   fmt.Sprintf("%s; grain retries it automatically in %s", detail, wait),
				ResumeAt: &resumeAt, CreatedAt: at})
		} else {
			detail = fmt.Sprintf("%s (the cluster's exec tunnel is failing; %d automatic retries did not get past it)",
				detail, n)
			defer noteExecTunnelRate(ctx, store, at)
		}
	}
	if err != nil || !durable {
		return errors.Join(err, store.FinishRun(ctx, runID, at, model.SetupFailedOutcome, detail))
	}
	resumeAt := at.Add(dispatch.DurableSetupBackoff)
	if err := store.Suspend(ctx, model.Suspension{RunID: runID, Kind: model.SuspendSetup, Detail: detail,
		ResumeAt: &resumeAt, CreatedAt: at}); err != nil {
		return err
	}
	n, err := store.SuspensionsInARow(ctx, taskID, model.SuspendSetup)
	if err != nil {
		log.Printf("orchestrator: task %s: counting its setup suspensions: %v", taskID, err)
		return nil
	}
	if n >= model.DurableSetupFailingAfter {
		log.Printf("orchestrator: %s: durable task %s has failed setup %d times in a row; last: %s",
			model.DurableSetupFailingSubject(taskID), taskID, n, detail)
	}
	return nil
}
