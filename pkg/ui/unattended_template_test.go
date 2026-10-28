package ui_test

// A template with a required input and no default, attached to a
// schedule or a suite (grain/task-2433): every firing of one carries the
// default, so such a field would substitute nothing on each of them --
// "Audit  for X" -- while the same template refuses a human who leaves it
// blank. Refused where it is attached, and where an attached template
// gains such a field.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

func requiredNoDefault(t *testing.T, c *ui.Client, ctx context.Context) ui.Template {
	t.Helper()
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "walk-tpl", Title: "Audit {{service}} for X",
		Fields: []ui.TemplateField{
			{Name: "service", Label: "Service", Required: true},
			{Name: "level", Options: []string{"low", "high"}, Default: "high"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func wantUnfillable(t *testing.T, what string, err error) {
	t.Helper()
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("%s: error = %v, want a ValidationError", what, err)
	}
	if !strings.Contains(err.Error(), `field "Service" is required and has no default`) {
		t.Errorf("%s: error = %q, want it to name the field and say it has no default", what, err)
	}
}

func TestScheduleRefusesATemplateWithARequiredFieldAndNoDefault(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := requiredNoDefault(t, c, ctx)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	wantUnfillable(t, "creating the schedule", err)

	// The form's preview asks the same question, so it says so too.
	_, err = c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	wantUnfillable(t, "previewing the schedule", err)

	// Repointing an existing schedule at it is refused as well.
	other, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "plain", Title: "Plain"})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: other.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &tmpl.ID})
	wantUnfillable(t, "attaching it to a schedule", err)
}

func TestSuiteRefusesATemplateWithARequiredFieldAndNoDefault(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := requiredNoDefault(t, c, ctx)
	_, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "sweep", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 1,
	})
	wantUnfillable(t, "creating the suite", err)

	other, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "plain", Title: "Plain"})
	if err != nil {
		t.Fatal(err)
	}
	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "sweep", TemplateIDs: []string{other.ID}, Mode: "count", Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{other.ID, tmpl.ID}
	_, err = c.UpdateSuite(ctx, suite.ID, ui.UpdateSuiteRequest{TemplateIDs: &ids})
	wantUnfillable(t, "adding it to a suite", err)
}

func TestTemplateWithADefaultIsAttachable(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "walk-tpl", Title: "Audit {{service}} for X",
		Fields: []ui.TemplateField{{Name: "service", Required: true, Default: "api"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: tmpl.ID, Recurrence: everyDay,
	}); err != nil {
		t.Errorf("a required field with a default: %v, want the schedule accepted", err)
	}
	if _, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "sweep", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 1,
	}); err != nil {
		t.Errorf("a required field with a default: %v, want the suite accepted", err)
	}
}

func TestUpdateTemplateRefusesAnUnfillableFieldWhileAttached(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	scheduled, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "scheduled", Title: "Audit {{service}}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: scheduled.ID, Recurrence: everyDay,
	}); err != nil {
		t.Fatal(err)
	}
	unfillable := []ui.TemplateField{{Name: "service", Label: "Service", Required: true}}
	_, err = c.UpdateTemplate(ctx, scheduled.ID, ui.UpdateTemplateRequest{Fields: &unfillable})
	wantUnfillable(t, "editing a scheduled template", err)
	if err != nil && !strings.Contains(err.Error(), "schedule") {
		t.Errorf("error = %q, want it to say a schedule fires the template", err)
	}

	suited, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "suited", Title: "Audit {{service}}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "sweep", TemplateIDs: []string{suited.ID}, Mode: "count", Count: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = c.UpdateTemplate(ctx, suited.ID, ui.UpdateTemplateRequest{Fields: &unfillable})
	wantUnfillable(t, "editing a suite's template", err)

	// A template nothing fires may declare one: a human fills it in.
	loose, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "loose", Title: "Audit {{service}}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateTemplate(ctx, loose.ID, ui.UpdateTemplateRequest{Fields: &unfillable}); err != nil {
		t.Errorf("editing an unattached template: %v, want it accepted", err)
	}
}
