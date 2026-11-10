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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A capability server dropping to needs-config raises a deployment alert
// naming it and the reason; its attaching again clears it.
func TestCapabilityServerDropRaisesAndClearsAnAlert(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	defer closeStore()
	const id = "https://grain.dev/capabilities/gcp"
	onDrop, onAttach := capabilityServerAlerts(ctx, store)
	subject := model.NotificationSubject(model.FamilyCapabilityServerDown, id)

	onDrop(servers.Status{ID: id, State: servers.NeedsConfig, ConfigRefused: true,
		Reason: `permission set "iap-test": roles/run.admin exceeds admin ambient-host's role ceiling: add it to the admin's role_ceiling too`})
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no open notification for %s (err %v)", subject, err)
	}
	if !strings.Contains(n.Title, id) || !strings.Contains(n.Body, "role_ceiling") || !strings.Contains(n.Body, "needs-config") {
		t.Errorf("notification = %q / %q, want it to name the server, its state and the reason", n.Title, n.Body)
	}

	onAttach(id)
	if n, err := store.OpenNotification(ctx, subject); err != nil || n != nil {
		t.Errorf("after attaching, open notification = %+v (err %v), want none", n, err)
	}
	// Attaching with nothing raised is a no-op.
	onAttach("https://grain.dev/capabilities/other")

	// A secondary hostd's alert is scoped to that hostd, so its attaching
	// cannot clear an alert open on the deployment's own registry.
	onDrop(servers.Status{ID: id, State: servers.Failed, Error: "local hostd down"})
	wsDrop, wsAttach := capabilityServerAlertsOnHostd(ctx, store, "ws-a")
	wsSubject := model.NotificationSubject(model.FamilyCapabilityServerDown, id+"@ws-a")
	wsDrop(servers.Status{ID: id, State: servers.Failed, Error: "ws-a disconnected"})
	wsAttach(id)
	if n, err := store.OpenNotification(ctx, subject); err != nil || n == nil {
		t.Errorf("ws-a attaching cleared the deployment's own alert: %+v (%v)", n, err)
	}
	if n, err := store.OpenNotification(ctx, wsSubject); err != nil || n != nil {
		t.Errorf("ws-a's own alert remained open after ws-a attached: %+v (%v)", n, err)
	}
}

// A server waiting on something outside grain -- a host credential to be
// renewed -- is raised as waiting for it, without sending anybody to
// Settings, and clears when it attaches.
func TestAWaitingCapabilityServerIsRaisedAsWaiting(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	defer closeStore()
	const id = "https://grain.dev/capabilities/corp-thing"
	onDrop, onAttach := capabilityServerAlerts(ctx, store)
	subject := model.NotificationSubject(model.FamilyCapabilityServerDown, id)

	onDrop(servers.Status{ID: id, State: servers.NeedsConfig, Waiting: "a renewed host credential",
		Reason: "capability server corp-thing: configure: the host credential expired"})
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no open notification for %s (err %v)", subject, err)
	}
	if !strings.Contains(n.Title, "waiting for a renewed host credential") || !strings.Contains(n.Body, "every minute") || strings.Contains(n.Body, "Fix it in Settings") {
		t.Errorf("notification = %q / %q, want it to say what it waits for and that it is retried", n.Title, n.Body)
	}
	onAttach(id)
	if n, err := store.OpenNotification(ctx, subject); err != nil || n != nil {
		t.Errorf("after attaching, open notification = %+v (err %v), want none", n, err)
	}
}
