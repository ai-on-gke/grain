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

func TestSchema122MigratesPriorityDispatchAndDurableToAdmission(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	for _, tk := range []model.Task{
		task("t-bypass", true),
		task("t-durable", true),
		task("t-both", true),
		{
			ID:          "t-session",
			Intent:      model.IntentImplement,
			Title:       "Director",
			Origin:      model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonConsoleSession},
			Interactive: true,
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
		ID:        "tmpl-both",
		Name:      "Both",
		Title:     "Both",
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID:         "sched-bypass",
		Title:      "Bypass",
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
		DefaultCapabilities: []string{"self-debug", model.PriorityDispatchCapability, model.DurableCapability},
		CICapabilities:      []string{"github-sandbox", model.PriorityDispatchCapability},
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
	cfg.DefaultCapabilities = []string{"read-issues", model.PriorityDispatchCapability}
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:           model.DirectorSessionID,
		Title:        "Director",
		Capabilities: []string{"self-debug", model.DurableCapability, model.PriorityDispatchCapability},
	}}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: model.PriorityDispatchCapability, Repos: []string{"acme/widgets"}},
			{Capability: model.DurableCapability, Repos: []string{"acme/widgets"}},
			{Capability: "self-debug", Repos: []string{"acme/widgets"}},
		},
		Catalogue: &model.CapabilityCatalogue{
			InProcess: map[string]model.CatalogueInProcess{
				model.PriorityDispatchCapability: {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
				model.DurableCapability:          {Restrictions: &model.CapabilityRestrictions{RequiresPerson: true}},
				"self-debug":                     {},
			},
			Defaults: model.CatalogueDefaults{
				Task: []string{"read-issues", model.PriorityDispatchCapability},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets":      {Task: []string{"self-debug", model.DurableCapability}},
					"Acme/Unconfigured": {Task: []string{model.PriorityDispatchCapability}},
					"acme/unconfigured": {Task: []string{model.DurableCapability}},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}

	// Seed pre-122 grant rows directly, including a task holding both grants.
	for _, row := range []struct {
		table, col, id, capName string
	}{
		{"task_grant", "task_id", "t-bypass", model.PriorityDispatchCapability},
		{"task_grant", "task_id", "t-bypass", "self-debug"},
		{"task_grant", "task_id", "t-durable", model.DurableCapability},
		{"task_grant", "task_id", "t-both", model.PriorityDispatchCapability},
		{"task_grant", "task_id", "t-both", model.DurableCapability},
		{"task_grant", "task_id", "t-session", model.DurableCapability},
		{"template_grant", "template_id", "tmpl-both", model.PriorityDispatchCapability},
		{"template_grant", "template_id", "tmpl-both", model.DurableCapability},
		{"template_grant", "template_id", "tmpl-both", "self-debug"},
		{"schedule_grant", "schedule_id", "sched-bypass", model.PriorityDispatchCapability},
		{"schedule_grant", "schedule_id", "sched-bypass", "read-issues"},
	} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `"+row.table+"` (`"+row.col+"`, `capability`, `via`) VALUES (?, ?, 'label')",
			row.id, row.capName); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "UPDATE `task` SET `admission` = 'standard' WHERE `id` = 't-session'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `console_session_sync` (`task_id`, `snapshot`) VALUES ('t-session', ?)",
		`{"capabilities":["self-debug","durable"],"offeredDefaults":["priority-dispatch","read-issues"]}`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 121 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("Init from schema 121: %v", err)
	}

	for id, want := range map[string]model.Admission{
		"t-bypass":  model.AdmissionBypass,
		"t-durable": model.AdmissionDurable,
		"t-both":    model.AdmissionDurable,
		"t-session": model.AdmissionDurable,
	} {
		got, err := store.GetTask(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("GetTask(%s): %v, %v", id, got, err)
		}
		if got.Admission != want {
			t.Errorf("task %s Admission = %q, want %q", id, got.Admission, want)
		}
		for _, g := range got.Grants {
			if g.Capability == model.PriorityDispatchCapability || g.Capability == model.DurableCapability {
				t.Errorf("task %s still holds retired grant %q", id, g.Capability)
			}
		}
	}

	tmpl, err := store.GetTemplate(ctx, "tmpl-both")
	if err != nil || tmpl == nil {
		t.Fatalf("GetTemplate: %v, %v", tmpl, err)
	}
	if tmpl.Admission != model.AdmissionDurable || len(tmpl.Grants) != 1 || tmpl.Grants[0].Capability != "self-debug" {
		t.Errorf("migrated template = (%q, %+v), want (durable, [self-debug])", tmpl.Admission, tmpl.Grants)
	}

	sched, err := store.GetSchedule(ctx, "sched-bypass")
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule: %v, %v", sched, err)
	}
	if sched.Admission != model.AdmissionBypass || len(sched.Grants) != 1 || sched.Grants[0].Capability != "read-issues" {
		t.Errorf("migrated schedule = (%q, %+v), want (bypass, [read-issues])", sched.Admission, sched.Grants)
	}

	afterCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterCfg.DefaultAdmission != model.AdmissionBypass {
		t.Errorf("Config.DefaultAdmission = %q, want bypass", afterCfg.DefaultAdmission)
	}
	if !slices.Equal(afterCfg.DefaultCapabilities, []string{"read-issues"}) {
		t.Errorf("Config.DefaultCapabilities = %v, want [read-issues]", afterCfg.DefaultCapabilities)
	}
	if !slices.Equal(afterCfg.ConsoleSessions[0].Capabilities, []string{"self-debug"}) {
		t.Errorf("ConsoleSession.Capabilities = %v, want [self-debug]", afterCfg.ConsoleSessions[0].Capabilities)
	}
	if len(afterCfg.CapabilityPolicy.Rules) != 1 || afterCfg.CapabilityPolicy.Rules[0].Capability != "self-debug" {
		t.Errorf("CapabilityPolicy.Rules = %+v, want only self-debug", afterCfg.CapabilityPolicy.Rules)
	}
	cat := afterCfg.CapabilityPolicy.Catalogue
	if _, has := cat.InProcess[model.PriorityDispatchCapability]; has {
		t.Error("catalogue still holds priority-dispatch in InProcess")
	}
	if _, has := cat.InProcess[model.DurableCapability]; has {
		t.Error("catalogue still holds durable in InProcess")
	}
	if !slices.Equal(cat.Defaults.Task, []string{"read-issues"}) {
		t.Errorf("catalogue Defaults.Task = %v, want [read-issues]", cat.Defaults.Task)
	}

	afterRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil || afterRepo == nil {
		t.Fatalf("GetRepoConfig: %v, %v", afterRepo, err)
	}
	if afterRepo.DefaultAdmission != model.AdmissionDurable {
		t.Errorf("RepoConfig.DefaultAdmission = %q, want durable", afterRepo.DefaultAdmission)
	}
	if !slices.Equal(afterRepo.DefaultCapabilities, []string{"self-debug"}) {
		t.Errorf("RepoConfig.DefaultCapabilities = %v, want [self-debug]", afterRepo.DefaultCapabilities)
	}
	if !slices.Equal(afterRepo.CICapabilities, []string{model.GitHubSandboxRepoGrant}) {
		t.Errorf("RepoConfig.CICapabilities = %v, want [%s]", afterRepo.CICapabilities, model.GitHubSandboxRepoGrant)
	}

	unconfigured := model.RepoRef{Owner: "acme", Name: "unconfigured"}
	afterUnconfigured, err := store.GetRepoConfig(ctx, unconfigured)
	if err != nil || afterUnconfigured == nil {
		t.Fatalf("GetRepoConfig(acme/unconfigured): %v, %v", afterUnconfigured, err)
	}
	if afterUnconfigured.DefaultAdmission != model.AdmissionDurable {
		t.Errorf("unconfigured RepoConfig.DefaultAdmission = %q, want durable", afterUnconfigured.DefaultAdmission)
	}
	if len(afterUnconfigured.DefaultCapabilities) != 0 {
		t.Errorf("unconfigured RepoConfig.DefaultCapabilities = %v, want empty", afterUnconfigured.DefaultCapabilities)
	}
}

func TestTaskAdmissionChangesAreRecordedAsTaskEvents(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	tk := task("adm-evt", true)
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com"}
	actx := model.WithAdmissionChange(ctx, model.Attribution{Actor: actor})
	if err := store.UpdateTask(actx, "adm-evt", func(t *model.Task) error {
		t.Admission = model.AdmissionBypass
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(actx, "adm-evt", func(t *model.Task) error {
		t.Admission = model.AdmissionDurable
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.TaskEvents(ctx, "adm-evt")
	if err != nil {
		t.Fatal(err)
	}
	var admEvents []model.TaskEvent
	for _, ev := range events {
		if ev.Kind == model.TaskEventAdmissionChanged {
			admEvents = append(admEvents, ev)
		}
	}
	if len(admEvents) != 2 {
		t.Fatalf("got %d admission-changed events (%+v), want 2", len(admEvents), admEvents)
	}
	old1, new1, ok1 := model.ParseAdmissionChangeReason(admEvents[0].Reason)
	if !ok1 || old1 != model.AdmissionStandard || new1 != model.AdmissionBypass || admEvents[0].By != actor {
		t.Errorf("event[0] = %+v (old=%q new=%q), want standard -> bypass by %+v", admEvents[0], old1, new1, actor)
	}
	old2, new2, ok2 := model.ParseAdmissionChangeReason(admEvents[1].Reason)
	if !ok2 || old2 != model.AdmissionBypass || new2 != model.AdmissionDurable || admEvents[1].By != actor {
		t.Errorf("event[1] = %+v (old=%q new=%q), want bypass -> durable by %+v", admEvents[1], old2, new2, actor)
	}
}
