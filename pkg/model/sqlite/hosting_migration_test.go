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

package sqlite_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSchema123MigratesConsoleToHosting(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	for _, tk := range []model.Task{
		task("t-unattended", true),
		task("t-console", true),
		{
			ID:          "t-session-console",
			Intent:      model.IntentImplement,
			Title:       "Ops Terminal",
			Origin:      model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonConsoleSession},
			Interactive: true,
			Admission:   model.AdmissionDurable,
			Tags:        []string{model.ConsoleSessionTag("ops")},
			Approval:    &model.Attribution{Actor: human},
			Binding:     model.BindingDirective,
		},
		{
			ID:          "t-session-plain",
			Intent:      model.IntentImplement,
			Title:       "Director",
			Origin:      model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonConsoleSession},
			Interactive: true,
			Admission:   model.AdmissionDurable,
			Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
			Approval:    &model.Attribution{Actor: human},
			Binding:     model.BindingDirective,
		},
	} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.PutTemplate(ctx, model.Template{
		ID:        "tmpl-console",
		Name:      "Console",
		Title:     "Console",
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID:         "sched-console",
		Title:      "Console",
		Target:     model.RepoRef{Owner: "acme", Name: "widgets"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
		NextRunAt:  now.Add(time.Hour),
		CreatedAt:  now,
	}); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"release-gate-read", model.ConsoleCapability},
		CICapabilities:      []string{"release-gate-read", model.ConsoleCapability},
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	cfg.TargetRepos = []string{"Acme/Unconfigured"}
	cfg.DefaultCapabilities = []string{"release-gate-read", model.ConsoleCapability}
	cfg.ConsoleSessions = []model.ConsoleSession{
		{
			ID:           "ops",
			Title:        "Ops Terminal",
			Capabilities: []string{"release-gate-read", model.ConsoleCapability},
		},
		{
			ID:           model.DirectorSessionID,
			Title:        "Director",
			Capabilities: []string{"release-gate-read"},
		},
	}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: model.ConsoleCapability, Repos: []string{"acme/widgets"}},
			{Capability: "release-gate-read", Repos: []string{"acme/widgets"}},
		},
		Catalogue: &model.CapabilityCatalogue{
			InProcess: map[string]model.CatalogueInProcess{
				model.ConsoleCapability: {},
				"release-gate-read":     {},
			},
			Defaults: model.CatalogueDefaults{
				Task: []string{"release-gate-read", model.ConsoleCapability},
				CI:   []string{model.ConsoleCapability},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets":      {Task: []string{"release-gate-read", model.ConsoleCapability}, CI: []string{model.ConsoleCapability}},
					"acme/unconfigured": {Task: []string{model.ConsoleCapability}},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}

	// Seed pre-123 grant rows directly.
	for _, row := range []struct {
		table, col, id, capName string
	}{
		{"task_grant", "task_id", "t-console", model.ConsoleCapability},
		{"task_grant", "task_id", "t-console", "release-gate-read"},
		{"template_grant", "template_id", "tmpl-console", model.ConsoleCapability},
		{"template_grant", "template_id", "tmpl-console", "release-gate-read"},
		{"schedule_grant", "schedule_id", "sched-console", model.ConsoleCapability},
		{"schedule_grant", "schedule_id", "sched-console", "release-gate-read"},
	} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `"+row.table+"` (`"+row.col+"`, `capability`, `via`) VALUES (?, ?, 'label')",
			row.id, row.capName); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `console_session_sync` (`task_id`, `snapshot`) VALUES ('t-session-console', ?)",
		`{"capabilities":["release-gate-read","console"],"offeredDefaults":["console","release-gate-read"]}`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 122 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("Init from schema 122: %v", err)
	}

	for id, want := range map[string]model.Hosting{
		"t-unattended":      model.HostingUnattended,
		"t-console":         model.HostingConsole,
		"t-session-console": model.HostingConsole,
		"t-session-plain":   model.HostingUnattended,
	} {
		got, err := store.GetTask(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("GetTask(%s): %v, %v", id, got, err)
		}
		if got.Hosting != want {
			t.Errorf("task %s Hosting = %q, want %q", id, got.Hosting, want)
		}
		for _, g := range got.Grants {
			if g.Capability == model.ConsoleCapability {
				t.Errorf("task %s still holds retired grant %q", id, g.Capability)
			}
		}
	}

	tmpl, err := store.GetTemplate(ctx, "tmpl-console")
	if err != nil || tmpl == nil {
		t.Fatalf("GetTemplate: %v, %v", tmpl, err)
	}
	if tmpl.Hosting != model.HostingConsole || len(tmpl.Grants) != 1 || tmpl.Grants[0].Capability != "release-gate-read" {
		t.Errorf("migrated template = (%q, %+v), want (console, [release-gate-read])", tmpl.Hosting, tmpl.Grants)
	}

	sched, err := store.GetSchedule(ctx, "sched-console")
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule: %v, %v", sched, err)
	}
	if sched.Hosting != model.HostingConsole || len(sched.Grants) != 1 || sched.Grants[0].Capability != "release-gate-read" {
		t.Errorf("migrated schedule = (%q, %+v), want (console, [release-gate-read])", sched.Hosting, sched.Grants)
	}

	afterCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterCfg.DefaultHosting != model.HostingConsole {
		t.Errorf("Config.DefaultHosting = %q, want console", afterCfg.DefaultHosting)
	}
	if !slices.Equal(afterCfg.DefaultCapabilities, []string{"release-gate-read"}) {
		t.Errorf("Config.DefaultCapabilities = %v, want [release-gate-read]", afterCfg.DefaultCapabilities)
	}
	if afterCfg.ConsoleSessions[0].Hosting != model.HostingConsole || !slices.Equal(afterCfg.ConsoleSessions[0].Capabilities, []string{"release-gate-read"}) {
		t.Errorf("ConsoleSessions[0] = (%q, %v), want (console, [release-gate-read])", afterCfg.ConsoleSessions[0].Hosting, afterCfg.ConsoleSessions[0].Capabilities)
	}
	if afterCfg.ConsoleSessions[1].Hosting != "" || !slices.Equal(afterCfg.ConsoleSessions[1].Capabilities, []string{"release-gate-read"}) {
		t.Errorf("ConsoleSessions[1] = (%q, %v), want ('', [release-gate-read])", afterCfg.ConsoleSessions[1].Hosting, afterCfg.ConsoleSessions[1].Capabilities)
	}
	if len(afterCfg.CapabilityPolicy.Rules) != 1 || afterCfg.CapabilityPolicy.Rules[0].Capability != "release-gate-read" {
		t.Errorf("CapabilityPolicy.Rules = %+v, want only release-gate-read", afterCfg.CapabilityPolicy.Rules)
	}
	cat := afterCfg.CapabilityPolicy.Catalogue
	if _, has := cat.InProcess[model.ConsoleCapability]; has {
		t.Error("catalogue still holds console in InProcess")
	}
	if !slices.Equal(cat.Defaults.Task, []string{"release-gate-read"}) {
		t.Errorf("catalogue Defaults.Task = %v, want [release-gate-read]", cat.Defaults.Task)
	}
	if len(cat.Defaults.CI) != 0 {
		t.Errorf("catalogue Defaults.CI = %v, want empty", cat.Defaults.CI)
	}

	snap, hasSnap := model.LastSyncedConsoleSession(ctx, store, "t-session-console")
	if !hasSnap || snap.Hosting != model.HostingConsole || !slices.Equal(snap.Capabilities, []string{"release-gate-read"}) || !slices.Equal(snap.OfferedDefaults, []string{"release-gate-read"}) {
		t.Errorf("migrated console_session_sync = %+v (has=%v), want Hosting=console and console stripped", snap, hasSnap)
	}

	afterRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil || afterRepo == nil {
		t.Fatalf("GetRepoConfig: %v, %v", afterRepo, err)
	}
	if afterRepo.DefaultHosting != model.HostingConsole {
		t.Errorf("RepoConfig.DefaultHosting = %q, want console", afterRepo.DefaultHosting)
	}
	if !slices.Equal(afterRepo.DefaultCapabilities, []string{"release-gate-read"}) {
		t.Errorf("RepoConfig.DefaultCapabilities = %v, want [release-gate-read]", afterRepo.DefaultCapabilities)
	}
	if !slices.Equal(afterRepo.CICapabilities, []string{"release-gate-read"}) {
		t.Errorf("RepoConfig.CICapabilities = %v, want [release-gate-read]", afterRepo.CICapabilities)
	}

	unconfigured := model.RepoRef{Owner: "Acme", Name: "Unconfigured"}
	afterUnconfigured, err := store.GetRepoConfig(ctx, unconfigured)
	if err != nil || afterUnconfigured == nil {
		t.Fatalf("GetRepoConfig(Acme/Unconfigured): %v, %v", afterUnconfigured, err)
	}
	if afterUnconfigured.DefaultHosting != model.HostingConsole {
		t.Errorf("unconfigured RepoConfig.DefaultHosting = %q, want console", afterUnconfigured.DefaultHosting)
	}
	if len(afterUnconfigured.DefaultCapabilities) != 0 {
		t.Errorf("unconfigured RepoConfig.DefaultCapabilities = %v, want empty", afterUnconfigured.DefaultCapabilities)
	}
}

func TestTaskHostingChangesAreRecordedAsTaskEvents(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	tk := task("host-evt", true)
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	hctx := model.WithHostingChange(ctx, model.Attribution{Actor: actor})
	if err := store.UpdateTask(hctx, "host-evt", func(t *model.Task) error {
		t.Hosting = model.HostingConsole
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(hctx, "host-evt", func(t *model.Task) error {
		t.Hosting = model.HostingUnattended
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.TaskEvents(ctx, "host-evt")
	if err != nil {
		t.Fatal(err)
	}
	var hostEvents []model.TaskEvent
	for _, ev := range events {
		if ev.Kind == model.TaskEventHostingChanged {
			hostEvents = append(hostEvents, ev)
		}
	}
	if len(hostEvents) != 2 {
		t.Fatalf("got %d hosting-changed events (%+v), want 2", len(hostEvents), hostEvents)
	}
	old1, new1, ok1 := model.ParseHostingChangeReason(hostEvents[0].Reason)
	if !ok1 || old1 != model.HostingUnattended || new1 != model.HostingConsole || hostEvents[0].By != actor {
		t.Errorf("event[0] = %+v (old=%q new=%q), want unattended -> console by %+v", hostEvents[0], old1, new1, actor)
	}
	old2, new2, ok2 := model.ParseHostingChangeReason(hostEvents[1].Reason)
	if !ok2 || old2 != model.HostingConsole || new2 != model.HostingUnattended || hostEvents[1].By != actor {
		t.Errorf("event[1] = %+v (old=%q new=%q), want console -> unattended by %+v", hostEvents[1], old2, new2, actor)
	}
}
