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
	"log"
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
// when hostd drops and is back within -hostd-reattach-timeout. nil where
// the host executor is not hostd.
func newHostdRuns(cfg Config, store *model.Store) *runhost.Hostd {
	exec := cfg.hostExecutor()
	if exec.Kind != HostExecutorHostd {
		return nil
	}
	return hostdRunsOn(cfg, store, exec, orchestrator.StoppedByDaemon)
}

// newPinnedHostdRuns is what a run's host CLI goes through on a hostd
// its task is pinned or selected to that is not the default (hostdRoute.
// apply): started detached too, so a hostd that drops is re-attached
// within -hostd-reattach-timeout, but never left running for the next
// daemon -- only the default hostd's runs are re-adopted at start -- so
// a daemon stopping kills it as an attached CLI's would be. One a daemon
// could not kill (it crashed, or hostd was away as it stopped) is
// cancelled when that hostd next connects, once its run has finished
// (releaseFinishedHostdRuns). nil without a store.
func newPinnedHostdRuns(cfg Config, store *model.Store) func(HostExecutor) *runhost.Hostd {
	if store == nil {
		return nil
	}
	return func(exec HostExecutor) *runhost.Hostd { return hostdRunsOn(cfg, store, exec, nil) }
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

// daemonBuild is this daemon's build, as it says it in hello and on its
// run hosts' generation label.
func daemonBuild() string {
	v := version.Get()
	if v.Release != "" {
		return v.Release
	}
	return v.Revision
}
