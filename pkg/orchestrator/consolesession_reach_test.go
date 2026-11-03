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
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-2641: every console session reads every repository this
// deployment works in, resolved each cycle, and holds CI read.

func putWorkingRepo(t *testing.T, store *model.Store, owner, name string, ownership model.Ownership) {
	t.Helper()
	if err := store.PutRepoConfig(context.Background(), model.RepoConfig{
		Repo: model.RepoRef{Owner: owner, Name: name},
		Work: model.WorkOwnership{Default: ownership},
	}); err != nil {
		t.Fatal(err)
	}
}

func holdsGrant(task model.Task, capability string) bool {
	return slices.ContainsFunc(task.Grants, func(g model.Grant) bool { return g.Capability == capability })
}

func reloadTask(t *testing.T, store *model.Store, id string) model.Task {
	t.Helper()
	task, err := store.GetTask(context.Background(), id)
	if err != nil || task == nil {
		t.Fatalf("GetTask(%s) = %v, %v", id, task, err)
	}
	return *task
}

func TestConsoleSessionReadsEveryWorkingRepoAndHoldsCIRead(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	putWorkingRepo(t, store, "acme", "widgets", model.OwnershipOn)
	putWorkingRepo(t, store, "acme", "retired", model.OwnershipOff)
	settings(t, store, func(cfg *model.Config) {
		cfg.TargetRepos = []string{"acme/docs", "acme/retired", "acme/*"}
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            "releases",
			Title:         "Releases",
			Capabilities:  []string{"self-debug"},
			NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	session := sessionTasks(t, store)[0]

	for _, want := range []string{"widgets", "docs"} {
		if !hasRead(session.Reads, "acme", want) {
			t.Errorf("Reads = %v, want acme/%s, a repository this deployment works in", session.Reads, want)
		}
	}
	if hasRead(session.Reads, "acme", "retired") {
		t.Errorf("Reads = %v, want the retired acme/retired left out", session.Reads)
	}
	if !holdsGrant(session, model.ConsoleSessionCIReadGrant) {
		t.Errorf("Grants = %+v, want %s", session.Grants, model.ConsoleSessionCIReadGrant)
	}
	if len(session.Writes) != 0 {
		t.Errorf("Writes = %v, want none: reading every repository grants no writes", session.Writes)
	}

	// A repository configured later is readable on the next cycle, and
	// one retired since is dropped.
	putWorkingRepo(t, store, "acme", "infra", model.OwnershipOn)
	putWorkingRepo(t, store, "acme", "widgets", model.OwnershipOff)
	runSessions(t, store, baseTime.Add(time.Minute))
	synced := reloadTask(t, store, session.ID)
	if !hasRead(synced.Reads, "acme", "infra") {
		t.Errorf("after adding acme/infra, Reads = %v, want it", synced.Reads)
	}
	if hasRead(synced.Reads, "acme", "widgets") {
		t.Errorf("after retiring acme/widgets, Reads = %v, want it dropped", synced.Reads)
	}
	if !hasRead(synced.Reads, "acme", "docs") {
		t.Errorf("Reads = %v, lost acme/docs", synced.Reads)
	}
}

func TestConsoleSessionReadsAllOptOutSticks(t *testing.T) {
	t.Parallel()
	store, _ := openStore(t)
	putWorkingRepo(t, store, "acme", "widgets", model.OwnershipOn)
	declare := func(readsAll *bool) {
		settings(t, store, func(cfg *model.Config) {
			cfg.ConsoleSessions = []model.ConsoleSession{{
				ID:            model.DirectorSessionID,
				Title:         "Director",
				NetworkAccess: model.NetworkOpen,
				Reads:         []model.RepoRef{{Owner: "acme", Name: "docs"}},
				ReadsAll:      readsAll,
			}}
		})
	}
	declare(nil)
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	if !hasRead(director.Reads, "acme", "widgets") {
		t.Fatalf("Reads = %v, want acme/widgets by default", director.Reads)
	}

	off := false
	declare(&off)
	for i := 1; i <= 3; i++ {
		runSessions(t, store, baseTime.Add(time.Duration(i)*time.Minute))
		synced := reloadTask(t, store, director.ID)
		if hasRead(synced.Reads, "acme", "widgets") {
			t.Fatalf("cycle %d after opting out, Reads = %v, want acme/widgets gone", i, synced.Reads)
		}
		if !hasRead(synced.Reads, "acme", "docs") {
			t.Fatalf("cycle %d after opting out, Reads = %v, want the declared acme/docs kept", i, synced.Reads)
		}
	}
	// A repository added while opted out stays out too.
	putWorkingRepo(t, store, "acme", "infra", model.OwnershipOn)
	runSessions(t, store, baseTime.Add(5*time.Minute))
	if synced := reloadTask(t, store, director.ID); hasRead(synced.Reads, "acme", "infra") {
		t.Errorf("Reads = %v, want acme/infra left out of an opted-out session", synced.Reads)
	}
}

// A session stored before CI read was a default -- its Capabilities list
// does not name it, and its task's snapshot predates OfferedDefaults --
// is given it on the next cycle; once an operator removes it from the
// task it is not given back.
func TestConsoleSessionCIReadReachesExistingSessionsAndRemovalSticks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{"self-debug"},
			NetworkAccess: model.NetworkOpen,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]

	// Make it look filed before this change: no CI read on the task, and
	// a snapshot that never offered it.
	if err := store.UpdateTask(ctx, director.ID, func(task *model.Task) error {
		task.Grants = slices.DeleteFunc(task.Grants, func(g model.Grant) bool {
			return g.Capability == model.ConsoleSessionCIReadGrant
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "DELETE FROM `console_session_sync`"); err != nil {
		t.Fatal(err)
	}
	if err := model.RecordSyncedConsoleSession(ctx, store, director.ID, model.ConsoleSessionConfigSnapshot{
		Capabilities:  []string{"self-debug"},
		NetworkAccess: model.NetworkOpen,
		Title:         "Director",
	}); err != nil {
		t.Fatal(err)
	}

	runSessions(t, store, baseTime.Add(time.Minute))
	upgraded := reloadTask(t, store, director.ID)
	if !holdsGrant(upgraded, model.ConsoleSessionCIReadGrant) {
		t.Fatalf("after the upgrade's first cycle, Grants = %+v, want %s", upgraded.Grants, model.ConsoleSessionCIReadGrant)
	}
	for _, g := range upgraded.Grants {
		if g.Capability == model.ConsoleSessionCIReadGrant && g.Via != model.GrantByDefault {
			t.Errorf("%s granted %q, want %q", g.Capability, g.Via, model.GrantByDefault)
		}
	}

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	if err := client.SetCapability(ctx, director.ID, model.ConsoleSessionCIReadGrant, false); err != nil {
		t.Fatalf("SetCapability detach %s: %v", model.ConsoleSessionCIReadGrant, err)
	}
	for i := 3; i <= 5; i++ {
		runSessions(t, store, baseTime.Add(time.Duration(i)*time.Minute))
		if synced := reloadTask(t, store, director.ID); holdsGrant(synced, model.ConsoleSessionCIReadGrant) {
			t.Fatalf("cycle %d after the operator removed it, Grants = %+v, want %s still gone",
				i, synced.Grants, model.ConsoleSessionCIReadGrant)
		}
	}
}

// Unticking CI read in the session's own pane declines it on the
// settings row, so it stays off across cycles and when the session's
// task is filed again.
func TestConsoleSessionCIReadUntickedInThePaneStaysOffAcrossRestart(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = model.DefaultConsoleSessions()
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	if !holdsGrant(director, model.ConsoleSessionCIReadGrant) {
		t.Fatalf("filed director Grants = %+v, want %s", director.Grants, model.ConsoleSessionCIReadGrant)
	}

	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime.Add(time.Minute) }
	listed, err := client.ListConsoleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(listed[0].Capabilities, model.ConsoleSessionCIReadGrant) {
		t.Fatalf("ListConsoleSessions capabilities = %v, want %s listed as held", listed[0].Capabilities, model.ConsoleSessionCIReadGrant)
	}
	caps := slices.DeleteFunc(slices.Clone(listed[0].Capabilities), func(c string) bool {
		return c == model.ConsoleSessionCIReadGrant
	})
	if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID, ui.UpdateConsoleSessionRequest{Capabilities: &caps}); err != nil {
		t.Fatalf("UpdateConsoleSession: %v", err)
	}
	runSessions(t, store, baseTime.Add(2*time.Minute))
	if synced := reloadTask(t, store, director.ID); holdsGrant(synced, model.ConsoleSessionCIReadGrant) {
		t.Fatalf("after unticking, Grants = %+v, want %s gone", synced.Grants, model.ConsoleSessionCIReadGrant)
	}

	restarted, err := client.RestartConsoleSession(ctx, model.DirectorSessionID, ui.RestartConsoleSessionRequest{})
	if err != nil {
		t.Fatalf("RestartConsoleSession: %v", err)
	}
	runSessions(t, store, baseTime.Add(3*time.Minute))
	replacement := reloadTask(t, store, restarted[0].TaskID)
	if replacement.ID == director.ID {
		t.Fatalf("restart kept task %s, want a new one", director.ID)
	}
	if holdsGrant(replacement, model.ConsoleSessionCIReadGrant) {
		t.Errorf("after restart, Grants = %+v, want the declined %s still off", replacement.Grants, model.ConsoleSessionCIReadGrant)
	}

	// Ticking it again brings it back.
	caps = append(caps, model.ConsoleSessionCIReadGrant)
	if _, err := client.UpdateConsoleSession(ctx, model.DirectorSessionID, ui.UpdateConsoleSessionRequest{Capabilities: &caps}); err != nil {
		t.Fatalf("UpdateConsoleSession: %v", err)
	}
	runSessions(t, store, baseTime.Add(4*time.Minute))
	if synced := reloadTask(t, store, replacement.ID); !holdsGrant(synced, model.ConsoleSessionCIReadGrant) {
		t.Errorf("after ticking it again, Grants = %+v, want %s", synced.Grants, model.ConsoleSessionCIReadGrant)
	}
}
