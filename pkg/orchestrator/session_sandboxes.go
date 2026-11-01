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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// warmSessionSandbox is an interactive session task's sandbox held warm
// between turns while the task awaits the person's next message.
type warmSessionSandbox struct {
	TaskID    string
	SandboxID string
	// Key is sessionSandboxKey for the run that built the sandbox: a
	// later turn reuses it only under the same key.
	Key string
	// Grants is sessionGrantsKey for the task as the run that built the
	// sandbox saw it: reconcileSessionSandboxes releases the sandbox as
	// soon as the task's grants no longer match, rather than leaving what
	// a withdrawn grant placed in it until the next turn or the TTL.
	Grants      string
	Sandbox     Sandbox
	Counted     func(context.Context)
	RevokeToken func(string) error
	Shape       model.Commitment
	IdleSince   time.Time
	ExpiresAt   time.Time
	Ledger      *model.Ledger
}

type storeSessionPool struct {
	mu            sync.Mutex
	warm          map[string]*warmSessionSandbox // taskID -> warm sandbox
	bySandbox     map[string]string              // sandboxID -> taskID
	inProcessIdle map[string]time.Time           // sandboxID -> idleSince
	reclaimed     map[string]bool                // taskID -> true if warm sandbox was reclaimed
}

var (
	sessionPoolsMu sync.Mutex
	sessionPools   = map[*model.Store]*storeSessionPool{}
)

func getSessionPool(store *model.Store) *storeSessionPool {
	sessionPoolsMu.Lock()
	defer sessionPoolsMu.Unlock()
	p, ok := sessionPools[store]
	if !ok {
		p = &storeSessionPool{
			warm:          map[string]*warmSessionSandbox{},
			bySandbox:     map[string]string{},
			inProcessIdle: map[string]time.Time{},
			reclaimed:     map[string]bool{},
		}
		sessionPools[store] = p
	}
	return p
}

// IsIdleSessionSandbox reports whether sandboxID is currently held as an
// idle session sandbox (either parked warm between dispatches or waiting
// idle between turns in-process).
func IsIdleSessionSandbox(sandboxID string) bool {
	if sandboxID == "" {
		return false
	}
	sessionPoolsMu.Lock()
	pools := make([]*storeSessionPool, 0, len(sessionPools))
	for _, p := range sessionPools {
		pools = append(pools, p)
	}
	sessionPoolsMu.Unlock()

	for _, p := range pools {
		p.mu.Lock()
		_, isWarm := p.bySandbox[sandboxID]
		_, isInProc := p.inProcessIdle[sandboxID]
		p.mu.Unlock()
		if isWarm || isInProc {
			return true
		}
	}
	return false
}

// WarmSessionSandboxID returns the sandbox ID currently held warm for taskID,
// or "" if none is held.
func WarmSessionSandboxID(store *model.Store, taskID string) string {
	if store == nil || taskID == "" {
		return ""
	}
	p := getSessionPool(store)
	p.mu.Lock()
	defer p.mu.Unlock()
	if sb, ok := p.warm[taskID]; ok {
		return sb.SandboxID
	}
	return ""
}

// WarmSessionSandboxCount returns how many warm session sandboxes are
// currently held for store.
func WarmSessionSandboxCount(store *model.Store) int {
	if store == nil {
		return 0
	}
	p := getSessionPool(store)
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.warm)
}

func sessionSandboxIdleTTL(ctx context.Context, deps Deps) time.Duration {
	if deps.Config.SessionSandboxIdleTTL < 0 {
		return 0
	}
	if deps.Config.SessionSandboxIdleTTL > 0 {
		return deps.Config.SessionSandboxIdleTTL
	}
	if deps.Store != nil {
		if cfg, err := deps.Store.GetConfig(ctx); err == nil && cfg != nil {
			return cfg.SessionSandboxIdleTTLOr()
		}
	}
	return model.DefaultSessionSandboxIdleTTL
}

func releaseWarmSandboxEntry(ctx context.Context, entry *warmSessionSandbox) {
	if entry == nil {
		return
	}
	if entry.Ledger != nil {
		entry.Ledger.Release(model.PoolAgent, entry.SandboxID)
	}
	if entry.Sandbox != nil {
		if err := entry.Sandbox.Release(ctx); err != nil {
			log.Printf("orchestrator: releasing warm session sandbox %s (task %s): %v",
				entry.SandboxID, entry.TaskID, err)
		}
	}
	if entry.Counted != nil {
		entry.Counted(ctx)
	}
	if entry.RevokeToken != nil {
		if err := entry.RevokeToken(entry.SandboxID); err != nil {
			log.Printf("orchestrator: revoking warm session sandbox %s token: %v", entry.SandboxID, err)
		}
	}
}

func parkWarmSessionSandbox(deps Deps, taskID, sandboxID, key, grants string, sb Sandbox, counted func(context.Context), shape model.Commitment, now time.Time, ttl time.Duration) {
	if deps.Store == nil || taskID == "" || sb == nil || ttl <= 0 {
		return
	}
	p := getSessionPool(deps.Store)
	var prev *warmSessionSandbox
	p.mu.Lock()
	if existing, ok := p.warm[taskID]; ok && existing.SandboxID != sandboxID {
		prev = existing
		delete(p.bySandbox, existing.SandboxID)
	}
	entry := &warmSessionSandbox{
		TaskID:      taskID,
		SandboxID:   sandboxID,
		Key:         key,
		Grants:      grants,
		Sandbox:     sb,
		Counted:     counted,
		RevokeToken: deps.RevokeSandboxToken,
		Shape:       shape,
		IdleSince:   now,
		ExpiresAt:   now.Add(ttl),
		Ledger:      deps.Ledger,
	}
	p.warm[taskID] = entry
	p.bySandbox[sandboxID] = taskID
	delete(p.reclaimed, taskID)
	p.mu.Unlock()

	if prev != nil {
		releaseWarmSandboxEntry(context.Background(), prev)
	}
}

// sessionSandboxKey names what a sandbox was built for: its shape, the
// task's target, base and repo access, and the capability grants the task
// holds and the run materialized. Two runs with the same key may share a
// warm sandbox; a run whose key differs from the warm sandbox's gets a fresh one.
func sessionSandboxKey(shape Shape, task model.Task, prepared []model.Materialized) string {
	var b strings.Builder
	fmt.Fprintf(&b, "shape=%+v|", shape)
	b.WriteString(sessionTaskAccessKey(task))
	if len(prepared) > 0 {
		runGrants := make([]string, 0, len(prepared))
		for _, m := range prepared {
			runGrants = append(runGrants, grantKey(m.Grant))
		}
		sort.Strings(runGrants)
		fmt.Fprintf(&b, "|run_grants=%s", strings.Join(runGrants, ","))
	}
	return b.String()
}

// sessionGrantsKey is the task-access and task-grant part of sessionSandboxKey:
// what reconcileSessionSandboxes can check between turns, when no run is there
// to say what it would materialize.
func sessionGrantsKey(task model.Task) string {
	out := sessionTaskAccessKey(task)
	if task.NetworkAccess != "" {
		out += "|net=" + string(task.NetworkAccess)
	}
	return out
}

func sessionTaskAccessKey(task model.Task) string {
	var b strings.Builder
	if task.Target != nil {
		fmt.Fprintf(&b, "target=%s/%s|base=%s|", task.Target.StoredOwner(), task.Target.Name, task.Base)
	}
	if len(task.Reads) > 0 {
		reads := make([]string, 0, len(task.Reads))
		for _, r := range task.Reads {
			reads = append(reads, r.StoredOwner()+"/"+r.Name)
		}
		sort.Strings(reads)
		fmt.Fprintf(&b, "reads=%s|", strings.Join(reads, ","))
	}
	if len(task.Writes) > 0 {
		writes := make([]string, 0, len(task.Writes))
		for _, w := range task.Writes {
			writes = append(writes, w.Repo.StoredOwner()+"/"+w.Repo.Name+"@"+w.Base)
		}
		sort.Strings(writes)
		fmt.Fprintf(&b, "writes=%s|", strings.Join(writes, ","))
	}
	grants := make([]string, 0, len(task.Grants))
	for _, g := range task.Grants {
		grants = append(grants, grantKey(g))
	}
	sort.Strings(grants)
	fmt.Fprintf(&b, "grants=%s", strings.Join(grants, ","))
	return b.String()
}

func grantKey(g model.Grant) string {
	s := g.Capability + ":" + string(g.Via)
	if g.Folder != nil {
		s += ":" + g.Folder.String()
	}
	return s
}

func parkWarmSessionInLedger(deps Deps, taskID string) {
	if deps.Store == nil || deps.Ledger == nil || taskID == "" {
		return
	}
	p := getSessionPool(deps.Store)
	p.mu.Lock()
	entry, ok := p.warm[taskID]
	if !ok {
		p.mu.Unlock()
		return
	}
	sandboxID := entry.SandboxID
	shape := entry.Shape
	idleSince := entry.IdleSince
	p.mu.Unlock()

	deps.Ledger.ParkIdle(model.PoolAgent, sandboxID, shape, idleSince, func() {
		var evicted *warmSessionSandbox
		p.mu.Lock()
		if cur, exists := p.warm[taskID]; exists && cur.SandboxID == sandboxID {
			evicted = cur
			delete(p.warm, taskID)
			delete(p.bySandbox, sandboxID)
			p.reclaimed[taskID] = true
		}
		p.mu.Unlock()
		if evicted != nil {
			// Ledger already removed the entry; clear Ledger field so we don't call Release again inside Ledger.
			evicted.Ledger = nil
			releaseWarmSandboxEntry(context.Background(), evicted)
		}
	})
}

func claimWarmSessionSandbox(deps Deps, taskID string, now time.Time) (*warmSessionSandbox, bool) {
	if deps.Store == nil || taskID == "" {
		return nil, false
	}
	p := getSessionPool(deps.Store)
	var expired *warmSessionSandbox
	p.mu.Lock()
	entry, ok := p.warm[taskID]
	if ok {
		delete(p.warm, taskID)
		delete(p.bySandbox, entry.SandboxID)
		if now.Before(entry.ExpiresAt) {
			delete(p.reclaimed, taskID)
			p.mu.Unlock()
			if entry.Ledger != nil {
				entry.Ledger.Release(model.PoolAgent, entry.SandboxID)
			}
			return entry, false
		}
		expired = entry
		p.reclaimed[taskID] = true
	}
	wasReclaimed := p.reclaimed[taskID]
	delete(p.reclaimed, taskID)
	p.mu.Unlock()

	if expired != nil {
		releaseWarmSandboxEntry(context.Background(), expired)
		return nil, true
	}
	return nil, wasReclaimed
}

func discardWarmSessionSandbox(ctx context.Context, deps Deps, taskID string) bool {
	if deps.Store == nil || taskID == "" {
		return false
	}
	p := getSessionPool(deps.Store)
	p.mu.Lock()
	entry, ok := p.warm[taskID]
	if ok {
		delete(p.warm, taskID)
		delete(p.bySandbox, entry.SandboxID)
	}
	wasReclaimed := p.reclaimed[taskID]
	delete(p.reclaimed, taskID)
	p.mu.Unlock()
	if ok {
		releaseWarmSandboxEntry(ctx, entry)
		return true
	}
	return wasReclaimed
}

func releaseWarmSessionSandbox(ctx context.Context, deps Deps, taskID string, markReclaimed bool) bool {
	if deps.Store == nil || taskID == "" {
		return false
	}
	p := getSessionPool(deps.Store)
	p.mu.Lock()
	entry, ok := p.warm[taskID]
	if ok {
		delete(p.warm, taskID)
		delete(p.bySandbox, entry.SandboxID)
		if markReclaimed {
			p.reclaimed[taskID] = true
		}
	}
	p.mu.Unlock()
	if ok {
		releaseWarmSandboxEntry(ctx, entry)
	}
	return ok
}

// EvictOldestWarmSessionSandboxes evicts up to n warm session sandboxes for
// deps.Store in order of oldest IdleSince first.
func EvictOldestWarmSessionSandboxes(deps Deps, n int) int {
	if deps.Store == nil || n <= 0 {
		return 0
	}
	p := getSessionPool(deps.Store)
	p.mu.Lock()
	if len(p.warm) == 0 {
		p.mu.Unlock()
		return 0
	}
	candidates := make([]*warmSessionSandbox, 0, len(p.warm))
	for _, entry := range p.warm {
		candidates = append(candidates, entry)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].IdleSince.Equal(candidates[j].IdleSince) {
			return candidates[i].IdleSince.Before(candidates[j].IdleSince)
		}
		return candidates[i].TaskID < candidates[j].TaskID
	})
	if n > len(candidates) {
		n = len(candidates)
	}
	evicted := make([]*warmSessionSandbox, 0, n)
	for i := 0; i < n; i++ {
		c := candidates[i]
		delete(p.warm, c.TaskID)
		delete(p.bySandbox, c.SandboxID)
		p.reclaimed[c.TaskID] = true
		evicted = append(evicted, c)
	}
	p.mu.Unlock()

	for _, c := range evicted {
		releaseWarmSandboxEntry(context.Background(), c)
	}
	return len(evicted)
}

func reconcileSessionSandboxes(ctx context.Context, deps Deps, now time.Time) {
	if deps.Store == nil {
		return
	}
	ttl := sessionSandboxIdleTTL(ctx, deps)
	p := getSessionPool(deps.Store)
	p.mu.Lock()
	if len(p.warm) == 0 {
		p.mu.Unlock()
		return
	}
	snapshot := make([]*warmSessionSandbox, 0, len(p.warm))
	for _, entry := range p.warm {
		snapshot = append(snapshot, entry)
	}
	p.mu.Unlock()

	for _, entry := range snapshot {
		if ttl <= 0 || !now.Before(entry.ExpiresAt) {
			releaseWarmSessionSandbox(ctx, deps, entry.TaskID, true)
			continue
		}
		st, err := deps.Store.State(ctx, entry.TaskID)
		if err != nil {
			continue
		}
		// A grant or repository access withdrawn (or added) since the sandbox was
		// built, or keep_running cleared on a non-interactive task: what the run
		// placed in the warm sandbox (or its live token) must not outlive that
		// grant or session mode until TTL expiry.
		if task, terr := deps.Store.GetTask(ctx, entry.TaskID); terr == nil && task != nil &&
			((!task.Interactive && !task.KeepRunning) || sessionGrantsKey(*task) != entry.Grants) {
			releaseWarmSessionSandbox(ctx, deps, entry.TaskID, true)
			continue
		}
		if st == "" || st == model.StateClosed || st == model.StateFailed ||
			st == model.StateCompleted || st == model.StateAwaitingSubmit ||
			taskPaused(ctx, deps.Store, entry.TaskID) {
			releaseWarmSessionSandbox(ctx, deps, entry.TaskID, true)
		}
	}
}

func warmSessionSandboxIDs(store *model.Store) map[string]model.Commitment {
	if store == nil {
		return nil
	}
	p := getSessionPool(store)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.warm) == 0 {
		return nil
	}
	out := make(map[string]model.Commitment, len(p.warm))
	for _, entry := range p.warm {
		out[entry.SandboxID] = entry.Shape
	}
	return out
}

func prepareWarmSandboxesForDispatch(ctx context.Context, deps Deps, now time.Time) func([]dispatch.Dispatch) {
	if deps.Store == nil {
		return func([]dispatch.Dispatch) {}
	}
	reconcileSessionSandboxes(ctx, deps, now)

	p := getSessionPool(deps.Store)
	p.mu.Lock()
	if len(p.warm) == 0 {
		p.mu.Unlock()
		return func([]dispatch.Dispatch) {}
	}
	warmTasks := make(map[string]*warmSessionSandbox, len(p.warm))
	for taskID, entry := range p.warm {
		warmTasks[taskID] = entry
	}
	p.mu.Unlock()

	requested, _ := deps.Store.RunNow(ctx)
	requestedSet := make(map[string]bool, len(requested))
	for _, r := range requested {
		requestedSet[r.TaskID] = true
	}
	readyIDs, _ := deps.Store.Ready(ctx)
	readySet := make(map[string]bool, len(readyIDs))
	for _, id := range readyIDs {
		readySet[id] = true
	}

	var unparked []string
	for taskID, entry := range warmTasks {
		if readySet[taskID] || requestedSet[taskID] {
			if deps.Ledger != nil {
				deps.Ledger.Release(model.PoolAgent, entry.SandboxID)
			}
			unparked = append(unparked, taskID)
		}
	}

	// When capacity is bounded by MaxWorkers, evict the oldest idle warm
	// session sandboxes if other ready/requested tasks need slots.
	if deps.MaxWorkers > 0 {
		idleRemaining := len(warmTasks) - len(unparked)
		if idleRemaining > 0 {
			neededNew := 0
			seen := map[string]bool{}
			for _, r := range requested {
				if _, hasWarm := warmTasks[r.TaskID]; !hasWarm && !seen[r.TaskID] {
					seen[r.TaskID] = true
					neededNew++
				}
			}
			for _, id := range readyIDs {
				if _, hasWarm := warmTasks[id]; !hasWarm && !seen[id] {
					seen[id] = true
					neededNew++
				}
			}
			if neededNew > 0 {
				liveRuns, _ := deps.Store.LiveRuns(ctx)
				totalWanted := len(liveRuns) + len(unparked) + idleRemaining + neededNew
				if totalWanted > deps.MaxWorkers {
					toEvict := totalWanted - deps.MaxWorkers
					if toEvict > idleRemaining {
						toEvict = idleRemaining
					}
					p.mu.Lock()
					var idleCandidates []*warmSessionSandbox
					unparkedSet := make(map[string]bool, len(unparked))
					for _, id := range unparked {
						unparkedSet[id] = true
					}
					for taskID, entry := range p.warm {
						if !unparkedSet[taskID] {
							idleCandidates = append(idleCandidates, entry)
						}
					}
					sort.Slice(idleCandidates, func(i, j int) bool {
						if !idleCandidates[i].IdleSince.Equal(idleCandidates[j].IdleSince) {
							return idleCandidates[i].IdleSince.Before(idleCandidates[j].IdleSince)
						}
						return idleCandidates[i].TaskID < idleCandidates[j].TaskID
					})
					if toEvict > len(idleCandidates) {
						toEvict = len(idleCandidates)
					}
					var evicted []*warmSessionSandbox
					for i := 0; i < toEvict; i++ {
						c := idleCandidates[i]
						delete(p.warm, c.TaskID)
						delete(p.bySandbox, c.SandboxID)
						p.reclaimed[c.TaskID] = true
						evicted = append(evicted, c)
					}
					p.mu.Unlock()
					for _, c := range evicted {
						releaseWarmSandboxEntry(ctx, c)
					}
				}
			}
		}
	}

	return func(dispatched []dispatch.Dispatch) {
		if len(unparked) == 0 {
			return
		}
		dispatchedSet := make(map[string]bool, len(dispatched))
		for _, d := range dispatched {
			dispatchedSet[d.TaskID] = true
		}
		for _, taskID := range unparked {
			if !dispatchedSet[taskID] {
				parkWarmSessionInLedger(deps, taskID)
			}
		}
	}
}

func markInProcessSessionIdle(store *model.Store, ledger *model.Ledger, runID, sandboxID string, shape model.Commitment, idleSince time.Time, evict func()) func() {
	if store != nil && sandboxID != "" {
		p := getSessionPool(store)
		p.mu.Lock()
		p.inProcessIdle[sandboxID] = idleSince
		p.mu.Unlock()
	}
	if ledger != nil && runID != "" {
		ledger.ParkIdle(model.PoolAgent, runID, shape, idleSince, evict)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if store != nil && sandboxID != "" {
				p := getSessionPool(store)
				p.mu.Lock()
				delete(p.inProcessIdle, sandboxID)
				p.mu.Unlock()
			}
			if ledger != nil && runID != "" {
				ledger.UnparkIdle(model.PoolAgent, runID)
			}
		})
	}
}

type relayedRunKey struct {
	store *model.Store
	runID string
}

var inProcessRelayedRuns sync.Map

func takeInProcessRelayed(store *model.Store, runID string) bool {
	if store == nil || runID == "" {
		return false
	}
	_, ok := inProcessRelayedRuns.LoadAndDelete(relayedRunKey{store: store, runID: runID})
	return ok
}

func (c Config) sessionSandboxIdleTTL() time.Duration {
	if c.SessionSandboxIdleTTL < 0 {
		return 0
	}
	if c.SessionSandboxIdleTTL > 0 {
		return c.SessionSandboxIdleTTL
	}
	return model.DefaultSessionSandboxIdleTTL
}

// sessionTurnReserve is how much of a run's wall clock an in-process
// session keeps in hand for the turn a message would wake: a session
// whose deadline is nearer than its idle TTL plus this does not wait
// in-process at all (continueSessionTurns), since a message arriving late
// in the wait would start a turn the clock could cancel mid-answer. Its
// run ends instead, and the next message is answered by a fresh dispatch
// on the warm sandbox, with a clock of its own.
const sessionTurnReserve = 30 * time.Minute

// continueSessionTurns keeps an interactive session's run alive between
// turns: it relays the turn's answer (and proposals and handover), waits
// up to the idle TTL for a person's next message, and resumes the same
// agent session with it.
//
// deadline is the run's wall clock as it stands (runClock.deadline): the
// idle wait is not credited to it, so a run too close to it to wait a
// full TTL and still answer stops here instead (sessionTurnReserve). nil,
// or a zero time, is no deadline.
func continueSessionTurns(
	hostCtx, daemonCtx context.Context,
	store *model.Store,
	cfg Config,
	framework agent.Framework,
	runCfg agent.RunConfig,
	task model.Task,
	runID string,
	deadline func() time.Time,
	comments []model.Comment,
	result *agent.Result,
	runErr error,
) (*agent.Result, error, []model.Comment) {
	ttl := cfg.sessionSandboxIdleTTL()
	if !task.Interactive || task.KeepRunning || !cfg.inProcessSessionWait || ttl <= 0 || store == nil {
		return result, runErr, comments
	}
	if taskKeepRunning(daemonCtx, store, task) {
		return result, runErr, comments
	}
	goal, err := store.TaskGoal(daemonCtx, task.ID)
	if err != nil || goal == nil || goal.Kind != model.GoalPersonCloses {
		return result, runErr, comments
	}

	turnResult := result
	// turnAfter is where the conversation in the turn's own prompt ends
	// (agent.RunConfig.AddendaAfter): what the turn was dispatched with.
	turnAfter := runCfg.AddendaAfter
	for {
		if runErr != nil || turnResult == nil || turnResult.ResumableSession == "" || hostCtx.Err() != nil || daemonCtx.Err() != nil {
			break
		}
		if _, ok := firstToolCallArg(turnResult, "nothing_needed", "reason"); ok {
			break
		}
		if _, ok := firstToolCallArg(turnResult, "ask_question", "question"); ok {
			break
		}
		if _, ok := firstToolCallArg(turnResult, "request_secret", "secret"); ok {
			break
		}
		if _, ok := firstToolCallArg(turnResult, "propose_config_change", "summary"); ok {
			break
		}
		if _, ok, _ := requestedSuspension(daemonCtx, store, task.ID, turnResult); ok {
			break
		}
		if taskPaused(daemonCtx, store, task.ID) {
			break
		}
		if closed, _ := taskClosed(daemonCtx, store, task.ID); closed {
			break
		}

		commentText, hasComment := effectiveCommentOnIssue(turnResult)
		finalText := strings.TrimSpace(turnResult.FinalText)
		if !hasComment && finalText == "" {
			break
		}

		now := cfg.now()
		if err := relayProposedTasks(daemonCtx, store, cfg.sessionReach, cfg.sessionClient, task, turnResult, now); err != nil {
			log.Printf("orchestrator: run %s: relaying proposed tasks in-process: %v", runID, err)
			break
		}
		recordRelayedInProcess(daemonCtx, store, runID, result, "propose_task")
		if err := relayHandover(daemonCtx, store, task, turnResult, now); err != nil {
			log.Printf("orchestrator: run %s: relaying handover in-process: %v", runID, err)
			break
		}
		recordRelayedInProcess(daemonCtx, store, runID, result, "write_handover")

		replyBody := commentText
		var attachments []mcp.ResponseAttachment
		if hasComment {
			staged := stagedAttachments(daemonCtx, store, runID)
			var dropped []mcp.DroppedAttachment
			attachments, dropped = mcp.ResponseAttachmentsTolerant(firstToolCallArgs(turnResult, "comment_on_issue"), staged)
			replyBody = withDroppedAttachments(commentText, dropped)
		} else {
			replyBody = finalText
		}
		if _, err := relayCommentWithAttachments(daemonCtx, store, task, replyBody, attachments, now); err != nil {
			log.Printf("orchestrator: run %s: relaying session answer in-process: %v", runID, err)
			break
		}
		if hasComment {
			recordRelayedInProcess(daemonCtx, store, runID, result, "comment_on_issue")
		}
		inProcessRelayedRuns.Store(relayedRunKey{store: store, runID: runID}, true)

		// What the turn was actually shown: its prompt, and every
		// comment its forked mcpserver handed it since as an addendum on
		// a tool result (Observation.DeliveredCommentID, which that
		// server reports through ui.Client.RecordDelivered). Not the
		// whole conversation as it stands now: a person's message posted
		// after the turn's last tool call, while it wrote this answer,
		// reached it by neither route, and is what wakes the next turn
		// below at once rather than being counted as read
		// (undeliveredHumanComment, grain/task-1774).
		lastID := turnAfter
		if obs, err := store.GetObservation(daemonCtx, task.ID); err != nil {
			log.Printf("orchestrator: run %s: reading what the turn was shown: %v", runID, err)
		} else if obs != nil && obs.DeliveredCommentID != nil && *obs.DeliveredCommentID > lastID {
			lastID = *obs.DeliveredCommentID
		}
		if err := store.AdvanceDelivered(daemonCtx, task.ID, lastID, now); err != nil {
			log.Printf("orchestrator: run %s: advancing delivered comment in-process: %v", runID, err)
		}
		// The conversation through lastID, and nothing after it: what
		// this returns is what RunDispatch marks delivered once the run
		// ends, so a message the wait below gives up on stays undelivered
		// and gets a dispatch of its own.
		if all, err := store.Comments(daemonCtx, task.ID); err == nil {
			comments = comments[:0:0]
			for _, c := range all {
				if c.ID <= lastID {
					comments = append(comments, c)
				}
			}
		}

		// A run whose clock would not survive a full wait and the turn
		// after it stops here: its answer is relayed, and the next
		// message is a fresh dispatch's (sessionTurnReserve).
		if deadline != nil {
			if d := deadline(); !d.IsZero() && time.Until(d) < ttl+sessionTurnReserve {
				log.Printf("orchestrator: run %s: %s left on its clock, too little to wait %s for the session's next message in-process; "+
					"ending the run so the next message is a fresh dispatch's", runID, time.Until(d).Round(time.Second), ttl)
				break
			}
		}

		waitCtx, cancelWait := context.WithTimeout(hostCtx, ttl)
		unmark := markInProcessSessionIdle(store, cfg.sessionLedger, runID, cfg.sessionSandboxName, cfg.sessionShape, now, cancelWait)

		// Woken by AddComment the moment a comment lands in this process
		// (Store.CommentAdded), and otherwise polled at the deployment's
		// own interval for a comment another process wrote, a pause or a
		// close -- reading only the conversation after lastID, never the
		// whole thread again.
		var fresh []model.Comment
		ticker := time.NewTicker(cfg.cancelPollInterval())
	waitLoop:
		for {
			added := store.CommentAdded(task.ID)
			if closed, _ := taskClosed(daemonCtx, store, task.ID); closed {
				break waitLoop
			}
			if taskPaused(daemonCtx, store, task.ID) {
				break waitLoop
			}
			latest, err := store.CommentsAfter(daemonCtx, task.ID, lastID)
			if err == nil && slices.ContainsFunc(latest, func(c model.Comment) bool {
				return c.Author.Actor.Kind == model.PrincipalHuman
			}) {
				fresh = latest
				break waitLoop
			}
			select {
			case <-waitCtx.Done():
				break waitLoop
			case <-ticker.C:
			case <-added:
			}
		}
		ticker.Stop()
		cancelWait()
		unmark()

		if len(fresh) == 0 {
			break
		}
		if latestTask, err := store.GetTask(daemonCtx, task.ID); err == nil && latestTask != nil {
			if !slices.Equal(latestTask.Grants, task.Grants) ||
				!slices.Equal(latestTask.Reads, task.Reads) ||
				!slices.Equal(latestTask.Writes, task.Writes) ||
				latestTask.Model != task.Model ||
				latestTask.NetworkAccess != task.NetworkAccess {
				break
			}
			task = *latestTask
		}
		inProcessRelayedRuns.Delete(relayedRunKey{store: store, runID: runID})

		// Only a person's message wakes the session, but the turn is told
		// everything said since what it last saw, each with who said it
		// (addendumText) -- as a mid-run comment is and a redispatch's
		// prompt is -- rather than bare bodies, so a grain notice or
		// another task's message is neither dropped nor read as the
		// person's own words. This task's own relayed answers are left out.
		var promptBuilder strings.Builder
		for _, c := range fresh {
			if ownRelayedAnswer(c, task.ID) {
				continue
			}
			if promptBuilder.Len() > 0 {
				promptBuilder.WriteString("\n\n")
			}
			promptBuilder.WriteString(addendumText(c))
		}
		comments = append(comments, fresh...)
		nextCfg := runCfg
		nextCfg.ResumeSession = turnResult.ResumableSession
		nextCfg.ContinuePrompt = promptBuilder.String()
		nextCfg.Addenda = addendaPoller(store, task.ID, comments)
		nextCfg.AddendaAfter = lastCommentID(comments)
		turnAfter = nextCfg.AddendaAfter
		// The messages are in front of the turn from here on, in its
		// prompt, as a redispatch's are: marked delivered as it starts
		// rather than once the run ends, since a daemon that adopts the
		// run part-way through this turn finishes it without knowing
		// what its prompt held, and would otherwise read the very
		// message it is answering as one no run was shown.
		if err := store.AdvanceDelivered(daemonCtx, task.ID, turnAfter, cfg.now()); err != nil {
			log.Printf("orchestrator: run %s: recording the messages its next turn is given: %v", runID, err)
		}

		nextResult, nextErr := framework.Run(hostCtx, nextCfg)
		nextResult, nextErr = continueStoppedRun(hostCtx, framework, nextCfg, nextResult, nextErr)
		if nextResult == nil {
			runErr = nextErr
			break
		}
		turnResult = nextResult
		result = agent.ContinueResult(result, nextResult, agent.StopBlock{})
		runErr = nextErr
	}

	return result, runErr, comments
}

// ownRelayedAnswer reports whether c is one of taskID's own run's answers,
// relayed on its behalf (relayQuestion).
func ownRelayedAnswer(c model.Comment, taskID string) bool {
	return c.Author.OnBehalfOf != nil && c.Author.OnBehalfOf.Kind == model.PrincipalAgent &&
		c.Author.OnBehalfOf.ID == taskID
}

// markToolCallsRelayed marks every successful call to name in result
// that is not already marked, and returns their fingerprints
// (toolCallFingerprint).
func markToolCallsRelayed(result *agent.Result, name string) []string {
	if result == nil {
		return nil
	}
	var marked []string
	for i := range result.ToolCalls {
		c := &result.ToolCalls[i]
		if c.Name == name && !c.IsError && !c.Relayed {
			c.Relayed = true
			marked = append(marked, toolCallFingerprint(*c))
		}
	}
	return marked
}

// recordRelayedInProcess is markToolCallsRelayed, written down as well
// (model.Store.RecordRelayedCalls): the mark on result lives only in
// this daemon's memory, and a daemon that adopts the run reads every
// turn's calls back out of its transcript (markRecordedRelayed). A
// failure to write is logged; at worst the adopting daemon relays the
// call a second time, as it did before this was recorded at all.
func recordRelayedInProcess(ctx context.Context, store *model.Store, runID string, result *agent.Result, name string) {
	marked := markToolCallsRelayed(result, name)
	if store == nil || len(marked) == 0 {
		return
	}
	if err := store.RecordRelayedCalls(ctx, runID, marked); err != nil {
		log.Printf("orchestrator: run %s: recording the %s calls relayed in-process: %v", runID, name, err)
	}
}

// markRecordedRelayed marks the calls in result that runID's in-process
// turns already acted on (recordRelayedInProcess), for a daemon that has
// parsed them back out of the run's transcript (ResumeAdoptedRun): the
// earlier turns' answers and proposals are on the task already, and the
// end-of-run pass must act only on the turn still in flight. Matched by
// fingerprint and counted, so a call made twice and relayed once is
// relayed once more.
func markRecordedRelayed(ctx context.Context, store *model.Store, runID string, result *agent.Result) {
	if store == nil || result == nil {
		return
	}
	relayed, err := store.RelayedCalls(ctx, runID)
	if err != nil {
		log.Printf("orchestrator: run %s: reading the calls relayed in-process: %v", runID, err)
		return
	}
	if len(relayed) == 0 {
		return
	}
	for i := range result.ToolCalls {
		c := &result.ToolCalls[i]
		if c.IsError || c.Relayed {
			continue
		}
		fp := toolCallFingerprint(*c)
		if relayed[fp] > 0 {
			relayed[fp]--
			c.Relayed = true
		}
	}
}

// toolCallFingerprint identifies a tool call by its name and arguments,
// the same however the call was read: from the live run's result, or
// parsed again out of its transcript by an adopting daemon. encoding/json
// writes a map's keys in order, so equal arguments give equal bytes.
func toolCallFingerprint(c agent.ToolCall) string {
	args, err := json.Marshal(c.Arguments)
	if err != nil {
		args = []byte(fmt.Sprint(c.Arguments))
	}
	sum := sha256.Sum256(append([]byte(c.Name+"\x00"), args...))
	return hex.EncodeToString(sum[:])
}
