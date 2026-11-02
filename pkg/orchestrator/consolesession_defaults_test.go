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
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
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

// grain/task-2284: capability edits made directly on a console session's task
// via POST /api/tasks/{id}/capabilities (without ever storing
// Config.ConsoleSessions) must hold across daemon restarts -- a removed
// default stays removed and an attached capability stays attached -- while an
// upgrade's new build-default capability and config edits made while the
// daemon was down still reach the existing session.
func TestConsoleSessionTaskCapabilityEditsSurviveDaemonRestart(t *testing.T) {
	orchestrator.LogToTest(t)
	dir := t.TempDir()
	store, db := openStoreInDir(t, dir)
	closed := false
	t.Cleanup(func() {
		if !closed {
			db.Close()
		}
	})
	ctx := context.Background()

	if _, err := daemon.LoadConfig(ctx, store, daemon.Config{}); err != nil {
		t.Fatalf("initial LoadConfig: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.ConsoleSessions) != 0 {
		t.Fatalf("initial cfg.ConsoleSessions = %+v, want unstored (len 0)", cfg)
	}

	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	hasGrant := func(grants []model.Grant, capID string) bool {
		return slices.ContainsFunc(grants, func(g model.Grant) bool {
			return g.Capability == capID
		})
	}
	if !hasGrant(director.Grants, "self-debug") {
		t.Fatalf("initial director grants = %+v, want self-debug", director.Grants)
	}
	if hasGrant(director.Grants, "self-repair") {
		t.Fatalf("initial director grants = %+v, want self-repair absent", director.Grants)
	}

	const accessCode = "TEST-ACCESS-CODE-2284"
	newClient := func(s *model.Store, now time.Time) *ui.Client {
		cl := ui.NewClient(ui.Config{
			Actor:         ui.DefaultActor("alice"),
			LocalAPIToken: accessCode,
			Capabilities:  ui.OfferedCapabilities(),
			StateRepoRef:  func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
		}, s)
		cl.Now = func() time.Time { return now }
		return cl
	}
	client := newClient(store, baseTime.Add(time.Minute))
	srv := ui.NewServerWithClient(client)
	postCapability := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+director.ID+"/capabilities", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessCode)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
			t.Fatalf("POST /api/tasks/%s/capabilities %s: %d %s", director.ID, body, rec.Code, rec.Body.String())
		}
	}

	postCapability(`{"id":"self-debug","attach":false}`)
	postCapability(`{"id":"self-repair","attach":true}`)

	edited, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(edited.Grants, "self-debug") {
		t.Fatalf("after detach, task grants = %+v, want self-debug absent", edited.Grants)
	}
	if !hasGrant(edited.Grants, "self-repair") {
		t.Fatalf("after attach, task grants = %+v, want self-repair present", edited.Grants)
	}
	sessions, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sessions[0].Capabilities, "self-debug") || !slices.Contains(sessions[0].Capabilities, "self-repair") {
		t.Fatalf("before restart, ListConsoleSessions capabilities = %v, want self-debug absent and self-repair present", sessions[0].Capabilities)
	}
	cfg, err = store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ConsoleSessions) != 0 {
		t.Fatalf("cfg.ConsoleSessions = %+v after task capability edit, want still unstored", cfg.ConsoleSessions)
	}

	// Simulate a daemon restart alongside an upgrade that registers a new
	// DefaultOn capability while the daemon was down.
	db.Close()
	closed = true

	unregisterUpgrade := capability.RegisterProvider(capability.Registration{
		ID:          "upgrade-default",
		Name:        "Upgrade Default",
		Description: "Added by an upgrade while down.",
		New:         func() model.CapabilityProvider { return &capabilitytest.Provider{} },
		RunGrant:    true,
		DefaultOn:   true,
	})
	t.Cleanup(unregisterUpgrade)

	store, db = openStoreInDir(t, dir)
	closed = false

	if _, err := daemon.LoadConfig(ctx, store, daemon.Config{}); err != nil {
		t.Fatalf("restart LoadConfig: %v", err)
	}
	runSessions(t, store, baseTime.Add(2*time.Minute))

	restartedTask, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(restartedTask.Grants, "self-debug") {
		t.Errorf("after daemon restart, task grants = %+v, reverted removed default self-debug", restartedTask.Grants)
	}
	if !hasGrant(restartedTask.Grants, "self-repair") {
		t.Errorf("after daemon restart, task grants = %+v, lost attached self-repair", restartedTask.Grants)
	}
	if !hasGrant(restartedTask.Grants, "upgrade-default") {
		t.Errorf("after daemon restart, task grants = %+v, missing new build-default upgrade-default", restartedTask.Grants)
	}

	restartClient := newClient(store, baseTime.Add(3*time.Minute))
	sessions, err = restartClient.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sessions[0].Capabilities, "self-debug") {
		t.Errorf("after daemon restart, ListConsoleSessions capabilities = %v, reverted self-debug", sessions[0].Capabilities)
	}
	if !slices.Contains(sessions[0].Capabilities, "self-repair") {
		t.Errorf("after daemon restart, ListConsoleSessions capabilities = %v, lost self-repair", sessions[0].Capabilities)
	}
	if !slices.Contains(sessions[0].Capabilities, "upgrade-default") {
		t.Errorf("after daemon restart, ListConsoleSessions capabilities = %v, missing upgrade-default", sessions[0].Capabilities)
	}
	cfg, err = store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ConsoleSessions) != 0 {
		t.Errorf("after daemon restart, cfg.ConsoleSessions = %+v, want still unstored", cfg.ConsoleSessions)
	}

	// Simulate a config edit made while the daemon is down (dropping
	// bootstrap-playbooks from the declaration), then restart again.
	db.Close()
	closed = true
	store, db = openStoreInDir(t, dir)
	closed = false
	settings(t, store, func(c *model.Config) {
		defs := model.DefaultConsoleSessions()
		defs[0].Capabilities = slices.DeleteFunc(defs[0].Capabilities, func(capID string) bool {
			return capID == "bootstrap-playbooks"
		})
		c.ConsoleSessions = defs
	})
	runSessions(t, store, baseTime.Add(4*time.Minute))

	afterConfigEdit, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(afterConfigEdit.Grants, "bootstrap-playbooks") {
		t.Errorf("after offline config removal, task grants = %+v, kept dropped bootstrap-playbooks", afterConfigEdit.Grants)
	}
	if hasGrant(afterConfigEdit.Grants, "self-debug") {
		t.Errorf("after offline config removal, task grants = %+v, reverted removed self-debug", afterConfigEdit.Grants)
	}
	if !hasGrant(afterConfigEdit.Grants, "self-repair") {
		t.Errorf("after offline config removal, task grants = %+v, lost attached self-repair", afterConfigEdit.Grants)
	}
}

// When a task edit seeds console_session_sync on a task that has no snapshot
// yet (such as the first edit after upgrading into console_session_sync,
// before the reconciler's own sync runs), it must record the task's own
// GrantByGrain capabilities and KeepRunning rather than the new declaration's:
// otherwise a GrantByGrain capability dropped while the daemon was down stays
// on the task forever, and one the declaration gained while down is swallowed.
func TestConsoleSessionTaskEditWithoutSnapshotHonoursDeclarationChanges(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug", "self-repair"},
			NetworkAccess: model.NetworkOpen,
			KeepRunning:   true,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	// Clear the persisted snapshot and change the declaration as if the
	// deployment upgraded and edited grain/config while down: self-repair is
	// dropped, bootstrap-playbooks is added, and KeepRunning is turned off.
	if _, err := store.DB().ExecContext(ctx, "DELETE FROM `console_session_sync`"); err != nil {
		t.Fatal(err)
	}
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug", "bootstrap-playbooks"},
			NetworkAccess: model.NetworkOpen,
			KeepRunning:   false,
		}}
	})

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		StateRepoRef: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }

	// Operator detaches self-debug through the task capability API before the
	// reconciler runs its first post-restart sync.
	if err := client.SetCapability(ctx, director.ID, "self-debug", false); err != nil {
		t.Fatalf("SetCapability detach self-debug: %v", err)
	}

	runSessions(t, store, baseTime.Add(2*time.Minute))

	synced, err := store.GetTask(ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasGrant := func(capID string) bool {
		return slices.ContainsFunc(synced.Grants, func(g model.Grant) bool {
			return g.Capability == capID
		})
	}
	if hasGrant("self-debug") {
		t.Errorf("synced Grants = %+v, reverted operator-detached self-debug", synced.Grants)
	}
	if hasGrant("self-repair") {
		t.Errorf("synced Grants = %+v, kept self-repair dropped from declaration while down", synced.Grants)
	}
	if !hasGrant("bootstrap-playbooks") {
		t.Errorf("synced Grants = %+v, swallowed bootstrap-playbooks added to declaration while down", synced.Grants)
	}
	if synced.KeepRunning {
		t.Errorf("synced KeepRunning = true, want false from declaration change while down")
	}
}
