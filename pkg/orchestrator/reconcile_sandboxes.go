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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// LiveSandbox describes one live sandbox held by a backend right now, read from
// the backend's in-memory tracking without probing the guest.
type LiveSandbox struct {
	// ID is the logical sandbox identifier passed to Acquire or Serve.
	ID string
	// Name is the backend-specific resource name (granule VM name, container
	// name, pod name, or host directory path).
	Name string
	// Backend identifies the sandbox implementation ("granule", "docker",
	// "kubernetes", "kubernetes-granule", "host").
	Backend string
	// Kind is the SandboxUsage kind ("agent" or "ci").
	Kind string
	// Pool is the model.Ledger pool this sandbox belongs to ("agent", "ci",
	// "persistent", or "standing").
	Pool string
	// Holder is the ID under which this sandbox is charged in Pool.
	Holder string
	// Shape is the CPU and memory commitment of this sandbox.
	Shape model.Commitment
	// Release tears down the sandbox if reconciliation determines it is an
	// orphan whose run has already finished.
	Release func(context.Context) error
}

// LiveSandboxLister is implemented by Sandboxes backends that can enumerate
// their currently held sandboxes without blocking on guest probes.
type LiveSandboxLister interface {
	LiveSandboxes() []LiveSandbox
}

// sandboxPoolAndHolder maps a sandbox name to its ledger pool and holder ID.
func sandboxPoolAndHolder(name string) (pool, holder string) {
	switch {
	case strings.HasPrefix(name, "p-"):
		return model.PoolPersistent, strings.TrimPrefix(name, "p-")
	case len(name) == 9 && strings.HasPrefix(name, "p") && isLowerHex(name[1:]):
		return model.PoolPersistent, name
	case len(name) == 9 && strings.HasPrefix(name, "s") && isLowerHex(name[1:]):
		return model.PoolStanding, name
	case strings.HasPrefix(name, "c"):
		return model.PoolCI, name
	default:
		return model.PoolAgent, name
	}
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}

// LiveSandboxes returns the live VMs currently held by GranuleSandboxes.
func (k *GranuleSandboxes) LiveSandboxes() []LiveSandbox {
	k.mu.Lock()
	defaults := k.defaults
	out := make([]LiveSandbox, 0, len(k.live))
	for id, sb := range k.live {
		resolved := sb.shape.orDefault(defaults).orDefault(DefaultShape())
		pool, holder := sandboxPoolAndHolder(id)
		out = append(out, LiveSandbox{
			ID:      id,
			Name:    sb.vmName,
			Backend: "granule",
			Kind:    sandboxKind(id),
			Pool:    pool,
			Holder:  holder,
			Shape:   model.Commitment{CPUs: resolved.CPUs, MemoryMB: resolved.MemoryMB, DiskGB: resolved.DiskGB},
			Release: sb.Release,
		})
	}
	k.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LiveSandboxes returns the live directories currently held by HostSandboxes.
func (h *HostSandboxes) LiveSandboxes() []LiveSandbox {
	h.mu.Lock()
	out := make([]LiveSandbox, 0, len(h.live))
	for id, sb := range h.live {
		pool, holder := sandboxPoolAndHolder(id)
		out = append(out, LiveSandbox{
			ID:      id,
			Name:    sb.root,
			Backend: "host",
			Kind:    sandboxKind(id),
			Pool:    pool,
			Holder:  holder,
			Release: sb.Release,
		})
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LiveSandboxes returns the live containers currently held by ContainerSandboxes.
func (c *ContainerSandboxes) LiveSandboxes() []LiveSandbox {
	c.mu.Lock()
	defaults := c.defaults
	out := make([]LiveSandbox, 0, len(c.live))
	for id, sb := range c.live {
		resolved := sb.shape.orDefault(defaults).orDefault(DefaultShape())
		pool, holder := sandboxPoolAndHolder(id)
		out = append(out, LiveSandbox{
			ID:      id,
			Name:    sb.container,
			Backend: "docker",
			Kind:    sandboxKind(id),
			Pool:    pool,
			Holder:  holder,
			Shape:   model.Commitment{CPUs: resolved.CPUs, MemoryMB: resolved.MemoryMB, DiskGB: resolved.DiskGB},
			Release: sb.Release,
		})
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LiveSandboxes returns the live pods currently held by K8sSandboxes.
func (k *K8sSandboxes) LiveSandboxes() []LiveSandbox {
	k.mu.Lock()
	defaults := k.defaults
	out := make([]LiveSandbox, 0, len(k.live))
	for id, sb := range k.live {
		resolved := sb.shape.orDefault(defaults).orDefault(DefaultShape())
		pool, holder := sandboxPoolAndHolder(id)
		out = append(out, LiveSandbox{
			ID:      id,
			Name:    sb.podName,
			Backend: "kubernetes",
			Kind:    sandboxKind(id),
			Pool:    pool,
			Holder:  holder,
			Shape:   model.Commitment{CPUs: resolved.CPUs, MemoryMB: resolved.MemoryMB, DiskGB: resolved.DiskGB},
			Release: sb.Release,
		})
	}
	k.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LiveSandboxes returns the live pods currently held by K8sGranuleSandboxes.
func (k *K8sGranuleSandboxes) LiveSandboxes() []LiveSandbox {
	k.mu.Lock()
	defaults := k.defaults
	out := make([]LiveSandbox, 0, len(k.live))
	for id, sb := range k.live {
		resolved := sb.shape.orDefault(defaults).orDefault(DefaultShape())
		pool, holder := sandboxPoolAndHolder(id)
		out = append(out, LiveSandbox{
			ID:      id,
			Name:    sb.podName,
			Backend: "kubernetes-granule",
			Kind:    sandboxKind(id),
			Pool:    pool,
			Holder:  holder,
			Shape:   model.Commitment{CPUs: resolved.CPUs, MemoryMB: resolved.MemoryMB, DiskGB: resolved.DiskGB},
			Release: sb.Release,
		})
	}
	k.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SandboxReconciliation summarizes the most recent comparison between the
// deployment's accounted ledger commitments and the live sandboxes reported by
// the backend.
type SandboxReconciliation struct {
	CheckedAt          time.Time                   `json:"checked_at"`
	AccountedCount     int                         `json:"accounted_count"`
	Committed          model.Commitment            `json:"committed"`
	Pools              map[string]model.Commitment `json:"pools,omitempty"`
	PoolCounts         map[string]int              `json:"pool_counts,omitempty"`
	LiveCount          int                         `json:"live_count"`
	LiveCommitted      model.Commitment            `json:"live_committed"`
	InSync             bool                        `json:"in_sync"`
	Discrepancies      []string                    `json:"discrepancies,omitempty"`
	OrphansReaped      int                         `json:"orphans_reaped"`
	TotalOrphansReaped int                         `json:"total_orphans_reaped"`
}

type ledgerReconcileState struct {
	latest             SandboxReconciliation
	totalOrphansReaped int
	reconciledHolders  map[string]bool
}

var (
	reconcileStateMu sync.Mutex
	reconcileStates  = map[*model.Ledger]*ledgerReconcileState{}
	defaultRecState  ledgerReconcileState
)

func stateForLedger(l *model.Ledger) *ledgerReconcileState {
	if l == nil {
		return &defaultRecState
	}
	st, ok := reconcileStates[l]
	if !ok {
		st = &ledgerReconcileState{reconciledHolders: map[string]bool{}}
		reconcileStates[l] = st
	}
	return st
}

// LatestSandboxReconciliation returns the most recent reconciliation report
// recorded for ledger, if any.
func LatestSandboxReconciliation(ledger *model.Ledger) (SandboxReconciliation, bool) {
	reconcileStateMu.Lock()
	defer reconcileStateMu.Unlock()
	st := stateForLedger(ledger)
	if st.latest.CheckedAt.IsZero() {
		return SandboxReconciliation{}, false
	}
	cp := st.latest
	if len(st.latest.Pools) > 0 {
		cp.Pools = make(map[string]model.Commitment, len(st.latest.Pools))
		for k, v := range st.latest.Pools {
			cp.Pools[k] = v
		}
	}
	if len(st.latest.PoolCounts) > 0 {
		cp.PoolCounts = make(map[string]int, len(st.latest.PoolCounts))
		for k, v := range st.latest.PoolCounts {
			cp.PoolCounts[k] = v
		}
	}
	cp.Discrepancies = append([]string(nil), st.latest.Discrepancies...)
	return cp, true
}

func poolHolderKey(pool, holder string) string {
	return pool + ":" + holder
}

// ReconcileSandboxes compares the accounted commitments in deps.Ledger against
// the live sandboxes in deps.Sandboxes and the live runs in deps.Store, logs
// and repairs any discrepancies, sweeps orphaned backend sandboxes when
// supported, and records the reconciliation snapshot for System -> Sandboxes.
func ReconcileSandboxes(ctx context.Context, deps Deps, now time.Time) error {
	return reconcileSandboxes(ctx, deps, now)
}

func reconcileSandboxes(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Ledger == nil && deps.Sandboxes == nil && deps.RunHost == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var live []LiveSandbox
	hasLister := false
	if lister, ok := deps.Sandboxes.(LiveSandboxLister); ok && deps.Sandboxes != nil {
		hasLister = true
		live = lister.LiveSandboxes()
	}

	liveRunIDs := map[string]bool{}
	haveLiveRuns := false
	if deps.Store != nil {
		if runs, err := deps.Store.LiveRuns(ctx); err == nil {
			haveLiveRuns = true
			for _, r := range runs {
				liveRunIDs[r.ID] = true
				if r.Sandbox != "" {
					liveRunIDs[r.Sandbox] = true
				}
			}
		}
	}

	// InFlight is read before the session pool: a run leaves InFlight only
	// after parking its session sandbox warm, and a warm sandbox moves to a
	// claiming run's hold under the pool's lock, so a sandbox missed by one
	// read is in the next.
	inFlightRunIDs := map[string]bool{}
	if deps.Runs != nil {
		for _, id := range deps.Runs.Runs() {
			inFlightRunIDs[id] = true
		}
	}
	if haveLiveRuns {
		for id := range sessionSandboxesInUse(deps.Store) {
			liveRunIDs[id] = true
		}
	}

	var discrepancies []string
	var orphansReaped int

	// 1. Check live agent sandboxes for any whose run has already finished
	// in the store and is no longer running in deps.Runs. Reap them
	// immediately so a leaked sandbox VM does not sit on capacity indefinitely.
	if hasLister && haveLiveRuns && deps.Store != nil {
		kept := make([]LiveSandbox, 0, len(live))
		for _, sb := range live {
			if sb.Pool == model.PoolAgent && !liveRunIDs[sb.Holder] && !liveRunIDs[sb.ID] && !inFlightRunIDs[sb.Holder] {
				if finished, found, err := deps.Store.RunFinished(ctx, sb.Holder); err == nil && found && finished {
					msg := fmt.Sprintf("reaped leaked agent sandbox %s (%s): run %s already finished", sb.ID, sb.Name, sb.Holder)
					log.Printf("orchestrator: sandbox reconciliation: %s", msg)
					discrepancies = append(discrepancies, msg)
					if sb.Release != nil {
						rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
						_ = sb.Release(rctx)
						cancel()
					}
					if deps.Ledger != nil {
						deps.Ledger.Release(model.PoolAgent, sb.Holder)
					}
					orphansReaped++
					continue
				}
			}
			kept = append(kept, sb)
		}
		live = kept
	}

	// 2. Sweep backend-level orphans (e.g. GranuleSandboxes.SweepOrphans)
	// when the backend supports live orphan sweeping.
	if sweeper, ok := deps.Sandboxes.(interface {
		SweepOrphans(context.Context) (int, error)
	}); ok && deps.Sandboxes != nil {
		canSweep := true
		if g, isGranule := deps.Sandboxes.(*GranuleSandboxes); isGranule && g.cfg.StateDir == "" {
			canSweep = false
		}
		if canSweep {
			swept, err := sweeper.SweepOrphans(ctx)
			if err != nil {
				log.Printf("orchestrator: sandbox reconciliation: sweeping orphaned sandboxes: %v", err)
			}
			if swept > 0 {
				orphansReaped += swept
				msg := fmt.Sprintf("reaped %d orphaned backend sandbox(es) not held by any live run", swept)
				log.Printf("orchestrator: sandbox reconciliation: %s", msg)
				discrepancies = append(discrepancies, msg)
			}
		}
	}

	// 2b. Sweep run hosts no live run claims: an agent CLI's run host
	// that exited but whose own Destroy failed as its run finished (a
	// Succeeded or Failed runhost pod, a stopped container).
	if deps.RunHost != nil && deps.Store != nil {
		var inFlight func() []string
		if deps.Runs != nil {
			inFlight = deps.Runs.Runs
		}
		swept, err := SweepOrphanRunHosts(ctx, deps.Store, deps.RunHost, inFlight, now)
		if err != nil {
			log.Printf("orchestrator: sandbox reconciliation: sweeping orphaned run hosts: %v", err)
		}
		if swept > 0 {
			orphansReaped += swept
			msg := fmt.Sprintf("reaped %d orphaned run host(s) not claimed by any live run", swept)
			log.Printf("orchestrator: sandbox reconciliation: %s", msg)
			discrepancies = append(discrepancies, msg)
		}
	}

	liveByKey := make(map[string]LiveSandbox, len(live))
	for _, sb := range live {
		liveByKey[poolHolderKey(sb.Pool, sb.Holder)] = sb
	}

	reconcileStateMu.Lock()
	st := stateForLedger(deps.Ledger)
	prevReconciled := make(map[string]bool, len(st.reconciledHolders))
	for k, v := range st.reconciledHolders {
		prevReconciled[k] = v
	}
	reconcileStateMu.Unlock()

	nextReconciled := map[string]bool{}

	if deps.Ledger != nil {
		// 3. Release any synthetic reconciliation charges from earlier cycles
		// whose live sandbox has since been released, and detect stale agent
		// ledger entries before Sync drops them so the discrepancy is logged.
		holdersBefore := deps.Ledger.Holders()
		for _, h := range holdersBefore {
			key := poolHolderKey(h.Pool, h.ID)
			_, isLiveSandbox := liveByKey[key]
			if prevReconciled[key] {
				if !isLiveSandbox && (h.Pool != model.PoolAgent || !liveRunIDs[h.ID]) {
					deps.Ledger.Release(h.Pool, h.ID)
					continue
				}
				nextReconciled[key] = true
			}
			if h.Pool == model.PoolAgent && haveLiveRuns && !liveRunIDs[h.ID] && !isLiveSandbox {
				if !h.Since.IsZero() && now.Sub(h.Since) >= model.LedgerSyncGrace {
					msg := fmt.Sprintf("released stale agent ledger commitment %s (%d vCPU / %d MiB) with no live run or sandbox", h.ID, h.Shape.CPUs, h.Shape.MemoryMB)
					log.Printf("orchestrator: sandbox reconciliation: %s", msg)
					discrepancies = append(discrepancies, msg)
					deps.Ledger.Release(model.PoolAgent, h.ID)
				}
			}
		}

		// Sync live agent runs from the store.
		syncSandboxLedger(ctx, deps)

		// 4. Ensure every live backend sandbox is accounted for in deps.Ledger.
		if hasLister {
			holdersNow := deps.Ledger.Holders()
			holderMap := make(map[string]model.LedgerHolder, len(holdersNow))
			unmatchedGenericCI := 0
			for _, h := range holdersNow {
				holderMap[poolHolderKey(h.Pool, h.ID)] = h
				if h.Pool == model.PoolCI && strings.HasPrefix(h.ID, "job-") {
					unmatchedGenericCI++
				}
			}

			for _, sb := range live {
				key := poolHolderKey(sb.Pool, sb.Holder)
				want := sb.Shape
				if want.IsZero() {
					want = deps.AgentShape
				}
				h, exists := holderMap[key]
				if !exists && sb.Pool == model.PoolCI && unmatchedGenericCI > 0 {
					unmatchedGenericCI--
					continue
				}
				if !exists {
					deps.Ledger.Charge(sb.Pool, sb.Holder, want)
					nextReconciled[key] = true
					msg := fmt.Sprintf("live %s sandbox %s (%s) was missing from ledger; charged %d vCPU / %d MiB", sb.Pool, sb.ID, sb.Name, want.CPUs, want.MemoryMB)
					log.Printf("orchestrator: sandbox reconciliation: %s", msg)
					discrepancies = append(discrepancies, msg)
				} else if h.Shape.IsZero() && !want.IsZero() {
					deps.Ledger.Charge(sb.Pool, sb.Holder, want)
					msg := fmt.Sprintf("live %s sandbox %s (%s) had zero commitment in ledger; updated to %d vCPU / %d MiB", sb.Pool, sb.ID, sb.Name, want.CPUs, want.MemoryMB)
					log.Printf("orchestrator: sandbox reconciliation: %s", msg)
					discrepancies = append(discrepancies, msg)
				}
			}
		}
	}

	// 5. Build the post-reconciliation snapshot.
	pools := map[string]model.Commitment{
		model.PoolAgent:      {},
		model.PoolCI:         {},
		model.PoolPersistent: {},
		model.PoolStanding:   {},
	}
	poolCounts := map[string]int{
		model.PoolAgent:      0,
		model.PoolCI:         0,
		model.PoolPersistent: 0,
		model.PoolStanding:   0,
	}
	var committed model.Commitment
	accountedCount := 0
	if deps.Ledger != nil {
		for _, h := range deps.Ledger.Holders() {
			accountedCount++
			committed = committed.Plus(h.Shape)
			pools[h.Pool] = pools[h.Pool].Plus(h.Shape)
			poolCounts[h.Pool]++
		}
	}

	var liveCommitted model.Commitment
	for _, sb := range live {
		want := sb.Shape
		if want.IsZero() {
			want = deps.AgentShape
		}
		liveCommitted = liveCommitted.Plus(want)
	}

	reconcileStateMu.Lock()
	st = stateForLedger(deps.Ledger)
	st.totalOrphansReaped += orphansReaped
	st.reconciledHolders = nextReconciled
	st.latest = SandboxReconciliation{
		CheckedAt:          now,
		AccountedCount:     accountedCount,
		Committed:          committed,
		Pools:              pools,
		PoolCounts:         poolCounts,
		LiveCount:          len(live),
		LiveCommitted:      liveCommitted,
		InSync:             len(discrepancies) == 0,
		Discrepancies:      discrepancies,
		OrphansReaped:      orphansReaped,
		TotalOrphansReaped: st.totalOrphansReaped,
	}
	reconcileStateMu.Unlock()

	return nil
}
