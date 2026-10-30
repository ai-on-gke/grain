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

package model

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// The pools a sandbox belongs to, as the ledger names them. They are
// a label rather than a quota: what a pool buys is a sentence that can
// say where the machine went ("18 of it in 3 agent sandboxes, 32 in 4 CI
// jobs"), and nothing in the arithmetic treats one differently from the
// other. That is the whole of grain/task-28: the agent pool used to hold
// a standing reservation the CI pool was admitted *underneath*, and all
// pools now share one total.
const (
	// PoolAgent is a sandbox an agent run holds -- an ordinary worker, a
	// merge-queue repair, or a console/director session answering a turn.
	PoolAgent = "agent"
	// PoolCI is a sandbox one grain CI job holds.
	PoolCI = "ci"
	// PoolPersistent is a persistent sandbox's up-front reservation.
	PoolPersistent = "persistent"
	// PoolStanding is a standing/serving sandbox's commitment while live.
	PoolStanding = "standing"
)

// Ledger is what this deployment's *live* sandboxes commit, right now,
// across every team and both pools: the one place a shape is charged
// when a sandbox is admitted and given back when it goes away.
//
// It replaces the arrangement grain/task-28 was filed against, in which
// the two pools each held their own idea of the machine and neither could
// see the other's. The agent side was admitted against the whole budget
// counting only agent runs; CI was admitted against the budget less a
// *standing reservation* for every agent slot the deployment was
// configured for, occupied or not (the late Limits.AgentReserve). So an
// idle deployment configured for seven agent runs held half a host
// against CI while sitting at load 0.00, and raising max_workers took
// capacity away from CI whether or not a worker ever used it. Neither
// number was a lie about what was configured; both were lies about what
// was running.
//
// One ledger of live commitments has the property the two halves could
// not have between them: whatever admits a sandbox -- dispatch starting a
// worker, a merge repair, the Director taking its turn, or pkg/ci's gate
// starting a job -- tests and charges the same total under the same
// mutex, so two admissions racing for the last of a budget cannot both
// have it (Admit). An idle slot costs nothing, because there is nothing
// live to charge for it.
//
// The counts are elsewhere and stay there: whatever ceiling a caller
// imposes on workers, mergers and CI jobs (Limits.Admits) is enforced
// apart from this. Both bounds apply, and whichever binds first holds the
// work.
//
// The zero value is usable and a nil *Ledger is too: every method below
// tolerates one, so a caller with no deployment to charge -- a test
// driving dispatch directly, a one-shot cycle -- behaves exactly as it
// did before a ledger existed.
type Ledger struct {
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time
	// Wake, when set, is called when a live commitment is released from
	// this ledger while an agent sandbox is waiting for room in the
	// shared budget -- so the reconcile loop runs a dispatch cycle
	// immediately rather than waiting out its poll interval.
	Wake func()

	mu             sync.Mutex
	held           map[ledgerKey]ledgerEntry
	agentWaiting   bool
	agentWaitShape Commitment
	change         chan struct{}
}

func (l *Ledger) notifyChangeLocked() {
	if l.change != nil {
		close(l.change)
		l.change = nil
	}
}

// ledgerKey is what one entry is filed under: the pool and the holder's
// own id (a run id for an agent sandbox, the gate's own sequence number
// for a CI job). Two pools may use the same id without colliding.
type ledgerKey struct {
	Pool string
	ID   string
}

type ledgerEntry struct {
	Shape     Commitment
	Since     time.Time
	Idle      bool
	IdleSince time.Time
	Evict     func()
}

func (l *Ledger) now() time.Time {
	if l != nil && l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Live is what every sandbox this ledger knows about commits between
// them -- the figure every admission is tested against.
func (l *Ledger) Live() Commitment {
	if l == nil {
		return Commitment{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live()
}

// live is Live with the mutex already held.
func (l *Ledger) live() Commitment {
	var out Commitment
	for _, e := range l.held {
		out = out.Plus(e.Shape)
	}
	return out
}

// nonIdleLive is live excluding idle session sandboxes that can be evicted
// on demand under capacity pressure.
func (l *Ledger) nonIdleLive() Commitment {
	var out Commitment
	for _, e := range l.held {
		if e.Idle {
			continue
		}
		out = out.Plus(e.Shape)
	}
	return out
}

// liveWithout is live excluding whatever (pool, id) already holds, so a
// repeated admission for the same holder tests the replacement rather than
// adding the new shape on top of the old.
func (l *Ledger) liveWithout(pool, id string) Commitment {
	var out Commitment
	skip := ledgerKey{Pool: pool, ID: id}
	for k, e := range l.held {
		if k == skip {
			continue
		}
		out = out.Plus(e.Shape)
	}
	return out
}

// nonIdleLiveWithout is liveWithout excluding idle entries.
func (l *Ledger) nonIdleLiveWithout(pool, id string) Commitment {
	var out Commitment
	skip := ledgerKey{Pool: pool, ID: id}
	for k, e := range l.held {
		if k == skip || e.Idle {
			continue
		}
		out = out.Plus(e.Shape)
	}
	return out
}

// LiveIn is Live narrowed to one pool. Nothing is admitted or refused on
// it: it is for the sentence a refusal says and the figure a pane shows,
// which is the only thing a pool is for here.
func (l *Ledger) LiveIn(pool string) Commitment {
	if l == nil {
		return Commitment{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out Commitment
	for k, e := range l.held {
		if k.Pool == pool {
			out = out.Plus(e.Shape)
		}
	}
	return out
}

// Count is how many sandboxes of one pool are live.
func (l *Ledger) Count(pool string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k := range l.held {
		if k.Pool == pool {
			n++
		}
	}
	return n
}

// IdleCount is how many sandboxes of one pool are currently held idle (e.g.
// warm session sandboxes awaiting the next message).
func (l *Ledger) IdleCount(pool string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, e := range l.held {
		if k.Pool == pool && e.Idle {
			n++
		}
	}
	return n
}

// LedgerHolder is one entry as Holders reports it: which pool and which
// id it is filed under, what it is holding, and when it was charged.
type LedgerHolder struct {
	Pool      string
	ID        string
	Shape     Commitment
	Since     time.Time
	Idle      bool
	IdleSince time.Time
}

// Holders is every live entry this ledger has, broken out by its own
// pool and id -- what LiveIn and Describe only ever aggregate, and what
// a caller asking "is everything in my way held by *these specific*
// holders" cannot answer without. ci.SharedGate's own Stuck hook is the
// one caller: it has a run id and a short list of the agent runs that
// run's own tasks are waiting on, and has to know whether every scrap of
// capacity on the host belongs to one of them before it dares decide
// that no release is coming.
//
// Order is undefined, as a map's always is. Empty rather than nil where
// this ledger holds nothing, so a caller can range over the result
// without a nil check.
func (l *Ledger) Holders() []LedgerHolder {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LedgerHolder, 0, len(l.held))
	for k, e := range l.held {
		out = append(out, LedgerHolder{
			Pool:      k.Pool,
			ID:        k.ID,
			Shape:     e.Shape,
			Since:     e.Since,
			Idle:      e.Idle,
			IdleSince: e.IdleSince,
		})
	}
	return out
}

// Verdict is what budget says about one more sandbox of shape want,
// against everything this ledger is already holding -- the read-only
// look-before-you-leap a caller makes before it decides to ask for
// capacity at all.
//
// It is not what enforces anything: two callers can both read an
// admission out of this and only one of them can have it. Admit is the
// test that also takes.
func (l *Ledger) Verdict(budget Budget, want Commitment) BudgetVerdict {
	if l == nil {
		return budget.Admits(Commitment{}, want)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := budget.Admits(l.live(), want)
	if !v.OK {
		if nonIdle := budget.Admits(l.nonIdleLive(), want); nonIdle.OK {
			return nonIdle
		}
	}
	return v
}

// Admits is Verdict's boolean.
func (l *Ledger) Admits(budget Budget, want Commitment) bool {
	return l.Verdict(budget, want).OK
}

// VerdictException is Verdict under the explicit exception policy
// (Budget.AdmitsException): at most one exception sandbox may exceed the
// budget ceiling when existing commitments are still within cap.
func (l *Ledger) VerdictException(budget Budget, want Commitment) BudgetVerdict {
	if l == nil {
		return budget.AdmitsException(Commitment{}, want)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := budget.AdmitsException(l.live(), want)
	if !v.OK {
		if nonIdle := budget.AdmitsException(l.nonIdleLive(), want); nonIdle.OK {
			return nonIdle
		}
	}
	return v
}

// AdmitsException is VerdictException's boolean.
func (l *Ledger) AdmitsException(budget Budget, want Commitment) bool {
	return l.VerdictException(budget, want).OK
}

// Admit tests one more sandbox of shape want against budget and, if it
// fits, charges it under the same lock -- the whole point of this type.
// The returned release gives the charge back and is safe to call more
// than once; it is nil on a refusal, and there is nothing owed then.
//
// A charge already filed under this pool and id is replaced rather than
// added to, so an admission repeated for the same holder (a run the
// cycle charged and a reconcile then re-derived) cannot double-spend the
// budget.
//
// Release(pool, id) is the other way to give it back, and the one the
// agent side uses: a run's sandbox outlives the cycle that admitted it,
// so whoever finishes the run has an id and no closure.
func (l *Ledger) Admit(budget Budget, pool, id string, want Commitment) (BudgetVerdict, func()) {
	return l.AdmitAs(budget, pool, id, want, false)
}

// VerdictAs is Verdict for a base-branch repair when repair is set:
// held against the whole budget, its RepairReserve included
// (Budget.AdmitsRepair).
func (l *Ledger) VerdictAs(budget Budget, want Commitment, repair bool) BudgetVerdict {
	if l == nil {
		return budget.AdmitsAs(Commitment{}, want, repair)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := budget.AdmitsAs(l.live(), want, repair)
	if !v.OK {
		if nonIdle := budget.AdmitsAs(l.nonIdleLive(), want, repair); nonIdle.OK {
			return nonIdle
		}
	}
	return v
}

// evictIdleForLocked evicts idle entries in oldest-IdleSince-first order
// when doing so allows fits to succeed. Must be called with l.mu held.
func (l *Ledger) evictIdleForLocked(pool, id string, fits func(Commitment) BudgetVerdict) (BudgetVerdict, []func()) {
	verdict := fits(l.liveWithout(pool, id))
	if verdict.OK {
		return verdict, nil
	}
	if !fits(l.nonIdleLiveWithout(pool, id)).OK {
		return verdict, nil
	}
	type candidate struct {
		key   ledgerKey
		entry ledgerEntry
	}
	skip := ledgerKey{Pool: pool, ID: id}
	var idles []candidate
	for k, e := range l.held {
		if k == skip || !e.Idle {
			continue
		}
		idles = append(idles, candidate{key: k, entry: e})
	}
	sort.Slice(idles, func(i, j int) bool {
		if !idles[i].entry.IdleSince.Equal(idles[j].entry.IdleSince) {
			return idles[i].entry.IdleSince.Before(idles[j].entry.IdleSince)
		}
		if !idles[i].entry.Since.Equal(idles[j].entry.Since) {
			return idles[i].entry.Since.Before(idles[j].entry.Since)
		}
		return idles[i].key.ID < idles[j].key.ID
	})
	var evicts []func()
	for _, c := range idles {
		delete(l.held, c.key)
		if c.entry.Evict != nil {
			evicts = append(evicts, c.entry.Evict)
		}
		verdict = fits(l.liveWithout(pool, id))
		if verdict.OK {
			break
		}
	}
	if len(evicts) > 0 {
		l.notifyChangeLocked()
	}
	return verdict, evicts
}

// AdmitAs is Admit for a base-branch repair when repair is set, which
// may spend the budget's RepairReserve.
func (l *Ledger) AdmitAs(budget Budget, pool, id string, want Commitment, repair bool) (BudgetVerdict, func()) {
	if l == nil {
		return budget.AdmitsAs(Commitment{}, want, repair), func() {}
	}
	l.mu.Lock()
	verdict, evicts := l.evictIdleForLocked(pool, id, func(committed Commitment) BudgetVerdict {
		return budget.AdmitsAs(committed, want, repair)
	})
	if !verdict.OK {
		l.mu.Unlock()
		return verdict, nil
	}
	l.charge(pool, id, want)
	l.mu.Unlock()
	for _, evict := range evicts {
		evict()
	}
	return verdict, l.releaser(pool, id)
}

// AdmitException tests one more sandbox of shape want under the explicit
// exception policy (Budget.AdmitsException) and, if admitted, charges it
// under the same lock.
//
// Used by console/director sessions and deadlock-breaking CI jobs: an
// exception may exceed the budget ceiling by at most one sandbox while
// existing commitments are within the cap. Once committed exceeds the cap,
// further exceptions and ordinary work wait until committed drops back
// within the cap.
func (l *Ledger) AdmitException(budget Budget, pool, id string, want Commitment) (BudgetVerdict, func()) {
	if l == nil {
		return budget.AdmitsException(Commitment{}, want), func() {}
	}
	l.mu.Lock()
	verdict, evicts := l.evictIdleForLocked(pool, id, func(committed Commitment) BudgetVerdict {
		return budget.AdmitsException(committed, want)
	})
	if !verdict.OK {
		l.mu.Unlock()
		return verdict, nil
	}
	l.charge(pool, id, want)
	l.mu.Unlock()
	for _, evict := range evicts {
		evict()
	}
	return verdict, l.releaser(pool, id)
}

// ParkIdle records or converts an entry in pool under id as an idle
// commitment (such as a warm session sandbox between turns) that counts
// toward Live/Count honestly and is evicted oldest-first when a new
// admission needs its capacity.
func (l *Ledger) ParkIdle(pool, id string, want Commitment, idleSince time.Time, evict func()) func() {
	if l == nil {
		return func() {}
	}
	l.mu.Lock()
	if l.held == nil {
		l.held = map[ledgerKey]ledgerEntry{}
	}
	now := l.now()
	if idleSince.IsZero() {
		idleSince = now
	}
	key := ledgerKey{Pool: pool, ID: id}
	since := now
	if existing, ok := l.held[key]; ok && !existing.Since.IsZero() {
		since = existing.Since
	}
	l.held[key] = ledgerEntry{
		Shape:     want,
		Since:     since,
		Idle:      true,
		IdleSince: idleSince,
		Evict:     evict,
	}
	l.notifyChangeLocked()
	l.mu.Unlock()
	return l.releaser(pool, id)
}

// UnparkIdle marks an entry in pool under id as active (no longer idle or
// evictable) while keeping its commitment charged.
func (l *Ledger) UnparkIdle(pool, id string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	key := ledgerKey{Pool: pool, ID: id}
	if existing, ok := l.held[key]; ok && existing.Idle {
		existing.Idle = false
		existing.IdleSince = time.Time{}
		existing.Evict = nil
		l.held[key] = existing
		l.notifyChangeLocked()
	}
	l.mu.Unlock()
}

// EvictOldestIdle evicts up to n idle entries across all pools in order of
// oldest IdleSince first, returning how many were evicted.
func (l *Ledger) EvictOldestIdle(n int) int {
	if l == nil || n <= 0 {
		return 0
	}
	l.mu.Lock()
	type candidate struct {
		key   ledgerKey
		entry ledgerEntry
	}
	var idles []candidate
	for k, e := range l.held {
		if e.Idle {
			idles = append(idles, candidate{key: k, entry: e})
		}
	}
	if len(idles) == 0 {
		l.mu.Unlock()
		return 0
	}
	sort.Slice(idles, func(i, j int) bool {
		if !idles[i].entry.IdleSince.Equal(idles[j].entry.IdleSince) {
			return idles[i].entry.IdleSince.Before(idles[j].entry.IdleSince)
		}
		if !idles[i].entry.Since.Equal(idles[j].entry.Since) {
			return idles[i].entry.Since.Before(idles[j].entry.Since)
		}
		return idles[i].key.ID < idles[j].key.ID
	})
	if n > len(idles) {
		n = len(idles)
	}
	var evicts []func()
	for i := 0; i < n; i++ {
		delete(l.held, idles[i].key)
		if idles[i].entry.Evict != nil {
			evicts = append(evicts, idles[i].entry.Evict)
		}
	}
	l.notifyChangeLocked()
	wake := l.agentWaiting && l.Wake != nil
	wakeFn := l.Wake
	l.mu.Unlock()
	for _, evict := range evicts {
		evict()
	}
	if wake {
		wakeFn()
	}
	return n
}

// Charge files a commitment without asking the budget about it -- what
// an admission that is not the budget's to refuse still owes the
// deployment's accounting.
//
// The canonical Director is the one caller (dispatch.Cycle): its bypass
// exists so that an operator can always reach the live console, and a
// console that cannot start on a full host is the failure that bypass is
// for. Its sandbox is real while it lasts, though, so what it commits is
// charged here and everything else is admitted against a budget that
// includes it.
func (l *Ledger) Charge(pool, id string, want Commitment) func() {
	if l == nil {
		return func() {}
	}
	l.mu.Lock()
	l.charge(pool, id, want)
	l.mu.Unlock()
	return l.releaser(pool, id)
}

// charge files one entry with the mutex held. A zero shape is filed all
// the same: a sandbox that commits nothing this ledger can add up still
// exists, and Count is what says so.
func (l *Ledger) charge(pool, id string, want Commitment) {
	if l.held == nil {
		l.held = map[ledgerKey]ledgerEntry{}
	}
	l.held[ledgerKey{Pool: pool, ID: id}] = ledgerEntry{Shape: want, Since: l.now()}
	l.notifyChangeLocked()
}

// Release gives back whatever pool and id were holding. Releasing
// something nobody charged is not an error: a run that never reached a
// sandbox, and a run this process did not start, both end up here.
func (l *Ledger) Release(pool, id string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	_, existed := l.held[ledgerKey{Pool: pool, ID: id}]
	delete(l.held, ledgerKey{Pool: pool, ID: id})
	if existed {
		l.notifyChangeLocked()
	}
	wake := existed && l.agentWaiting && l.Wake != nil
	wakeFn := l.Wake
	l.mu.Unlock()
	if wake {
		wakeFn()
	}
}

func (l *Ledger) releaser(pool, id string) func() {
	var once sync.Once
	return func() { once.Do(func() { l.Release(pool, id) }) }
}

// SetAgentDemand records whether dispatch has at least one eligible agent
// task held back solely by the sandbox resource budget, and the sandbox
// shape it is waiting to fit.
func (l *Ledger) SetAgentDemand(waiting bool, shape Commitment) {
	if l == nil {
		return
	}
	l.mu.Lock()
	changed := l.agentWaiting != waiting || (waiting && l.agentWaitShape != shape)
	l.agentWaiting = waiting
	if waiting {
		l.agentWaitShape = shape
	} else {
		l.agentWaitShape = Commitment{}
	}
	if changed {
		l.notifyChangeLocked()
	}
	l.mu.Unlock()
}

// SetAgentWaiting records whether an agent task is waiting for room in this
// deployment's sandbox budget.
func (l *Ledger) SetAgentWaiting(waiting bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	changed := l.agentWaiting != waiting
	l.agentWaiting = waiting
	if !waiting {
		l.agentWaitShape = Commitment{}
	}
	if changed {
		l.notifyChangeLocked()
	}
	l.mu.Unlock()
}

// AgentWaiting reports whether an agent task is currently waiting for room
// in the shared sandbox budget.
func (l *Ledger) AgentWaiting() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.agentWaiting
}

// AgentWaitShape returns the sandbox shape a waiting agent task needs, or
// the zero Commitment if none was specified.
func (l *Ledger) AgentWaitShape() Commitment {
	if l == nil {
		return Commitment{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.agentWaitShape
}

// Change returns a channel closed the next time this ledger's live
// commitments or waiting-agent state changes. A nil ledger returns nil.
func (l *Ledger) Change() <-chan struct{} {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.change == nil {
		l.change = make(chan struct{})
	}
	return l.change
}

// LedgerSyncGrace is how long a charge is left alone by Sync before the
// live set is allowed to say it does not exist.
//
// A charge is filed before the row that proves it: dispatch charges the
// budget for a candidate and then records the run, and a reconcile that
// read the live runs in between would find a charge for a run the store
// has never heard of. Dropping it there would hand the same capacity out
// twice. A minute is far longer than that window and far shorter than
// any run, so what Sync actually collects is the other thing: a charge
// whose run ended without anybody releasing it, because the process
// holding the release went away.
const LedgerSyncGrace = time.Minute

// Sync reconciles one pool against the sandboxes that are really live:
// anything in live that is not charged is charged, and anything charged
// that is not in live is dropped once it is older than LedgerSyncGrace.
//
// It is the restart path and the leak path at once. A daemon that comes
// up with runs already in flight has an empty ledger and a store full of
// them, and the first tick's Sync is what makes the budget true again; a
// run whose release was lost -- a panic, a process killed between the
// row finishing and the release -- is collected by a later one instead
// of holding capacity until the next restart.
//
// Called with the pool's whole live set, keyed the way the charges are:
// run ids for PoolAgent. A pool nothing syncs (PoolCI, whose charges
// live and die inside one process with the gate that made them) is left
// entirely alone.
func (l *Ledger) Sync(pool string, live map[string]Commitment) {
	if l == nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	changed := false
	freed := false
	for id, shape := range live {
		key := ledgerKey{Pool: pool, ID: id}
		if existing, ok := l.held[key]; ok {
			if existing.Shape != shape {
				existing.Shape = shape
				l.held[key] = existing
				changed = true
				if shape.CPUs < existing.Shape.CPUs || shape.MemoryMB < existing.Shape.MemoryMB {
					freed = true
				}
			}
			continue
		}
		if l.held == nil {
			l.held = map[ledgerKey]ledgerEntry{}
		}
		l.held[key] = ledgerEntry{Shape: shape, Since: now}
		changed = true
	}
	for key, e := range l.held {
		if key.Pool != pool {
			continue
		}
		if _, ok := live[key.ID]; ok {
			continue
		}
		if now.Sub(e.Since) < LedgerSyncGrace {
			continue
		}
		delete(l.held, key)
		changed = true
		freed = true
	}
	if changed {
		l.notifyChangeLocked()
	}
	wake := freed && l.agentWaiting && l.Wake != nil
	wakeFn := l.Wake
	l.mu.Unlock()
	if wake {
		wakeFn()
	}
}

// Reserve sets one pool's entries to the exact set in reserved without a
// grace period. Used for PoolPersistent so enabled persistent sandboxes
// reserve their configured share up front, and disabled or deleted ones
// release their reservation immediately.
func (l *Ledger) Reserve(pool string, reserved map[string]Commitment) {
	if l == nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	changed := false
	freed := false
	for id, shape := range reserved {
		key := ledgerKey{Pool: pool, ID: id}
		if existing, ok := l.held[key]; ok {
			if existing.Shape != shape {
				existing.Shape = shape
				l.held[key] = existing
				changed = true
				if shape.CPUs < existing.Shape.CPUs || shape.MemoryMB < existing.Shape.MemoryMB {
					freed = true
				}
			}
			continue
		}
		if l.held == nil {
			l.held = map[ledgerKey]ledgerEntry{}
		}
		l.held[key] = ledgerEntry{Shape: shape, Since: now}
		changed = true
	}
	for key := range l.held {
		if key.Pool != pool {
			continue
		}
		if _, ok := reserved[key.ID]; ok {
			continue
		}
		delete(l.held, key)
		changed = true
		freed = true
	}
	if changed {
		l.notifyChangeLocked()
	}
	wake := freed && l.agentWaiting && l.Wake != nil
	wakeFn := l.Wake
	l.mu.Unlock()
	if wake {
		wakeFn()
	}
}

// Describe is this ledger as one clause of a refusal or a status line:
// what is live, and where it went. "nothing" where no sandbox is
// charged, which is a deployment with an empty host rather than one
// nobody measured.
func (l *Ledger) Describe() string {
	if l == nil {
		return "nothing"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.held) == 0 {
		return "nothing"
	}
	type poolSum struct {
		pool  string
		n     int
		shape Commitment
	}
	sums := map[string]*poolSum{}
	for k, e := range l.held {
		s := sums[k.Pool]
		if s == nil {
			s = &poolSum{pool: k.Pool}
			sums[k.Pool] = s
		}
		s.n++
		s.shape = s.shape.Plus(e.Shape)
	}
	ordered := make([]*poolSum, 0, len(sums))
	for _, s := range sums {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].pool < ordered[j].pool })
	out := l.live().String()
	sep := " ("
	for _, s := range ordered {
		out += fmt.Sprintf("%s%s in %d %s", sep, s.shape, s.n,
			plural(s.n, s.pool+" sandbox", s.pool+" sandboxes"))
		sep = ", "
	}
	if len(l.held) <= 3 {
		type holderItem struct {
			pool string
			id   string
		}
		holders := make([]holderItem, 0, len(l.held))
		for k := range l.held {
			holders = append(holders, holderItem{pool: k.Pool, id: k.ID})
		}
		sort.Slice(holders, func(i, j int) bool {
			if holders[i].pool != holders[j].pool {
				return holders[i].pool < holders[j].pool
			}
			return holders[i].id < holders[j].id
		})
		items := make([]string, len(holders))
		for i, h := range holders {
			switch h.pool {
			case PoolAgent:
				items[i] = "agent run " + h.id
			case PoolCI:
				items[i] = "CI job " + h.id
			case PoolPersistent:
				items[i] = "persistent sandbox " + h.id
			case PoolStanding:
				items[i] = "standing sandbox " + h.id
			default:
				items[i] = h.pool + " " + h.id
			}
		}
		out += ": " + strings.Join(items, ", ")
	}
	return out + ")"
}
