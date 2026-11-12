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

package model_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

const ownSettingsGrant = "https://grain.dev/capabilities/api#secrets:write"

func holdsGrant(grants []model.Grant, capability string) bool {
	return slices.ContainsFunc(grants, func(g model.Grant) bool { return g.Capability == capability })
}

// fireStoredSchedule fires the schedule as it is stored -- read back each
// time, never re-put, which is what the firing loop does -- and returns
// the task it filed.
func fireStoredSchedule(t *testing.T, store *model.Store, id string, at time.Time) *model.Task {
	t.Helper()
	ctx := context.Background()
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("reading schedule %s back: (%+v, %v)", id, sched, err)
	}
	firing, err := store.FireScheduleNow(ctx, *sched, at, model.RepoRef{})
	if err != nil || firing.TaskID == "" {
		t.Fatalf("firing %s: (%+v, %v)", id, firing, err)
	}
	task, err := store.GetTask(ctx, firing.TaskID)
	if err != nil || task == nil {
		t.Fatalf("reading the filed task: (%+v, %v)", task, err)
	}
	return task
}

// A template-backed schedule may raise its template's admission and
// hosting and grant capabilities beside its template's
// (model.ComposeScheduleTask), whether a repository's declaration or a
// settings row says so. Recording a firing used to copy the template's
// admission, hosting and grants over the schedule's own, so the raise and
// the extra grant were filed on the first firing only, and every firing
// after it -- read back from the row that firing rewrote -- was filed at
// the template's (2026-11-11 capabilities walkthrough, task 4607).
func TestATemplateBackedSchedulesOwnSettingsSurviveItsFirings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	tmplID := "tmpl-own-settings"
	if err := store.PutTemplate(ctx, model.Template{ID: tmplID, Name: "sweep", Title: "Sweep", Body: "Sweep it.",
		Grants: []model.Grant{{Capability: "memory", Via: model.GrantByLabel}}}); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	const schedID = "sched-own-settings"
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: schedID, TemplateID: &tmplID, Title: "Sweep", Target: nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true, NextRunAt: firstNight, CreatedAt: firstNight.Add(-time.Hour), Overlap: model.OverlapAllow,
		Admission: model.AdmissionDurable, Hosting: model.HostingConsole,
		Grants: []model.Grant{{Capability: ownSettingsGrant, Via: model.GrantByLabel}},
	}); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}

	for i, at := range []time.Time{firstNight, firstNight.Add(time.Hour), firstNight.Add(2 * time.Hour)} {
		task := fireStoredSchedule(t, store, schedID, at)
		if task.Admission != model.AdmissionDurable {
			t.Errorf("firing %d: admission %q, want the schedule's own %q", i+1, task.Admission, model.AdmissionDurable)
		}
		if task.Hosting != model.HostingConsole {
			t.Errorf("firing %d: hosting %q, want the schedule's own %q", i+1, task.Hosting, model.HostingConsole)
		}
		if !holdsGrant(task.Grants, ownSettingsGrant) || !holdsGrant(task.Grants, "memory") {
			t.Errorf("firing %d: grants %+v, want the template's memory and the schedule's own %s",
				i+1, task.Grants, ownSettingsGrant)
		}
	}

	sched, err := store.GetSchedule(ctx, schedID)
	if err != nil || sched == nil {
		t.Fatalf("reading the schedule back: (%+v, %v)", sched, err)
	}
	if sched.Admission != model.AdmissionDurable || sched.Hosting != model.HostingConsole {
		t.Errorf("after its firings the schedule says admission %q, hosting %q; want its own durable, console",
			sched.Admission, sched.Hosting)
	}
	if !holdsGrant(sched.Grants, ownSettingsGrant) || holdsGrant(sched.Grants, "memory") {
		t.Errorf("after its firings the schedule holds %+v; want its own %s and no copy of the template's",
			sched.Grants, ownSettingsGrant)
	}
}

// What a schedule adds to its template is its own, and what the template
// says is the template's: an edit to the template that drops a grant or
// lowers its admission reaches the schedule's next firing, and the
// schedule's own settings survive the resync that edit does
// (ResyncTemplateSchedules). A copy of the template's on the schedule
// would have gone on granting what the template no longer does.
func TestATemplateEditReachesItsSchedulesWithoutTheirOwnSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	tmplID := "tmpl-edited"
	tmpl := model.Template{ID: tmplID, Name: "sweep", Title: "Sweep", Body: "Sweep it.",
		Admission: model.AdmissionBypass,
		Grants:    []model.Grant{{Capability: "memory", Via: model.GrantByLabel}}}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	const schedID = "sched-edited"
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: schedID, TemplateID: &tmplID, Title: "Sweep", Target: nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true, NextRunAt: firstNight, CreatedAt: firstNight.Add(-time.Hour), Overlap: model.OverlapAllow,
		Grants: []model.Grant{{Capability: ownSettingsGrant, Via: model.GrantByLabel}},
	}); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}
	first := fireStoredSchedule(t, store, schedID, firstNight)
	if first.Admission != model.AdmissionBypass || !holdsGrant(first.Grants, "memory") {
		t.Fatalf("first firing: admission %q, grants %+v; want the template's bypass and memory", first.Admission, first.Grants)
	}

	tmpl.Admission, tmpl.Grants = "", nil
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("editing the template: %v", err)
	}
	if err := store.ResyncTemplateSchedules(ctx, tmpl); err != nil {
		t.Fatalf("resyncing: %v", err)
	}
	second := fireStoredSchedule(t, store, schedID, firstNight.Add(time.Hour))
	if model.NormalizeAdmission(second.Admission) != model.AdmissionStandard {
		t.Errorf("after the template dropped bypass the firing was admitted %q", second.Admission)
	}
	if holdsGrant(second.Grants, "memory") {
		t.Errorf("after the template dropped memory the firing still holds it: %+v", second.Grants)
	}
	if !holdsGrant(second.Grants, ownSettingsGrant) {
		t.Errorf("the schedule's own %s did not survive the template edit: %+v", ownSettingsGrant, second.Grants)
	}
}

// Migration 143 takes the copies earlier builds left on template-backed
// schedules off them, and leaves what each schedule adds to its template:
// a raise above the template's, a grant the template does not give, and a
// grant a repository's schedule declaration gave even where the template
// gives it too. An inline schedule is not touched.
func TestMigration143ClearsTemplateCopiesOffSchedules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}
	tmplID := "tmpl-copied"
	if err := store.PutTemplate(ctx, model.Template{ID: tmplID, Name: "sweep", Title: "Sweep", Body: "Sweep it.",
		Admission: model.AdmissionBypass, Hosting: model.HostingUnattended,
		Grants: []model.Grant{
			{Capability: "memory", Via: model.GrantByLabel},
			{Capability: "web", Via: model.GrantByLabel},
		}}); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	put := func(s model.Schedule) {
		t.Helper()
		s.Title, s.Target, s.Enabled, s.NextRunAt, s.CreatedAt = "Sweep", nightlyRepo, true, firstNight, firstNight
		s.Recurrence = model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60}
		if err := store.PutSchedule(ctx, s); err != nil {
			t.Fatalf("writing schedule %s: %v", s.ID, err)
		}
	}
	// A copy, as attaching and every firing left it.
	put(model.Schedule{ID: "copied", TemplateID: &tmplID,
		Admission: model.AdmissionBypass, Hosting: model.HostingUnattended,
		Grants: []model.Grant{{Capability: "memory", Via: model.GrantByLabel}, {Capability: "web", Via: model.GrantByLabel}}})
	// The schedule's own, beside a copy.
	put(model.Schedule{ID: "raised", TemplateID: &tmplID,
		Admission: model.AdmissionDurable, Hosting: model.HostingConsole,
		Grants: []model.Grant{
			{Capability: "memory", Via: model.GrantByLabel},
			{Capability: "web", Via: ".grain/schedules/sweep.yml"},
			{Capability: ownSettingsGrant, Via: model.GrantByLabel},
		}})
	// Inline: its columns are its content, whatever a template says.
	put(model.Schedule{ID: "inline", Body: "Sweep it.", Admission: model.AdmissionBypass,
		Grants: []model.Grant{{Capability: "memory", Via: model.GrantByLabel}}})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := model.ApplyDataMigrations(ctx, tx, 142); err != nil {
		tx.Rollback()
		t.Fatalf("migrating: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	get := func(id string) model.Schedule {
		t.Helper()
		s, err := store.GetSchedule(ctx, id)
		if err != nil || s == nil {
			t.Fatalf("reading schedule %s: (%+v, %v)", id, s, err)
		}
		return *s
	}
	if s := get("copied"); s.Admission != "" || s.Hosting != "" || len(s.Grants) != 0 {
		t.Errorf("copied: admission %q, hosting %q, grants %+v; want the copies gone", s.Admission, s.Hosting, s.Grants)
	}
	s := get("raised")
	if s.Admission != model.AdmissionDurable || s.Hosting != model.HostingConsole {
		t.Errorf("raised: admission %q, hosting %q; want its own durable, console kept", s.Admission, s.Hosting)
	}
	if holdsGrant(s.Grants, "memory") || !holdsGrant(s.Grants, "web") || !holdsGrant(s.Grants, ownSettingsGrant) {
		t.Errorf("raised: grants %+v; want memory's copy gone, the declared web and its own %s kept", s.Grants, ownSettingsGrant)
	}
	if s := get("inline"); s.Admission != model.AdmissionBypass || !holdsGrant(s.Grants, "memory") {
		t.Errorf("inline: admission %q, grants %+v; want it untouched", s.Admission, s.Grants)
	}
}
