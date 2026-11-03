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
		hub.OnChange = hostdConnectionNotifier(store)
	}
	return hub
}

// hostdDisconnectedSubject is the notification a hostd's drop raises.
func hostdDisconnectedSubject(name string) string { return "hostd-disconnected:" + name }

// hostdConnectionNotifier raises hostd-disconnected:<name> when a hostd
// that dials in drops, and clears it when that hostd connects again.
// Work pinned to it waits meanwhile (HostdWaitError); nothing moves to
// another hostd.
func hostdConnectionNotifier(store *model.Store) func(name string, connected bool) {
	return func(name string, connected bool) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			subject := hostdDisconnectedSubject(name)
			if connected {
				if _, err := store.ClearNotification(ctx, subject, "automation:hostd"); err != nil {
					log.Printf("grain daemon: clearing %s: %v", subject, err)
				}
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
				Title:    "grain hostd " + name + " disconnected",
				Body: "Host work pinned to hostd " + name + " waits for it to dial in again (grain hostd -server); " +
					"nothing is moved to another hostd. This clears itself when it reconnects.",
				RaisedBy: "automation:hostd",
				RaisedAt: time.Now().UTC(),
			}); err != nil {
				log.Printf("grain daemon: raising %s: %v", subject, err)
			}
		}()
	}
}

// registryResolver names a hostd token from the registry: the live hostd
// it belongs to, and that hostd's owner.
func registryResolver(store *model.Store) func(ctx context.Context, token string) (string, string, bool, error) {
	return func(ctx context.Context, token string) (string, string, bool, error) {
		h, err := store.HostdForToken(ctx, token)
		if err != nil || h == nil {
			return "", "", false, err
		}
		if h.Name == model.LocalHostd {
			// local is the socket's; nothing dials in as it.
			return "", "", false, nil
		}
		return h.Name, h.Owner, true, nil
	}
}

// HostdSelector says which hostd a piece of host work goes to: Name pins
// one hostd; Labels select any hostd carrying every one of them (the
// registry's labels and its allowlist's, merged); neither is the
// default hostd -- local, or the deprecated shared token's hostd on a
// daemon configured with one.
type HostdSelector struct {
	Name   string            `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// String is s as a log line or a wait reason says it.
func (s HostdSelector) String() string {
	if s.Name != "" {
		return "hostd " + s.Name
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
	return "a hostd labelled " + strings.Join(parts, ",")
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
		return "waiting for hostd " + e.Name
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

// hostdLabelsTimeout bounds asking a connected hostd for its allowlist's
// labels while selecting one.
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
		return HostExecutor{}, "", fmt.Errorf("no hostd registry to select %s from", sel)
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
		labels := map[string]string{}
		if connected {
			lctx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdLabelsTimeout)
			if hello, err := exec.hostdClient().Hello(lctx); err == nil {
				for k, v := range hello.Labels {
					labels[k] = v
				}
			}
			cancel()
		}
		// The registry's labels win over the allowlist's: they are what
		// the operator set for this hostd in grain.
		for k, v := range h.Labels {
			labels[k] = v
		}
		if !labelsMatch(labels, sel.Labels) {
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
		return HostExecutor{}, "", fmt.Errorf("no hostd in the registry is %s", strings.TrimPrefix(sel.String(), "a hostd "))
	case 1:
		return HostExecutor{}, "", &HostdWaitError{Name: matched[0], Selector: sel}
	}
	return HostExecutor{}, "", &HostdWaitError{Selector: sel}
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
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
			return HostExecutor{}, fmt.Errorf("this deployment has no co-located hostd (%s)", exec.Reason)
		}
		return exec, nil
	}
	shared := c.HostdRemote != nil && name == c.HostdRemote.DefaultName()
	if !shared {
		if store == nil {
			return HostExecutor{}, fmt.Errorf("hostd %s: no hostd registry to look it up in", name)
		}
		h, err := store.GetHostd(ctx, name)
		if err != nil {
			return HostExecutor{}, err
		}
		if h == nil {
			return HostExecutor{}, fmt.Errorf("hostd %s is not in the registry (/api/hostds)", name)
		}
		if h.Revoked() {
			return HostExecutor{}, fmt.Errorf("hostd %s is revoked", name)
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
		Reason: "host work pinned to grain hostd " + name + ", which dials in to this daemon"}, nil
}
