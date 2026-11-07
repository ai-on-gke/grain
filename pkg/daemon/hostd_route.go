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

package daemon

// Per-run hostd routing: which grain hostd one run's host work goes to.
//
// Where host work runs is a property of what does it, never of the
// task. A run's agent CLI goes to the hostd its model connection names
// (model.AgentBackend.Hostd, then its HostdFallback in order), resolved
// for each run by SelectHostd once the pool has chosen the connection
// (connectionRoutes). The run's framework is then built on a copy of the
// config whose host executor is that hostd's, so the agent CLI reaches
// it through its own client (HostExecutor.Hostd, hostd.Remote.Dialer),
// and starts detached through that hostd's own runhost.Hostd
// (newHostdRuns); task_run_hostd records it. Each run: hostd capability
// server -- host scripts among them -- starts on the hostd its own entry
// names (protocol.ServerConfig.Hostd, hostdCapabilityServers), and each
// capability audit record says which. A run on a connection on hostd A
// holding a grant of a server on hostd B so starts its CLI on A and
// reaches the server on B. A connection with no placement goes where
// the framework's run location says, through the default hostd, exactly
// as before.
//
// The pool also holds each hostd to its allowlist's capacity: (Hello.
// Capacity), counting the runs whose CLI starts there, and keys a usage
// limit met on an ambient credential -- one that lives on the hostd's
// machine, not in grain -- to that hostd, so a limit hit on one machine
// does not pause the same connection on another. A connection whose
// hostd is not connected (and has no fallback that is) is unavailable:
// a run pinned to it waits, and the rotation passes it over.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// hostdRoute is the hostd one run's host work goes to.
type hostdRoute struct {
	// Name is the hostd's registry name; "" where this deployment has
	// no hostd at all (a daemon on the bare machine, or Kubernetes with
	// none dialing in) and nothing pinned one.
	Name string
	Exec HostExecutor
	// Pinned is whether the connection's own selector chose it, rather
	// than the default.
	Pinned bool
}

// hostdLeaseKey is the inflight key a hostd's capacity is counted under,
// beside the connections' own (which are connection IDs).
func hostdLeaseKey(name string) string { return "hostd:" + name }

// errHostdFull is a run that found its hostd at its capacity.
type errHostdFull struct {
	name           string
	inUse, capcity int
}

func (e *errHostdFull) Error() string {
	return fmt.Sprintf("host %s is at its capacity (%d of %d runs); the run waits for one to finish", e.name, e.inUse, e.capcity)
}

// resolveHostdSelector is the route for one hostd selector
// (model.ParseHostdSelector's form): the default hostd for "", or the
// hostd SelectHostd resolves it to now. A pinned hostd that is not
// connected is a *HostdWaitError.
func resolveHostdSelector(ctx context.Context, live Config, store *model.Store, selector string) (hostdRoute, error) {
	if selector == "" {
		exec := live.hostExecutor()
		if exec.Kind != HostExecutorHostd {
			return hostdRoute{}, nil
		}
		return hostdRoute{Name: live.DefaultHostd(), Exec: exec}, nil
	}
	name, labels, err := model.ParseHostdSelector(selector)
	if err != nil {
		return hostdRoute{}, err
	}
	exec, resolved, err := live.SelectHostd(ctx, store, HostdSelector{Name: name, Labels: labels})
	if err != nil {
		return hostdRoute{}, err
	}
	return hostdRoute{Name: resolved, Exec: exec, Pinned: true}, nil
}

// connectionHostdRoute is the hostd a run on connection b starts its
// agent CLI on: the first of b's HostdTargets that resolves, or the
// default hostd for a connection that names none. When none resolves,
// the first wait is the answer -- nothing falls back past the list b
// itself gives -- wrapped to say whose hostd it is.
func connectionHostdRoute(ctx context.Context, live Config, store *model.Store, b model.AgentBackend) (hostdRoute, error) {
	targets := b.HostdTargets()
	r, badSel, wait, fatal := resolveConnectionTargets(ctx, live, store, targets)
	if err := wrapConnectionTargetErr(b.ID, targets, badSel, wait, fatal); err != nil {
		return hostdRoute{}, err
	}
	return r, nil
}

func resolveConnectionTargets(ctx context.Context, live Config, store *model.Store, targets []string) (route hostdRoute, badSel string, wait error, fatal error) {
	if len(targets) == 0 {
		r, err := resolveHostdSelector(ctx, live, store, "")
		return r, "", nil, err
	}
	for _, sel := range targets {
		r, err := resolveHostdSelector(ctx, live, store, sel)
		if err == nil {
			return r, "", nil, nil
		}
		var w *HostdWaitError
		if errors.As(err, &w) {
			if wait == nil {
				wait = err
			}
			continue
		}
		if fatal == nil {
			badSel, fatal = sel, err
		}
	}
	if wait != nil {
		return hostdRoute{}, "", wait, nil
	}
	return hostdRoute{}, badSel, nil, fatal
}

func wrapConnectionTargetErr(id string, targets []string, badSel string, wait, fatal error) error {
	if fatal != nil {
		if len(targets) == 0 {
			return fatal
		}
		return fmt.Errorf("connection %q's host %s: %w", id, badSel, fatal)
	}
	if wait != nil {
		return fmt.Errorf("connection %q runs its CLI on %s, which is not connected; its runs wait (%w)",
			id, describeHostdTargets(targets), wait)
	}
	return nil
}

// describeHostdTargets says a connection's hostd list as a wait reason
// does: "hostd ws-a", or "hostd ws-a (fallback ws-b)".
func describeHostdTargets(targets []string) string {
	out := "host " + targets[0]
	if len(targets) > 1 {
		out += " (fallback " + strings.Join(targets[1:], ", ") + ")"
	}
	return out
}

// connectionRoutes resolves the hostd of every connection in backends a
// run may go to: routes by connection ID, and blocked, the connections
// that cannot take a run now -- their hostd is not connected, or (for a
// run that is not priority dispatch) at its capacity -- with why. Each
// distinct placement is resolved once.
func (p *AgentPool) connectionRoutes(ctx context.Context, live Config, backends []model.AgentBackend, priority bool) (map[string]hostdRoute, map[string]error) {
	routes := map[string]hostdRoute{}
	blocked := map[string]error{}
	type resolved struct {
		route  hostdRoute
		badSel string
		wait   error
		fatal  error
	}
	seen := map[string]resolved{}
	for _, b := range backends {
		targets := b.HostdTargets()
		key := strings.Join(targets, "\x00")
		res, ok := seen[key]
		if !ok {
			res.route, res.badSel, res.wait, res.fatal = resolveConnectionTargets(ctx, live, p.Store, targets)
			seen[key] = res
		}
		if err := wrapConnectionTargetErr(b.ID, targets, res.badSel, res.wait, res.fatal); err != nil {
			blocked[b.ID] = err
			continue
		}
		routes[b.ID] = res.route
		if priority || !runsOnHostd(live, res.route, b.Framework) {
			continue
		}
		capacity := p.hostdCapacity(ctx, res.route)
		p.mu.Lock()
		full := p.hostdFullLocked(res.route, capacity, time.Now())
		p.mu.Unlock()
		if full != nil {
			blocked[b.ID] = full
		}
	}
	return routes, blocked
}

// apply is live with r's hostd as its host executor. Its runs start
// detached through that hostd's own runhost.Hostd (newHostdRuns), so a
// drop is re-attached within -hostd-reattach-timeout, a run on any hostd
// outlives a daemon upgrade, and it is re-adopted from the hostd it
// recorded (adoptHostd).
//
// A connection placed on a hostd runs its CLI there: framework's run
// location is the host for this run, unless it is already set to run on
// the host or detached.
func (r hostdRoute) apply(live Config, framework string) Config {
	if !r.Pinned {
		return live
	}
	exec := r.Exec
	live.HostExec = &exec
	if fw := model.NormalizeAgentFrameworkName(framework); fw != "" {
		if location, _ := live.CLIRunSetting(fw); location != model.CLIRunHost && location != model.CLIRunDetached {
			runs := make(map[string]model.CLIRunSetting, len(live.CLIRun)+1)
			for k, v := range live.CLIRun {
				runs[k] = v
			}
			runs[fw] = model.CLIRunSetting{Location: model.CLIRunHost}
			live.CLIRun = runs
		}
	}
	if r.Name != live.DefaultHostd() {
		live.hostdRuns = live.hostdRunSet.forHostd(r.Name, exec)
	}
	return live
}

// scopesPauses is whether a usage limit on an ambient credential is
// this hostd's own: every hostd but the default, whose pauses keep the
// connection's bare key they always had.
func (r hostdRoute) scopesPauses(live Config) bool {
	return r.Name != "" && r.Name != live.DefaultHostd()
}

// ambientCredential reports whether connection b spends a credential
// that lives on the machine its CLI runs on -- a CLI login, not a key
// grain holds -- so that the same connection on two hostds is two
// credentials.
func ambientCredential(live Config, b model.AgentBackend) bool {
	mode := b.AuthMode
	if b.CredentialBackendID != "" {
		for _, source := range live.AgentBackends {
			if source.ID == b.CredentialBackendID {
				mode = source.AuthMode
				break
			}
		}
	}
	return mode == model.AntigravityAuthAmbient || mode == model.CodexAuthSubscription
}

// hostdPauseOwner is the key a usage limit on connection b's credential
// is paused under for a run on hostd: b's credential owner, scoped to
// the hostd when the credential is ambient and the hostd is not the
// default.
func hostdPauseOwner(owner, hostdName string) string {
	if hostdName == "" {
		return owner
	}
	return owner + "@hostd/" + hostdName
}

// pauseHostdOf is the hostd a usage-limit pause on b's credential is
// scoped to for a run on b: b's own hostd (connectionRoutes), when that
// scopes pauses; "" for the connection's bare key.
func (req poolRequest) pauseHostdOf(live Config, b model.AgentBackend) string {
	if r, ok := req.routes[b.ID]; ok && r.scopesPauses(live) {
		return r.Name
	}
	return ""
}

// pauseOwner is hostdPauseOwner for b on b's hostd.
func (req poolRequest) pauseOwner(live Config, b model.AgentBackend) string {
	if h := req.pauseHostdOf(live, b); h != "" && ambientCredential(live, b) {
		return hostdPauseOwner(b.CredentialOwnerID(), h)
	}
	return b.CredentialOwnerID()
}

// pausedConnection names b as a usage-limit pause on req's hostd reads:
// `connection "agy"`, or `connection "agy" on hostd ws-a` when the pause
// is that hostd's alone (pauseOwner), so an operator reading why a task
// waits can tell the same connection is free on every other hostd.
func (req poolRequest) pausedConnection(live Config, b model.AgentBackend) string {
	name := fmt.Sprintf("connection %q", b.ID)
	if req.pauseOwner(live, b) != b.CredentialOwnerID() {
		name += " on host " + req.pauseHostdOf(live, b)
	}
	return name
}

// hostdCapacityTTL is how long a hostd's reported capacity is believed.
const hostdCapacityTTL = 30 * time.Second

type hostdCapacityEntry struct {
	capacity int
	at       time.Time
}

// hostdCapacity is how many runs at once r's hostd takes (its registry
// capacity and allowlist capacity:, Hello.Capacity), 0 for no limit.
func (p *AgentPool) hostdCapacity(ctx context.Context, r hostdRoute) int {
	if r.Name == "" {
		return 0
	}
	var regCap int
	if p.Store != nil {
		if h, err := p.Store.GetHostd(ctx, r.Name); err == nil && h != nil && h.Capacity > 0 {
			regCap = h.Capacity
		}
	}
	p.mu.Lock()
	if e, ok := p.hostdCapacities[r.Name]; ok && time.Since(e.at) < hostdCapacityTTL {
		p.mu.Unlock()
		return combineHostdCapacities(regCap, e.capacity)
	}
	p.mu.Unlock()
	hctx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdLabelsTimeout)
	defer cancel()
	hello, err := r.Exec.hostdClient().Hello(hctx)
	if err != nil {
		return regCap
	}
	p.mu.Lock()
	if p.hostdCapacities == nil {
		p.hostdCapacities = map[string]hostdCapacityEntry{}
	}
	p.hostdCapacities[r.Name] = hostdCapacityEntry{capacity: hello.Capacity, at: time.Now()}
	p.mu.Unlock()
	return combineHostdCapacities(regCap, hello.Capacity)
}

func combineHostdCapacities(a, b int) int {
	if a > 0 && b > 0 {
		if a < b {
			return a
		}
		return b
	}
	if a > 0 {
		return a
	}
	return b
}

// hostdFullLocked is the capacity error a run on r would meet now, or
// nil. p.mu must be held.
func (p *AgentPool) hostdFullLocked(r hostdRoute, capacity int, now time.Time) *errHostdFull {
	if capacity <= 0 || r.Name == "" {
		return nil
	}
	if n := p.leasesLocked(hostdLeaseKey(r.Name), now); n >= capacity {
		return &errHostdFull{name: r.Name, inUse: n, capcity: capacity}
	}
	return nil
}

// runsOnHostd is whether a run of framework on r starts its CLI through
// r's hostd: always for a connection placed on a hostd, whose placement
// says its CLI runs there, and otherwise when the framework is set to
// run on the host.
func runsOnHostd(live Config, r hostdRoute, framework string) bool {
	if r.Name == "" {
		return false
	}
	return r.Pinned || runsThroughHostd(live, model.NormalizeAgentFrameworkName(framework))
}

// hostdWaitReason is why a task routed to a hostd that cannot take it
// now waits, or "" -- a selector that will never resolve is not a wait
// but a setup failure, said by Build.
func hostdWaitReason(err error) string {
	var wait *HostdWaitError
	var full *errHostdFull
	if errors.As(err, &wait) || errors.As(err, &full) {
		return err.Error()
	}
	return ""
}

// hostdAudit is what a run on r records about its hostd
// (model.TaskRunHostd): the machine it ran on and its registry owner.
func (p *AgentPool) hostdAudit(ctx context.Context, live Config, r hostdRoute) (instance, owner string) {
	if r.Exec.Remote != nil {
		name := r.Exec.Hostd
		if name == "" {
			name = r.Exec.Remote.DefaultName()
		}
		st := r.Exec.Remote.HostdStatus(name)
		instance, owner = st.Host, st.Owner
	}
	if owner == "" && p.Store != nil {
		if h, err := p.Store.GetHostd(ctx, r.Name); err == nil && h != nil {
			owner = h.Owner
		}
	}
	return instance, owner
}

// recordRunHostd writes f's hostd against runID, logging rather than
// failing the run when it cannot.
func (f *BackendFramework) recordRunHostd(ctx context.Context, taskID, runID string) {
	if f.Hostd == "" || f.store == nil || runID == "" {
		return
	}
	if err := f.store.RecordTaskRunHostd(context.WithoutCancel(ctx), model.TaskRunHostd{
		RunID: runID, TaskID: taskID, Hostd: f.Hostd, Instance: f.HostdInstance, Owner: f.HostdOwner,
	}); err != nil {
		log.Printf("grain daemon: run %s: recording its hostd %s: %v", runID, f.Hostd, err)
	}
}
