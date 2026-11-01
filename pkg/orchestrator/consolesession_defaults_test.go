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

package orchestrator_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-2126: on a build that registers a DefaultOn capability (such as
// corp-research on the Google build), both Config.DefaultCapabilities and the
// shipped Director console session start holding it. Once an operator removes
// that capability from the Director session via UpdateConsoleSession, neither
// UpdateConsoleSession itself, nor syncConsoleSessionTask (across cycles or
// daemon restarts), nor restarting the session may re-add it from
// Config.DefaultCapabilities.
func TestRemovedDeploymentDefaultCapabilityStaysRemovedAcrossSessionSyncAndRestart(t *testing.T) {
	unregister := capability.RegisterProvider(capability.Registration{
		ID:          "corp-research",
		Name:        "Corp Research",
		Description: "Internal search tools.",
		New:         func() model.CapabilityProvider { return &capabilitytest.Provider{} },
		RunGrant:    true,
		DefaultOn:   true,
	})
	t.Cleanup(unregister)

	store, ctx := openStore(t)
	if _, err := daemon.LoadConfig(ctx, store, daemon.Config{}); err != nil {
		t.Fatalf("initial LoadConfig: %v", err)
	}
	initialCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initialCfg == nil || !slices.Contains(initialCfg.DefaultCapabilities, "corp-research") {
		t.Fatalf("seeded DefaultCapabilities = %v, want corp-research", initialCfg)
	}

	// First cycle files the Director task with the default session capabilities,
	// including corp-research.
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	hasGrant := func(grants []model.Grant, capID string) bool {
		return slices.ContainsFunc(grants, func(g model.Grant) bool {
			return g.Capability == capID
		})
	}
	if !hasGrant(director.Grants, "corp-research") {
		t.Fatalf("initial director task grants = %+v, want corp-research present", director.Grants)
	}

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }

	// Operator removes corp-research from the Director session while
	// Config.DefaultCapabilities still holds corp-research.
	empty := []string{}
	if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID, ui.UpdateConsoleSessionRequest{
		Capabilities: &empty,
	}); err != nil {
		t.Fatalf("UpdateConsoleSession: %v", err)
	}

	afterUpdate, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(afterUpdate.Grants, "corp-research") {
		t.Fatalf("immediately after UpdateConsoleSession, director task grants = %+v, want corp-research absent", afterUpdate.Grants)
	}

	// Simulate a daemon restart (LoadConfig + RunCycle) on the existing store.
	if _, err := daemon.LoadConfig(ctx, store, daemon.Config{}); err != nil {
		t.Fatalf("restart LoadConfig: %v", err)
	}
	storedCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(storedCfg.DefaultCapabilities, "corp-research") {
		t.Fatalf("stored DefaultCapabilities = %v, want corp-research still present", storedCfg.DefaultCapabilities)
	}

	runSessions(t, store, baseTime.Add(2*time.Minute))
	afterSync, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(afterSync.Grants, "corp-research") {
		t.Fatalf("after restart sync cycle, director task grants = %+v, want corp-research absent", afterSync.Grants)
	}

	// Restarting the Director session files a replacement task via
	// FileConsoleSessionTask and must also keep corp-research absent.
	restarted, err := client.RestartConsoleSession(ctx, model.DirectorSessionID, ui.RestartConsoleSessionRequest{})
	if err != nil {
		t.Fatalf("RestartConsoleSession: %v", err)
	}
	var newTaskID string
	for _, s := range restarted {
		if s.ID == model.DirectorSessionID {
			newTaskID = s.TaskID
		}
	}
	if newTaskID == "" || newTaskID == director.ID {
		t.Fatalf("restarted director taskId = %q, want a new task != %q", newTaskID, director.ID)
	}
	afterRestart, err := store.GetTask(ctx, newTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(afterRestart.Grants, "corp-research") {
		t.Fatalf("after session restart, director task grants = %+v, want corp-research absent", afterRestart.Grants)
	}

	// Another reconcile cycle on the replacement task keeps corp-research absent.
	runSessions(t, store, baseTime.Add(3*time.Minute))
	afterSecondSync, err := store.GetTask(ctx, newTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(afterSecondSync.Grants, "corp-research") {
		t.Fatalf("after post-restart sync cycle, director task grants = %+v, want corp-research absent", afterSecondSync.Grants)
	}
}
