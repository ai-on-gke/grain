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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// AdoptedRun is a live run RecoverRunHosts matched to a run host that is
// still going, as the daemon that adopts it carries it to its end
// (ResumeAdoptedRun, docs/non-disruptive-upgrade.md, Stage 2).
type AdoptedRun struct {
	runhost.Adoption
	// Cursor is the spool cursor the previous daemon persisted: where
	// this one's tail starts, so the transcript has no hole and no
	// duplicate.
	Cursor int64
	// Sandbox is the run's sandbox, claimed back before the startup reap
	// (SandboxReadopter), or nil when the backend could not -- in which
	// case the run is finished as interrupted rather than resumed against
	// a sandbox the reap is about to destroy.
	Sandbox Sandbox
	// SandboxErr is why Sandbox is nil.
	SandboxErr error
}

// ReadoptSandbox claims back the sandbox of an adopted run through
// sandboxes, when the backend can (SandboxReadopter). It runs before the
// startup reap, so the reap spares exactly the sandboxes of the runs that
// were adopted.
func ReadoptSandbox(ctx context.Context, sandboxes Sandboxes, name string) (Sandbox, error) {
	r, ok := sandboxes.(SandboxReadopter)
	if !ok {
		return nil, fmt.Errorf("this deployment's sandbox backend (%T) cannot take back a sandbox a previous process built", sandboxes)
	}
	if name == "" {
		return nil, errors.New("the run recorded no sandbox")
	}
	return r.Readopt(ctx, name)
}

// AdoptHost is where an adopted run's CLI is still running: a run host
// (*runhost.Host) or a grain hostd process (*runhost.Hostd).
type AdoptHost interface {
	// Attach follows the CLI from the persisted cursor to its end, as
	// runhost.Host.Attach does.
	Attach(ctx context.Context, info runhost.Info, cursor int64, tee io.Writer) (string, error)
	// Release frees what info names, and its spool.
	Release(ctx context.Context, info runhost.Info, reason string)
}

var (
	_ AdoptHost = (*runhost.Host)(nil)
	_ AdoptHost = (*runhost.Hostd)(nil)
)

// ResumeAdoptedRun carries a run another daemon started to its end in
// this one: it rebuilds what RunDispatch held on its stack from the store
// and the task, tails the run host from the persisted cursor (appending
// to the live transcript the previous daemon left), parses the run's
// whole stdout through its framework (agent.ResultParser), and then does
// what RunDispatch and runOne would have done -- the same outcome rules
// (classifyRunEnd), the same writes (recordRunEnd), the same pull request
// or salvage (afterAgentRun) -- before releasing the run's sandbox.
//
// The deadline is not reset: it is where the run's own clock started
// (adoptedClockStart) plus the run's budget, and the gap the restart cost
// is credited as wait time (newAdoptedRunClock). A daemon stopping again while it watches detaches
// again (runhost.Host.Detach), leaving all of it for the next one.
//
// A run it cannot resume -- its task is gone, its framework cannot parse
// a spool, its sandbox could not be claimed back -- is finished as
// interrupted with the reason, and its run host freed, which is what the
// adoption pass did with every match before this existed.
func ResumeAdoptedRun(ctx context.Context, deps Deps, host AdoptHost, a AdoptedRun) error {
	store := deps.Store
	cfg := deps.Config
	runID := a.Claim.Run
	run, err := adoptedLiveRun(ctx, store, runID)
	if err != nil {
		return err
	}
	if run == nil {
		// Finished meanwhile (a task closed in the gap, an operator):
		// nothing is left to resume, and nothing may finish it twice.
		releaseRunHost(ctx, host, a.Host, "its run was already finished")
		if a.Sandbox != nil {
			releaseAdoptedSandbox(ctx, deps, a.Sandbox, runID, nil)
		}
		return nil
	}
	var task *model.Task
	keptWarm := false
	if deps.Ledger != nil {
		if !deps.AgentShape.IsZero() {
			deps.Ledger.Charge(model.PoolAgent, runID, deps.AgentShape)
		}
		defer func() {
			deps.Ledger.Release(model.PoolAgent, runID)
			if keptWarm && task != nil {
				parkWarmSessionInLedger(deps, task.ID)
			}
		}()
	}

	// The leases the previous daemon recorded for this run, revoked once
	// it has ended however it ends, as RunDispatch's revokeAll would have.
	// Their task is what a provider reads its credential's team from, so
	// without one they are left for the reaper.
	revokeLeases := func(finishCtx context.Context, framework agent.Framework) {
		if task == nil {
			return
		}
		cc := model.CapabilityContext{Task: *task, Run: *run, Now: cfg.now(), Credentials: cfg.Credentials,
			Deadline: capabilityDeadline(run.StartedAt, cfg.maxRunRuntime())}
		if connection, ok := framework.(CredentialConnection); ok {
			cc.Connection, _ = connection.CredentialConnection()
		}
		revokeRecordedLeases(finishCtx, store, cfg.Capabilities, cc)
	}

	abandon := func(reason string) error {
		releaseRunHost(ctx, host, a.Host, reason)
		if a.Sandbox != nil {
			releaseAdoptedSandbox(ctx, deps, a.Sandbox, runID, run)
		}
		detail := "the daemon driving this run was restarted, and the one that adopted it " + reason +
			"; finished as interrupted and re-queued without a strike"
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
		defer cancel()
		err := recoverRun(finishCtx, store, deps.Client, *run, cfg.now(), detail)
		revokeLeases(finishCtx, nil)
		if err != nil {
			return fmt.Errorf("orchestrator: finishing adopted run %s: %w", runID, err)
		}
		return nil
	}

	task, err = store.GetTask(ctx, run.TaskID)
	if err != nil {
		return abandon("could not read its task: " + err.Error())
	}
	if task == nil {
		return abandon("found its task gone")
	}
	if deps.Ledger != nil && !deps.AgentShape.IsZero() {
		want := (model.Limits{AgentShape: deps.AgentShape}).TaskShape(*task)
		deps.Ledger.Charge(model.PoolAgent, runID, want)
	}
	if a.Sandbox == nil {
		reason := "could not take back its sandbox"
		if a.SandboxErr != nil {
			reason += ": " + a.SandboxErr.Error()
		}
		return abandon(reason)
	}
	if deps.Framework == nil {
		return abandon("has no agent framework to read its result with")
	}
	choice := deps.choiceFor(*task)
	if run.AgentFramework != "" {
		choice.Framework = run.AgentFramework
	}
	if run.Model != "" {
		choice.Model = run.Model
	}
	framework, err := deps.Framework(ctx, choice)
	if err != nil {
		return abandon("could not build its agent framework: " + err.Error())
	}
	defer releaseFramework(framework)
	parser, ok := framework.(agent.ResultParser)
	// A wrapper (the daemon's connection pool hands back one) parses
	// only if what it wraps does.
	if w, wrapped := framework.(interface{ Unwrap() agent.Framework }); wrapped {
		_, ok = w.Unwrap().(agent.ResultParser)
	}
	if !ok {
		return abandon(fmt.Sprintf("cannot read a run host's spool with its framework (%T)", framework))
	}
	cfg = interactiveDispatch(*task, cfg)
	cfg = durableDispatch(*task, cfg)
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		log.Printf("orchestrator: adopted run %s: reading its task's observation: %v", runID, err)
	}
	var preRunBranchSHA string
	if deps.Client != nil && task.Target != nil && (run.Attempt > 1 || obs.RepairInFlight() || hasFixesLink(*task)) {
		if head, err := deps.Client.GetBranchHead(task.Target.Owner, task.Target.Name, task.BranchName()); err == nil && head != nil {
			preRunBranchSHA = head.SHA
		}
	}

	// The run's own clock, carried rather than restarted: the deadline
	// the previous daemon set, with the restart credited.
	clockStart := adoptedClockStart(*run)
	runCtx, cancelRun := context.WithCancelCause(context.WithoutCancel(ctx))
	stopDaemonWatch := context.AfterFunc(ctx, func() { cancelRun(errDaemonStopped) })
	defer stopDaemonWatch()
	var clockBase time.Time
	if runtime := cfg.maxRunRuntime(); runtime > 0 {
		clockBase = clockStart.Add(runtime)
	}
	agentCtx, clock := newAdoptedRunClock(runCtx, clockBase, cfg.runWaitCreditCap(), a.Gap,
		adoptedWaitLedger(ctx, store, runID))
	stopCrediting := cfg.RunWaitCredits.register(task.ID, clock)
	stopCancelling := cfg.TaskCancellers.register(task.ID, cancelRun)
	defer stopCancelling()
	checkTaskClosed(ctx, store, task.ID, cancelRun)
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		watchForTaskClosed(runCtx, ctx, store, task.ID, cfg.cancelPollInterval(), cancelRun)
	}()
	scope := runPauseScope(framework, *task, cfg)
	stopPause := cfg.Pause.register(runID, scope, cfg.now(), cancelRun)
	stopTokenRefresh := keepSandboxTokenFresh(ctx, deps.RefreshSandboxToken, a.Sandbox.Name(), deps.SandboxTokenRefreshInterval)
	var (
		recordedSandboxKey string
		recordedGrantsKey  string
		hasRecordedKeys    bool
	)
	if store != nil {
		// A run with no recorded keys -- one a daemon from before
		// task_run_sandbox_key started, or one whose SetRunSandboxKeys
		// failed -- is never parked warm: what its sandbox was built
		// for cannot be told from the task as it stands now, which may
		// have been narrowed since.
		recordedSandboxKey, recordedGrantsKey, hasRecordedKeys, _ = store.RunSandboxKeys(ctx, runID)
	}
	// Recreatable as runOne's runs are (recreate_sandbox): unregistered
	// before releaseAdoptedSandbox below, which waits out a rebuild in
	// flight before the sandbox it was building is destroyed.
	stopRecreating, degradedRebuild := registerAdoptedRecreation(ctx, deps, *task, *run, a.Sandbox)
	defer stopRecreating()

	// The live transcript the previous daemon was writing, appended to
	// from the cursor it persisted -- and read first, for what the run's
	// earlier attempts wrote before the one in flight. Which part of it
	// that is is settled once Attach has returned the attempt's own
	// stream: a grain hostd spool cannot be read from here any other way.
	var transcriptPath string
	var live []byte
	var tee io.Writer
	if cfg.TranscriptDir != "" {
		transcriptPath = filepath.Join(cfg.TranscriptDir, runID)
		live, _ = os.ReadFile(transcriptPath)
		if f, err := os.OpenFile(transcriptPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
			log.Printf("orchestrator: adopted run %s: reopening its live transcript: %v", runID, err)
		} else {
			defer f.Close()
			tee = f
		}
	}
	stopIdle := func() {}
	if cfg.durable {
		stopIdle = watchIdle(runCtx, idleWatch{store: store, taskID: task.ID, runID: runID,
			transcript: transcriptPath, timeout: cfg.durableIdleTimeout(), cancel: cancelRun,
			renew: func(ctx context.Context, until time.Time) error {
				return deps.capabilityPasses().Extend(ctx, runID, until)
			}})
		defer stopIdle()
	}
	log.Printf("orchestrator: resuming adopted run %s (task %s) from cursor %d; %s of restart credited",
		runID, task.ID, a.Cursor, a.Gap.Round(time.Second))
	stdout, attachErr := host.Attach(agentCtx, a.Host, a.Cursor, tee)

	stopIdle()
	stopCrediting()
	clock.stop()
	stopPause()
	stopTokenRefresh()
	if errors.Is(attachErr, agent.ErrDetached) {
		// Stopping again: the next daemon adopts it from here, with the
		// wait credit carried so far.
		leaveWaitLedger(ctx, store, runID, clock)
		cancelRun(nil)
		<-watcherDone
		return fmt.Errorf("orchestrator: adopted run %s: %w", runID, attachErr)
	}
	cause := context.Cause(agentCtx)
	cancelRun(nil)
	<-watcherDone

	earlier := earlierAttempts(runID, live, []byte(stdout), a.Cursor)
	var result *agent.Result
	var runErr error
	if ap, ok := framework.(agent.AttemptsParser); ok && earlier != "" {
		result, runErr = ap.ParseAttempts(earlier, stdout, attachErr)
	} else {
		result, runErr = parser.ParseResult(stdout, attachErr)
	}
	// An interactive session's earlier turns, continued in-process by the
	// daemon that started it, are in the transcript just parsed, and
	// their answers and proposals were relayed turn by turn: only the
	// turn in flight is this pass's to act on (markRecordedRelayed).
	markRecordedRelayed(ctx, store, runID, result)
	var waitCredit *model.RunWaitCredit
	blocked, credited := clock.ledger()
	if blocked > 0 {
		waitCredit = &model.RunWaitCredit{Blocked: blocked, Credited: credited}
	}
	end := classifyRunEnd(ctx, store, cfg, *task, runID, result, runErr, cause, credited, scope, clockStart)
	resumableSession := ""
	if result != nil {
		resumableSession = result.ResumableSession
	}
	// What the previous daemon recorded early for this run (earlySession)
	// is on the observation, and is this run's own conversation.
	recordedEarly := ""
	if obs != nil && obs.SuspendedSession != nil && obs.SuspendedSession.RunID == runID {
		recordedEarly = obs.SuspendedSession.Token
	}
	if result == nil && resumableSession == "" {
		resumableSession = recordedEarly
	}
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancelFinish()
	// The comments the run's prompt held, marked delivered before
	// recordRunEnd asks whether a message is still waiting, as runOne
	// does: marked after, a message posted while the turn wrote its
	// answer was read as answered when nothing had been marked yet, and
	// the prompt's own message as unseen when an older mark stood.
	if end.outcome == "succeeded" && task.Interactive && store != nil {
		if comments, cerr := store.Comments(finishCtx, task.ID); cerr == nil {
			var promptCutoff int64
			for _, c := range comments {
				if !c.CreatedAt.After(run.StartedAt) && c.ID > promptCutoff {
					promptCutoff = c.ID
				}
			}
			if err := store.AdvanceDeliveredFrom(finishCtx, task.ID, promptCutoff, cfg.now()); err != nil {
				log.Printf("orchestrator: adopted run %s: recording the conversation it was dispatched for: %v", runID, err)
			}
		}
	}
	finishErr := recordRunEnd(ctx, finishCtx, store, cfg, *task, runID, obs, result, end, waitCredit,
		true, resumableSession, recordedEarly)
	if transcriptPath != "" {
		os.Remove(transcriptPath)
	}
	revokeLeases(finishCtx, framework)

	var errs []error
	var afterErr error
	switch {
	case finishErr != nil:
		errs = append(errs, end.runErr, fmt.Errorf("orchestrator: finishing adopted run %s: %w", runID, finishErr))
	default:
		afterErr = afterAgentRun(ctx, deps, *task, runID, result, end.runErr, preRunBranchSHA, "", cfg.now())
		errs = append(errs, afterErr)
	}
	stopRecreating()
	if finishErr == nil && afterErr == nil && end.runErr == nil && task.Interactive && store != nil && !degradedRebuild() {
		cleanupCtx := context.WithoutCancel(ctx)
		finishNow := cfg.now()
		if ttl := sessionSandboxIdleTTL(cleanupCtx, deps); ttl > 0 && !taskPaused(cleanupCtx, store, task.ID) {
			latestTask, terr := store.GetTask(cleanupCtx, task.ID)
			st, serr := store.State(cleanupCtx, task.ID)
			if terr == nil && latestTask != nil && serr == nil && st != "" &&
				st != model.StateClosed && st != model.StateFailed &&
				st != model.StateCompleted && st != model.StateAwaitingSubmit {
				sandboxKey, grantsKey := recordedSandboxKey, recordedGrantsKey
				canPark := hasRecordedKeys
				if canPark && sessionGrantsKey(*latestTask) != grantsKey {
					canPark = false
				}
				if canPark && (deps.Config.Capabilities == nil || !hasScarceGrant(deps.Config.Capabilities, *latestTask)) {
					if sessionSandboxKey(adoptedSessionShape(cleanupCtx, deps, *latestTask), *latestTask, nil) != sandboxKey {
						canPark = false
					}
				}
				if canPark {
					commitShape := (model.Limits{AgentShape: deps.AgentShape}).TaskShape(*latestTask)
					counted := func(cctx context.Context) {
						if usages, err := store.SandboxUsages(cctx); err == nil {
							for _, u := range usages {
								if u.Kind == model.SandboxKindAgent && u.Created > u.Finished {
									ended := cfg.now()
									_ = store.RecordSandboxFinish(cctx, model.SandboxKindAgent, max(ended.Sub(run.StartedAt), 0), ended)
									break
								}
							}
						}
					}
					parkWarmSessionSandbox(deps, task.ID, a.Sandbox.Name(), sandboxKey, grantsKey, a.Sandbox, counted, commitShape, finishNow, ttl)
					keptWarm = true
				}
			}
		}
	}
	if !keptWarm {
		releaseAdoptedSandbox(ctx, deps, a.Sandbox, runID, run)
	}
	return errors.Join(errs...)
}

// adoptedSessionShape is the sandbox Shape runOne would build for task,
// as far as an adopting daemon can tell it from the task alone: what the
// latest task's sessionSandboxKey is computed from before a warm park.
func adoptedSessionShape(ctx context.Context, deps Deps, tk model.Task) Shape {
	sh := Shape{
		CPUs:          tk.SandboxCPUs,
		MemoryMB:      tk.SandboxMemoryMB,
		DiskGB:        tk.SandboxDiskGB,
		NetworkAccess: tk.NetworkAccess,
		GrainSource:   hasGrant(tk, model.SelfDebugCapability),
		Class:         sandboxClassFor(tk),
	}
	if isUnisolated(deps.Sandboxes) {
		sh.OperatorEnv, _ = localTask(ctx, deps.Store, deps.Policy, tk)
	}
	return sh
}

// registerAdoptedRecreation registers an adopted run with
// Config.SandboxRecreations, as runOne does a run it dispatched, and
// returns what unregisters it alongside a predicate reporting whether a
// mid-run rebuild degraded the sandbox's capabilities or isolation shape
// so that it must not be parked warm across turns.
//
// What runOne records after registering -- the capabilities RunDispatch
// materialized and the pass it applied -- was held only by the daemon
// that started the run, so a rebuild cannot put that back. It names
// those capabilities instead (sandboxRecreation.unrestorable): the
// task's grants and the leases recorded against the run.
func registerAdoptedRecreation(ctx context.Context, deps Deps, task model.Task, run model.Run, sb Sandbox) (func(), func() bool) {
	noop := func() bool { return false }
	if deps.Config.SandboxRecreations == nil {
		return func() {}, noop
	}
	tools, err := sb.Tools(ctx)
	if err != nil {
		log.Printf("orchestrator: adopted run %s: reading its sandbox's tools, so it cannot be recreated: %v", run.ID, err)
		return func() {}, noop
	}
	var sandboxRoot string
	if rooted, ok := sb.(rootedSandbox); ok {
		if sandboxRoot, err = rooted.Root(); err != nil {
			log.Printf("orchestrator: adopted run %s: reading its sandbox's root, so it cannot be recreated: %v", run.ID, err)
			return func() {}, noop
		}
	}
	placer, _ := sb.(SandboxPlacer)
	seen := map[string]bool{}
	var unrestorable []string
	add := func(capability string) {
		if capability != "" && !seen[capability] {
			seen[capability] = true
			unrestorable = append(unrestorable, capability)
		}
	}
	for _, g := range task.Grants {
		add(g.Capability)
	}
	if deps.Store != nil {
		if leases, err := deps.Store.RunLeases(ctx, run.ID); err == nil {
			for _, l := range leases {
				add(l.Capability)
			}
		}
	}
	sort.Strings(unrestorable)
	rec := &sandboxRecreation{
		store:              deps.Store,
		cfg:                deps.Config,
		task:               task,
		sandbox:            sb,
		tools:              tools,
		sandboxRoot:        sandboxRoot,
		placer:             placer,
		mint:               deps.MintSandboxToken,
		credentialIdentity: deps.CredentialGitIdentity,
		unrestorable:       unrestorable,
	}
	hasSandboxCap := false
	for _, capName := range unrestorable {
		if capName != model.DurableCapability && capName != model.PriorityDispatchCapability {
			hasSandboxCap = true
			break
		}
	}
	degraded := func() bool {
		return rec.wasRecreated() && (hasSandboxCap || !isUnisolated(deps.Sandboxes))
	}
	return deps.Config.SandboxRecreations.register(task.ID, rec), degraded
}

// leaveWaitLedger records what clock has credited runID with, per source,
// for the daemon that adopts the run next (newAdoptedRunClock): called
// once the run has detached and its clock is stopped, so no report can
// land after it. Best-effort: a run whose ledger is not written loses
// only the credit, and its adopter starts from the restart gap as it did
// before the ledger existed.
func leaveWaitLedger(ctx context.Context, store *model.Store, runID string, clock *runClock) {
	if store == nil {
		return
	}
	sources := clock.sources()
	if len(sources) == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	if err := store.SetRunWaitLedger(wctx, runID, sources); err != nil {
		log.Printf("orchestrator: run %s: recording its wait ledger for the daemon that adopts it: %v", runID, err)
	}
}

// adoptedWaitLedger is what the previous daemon left of runID's wait
// ledger (leaveWaitLedger), or nil when it left none or it cannot be read.
func adoptedWaitLedger(ctx context.Context, store *model.Store, runID string) map[string]time.Duration {
	if store == nil {
		return nil
	}
	ledger, err := store.RunWaitLedger(ctx, runID)
	if err != nil {
		log.Printf("orchestrator: adopted run %s: reading the wait ledger its previous daemon left, so only the restart is credited: %v", runID, err)
		return nil
	}
	return ledger
}

// adoptedClockStart is where run's own clock started, which is where its
// deadline is carried from: the moment its agent got its first turn
// (Store.SetRunAgentStarted), as RunDispatch measured it, so the setup
// before it is not charged to the run's budget a second time. A row the
// previous daemon never stamped -- the write is best-effort -- falls back
// to StartedAt, which errs towards the shorter deadline.
func adoptedClockStart(run model.Run) time.Time {
	if run.AgentStartedAt != nil {
		return *run.AgentStartedAt
	}
	return run.StartedAt
}

// earlierAttempts is what an adopted run's live transcript, live as it
// stood before the attach, holds from before the attempt in flight: a
// framework that retries a run (claude) gives each attempt its own run
// host, so the spool is that attempt's alone, while the transcript has
// every attempt's stream in order. current is that attempt's whole
// stream, as Attach returned it -- for a grain hostd process, whose spool
// is not on this daemon's disk, there is nowhere else to read it from.
//
// The attempt in flight starts where its own stream's head -- what the
// previous daemon read of it, up to the persisted cursor -- was last
// written to the transcript; a cursor of zero has only the stream's first
// line to find it by, and a transcript without it holds none of this
// attempt yet. A transcript that does not hold the head the cursor says
// it does cannot be split, and gives no earlier attempts rather than
// ones that may repeat this one.
func earlierAttempts(runID string, live, current []byte, cursor int64) string {
	if len(live) == 0 {
		return ""
	}
	head := current[:min(cursor, int64(len(current)))]
	if len(head) == 0 {
		if i := bytes.IndexByte(current, '\n'); i >= 0 {
			head = current[:i+1]
		}
	}
	if len(head) == 0 {
		if cursor == 0 {
			return string(live)
		}
		log.Printf("orchestrator: adopted run %s: its attempt in flight returned none of the %d bytes "+
			"the previous daemon read, so its earlier attempts are left out of its stored transcript", runID, cursor)
		return ""
	}
	if i := bytes.LastIndex(live, head); i >= 0 {
		return string(live[:i])
	}
	if cursor == 0 {
		return string(live)
	}
	log.Printf("orchestrator: adopted run %s: its live transcript does not hold the %d bytes of its spool "+
		"the previous daemon read, so its earlier attempts are left out of its stored transcript", runID, cursor)
	return ""
}

// adoptedLiveRun returns runID's row if it is still live, or nil.
func adoptedLiveRun(ctx context.Context, store *model.Store, runID string) (*model.Run, error) {
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading live runs: %w", err)
	}
	for _, r := range runs {
		if r.ID == runID {
			return &r, nil
		}
	}
	return nil, nil
}

// releaseRunHost frees an adopted run's run host (or hostd process) and
// its spool.
func releaseRunHost(ctx context.Context, host AdoptHost, info runhost.Info, reason string) {
	log.Printf("orchestrator: freeing adopted run host %s (run %s): %s", info.Name, info.Labels.Run, reason)
	host.Release(ctx, info, reason)
}

// releaseAdoptedSandbox is runOne's sandbox release for an adopted run:
// the sandbox, its lifetime finish count, and the token it reached the git proxy with.
func releaseAdoptedSandbox(ctx context.Context, deps Deps, sb Sandbox, runID string, run *model.Run) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	if err := sb.Release(cctx); err != nil {
		log.Printf("orchestrator: releasing adopted run %s's sandbox: %v", runID, err)
	}
	if deps.Store != nil && run != nil {
		if usages, err := deps.Store.SandboxUsages(cctx); err == nil {
			for _, u := range usages {
				if u.Kind == model.SandboxKindAgent && u.Created > u.Finished {
					ended := deps.Config.now()
					_ = deps.Store.RecordSandboxFinish(cctx, model.SandboxKindAgent, max(ended.Sub(run.StartedAt), 0), ended)
					break
				}
			}
		}
	}
	if deps.RevokeSandboxToken != nil {
		if err := deps.RevokeSandboxToken(sb.Name()); err != nil {
			log.Printf("orchestrator: revoking adopted run %s's sandbox token: %v", runID, err)
		}
	}
}
