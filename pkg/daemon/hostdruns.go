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
	"log"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/version"
)

// newHostdRuns is what a run's host CLI goes through where host CLIs run
// through grain hostd (design/design/cli-run-location.design.md, "Upgrades and
// host runs"): started detached with the run's id, task and deadline, its
// stdout cursor persisted (model.Store.SetHostdCursor) for the next
// daemon to re-attach from, and left running when this daemon stops
// (orchestrator.StoppedByDaemon) or an upgrade flushes it, and re-attached
// when hostd drops and is back within -hostd-reattach-timeout.
//
// It holds a runhost.Hostd per grain hostd: the default hostd's, and one
// for each hostd a pinned task's run is routed to (hostdRoute.apply), so
// a run on any hostd is detached and re-adopted from the hostd it ran on
// (adoptHostd). nil where there is no hostd at all: the host executor is
// not hostd and no hostd can dial in.
func newHostdRuns(cfg Config, store *model.Store) *hostdRunSet {
	exec := cfg.hostExecutor()
	hub := cfg.HostdHub
	if hub == nil {
		hub = cfg.HostdRemote
	}
	if exec.Kind != HostExecutorHostd && hub == nil {
		return nil
	}
	newHostd := func(exec HostExecutor) *runhost.Hostd {
		return hostdRunsOn(cfg, store, exec, orchestrator.StoppedByDaemon)
	}
	set := &hostdRunSet{defaultName: cfg.DefaultHostd(), newHostd: newHostd, byName: map[string]*runhost.Hostd{}}
	if exec.Kind == HostExecutorHostd {
		set.byName[set.defaultName] = newHostd(exec)
	}
	if hub != nil {
		set.executor = func(name string) (HostExecutor, bool) {
			if name == model.LocalHostd {
				return HostExecutor{}, false
			}
			return HostExecutor{Kind: HostExecutorHostd, Remote: hub, RemoteHostd: true, Hostd: name}, true
		}
		set.wait = func(ctx context.Context, name string) error {
			if name == model.LocalHostd {
				return nil
			}
			return hub.WaitHostd(ctx, name)
		}
	}
	return set
}

// hostdRunsOn is the runhost.Hostd for exec's hostd, detaching on a
// cancellation detach says is the daemon stopping, re-attaching within
// cfg.HostdReattachTimeout of a drop -- the moment a hostd that dials in
// is back (hostd.Remote.WaitHostd) -- and persisting its runs' cursors in
// store.
func hostdRunsOn(cfg Config, store *model.Store, exec HostExecutor, detach func(context.Context) bool) *runhost.Hostd {
	d := &runhost.Hostd{
		Client:          exec.hostdClient(),
		Detach:          detach,
		ReattachTimeout: cfg.HostdReattachTimeout,
		Name:            exec.Hostd,
		OnCursor: func(ctx context.Context, run string, cursor int64) {
			if err := store.SetHostdCursor(ctx, run, cursor, time.Now().UTC()); err != nil {
				log.Printf("grain daemon: run %s: persisting its grain hostd cursor: %v", run, err)
			}
		},
	}
	if remote := exec.Remote; remote != nil {
		name := exec.Hostd
		if name == "" {
			name = remote.DefaultName()
		}
		d.Name = name
		d.Reconnected = func(ctx context.Context) error { return remote.WaitHostd(ctx, name) }
	}
	return d
}

// hostdRunSet is a runhost.Hostd per grain hostd, by registry name
// (newHostdRuns).
type hostdRunSet struct {
	// defaultName is the default hostd's name (Config.DefaultHostd): the
	// hostd a live run with no recorded hostd (task_run_hostd) ran on.
	defaultName string
	// newHostd builds the runhost.Hostd for the hostd exec reaches.
	newHostd func(exec HostExecutor) *runhost.Hostd
	// executor is the host executor reaching the hostd name, for one
	// this set has not met yet; nil, or false, where it cannot be
	// reached.
	executor func(name string) (HostExecutor, bool)
	// wait blocks until the hostd name is connected, or ctx ends; nil
	// for no wait.
	wait func(ctx context.Context, name string) error

	mu     sync.Mutex
	byName map[string]*runhost.Hostd
}

// defaultRuns is the default hostd's runhost.Hostd, nil where the host
// executor is not hostd (or s is nil).
func (s *hostdRunSet) defaultRuns() *runhost.Hostd {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byName[s.defaultName]
}

// forHostd is the runhost.Hostd of the hostd name, built on exec the
// first time it is asked for. nil when s is.
func (s *hostdRunSet) forHostd(name string, exec HostExecutor) *runhost.Hostd {
	if s == nil || name == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if hd := s.byName[name]; hd != nil {
		return hd
	}
	hd := s.newHostd(exec)
	s.byName[name] = hd
	return hd
}

// named is forHostd for a hostd only its name is known of -- one a run
// recorded (task_run_hostd) -- or false where it cannot be reached.
func (s *hostdRunSet) named(name string) (*runhost.Hostd, bool) {
	s.mu.Lock()
	hd := s.byName[name]
	s.mu.Unlock()
	if hd != nil {
		return hd, true
	}
	if s.executor == nil {
		return nil, false
	}
	exec, ok := s.executor(name)
	if !ok {
		return nil, false
	}
	return s.forHostd(name, exec), true
}

// each is every runhost.Hostd this set holds, by name.
func (s *hostdRunSet) each() map[string]*runhost.Hostd {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*runhost.Hostd, len(s.byName))
	for name, hd := range s.byName {
		out[name] = hd
	}
	return out
}

// Flush is runhost.Hostd.Flush on every hostd's runs.
func (s *hostdRunSet) Flush(ctx context.Context) ([]string, error) {
	var runs []string
	var errs []error
	for _, hd := range s.each() {
		r, err := hd.Flush(ctx)
		runs = append(runs, r...)
		errs = append(errs, err)
	}
	sort.Strings(runs)
	return runs, errors.Join(errs...)
}

// Resume is runhost.Hostd.Resume on every hostd's runs.
func (s *hostdRunSet) Resume() {
	for _, hd := range s.each() {
		hd.Resume()
	}
}

// daemonBuild is this daemon's build, as it says it in hello and on its
// run hosts' generation label.
func daemonBuild() string {
	v := version.Get()
	if v.Release != "" {
		return v.Release
	}
	return v.Revision
}
