package ui_test

// Content sent alongside a templateId or a suiteId, which is content
// nothing will ever read: the template decides it, or the suite and the
// templates it runs do. These check that such a request is refused
// rather than saved with half of it dropped -- and, just as important,
// that a request which only *resends* what is already in force is not,
// since a form that submits a whole schedule does exactly that.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// scheduleTemplate is the template these tests attach schedules to --
// content in every field a schedule could try to override, so a test
// can send back what the template says and get the "unchanged" answer.
func scheduleTemplate(t *testing.T, c *ui.Client, ctx context.Context) ui.Template {
	t.Helper()
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Dependency bump", Title: "Bump dependencies", Description: "Bump every dependency.",
		AutoMerge:    true,
		Capabilities: []string{"gemini-key"},
		Reads:        []string{"acme/shared-lib"},
		Writes:       []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
	})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	return tmpl
}

func wantRefused(t *testing.T, err error, mentions ...string) {
	t.Helper()
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	for _, want := range mentions {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestCreateScheduleRefusesContentAlongsideATemplate is this rule's
// whole point: the fields are named back, and so is the template that
// decides them, rather than the schedule being created with them
// silently dropped.
func TestCreateScheduleRefusesContentAlongsideATemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := scheduleTemplate(t, c, ctx)

	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
		Title: "my own title", Reads: []string{"acme/other"},
	})
	wantRefused(t, err, "title", "reads", "Dependency bump")

	// Nothing was written: the refusal is the whole of what happened.
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("schedules = %d, want none created", len(list))
	}
}

// Every content field is refused, one at a time, and each names itself
// -- a caller who sent one field should not have to guess which.
func TestCreateScheduleRefusesEachContentFieldAlongsideATemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := scheduleTemplate(t, c, ctx)

	for field, req := range map[string]ui.CreateScheduleRequest{
		"title":               {Title: "my own title"},
		"description":         {Description: "my own description"},
		"autoMerge":           {AutoMerge: false},
		"reads":               {Reads: []string{"acme/other"}},
		"writes":              {Writes: []ui.WriteTarget{{Repo: "acme/other", Base: "main"}}},
		"capabilities":        {Capabilities: []string{"gcp-key"}},
		"customFields":        {CustomFields: map[string]ui.CustomFieldValue{"team": {"platform"}}},
		"networkAccess":       {NetworkAccess: "none"},
		"nothingNeededAction": {NothingNeededAction: "ack"},
	} {
		t.Run(field, func(t *testing.T) {
			// autoMerge is the one field a create cannot refuse: the
			// template says true, and a request has no way to say
			// "false" that is distinguishable from saying nothing at
			// all (ui.scheduleContentOffered). It is in this list so
			// that the list is the whole list and the gap is written
			// down rather than discovered.
			req.TemplateID, req.Repo, req.Recurrence = tmpl.ID, "acme/widgets", everyDay
			_, err := c.CreateSchedule(ctx, req)
			if field == "autoMerge" {
				if err != nil {
					t.Fatalf("%s: %v, want it read as unsaid", field, err)
				}
				return
			}
			wantRefused(t, err, field, "Dependency bump")
		})
	}
}

// A create that resends exactly what the template says is not asking
// for anything, and is allowed -- the same rule that lets a form which
// submits a whole schedule go on saving one.
func TestCreateScheduleAllowsContentIdenticalToItsTemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := scheduleTemplate(t, c, ctx)

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
		Title: "Bump dependencies", Description: "Bump every dependency.", AutoMerge: true,
		Capabilities: []string{"gemini-key"}, Reads: []string{"acme/shared-lib"},
		Writes: []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
	})
	if err != nil {
		t.Fatalf("resending the template's own content: %v", err)
	}
	if sched.TemplateID != tmpl.ID || sched.Title != "Bump dependencies" {
		t.Errorf("schedule = %q/%q, want the template's own content", sched.TemplateID, sched.Title)
	}
}

func TestCreateScheduleRefusesContentAlongsideASuite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")

	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
		Description: "my own description", AutoMerge: true,
	})
	wantRefused(t, err, "description", "autoMerge", "Bug sweep")
}

// A suite-backed schedule's title is the suite's name, so sending it
// back is not an edit -- which is what the schedule overlay would do if
// it ever rendered the title it shows into the form it submits.
func TestCreateScheduleAllowsTheSuitesOwnNameAsTitle(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
		Title: "Bug sweep",
	})
	if err != nil {
		t.Fatalf("resending the suite's own name: %v", err)
	}
	if sched.Title != "Bug sweep" {
		t.Errorf("title = %q, want the suite's own name", sched.Title)
	}
}

// Editing a template-backed schedule's content is refused the same way
// creating one with content is, and for the same reason: the row is a
// cache of the template, so a change to it would be undone by the next
// firing even if it were saved.
func TestUpdateScheduleRefusesContentOnATemplateBackedSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := scheduleTemplate(t, c, ctx)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	title, autoMerge := "my own title", false
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		Title: &title, AutoMerge: &autoMerge,
	})
	wantRefused(t, err, "title", "autoMerge", "Dependency bump")

	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("schedules = %d, want 1", len(list))
	}
	if list[0].Title != "Bump dependencies" || !list[0].AutoMerge {
		t.Errorf("schedule = %q/%v, want the template's own content untouched", list[0].Title, list[0].AutoMerge)
	}
}

// Attaching an inline schedule to a template while also sending content
// is refused, and the content is measured against the template being
// attached rather than against the inline content on its way out.
func TestUpdateScheduleRefusesContentWhileAttachingATemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "old title", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	tmpl := scheduleTemplate(t, c, ctx)

	// The title the schedule already has, which the template is about to
	// replace: still a request to keep a title the template does not
	// say, so still refused.
	old := "old title"
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &tmpl.ID, Title: &old})
	wantRefused(t, err, "title", "Dependency bump")

	// The template's own, which asks for nothing: allowed, and the
	// attach goes through.
	its := "Bump dependencies"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &tmpl.ID, Title: &its})
	if err != nil {
		t.Fatalf("attaching while resending the template's own title: %v", err)
	}
	if updated.TemplateID != tmpl.ID || updated.Title != "Bump dependencies" {
		t.Errorf("schedule = %q/%q, want it attached to the template", updated.TemplateID, updated.Title)
	}
}

// Detaching is the one update that both names a templateId and carries
// content: "" means the schedule keeps its own content from now on, so
// the content on that same request is exactly what it will hold.
func TestUpdateScheduleAllowsContentWhileDetachingFromATemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := scheduleTemplate(t, c, ctx)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	none, title := "", "its own title now"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		TemplateID: &none, Title: &title,
	})
	if err != nil {
		t.Fatalf("detaching with content: %v", err)
	}
	if updated.TemplateID != "" {
		t.Errorf("templateId = %q, want it detached", updated.TemplateID)
	}
	if updated.Title != "its own title now" {
		t.Errorf("title = %q, want the one this request gave", updated.Title)
	}
}

// The cadence, the overlap policy and the pause switch are the
// schedule's own whatever it fires, and no content check may stand in
// their way -- this is the check that a refusal did not freeze the three
// things a template-backed schedule still decides.
func TestUpdateScheduleStillRetimesATemplateBackedSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := scheduleTemplate(t, c, ctx)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	// What the overlay actually sends for a template-backed schedule:
	// the template it already fires, its repo and base, the cadence and
	// the overlap policy, and no content at all.
	weekly := ui.Recurrence{Kind: "weekly", TimeOfDay: "09:00", Weekday: "monday"}
	repo, base, allow, paused := "acme/widgets", "", "allow", false
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		TemplateID: &tmpl.ID, Repo: &repo, Base: &base,
		Recurrence: &weekly, Overlap: &allow, Enabled: &paused,
	})
	if err != nil {
		t.Fatalf("what the overlay sends: %v", err)
	}
	if updated.Recurrence.Kind != "weekly" || updated.Overlap != "allow" || updated.Enabled {
		t.Errorf("schedule = %+v, want it retimed, overlapping and paused", updated)
	}
}

// A suite-backed schedule refuses content on update too, and the same
// save-the-whole-form save still works.
func TestUpdateScheduleRefusesContentOnASuiteBackedSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	reads := []string{"acme/shared-lib"}
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Reads: &reads})
	wantRefused(t, err, "reads", "Bug sweep")

	base, allow := "release", "allow"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		SuiteID: &suite.ID, Base: &base, Overlap: &allow,
	})
	if err != nil {
		t.Fatalf("what the overlay sends: %v", err)
	}
	if updated.Base != "release" || updated.Overlap != "allow" {
		t.Errorf("schedule = %q/%q, want it moved and overlapping", updated.Base, updated.Overlap)
	}
}

// Repointing at another suite measures the content against the suite
// being moved to, not the one being left.
func TestUpdateScheduleRefusesContentWhileRepointingAtAnotherSuite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	first := suiteToSchedule(t, c, ctx, "Bug sweep")
	second := suiteToSchedule(t, c, ctx, "Flake hunt")
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: first.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	leaving := "Bug sweep"
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		SuiteID: &second.ID, Title: &leaving,
	})
	wantRefused(t, err, "title", "Flake hunt")
}

// An inline schedule is untouched by any of this: it is the one kind
// whose content is its own, and every field stays editable.
func TestUpdateScheduleStillEditsAnInlineSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "old title", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	title, autoMerge := "new title", true
	reads := []string{"acme/shared-lib"}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		Title: &title, AutoMerge: &autoMerge, Reads: &reads,
	})
	if err != nil {
		t.Fatalf("editing an inline schedule: %v", err)
	}
	if updated.Title != "new title" || !updated.AutoMerge || len(updated.Reads) != 1 {
		t.Errorf("schedule = %+v, want every field taken", updated)
	}
}
