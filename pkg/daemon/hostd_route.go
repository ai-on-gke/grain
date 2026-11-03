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
// A task's hostd pin or label selector (model.SetTaskHostd) is resolved
// for each run by SelectHostd. The run's framework is then built on a
// copy of the config whose host executor is that hostd's, so the agent
// CLI reaches it through its own client (HostExecutor.Hostd,
// hostd.Remote.Dialer). run: host capability servers -- host scripts
// among them -- are not routed: they are started once, through the
// default hostd (Config.capabilityHostd), whichever hostd a run's CLI
// went to, and their audit records say so (Deps.CapabilityHostd). A
// task with no pin goes to the default hostd exactly as before.
//
// The pool also holds each hostd to its allowlist's capacity: (Hello.
// Capacity), counting the runs whose CLI starts there, and keys a usage
// limit met on an ambient credential -- one that lives on the hostd's
// machine, not in grain -- to that hostd, so a limit hit on one machine
// does not pause the same connection on another.

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	// Pinned is whether the task's own selector chose it, rather than
	// the default.
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
	return fmt.Sprintf("hostd %s is at its capacity (%d of %d runs); the run waits for one to finish", e.name, e.inUse, e.capcity)
}

// routeHostd is the hostd taskID's next run goes to: its pin or label
// selector resolved now, or the default hostd. A pinned hostd that is
// not connected is a *HostdWaitError; nothing falls back to another.
func (p *AgentPool) routeHostd(ctx context.Context, live Config, taskID string) (hostdRoute, error) {
	selector := ""
	if p.Store != nil && taskID != "" {
		var err error
		if selector, err = p.Store.TaskHostd(ctx, taskID); err != nil {
			return hostdRoute{}, err
		}
	}
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
	exec, resolved, err := live.SelectHostd(ctx, p.Store, HostdSelector{Name: name, Labels: labels})
	if err != nil {
		return hostdRoute{}, err
	}
	return hostdRoute{Name: resolved, Exec: exec, Pinned: true}, nil
}

// apply is live with r's hostd as its host executor. The default hostd's
// runs keep starting detached (newHostdRuns), so they outlive a daemon
// upgrade; another hostd's runs start detached too, so a drop is
// re-attached within -hostd-reattach-timeout, but die with this daemon
// (newPinnedHostdRuns), since only the default hostd's runs are
// re-adopted at start.
func (r hostdRoute) apply(live Config) Config {
	if !r.Pinned {
		return live
	}
	exec := r.Exec
	live.HostExec = &exec
	if r.Name != live.DefaultHostd() {
		live.hostdRuns = nil
		if live.pinnedHostdRuns != nil {
			live.hostdRuns = live.pinnedHostdRuns(exec)
		}
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

// pauseOwner is hostdPauseOwner for b on req's hostd.
func (req poolRequest) pauseOwner(live Config, b model.AgentBackend) string {
	if req.pauseHostd != "" && ambientCredential(live, b) {
		return hostdPauseOwner(b.CredentialOwnerID(), req.pauseHostd)
	}
	return b.CredentialOwnerID()
}

// hostdCapacityTTL is how long a hostd's reported capacity is believed.
const hostdCapacityTTL = 30 * time.Second

type hostdCapacityEntry struct {
	capacity int
	at       time.Time
}

// hostdCapacity is how many runs at once r's hostd takes (its
// allowlist's capacity:, Hello.Capacity), 0 for no limit. A hostd that
// does not answer is not limited here: its run fails on its own.
func (p *AgentPool) hostdCapacity(ctx context.Context, r hostdRoute) int {
	if r.Name == "" {
		return 0
	}
	p.mu.Lock()
	if e, ok := p.hostdCapacities[r.Name]; ok && time.Since(e.at) < hostdCapacityTTL {
		p.mu.Unlock()
		return e.capacity
	}
	p.mu.Unlock()
	hctx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdLabelsTimeout)
	defer cancel()
	hello, err := r.Exec.hostdClient().Hello(hctx)
	if err != nil {
		return 0
	}
	p.mu.Lock()
	if p.hostdCapacities == nil {
		p.hostdCapacities = map[string]hostdCapacityEntry{}
	}
	p.hostdCapacities[r.Name] = hostdCapacityEntry{capacity: hello.Capacity, at: time.Now()}
	p.mu.Unlock()
	return hello.Capacity
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
// r's hostd: always for a pinned task, whose pin says its work is that
// hostd's, and otherwise when the framework is set to run on the host.
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
	if errors.As(err, &wait) {
		return wait.Error()
	}
	var full *errHostdFull
	if errors.As(err, &full) {
		return full.Error()
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
