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

package demo

// demo_reconcile.go is `grain demo -reconcile <interval>`: the two steps
// of the daemon's reconcile loop a suite or release walkthrough waits on,
// run on a timer against the demo's own store. Without it nothing calls
// orchestrator.SyncSuites or SyncReleases, so a suite run never advances
// past its first pass and a release never leaves Provisioning, and a
// walkthrough that says "within two reconcile cycles" or "the daemon log"
// cannot be walked as written (docs/test-runs/2026-10-12-suite-release-
// batch-fixes.md, finding 5).
//
// It is those two steps and nothing else. The demo still runs no agents,
// so a pass's tasks only finish when something finishes them
// (ui/e2e/releasehelper's complete-suite-pass), and releases are only
// synced with -github-sim, whose simulated GitHub is the one thing here
// SyncReleases can create and move branches on.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// runDemoReconcile runs demoReconcileCycle once straight away and then
// every interval until ctx is done, logging a failed cycle the way the
// daemon's loop logs one (pkg/daemon's ReconcileWith), so a walkthrough
// can grep the demo's own output for it. A nil releases skips the
// release step.
func runDemoReconcile(ctx context.Context, store *model.Store, releases github.Client, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := demoReconcileCycle(ctx, store, releases, time.Now().UTC()); err != nil {
			log.Printf("grain demo: reconcile cycle: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// demoReconcileCycle is one cycle of runDemoReconcile: "releases" then
// "suites", in the order orchestrator.Reconcilers runs them, each error
// named after its reconciler as orchestrator.RunCycle names it.
func demoReconcileCycle(ctx context.Context, store *model.Store, releases github.Client, now time.Time) error {
	var errs []error
	if releases != nil {
		if v, ok := demoSimClients.Load(releases); ok {
			if st, _ := v.(*demoSimState); st != nil {
				if st.events != nil {
					processDemoGitHubWebhooks(ctx, store, st.events, st.calls, now)
				}
				if st.calls != nil {
					st.calls.pollDemoPullRequests(ctx, store)
				}
			}
		}
		if err := orchestrator.SyncReleases(ctx, store, releases, now); err != nil {
			errs = append(errs, fmt.Errorf("releases: %w", err))
		}
		// The periodic issue import, over the same simulated GitHub, so
		// a repository with it switched on imports at its interval here
		// as it does on a daemon, not only on "Import issues now".
		if err := runDemoReconcilers(ctx, orchestrator.Deps{Store: store, Client: releases}, now, "issue-imports"); err != nil {
			errs = append(errs, err)
		}
	}
	if err := orchestrator.SyncSuites(ctx, store, now); err != nil {
		errs = append(errs, fmt.Errorf("suites: %w", err))
	}
	return errors.Join(errs...)
}
