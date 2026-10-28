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

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/version"
)

// newHostdRuns is what a run's host CLI goes through where host CLIs run
// through grain hostd (docs/design/cli-run-location.md, "Upgrades and
// host runs"): started detached with the run's id, task and deadline, its
// stdout cursor persisted (model.Store.SetHostdCursor) for the next
// daemon to re-attach from, and left running when this daemon stops
// (orchestrator.StoppedByDaemon) or an upgrade flushes it. nil where the
// host executor is not hostd.
func newHostdRuns(cfg Config, store *model.Store) *runhost.Hostd {
	exec := cfg.hostExecutor()
	if exec.Kind != HostExecutorHostd {
		return nil
	}
	return &runhost.Hostd{
		Client: hostd.Client{Socket: exec.Socket, Build: daemonBuild()},
		Detach: orchestrator.StoppedByDaemon,
		OnCursor: func(ctx context.Context, run string, cursor int64) {
			if err := store.SetHostdCursor(ctx, run, cursor, time.Now().UTC()); err != nil {
				log.Printf("grain daemon: run %s: persisting its grain hostd cursor: %v", run, err)
			}
		},
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
