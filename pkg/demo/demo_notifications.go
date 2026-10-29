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

// demo_notifications.go is `grain demo -notifications`: the daemon's
// per-cycle notification sweep (orchestrator.ClearMetNotifications), run
// every -reconcile interval (default 2s) against the demo's store, so a
// notification raised with clears_when -- a task closing or merging, a
// base branch going green, a ttl passing -- clears on its own.
//
// Without it nothing in a demo ever clears a notification by its
// condition: the 2026-10-28 operator-notifications walkthrough
// (docs/test-runs/2026-10-28-operator-notifications.md) raised one with
// a one-minute ttl and watched it stay open. It is opt-in because the
// ordinary demo's seeded notifications are counted by other specs.

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// runDemoNotifications runs the notification sweep once straight away and
// then every interval until ctx is done, logging a failed cycle as
// runDemoSchedules does.
func runDemoNotifications(ctx context.Context, store *model.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := orchestrator.ClearMetNotifications(ctx, store, time.Now().UTC()); err != nil {
			log.Printf("grain demo: notifications cycle: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
