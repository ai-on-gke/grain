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

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// newHostdHub is the Remote every grain hostd dials in to: cfg's
// HostdRemote when the deprecated shared token configured one (so that
// hostd keeps its name and stays the default), or a new one, with the
// hostd registry in store as the way any other token is named.
func newHostdHub(cfg Config, store *model.Store) *hostd.Remote {
	hub := cfg.HostdRemote
	if hub == nil {
		hub = &hostd.Remote{Logf: log.Printf}
	}
	if store != nil {
		hub.Resolve = registryResolver(store)
		hub.ResolveCredential = registryCredentialResolver(store)
		hub.Renew = func(ctx context.Context, name, currentToken string) (string, time.Time, error) {
			return store.RenewHostdCredential(ctx, name, currentToken, time.Now().UTC())
		}
		hub.OnCheckin = func(ctx context.Context, peer hostd.RemotePeer) {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = store.RecordHostdCheckin(cctx, peer.Name, peer.Host, "", "", nil, peer.Build, "", time.Now().UTC())
		}
		hub.OnChange = hostdConnectionNotifier(store, hub)
	}
	return hub
}

// hostdDisconnectedSubject is the notification a hostd's drop raises.
func hostdDisconnectedSubject(name string) string { return "hostd-disconnected:" + name }

// hostdConnectionNotifier raises hostd-disconnected:<name> when a hostd
// that dials in drops, and clears it when that hostd connects again,
// waking dispatch so the work parked on it resumes at once. Work pinned
// to it waits meanwhile (HostdWaitError): a queued run is parked saying
// "waiting for hostd <name>", and a CLI running there detached is
// re-attached if it is back within -hostd-reattach-timeout
// (runhost.Hostd.ReattachTimeout). Nothing moves to another hostd.
// A hostd that connects is also rid of the CLIs it still runs for
// finished runs (releaseFinishedHostdRuns) when hub is set.
//
// Each change is written from its own goroutine, so a connect's clear --
// which first waits on releaseFinishedHostdRuns asking the hostd for its
// processes -- could land after the raise of a drop that followed it at
// once, leaving a hostd that is away with no notification saying so.
// Every change therefore takes the next sequence number for its hostd as
// it is reported, and a write is made, under that hostd's lock, only by
// the latest change: one overtaken by a later change leaves the
// notification to it.
func hostdConnectionNotifier(store *model.Store, hub *hostd.Remote) func(name string, connected bool) {
	var release func(ctx context.Context, name string)
	if hub != nil {
		release = func(ctx context.Context, name string) {
			releaseFinishedHostdRuns(ctx, store, hostd.Client{Dial: hub.Dialer(name), Build: daemonBuild()}, name)
		}
	}
	return hostdConnectionNotifierReleasing(store, release)
}

// hostdConnectionNotifierReleasing is hostdConnectionNotifier with the
// connect-time release of finished runs as a func (nil for none), so a
// test can hold a connect's clear back behind it.
func hostdConnectionNotifierReleasing(store *model.Store, release func(ctx context.Context, name string)) func(name string, connected bool) {
	var mu sync.Mutex
	seqs := map[string]uint64{}
	locks := map[string]*sync.Mutex{}
	report := func(name string) (uint64, *sync.Mutex) {
		mu.Lock()
		defer mu.Unlock()
		seqs[name]++
		if locks[name] == nil {
			locks[name] = &sync.Mutex{}
		}
		return seqs[name], locks[name]
	}
	latest := func(name string, seq uint64) bool {
		mu.Lock()
		defer mu.Unlock()
		return seqs[name] == seq
	}
	return func(name string, connected bool) {
		seq, lock := report(name)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			subject := hostdDisconnectedSubject(name)
			if connected {
				if release != nil {
					release(ctx, name)
				}
				lock.Lock()
				if latest(name, seq) {
					if _, err := store.ClearNotification(ctx, subject, "automation:hostd"); err != nil {
						log.Printf("grain daemon: clearing %s: %v", subject, err)
					}
				}
				lock.Unlock()
				// Tasks parked waiting for it are admitted on the next
				// cycle; wake it rather than wait out -poll-interval.
				wakeCycle()
				return
			}
			lock.Lock()
			defer lock.Unlock()
			if !latest(name, seq) {
				return
			}
			if h, err := store.GetHostd(ctx, name); err == nil && h != nil && h.Revoked() {
				if _, err := store.ClearNotification(ctx, subject, "automation:hostd"); err != nil {
					log.Printf("grain daemon: clearing %s: %v", subject, err)
				}
				return
			}
			if _, err := store.RaiseNotification(ctx, model.Notification{
				Subject:  subject,
				Severity: model.NotificationWarning,
				Title:    "Host " + name + " disconnected",
				Body: "Host work pinned to host " + name + " waits for it to dial in again (grain host -server); " +
					"nothing is moved to another host. This clears itself when it reconnects.",
				RaisedBy: "automation:hostd",
				RaisedAt: time.Now().UTC(),
			}); err != nil {
				log.Printf("grain daemon: raising %s: %v", subject, err)
			}
		}()
	}
}

// releaseFinishedHostdRuns cancels each detached process hostd name
// still holds for a run the store has finished. A pinned hostd's runs
// start detached (newHostdRuns) and are re-adopted at startup from the
// hostd each recorded (adoptHostd), but a hostd that was away then is
// left out of that pass: a CLI it still runs for a run that has since
// been recovered, or one whose run gave up waiting while its hostd was
// away (runhost.Hostd.cancelWhenBack, which does not outlive the
// daemon), would otherwise run on until its own deadline. A process
// whose run is live, or that names no run the store knows, is left
// alone.
func releaseFinishedHostdRuns(ctx context.Context, store *model.Store, c hostd.Client, name string) {
	procs, err := c.Processes(ctx)
	if err != nil {
		log.Printf("grain daemon: listing grain hostd %s's processes for finished runs: %v", name, err)
		return
	}
	for _, p := range procs {
		if !p.Detached || p.Run == "" {
			continue
		}
		finished, found, err := store.RunFinished(ctx, p.Run)
		if err != nil {
			log.Printf("grain daemon: grain hostd %s's process %s: reading run %s: %v", name, p.ID, p.Run, err)
			continue
		}
		if !found || !finished {
			continue
		}
		log.Printf("grain daemon: releasing grain hostd %s's process %s: its run %s has finished", name, p.ID, p.Run)
		if err := c.Cancel(ctx, p.ID); err != nil {
			log.Printf("grain daemon: cancelling grain hostd %s's process %s: %v", name, p.ID, err)
		}
	}
}

// registryResolver names a hostd token from the registry: the live hostd
// it belongs to, and that hostd's owner.
func registryResolver(store *model.Store) func(ctx context.Context, token string) (string, string, bool, error) {
	resolve := registryCredentialResolver(store)
	return func(ctx context.Context, token string) (string, string, bool, error) {
		cred, ok, err := resolve(ctx, token)
		if !ok || err != nil {
			return "", "", false, err
		}
		return cred.Name, cred.Owner, true, nil
	}
}

func registryCredentialResolver(store *model.Store) func(ctx context.Context, token string) (hostd.ResolvedCredential, bool, error) {
	return func(ctx context.Context, token string) (hostd.ResolvedCredential, bool, error) {
		h, err := store.HostdForToken(ctx, token)
		if err != nil || h == nil {
			return hostd.ResolvedCredential{}, false, err
		}
		if h.Name == model.LocalHostd {
			// local is the socket's; nothing dials in as it.
			return hostd.ResolvedCredential{}, false, nil
		}
		var exp time.Time
		if h.TokenExpiresAt != nil {
			exp = *h.TokenExpiresAt
		}
		return hostd.ResolvedCredential{
			Name:      h.Name,
			Owner:     h.Owner,
			PublicKey: h.PublicKey,
			ExpiresAt: exp,
		}, true, nil
	}
}

// HostdSelector says which hostd a piece of host work goes to: Name pins
// one hostd; Labels select any hostd whose registry row carries every
// one of them (never the labels a hostd's allowlist asserts about
// itself, which are for display); neither is the
// default hostd -- local, or the deprecated shared token's hostd on a
// daemon configured with one.
type HostdSelector struct {
	Name   string            `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// String is s as a log line or a wait reason says it.
func (s HostdSelector) String() string {
	if s.Name != "" {
		return "host " + s.Name
	}
	if len(s.Labels) == 0 {
		return "the default hostd"
	}
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+s.Labels[k])
	}
	return "a host labelled " + strings.Join(parts, ",")
}

// HostdWaitError is SelectHostd's answer when the hostd host work is
// pinned or selected to is known but not connected: the work waits for
// it. It never goes to another hostd instead.
type HostdWaitError struct {
	// Name is the hostd waited for; "" when a selector matched several
	// and none is connected.
	Name     string
	Selector HostdSelector
}

func (e *HostdWaitError) Error() string {
	if e.Name != "" {
		return "waiting for host " + e.Name
	}
	return "waiting for " + e.Selector.String()
}

// Is makes a HostdWaitError agent.ErrHostdWaiting, so a run whose
// framework build meets one goes back to the queue rather than failing
// setup (orchestrator's setupFailedEnding), and waits there.
func (e *HostdWaitError) Is(target error) bool { return target == agent.ErrHostdWaiting }

// DefaultHostd is the hostd host work goes to when nothing pins or
// selects one.
func (c Config) DefaultHostd() string {
	if c.HostdRemote != nil {
		return c.HostdRemote.DefaultName()
	}
	return model.LocalHostd
}

// hostdLabelsTimeout bounds asking a connected hostd for its hello (its
// capacity, for holding runs to it).
const hostdLabelsTimeout = 2 * time.Second

// SelectHostd is the host executor sel routes host work to, and that
// hostd's name. A pinned or selected hostd that is in the registry but
// not connected is a *HostdWaitError; one that is not in the registry,
// is revoked, or that nothing matches is an error that will not resolve
// by waiting.
func (c Config) SelectHostd(ctx context.Context, store *model.Store, sel HostdSelector) (HostExecutor, string, error) {
	if sel.Name == "" && len(sel.Labels) == 0 {
		sel.Name = c.DefaultHostd()
	}
	if sel.Name != "" {
		exec, err := c.hostdExecutor(ctx, store, sel.Name)
		return exec, sel.Name, err
	}
	if store == nil {
		return HostExecutor{}, "", fmt.Errorf("no host registry to select %s from", sel)
	}
	rows, err := store.ListHostds(ctx)
	if err != nil {
		return HostExecutor{}, "", err
	}
	var matched []string
	for _, h := range rows {
		if h.Revoked() {
			continue
		}
		exec, err := c.hostdExecutor(ctx, store, h.Name)
		connected := err == nil
		// Only the registry's labels select a hostd: they are what an
		// admin set for it in grain. The labels a hostd asserts about
		// itself (its allowlist's labels:, in its hello) are its
		// operator's word alone, so they are shown and never matched --
		// or any hostd's operator could claim a label and draw in the
		// host work, and the task tokens, selected by it.
		if !labelsMatch(h.Labels, sel.Labels) {
			continue
		}
		if connected {
			return exec, h.Name, nil
		}
		var waitErr *HostdWaitError
		if errors.As(err, &waitErr) {
			matched = append(matched, h.Name)
		}
	}
	switch len(matched) {
	case 0:
		return HostExecutor{}, "", fmt.Errorf("no host in the registry is %s", strings.TrimPrefix(sel.String(), "a host "))
	case 1:
		return HostExecutor{}, "", &HostdWaitError{Name: matched[0], Selector: sel}
	}
	return HostExecutor{}, "", &HostdWaitError{Selector: sel}
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		hv, ok := have[k]
		if !ok || hv != v {
			return false
		}
	}
	return true
}

// hostdExecutor is the host executor that reaches the hostd name: the
// co-located one at its socket for local, and otherwise the one dialed
// in under that name -- a *HostdWaitError while it is not connected.
func (c Config) hostdExecutor(ctx context.Context, store *model.Store, name string) (HostExecutor, error) {
	if name == model.LocalHostd {
		exec := c.hostExecutor()
		if exec.Remote != nil || !exec.HostAvailable() {
			return HostExecutor{}, fmt.Errorf("this deployment has no co-located host (%s)", exec.Reason)
		}
		return exec, nil
	}
	shared := c.HostdRemote != nil && name == c.HostdRemote.DefaultName()
	if !shared {
		if store == nil {
			return HostExecutor{}, fmt.Errorf("host %s: no host registry to look it up in", name)
		}
		h, err := store.GetHostd(ctx, name)
		if err != nil {
			return HostExecutor{}, err
		}
		if h == nil {
			return HostExecutor{}, fmt.Errorf("host %s is not in the registry (/api/hostds)", name)
		}
		if h.Revoked() {
			return HostExecutor{}, fmt.Errorf("host %s is revoked", name)
		}
	}
	hub := c.HostdHub
	if hub == nil {
		hub = c.HostdRemote
	}
	if hub == nil || !hub.HostdStatus(name).Connected {
		return HostExecutor{}, &HostdWaitError{Name: name, Selector: HostdSelector{Name: name}}
	}
	return HostExecutor{Kind: HostExecutorHostd, Remote: hub, RemoteHostd: true, Hostd: name,
		Reason: "host work pinned to host " + name + ", which dials in to this daemon"}, nil
}
