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

package ui_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A template-backed schedule stores only what it adds to its template
// (model.Schedule.Grants), and the API shows it with its template's
// admission, hosting and grants beside that, as its firings hold them.
// The overlay's save resends the template the schedule already fires,
// which keeps what the schedule adds; a save that detaches it keeps the
// template's grants and admission as content of its own.
func TestATemplateBackedSchedulesOwnSettingsSurviveTheOverlay(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Sweep", Title: "Sweep", Description: "Sweep it.",
		Capabilities: []string{model.GitHubSandboxRepoGrant}, Admission: string(model.AdmissionBypass),
	})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	created, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating the schedule: %v", err)
	}
	if !slices.Contains(created.Capabilities, model.GitHubSandboxRepoGrant) || created.Admission != string(model.AdmissionBypass) {
		t.Errorf("created: capabilities %v, admission %q; want the template's shown", created.Capabilities, created.Admission)
	}
	stored, err := store.GetSchedule(ctx, created.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the schedule: (%+v, %v)", stored, err)
	}
	if len(stored.Grants) != 0 || stored.Admission != "" {
		t.Errorf("stored: grants %+v, admission %q; want no copy of the template's", stored.Grants, stored.Admission)
	}

	// What a settings row or a declaration adds to the template.
	const own = "https://grain.dev/capabilities/api#secrets:write"
	if err := store.UpdateSchedule(ctx, created.ID, func(s *model.Schedule) error {
		s.Admission, s.Hosting = model.AdmissionDurable, model.HostingConsole
		s.Grants = []model.Grant{{Capability: own, Via: model.GrantByLabel}}
		return nil
	}); err != nil {
		t.Fatalf("adding the schedule's own settings: %v", err)
	}
	listed := scheduleByID(t, c, created.ID)
	// The overlay's save: the template it fires, and a cadence change.
	resend, overlap := tmpl.ID, "allow"
	saved, err := c.UpdateSchedule(ctx, created.ID, ui.UpdateScheduleRequest{
		TemplateID: &resend, Overlap: &overlap, Revision: &listed.Revision,
	})
	if err != nil {
		t.Fatalf("saving from the overlay: %v", err)
	}
	if saved.Admission != string(model.AdmissionDurable) || saved.Hosting != string(model.HostingConsole) ||
		!slices.Contains(saved.Capabilities, own) || !slices.Contains(saved.Capabilities, model.GitHubSandboxRepoGrant) {
		t.Errorf("saved: admission %q, hosting %q, capabilities %v; want the schedule's own beside the template's",
			saved.Admission, saved.Hosting, saved.Capabilities)
	}

	// A caller that resends the capabilities it was shown -- the
	// template's beside the schedule's own -- changes nothing stored:
	// the template's are not copied onto the row.
	shown := saved.Capabilities
	resent, err := c.UpdateSchedule(ctx, created.ID, ui.UpdateScheduleRequest{
		Capabilities: &shown, Revision: &saved.Revision,
	})
	if err != nil {
		t.Fatalf("resending the capabilities shown: %v", err)
	}
	stored, err = store.GetSchedule(ctx, created.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the schedule: (%+v, %v)", stored, err)
	}
	if len(stored.Grants) != 1 || stored.Grants[0].Capability != own {
		t.Errorf("stored after resending capabilities: grants %+v; want only the schedule's own %s", stored.Grants, own)
	}
	saved = resent

	detach := ""
	detached, err := c.UpdateSchedule(ctx, created.ID, ui.UpdateScheduleRequest{TemplateID: &detach, Revision: &saved.Revision})
	if err != nil {
		t.Fatalf("detaching: %v", err)
	}
	if detached.TemplateID != "" || detached.Admission != string(model.AdmissionDurable) ||
		!slices.Contains(detached.Capabilities, own) || !slices.Contains(detached.Capabilities, model.GitHubSandboxRepoGrant) {
		t.Errorf("detached: template %q, admission %q, capabilities %v; want the template's and its own kept inline",
			detached.TemplateID, detached.Admission, detached.Capabilities)
	}
}

func scheduleByID(t *testing.T, c *ui.Client, id string) ui.Schedule {
	t.Helper()
	list, err := c.ListSchedules(t.Context())
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("schedule %s is not listed", id)
	return ui.Schedule{}
}
