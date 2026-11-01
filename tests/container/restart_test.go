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

package container

// A daemon restart underneath a `grain mcpserver` call
// (design/design/non-disruptive-upgrade.design.md, Stage 1).
//
// An agent's tools reach the daemon through pkg/ui.HTTPClient, the same
// client `grain mcpserver` is built on. While an upgrade restarts the
// daemon its API refuses connections, and a tool call made in that window
// used to fail outright and land in the agent's transcript as an error.
// The client now waits a restart out (ui.RetryPolicy); this restarts the
// container the way the unit's restart does -- same container, same data
// directory -- with a call in flight, and asserts the call succeeds once
// the daemon is back.
//
// It also reads what the daemon measured of that restart
// (upgrade.GapRecorder): how long the API was down, in container mode.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func TestAnMCPServerCallInFlightSurvivesADaemonRestart(t *testing.T) {
	requireImage(t)
	d := start(t, t.TempDir(), options{})
	filed := createTask(t, d, "read across a restart")

	client := ui.NewHTTPClient(d.Base)
	// The token `grain mcpserver` reads out of the daemon's environment
	// (ui.LocalAPITokenFileEnv): a single-tenant daemon refuses a caller
	// without it, loopback included.
	client.Token = d.token()
	client.RetryPolicy = &ui.RetryPolicy{Budget: 2 * time.Minute, Initial: 200 * time.Millisecond, Max: 2 * time.Second}
	if _, err := client.Task(t.Context(), filed.ID); err != nil {
		t.Fatalf("reading the task before the restart: %v", err)
	}

	// Down, the way systemd's restart takes it down: SIGTERM, and the
	// drain the daemon gets before docker would kill it.
	docker(t, "stop", "--time", "30", d.Name)
	if d.running() {
		t.Fatalf("%s still running after docker stop", d.Name)
	}

	// The call goes out while nothing is listening.
	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, err := client.Task(ctx, filed.ID)
		done <- result{err: err, elapsed: time.Since(started)}
	}()

	const down = 3 * time.Second
	time.Sleep(down)
	select {
	case r := <-done:
		t.Fatalf("the call returned while the daemon was down (after %s): %v", r.elapsed, r.err)
	default:
	}

	docker(t, "start", d.Name)

	select {
	case r := <-done:
		if r.err != nil {
			var unavailable *ui.DaemonUnavailableError
			if errors.As(r.err, &unavailable) {
				t.Fatalf("the call gave up on the restart after %s: %v\n%s", r.elapsed, r.err, d.logs())
			}
			t.Fatalf("the call failed across the restart: %v\n%s", r.err, d.logs())
		}
		if r.elapsed < down {
			t.Errorf("the call took %s, less than the %s the daemon was down for", r.elapsed, down)
		}
		t.Logf("the call took %s across the restart", r.elapsed)
	case <-time.After(3 * time.Minute):
		t.Fatalf("the call never returned after the daemon came back\n%s", d.logs())
	}

	// What the daemon measured of that restart.
	gap, ok := upgrade.ReadRestartGap(upgrade.GapFile(d.Data))
	if !ok {
		t.Fatalf("no restart gap recorded in %s", filepath.Join(d.Data, "state"))
	}
	var api *upgrade.ServiceGap
	for i := range gap.Services {
		if gap.Services[i].Service == upgrade.ServiceAPI {
			api = &gap.Services[i]
		}
	}
	if api == nil {
		t.Fatalf("restart gap names no API: %+v", gap)
	}
	measured, known := api.Gap()
	if !known {
		t.Fatalf("the API's gap is unknown: the stopping daemon recorded nothing (%+v)\n%s", gap, d.logs())
	}
	if measured < down {
		t.Errorf("measured API gap %s, less than the %s the container was stopped for", measured, down)
	}
	t.Logf("restart gap, measured by the daemon: %s", gap.Summary())
}
