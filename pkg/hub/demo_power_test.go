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

package hub

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub/api"
)

// TestDemoSeedsAStoppedInstanceAndServesStopStart pins the demo fixture the
// SPA's stopped state is reviewed against, and the demo's stand-ins for
// POST /api/entries/{name}/stop and /start.
func TestDemoSeedsAStoppedInstanceAndServesStopStart(t *testing.T) {
	srv := httptest.NewServer(newDemoState().handler())
	t.Cleanup(srv.Close)
	cli, err := api.NewClient(api.ClientConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	seeded, err := cli.GetEntry(ctx, "docs-preview")
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Kind != api.KindInstance || seeded.State != api.StateStopped || seeded.Status != api.StatusOffline || !seeded.AutoWake {
		t.Fatalf("docs-preview = kind %q state %q status %q auto_wake %t, want a stopped auto-waking instance",
			seeded.Kind, seeded.State, seeded.Status, seeded.AutoWake)
	}

	started, err := cli.StartEntry(ctx, "docs-preview")
	if err != nil {
		t.Fatal(err)
	}
	if started.State != api.StateRunning || started.Status != api.StatusOnline {
		t.Fatalf("after start: state %q status %q, want running/online", started.State, started.Status)
	}
	stopped, err := cli.StopEntry(ctx, "docs-preview")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != api.StateStopped || stopped.Status != api.StatusOffline {
		t.Fatalf("after stop: state %q status %q, want stopped/offline", stopped.State, stopped.Status)
	}

	if _, err := cli.StopEntry(ctx, "alice-workstation"); err == nil || !strings.Contains(err.Error(), "can be stopped") {
		t.Fatalf("stopping a connected entry = %v, want invalid_entry_kind", err)
	}
	if _, err := cli.StartEntry(ctx, "no-such-entry"); err == nil {
		t.Fatal("starting an unknown entry succeeded")
	}
}
