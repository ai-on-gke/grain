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

// Migration 119 converts stored priority-dispatch and durable grants across
// tasks (holding both -> durable), templates, schedules, deployment and
// repository defaults, and console sessions into the admission columns,
// deletes the grant rows, and drops catalogue and policy entries for the two
// bare names (design/design/unified-grants.design.md §4.1, Q8/Q10).
func TestMigration119MovesAdmissionGrantsAndDropsPolicyEntries(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	for _, id := range []string{"std", "byp", "dur", "both"} {
		tk := task(id, true)
		tk.Grants = []model.Grant{{Capability: "read-issues", Via: model.GrantByLabel}}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tmpl-byp", Name: "Byp", Title: "Byp", Body: "body",
		Grants:    []model.Grant{{Capability: "read-issues", Via: model.GrantByGrain}},
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tmpl-both", Name: "Both", Title: "Both", Body: "body",
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-dur", Title: "Dur", Body: "body",
		Target:     model.RepoRef{Owner: "acme", Name: "widgets"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
		NextRunAt:  now.Add(time.Hour),
		CreatedAt:  now,
		Grants:     []model.Grant{{Capability: "read-issues", Via: model.GrantByGrain}},
	}); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"read-issues"},
		CICapabilities:      []string{"github-sandbox"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                gadgets,
		DefaultCapabilities: []string{"read-issues"},
	}); err != nil {
		t.Fatal(err)
	}

	disabled := false
	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"read-issues"}
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:           model.DirectorSessionID,
		Title:        "Director",
		Capabilities: []string{"read-issues"},
	}}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "priority-dispatch", Deny: true},
			{Capability: "durable", AllowUntrusted: true},
			{Capability: model.GrainSourceRef, Deny: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"priority-dispatch", "read-issues", "durable"},
				CI:   []string{"priority-dispatch"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {Task: []string{"durable", "read-issues"}, CI: []string{"durable"}},
				},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"priority-dispatch": {Enabled: &disabled},
				"durable":           {Enabled: &disabled},
				"read-issues":       {Enabled: &disabled},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// Seed pre-119 grant rows and list columns directly as a version-118 store held them.
	rawStmts := []string{
		"INSERT INTO `task_grant` (`task_id`, `capability`, `via`, `folder`) VALUES ('byp', 'priority-dispatch', 'label', NULL)",
		"INSERT INTO `task_grant` (`task_id`, `capability`, `via`, `folder`) VALUES ('dur', 'durable', 'label', NULL)",
		"INSERT INTO `task_grant` (`task_id`, `capability`, `via`, `folder`) VALUES ('both', 'priority-dispatch', 'label', NULL)",
		"INSERT INTO `task_grant` (`task_id`, `capability`, `via`, `folder`) VALUES ('both', 'durable', 'label', NULL)",
		"INSERT INTO `template_grant` (`template_id`, `capability`, `via`, `folder`) VALUES ('tmpl-byp', 'priority-dispatch', 'grain', NULL)",
		"INSERT INTO `template_grant` (`template_id`, `capability`, `via`, `folder`) VALUES ('tmpl-both', 'priority-dispatch', 'grain', NULL)",
		"INSERT INTO `template_grant` (`template_id`, `capability`, `via`, `folder`) VALUES ('tmpl-both', 'durable', 'grain', NULL)",
		"INSERT INTO `schedule_grant` (`schedule_id`, `capability`, `via`, `folder`) VALUES ('sched-dur', 'durable', 'grain', NULL)",
		"UPDATE `grain_config` SET `default_capabilities` = 'priority-dispatch,read-issues', " +
			"`console_sessions` = '[{\"ID\":\"director\",\"Title\":\"Director\",\"Capabilities\":[\"durable\",\"priority-dispatch\",\"read-issues\"]}]' WHERE `id` = 1",
		"UPDATE `repo_config` SET `default_capabilities` = '[\"priority-dispatch\",\"durable\",\"read-issues\"]', " +
			"`ci_capabilities` = 'priority-dispatch,github-sandbox' WHERE `owner` = 'acme' AND `name` = 'widgets'",
		"UPDATE `repo_config` SET `default_capabilities` = 'priority-dispatch' WHERE `owner` = 'acme' AND `name` = 'gadgets'",
		"UPDATE `grain_schema` SET `version` = 118 WHERE `id` = 1",
	}
	for _, stmt := range rawStmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating from version 118: %v", err)
	}
	// Idempotent across a second run (such as staterepo.Import).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := model.MigrateAdmissionGrants(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("second MigrateAdmissionGrants: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for id, want := range map[string]model.Admission{
		"std":  model.AdmissionStandard,
		"byp":  model.AdmissionBypass,
		"dur":  model.AdmissionDurable,
		"both": model.AdmissionDurable,
	} {
		got, err := store.GetTask(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("GetTask(%s) = %v, %v", id, got, err)
		}
		if got.Admission != want {
			t.Errorf("task %s Admission = %s, want %s", id, got.Admission, want)
		}
		if len(got.Grants) != 1 || got.Grants[0].Capability != "read-issues" {
			t.Errorf("task %s Grants = %+v, want [read-issues] only", id, got.Grants)
		}
	}

	tmplByp, err := store.GetTemplate(ctx, "tmpl-byp")
	if err != nil || tmplByp == nil || tmplByp.Admission != model.AdmissionBypass || len(tmplByp.Grants) != 1 {
		t.Fatalf("tmpl-byp = %+v (%v), want Admission=bypass and 1 grant", tmplByp, err)
	}
	tmplBoth, err := store.GetTemplate(ctx, "tmpl-both")
	if err != nil || tmplBoth == nil || tmplBoth.Admission != model.AdmissionDurable || len(tmplBoth.Grants) != 0 {
		t.Fatalf("tmpl-both = %+v (%v), want Admission=durable and 0 grants", tmplBoth, err)
	}
	schedDur, err := store.GetSchedule(ctx, "sched-dur")
	if err != nil || schedDur == nil || schedDur.Admission != model.AdmissionDurable || len(schedDur.Grants) != 1 {
		t.Fatalf("sched-dur = %+v (%v), want Admission=durable and 1 grant", schedDur, err)
	}

	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotCfg.DefaultAdmission != model.AdmissionBypass || !slices.Equal(gotCfg.DefaultCapabilities, []string{"read-issues"}) {
		t.Errorf("config DefaultAdmission=%s DefaultCapabilities=%v, want bypass and [read-issues]",
			gotCfg.DefaultAdmission, gotCfg.DefaultCapabilities)
	}
	if !slices.Equal(gotCfg.ConsoleSessions[0].Capabilities, []string{"read-issues"}) {
		t.Errorf("session Capabilities = %v, want [read-issues]", gotCfg.ConsoleSessions[0].Capabilities)
	}
	if len(gotCfg.CapabilityPolicy.Rules) != 1 || gotCfg.CapabilityPolicy.Rules[0].Capability != model.GrainSourceRef {
		t.Errorf("policy Rules = %+v, want only %s", gotCfg.CapabilityPolicy.Rules, model.GrainSourceRef)
	}
	cat := gotCfg.CapabilityPolicy.Catalogue
	if _, ok := cat.InProcess["priority-dispatch"]; ok {
		t.Error("catalogue InProcess still holds priority-dispatch")
	}
	if _, ok := cat.InProcess["durable"]; ok {
		t.Error("catalogue InProcess still holds durable")
	}
	if _, ok := cat.InProcess["read-issues"]; !ok {
		t.Error("catalogue InProcess dropped read-issues")
	}
	if !slices.Equal(cat.Defaults.Task, []string{"read-issues"}) || len(cat.Defaults.CI) != 0 {
		t.Errorf("catalogue defaults = task %v ci %v, want [read-issues] and []", cat.Defaults.Task, cat.Defaults.CI)
	}
	if rd := cat.Defaults.Repos["acme/widgets"]; !slices.Equal(rd.Task, []string{"read-issues"}) || len(rd.CI) != 0 {
		t.Errorf("catalogue repo defaults = %+v, want task [read-issues] and ci []", rd)
	}

	gotWidgets, err := store.GetRepoConfig(ctx, widgets)
	if err != nil || gotWidgets == nil {
		t.Fatalf("GetRepoConfig(widgets) = %v, %v", gotWidgets, err)
	}
	if gotWidgets.DefaultAdmission != model.AdmissionDurable ||
		!slices.Equal(gotWidgets.DefaultCapabilities, []string{"read-issues"}) ||
		!slices.Equal(gotWidgets.CICapabilities, []string{"github-sandbox"}) {
		t.Errorf("widgets = DefaultAdmission:%s DefaultCaps:%v CICaps:%v, want durable / [read-issues] / [github-sandbox]",
			gotWidgets.DefaultAdmission, gotWidgets.DefaultCapabilities, gotWidgets.CICapabilities)
	}
	gotGadgets, err := store.GetRepoConfig(ctx, gadgets)
	if err != nil || gotGadgets == nil || gotGadgets.DefaultAdmission != model.AdmissionBypass || len(gotGadgets.DefaultCapabilities) != 0 {
		t.Errorf("gadgets = %+v (%v), want DefaultAdmission=bypass and no DefaultCapabilities", gotGadgets, err)
	}
}

// Updating a task's admission records a TaskEventAdmissionChanged with the
// old value, the new value, and the actor (and exposes it as a
// TransitionAdmissionChanged step on the timeline); retired admission grants
// handed to PutTask are folded into Admission and never written to task_grant.
func TestTaskAdmissionEventsAndOneReleaseGrantFolding(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	tk := task("adm-1", true)
	tk.Grants = []model.Grant{
		{Capability: "priority-dispatch", Via: model.GrantByLabel},
		{Capability: "read-issues", Via: model.GrantByLabel},
	}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, "adm-1")
	if err != nil || got == nil {
		t.Fatalf("GetTask = %v, %v", got, err)
	}
	if got.Admission != model.AdmissionBypass {
		t.Fatalf("Admission = %s, want bypass", got.Admission)
	}
	if len(got.Grants) != 1 || got.Grants[0].Capability != "read-issues" {
		t.Fatalf("Grants = %+v, want [read-issues] (priority-dispatch must never be stored as a grant)", got.Grants)
	}
	var grantRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_grant` WHERE `capability` IN ('priority-dispatch', 'durable')").Scan(&grantRows); err != nil || grantRows != 0 {
		t.Fatalf("stored admission grant rows = %d (%v), want 0", grantRows, err)
	}

	operator := model.Attribution{
		Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP},
	}
	editCtx := model.WithAdmissionChange(ctx, operator)
	if err := store.UpdateTask(editCtx, "adm-1", func(t *model.Task) error {
		t.Admission = model.AdmissionDurable
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.TaskEvents(ctx, "adm-1")
	if err != nil {
		t.Fatal(err)
	}
	var found *model.TaskEvent
	for i := range events {
		if events[i].Kind == model.TaskEventAdmissionChanged {
			found = &events[i]
		}
	}
	if found == nil {
		t.Fatalf("TaskEvents = %+v, want TaskEventAdmissionChanged", events)
	}
	if found.Reason != "bypass -> durable" || found.By.ID != "alice@example.com" {
		t.Errorf("event = %+v, want Reason='bypass -> durable' By='alice@example.com'", *found)
	}
	oldAdm, newAdm, ok := model.ParseAdmissionChangeReason(found.Reason)
	if !ok || oldAdm != model.AdmissionBypass || newAdm != model.AdmissionDurable {
		t.Errorf("ParseAdmissionChangeReason(%q) = %s, %s, %v; want bypass, durable, true", found.Reason, oldAdm, newAdm, ok)
	}
}
