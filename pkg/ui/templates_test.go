package ui_test

// schedules_test.go's own doc comment gives the reasoning for testing
// against a real embedded store rather than a fake.

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateTemplateFilesATemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Dependency bump", Title: "Bump dependencies",
	})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	if tmpl.ID == "" {
		t.Fatal("want a non-empty id")
	}
	if tmpl.Name != "Dependency bump" || tmpl.Title != "Bump dependencies" {
		t.Errorf("template = %+v, want the fields just given", tmpl)
	}
}

func TestCreateTemplateRejectsAMissingName(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Title: "x"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateTemplateRejectsAMissingTitle(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateTemplateAcceptsCapabilitiesAndReads(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x",
		Capabilities: []string{"gemini-key"},
		Reads:        []string{"acme/shared-lib"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpl.Capabilities) != 1 || tmpl.Capabilities[0] != "gemini-key" {
		t.Errorf("capabilities = %v, want [gemini-key]", tmpl.Capabilities)
	}
	if len(tmpl.Reads) != 1 || tmpl.Reads[0] != "acme/shared-lib" {
		t.Errorf("reads = %v, want [acme/shared-lib]", tmpl.Reads)
	}
}

// A template carries the extra write targets every task filed from it
// gets (grain/task-86), each with the branch work goes on there --
// content like its reads, and the reason a recurring two-repo job no
// longer has to be filed by hand.
func TestCreateTemplateAcceptsExtraWriteTargets(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x",
		Writes: []ui.WriteTarget{
			{Repo: "acme/widgets-state", Base: "deploy"},
			{Repo: "acme/docs"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"acme/widgets-state": "deploy", "acme/docs": ""}
	got := map[string]string{}
	for _, w := range tmpl.Writes {
		got[w.Repo] = w.Base
	}
	if len(got) != len(want) {
		t.Fatalf("writes = %+v, want %v", tmpl.Writes, want)
	}
	for repo, base := range want {
		if got[repo] != base {
			t.Fatalf("writes = %+v, want %v", tmpl.Writes, want)
		}
	}
	// And they are on the row itself, not only in the reply: every
	// firing reads them back off the store.
	stored, err := store.GetTemplate(ctx, tmpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Writes) != 2 {
		t.Fatalf("stored writes = %+v, want both", stored.Writes)
	}
}

// The allowlist argument happens where somebody is having it -- saving
// the template -- rather than at three in the morning when a schedule
// built on it fires into a task nobody is watching (ui.parseWrites).
func TestCreateTemplateRefusesAWriteTargetOffTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/gadgets"})

	_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Writes: []ui.WriteTarget{{Repo: "acme/elsewhere"}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if _, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Writes: []ui.WriteTarget{{Repo: "acme/gadgets", Base: "deploy"}},
	}); err != nil {
		t.Fatalf("saving a template whose write target is on the allowlist: %v", err)
	}
}

// A template's binding decides what its firings target -- it wins over
// the schedule's own repo (model.Template.FiringTarget) -- so it passes
// the same boundary a task's target and a schedule's repo do
// (ui.refuseOffAllowlist, grain/task-336).
func TestCreateTemplateRefusesABindingOffTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Repo: "third/repo",
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	// An unbound template is the ordinary case and binds nothing, so
	// there is nothing to refuse.
	if _, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x"}); err != nil {
		t.Fatalf("saving an unbound template: %v", err)
	}
}

func TestUpdateTemplateRefusesABindingOffTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Repo: "acme/widgets",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Repo: ptr("third/repo")})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}

	// The rest of a template bound to a repo the list has since stopped
	// naming is still editable: the save resends the binding it already
	// has, which moves nothing.
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/gadgets"})
	renamed, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{
		Repo: ptr("acme/widgets"), Title: ptr("x, renamed"),
	})
	if err != nil {
		t.Fatalf("editing a template bound to a repo no longer on the list: %v", err)
	}
	if renamed.Title != "x, renamed" {
		t.Fatalf("title = %q, want the edit applied", renamed.Title)
	}
	// Unbinding it stays possible for the same reason: a template that
	// binds nothing decides nothing about where a firing lands.
	unbound, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Repo: ptr("")})
	if err != nil {
		t.Fatalf("unbinding: %v", err)
	}
	if unbound.Repo != "" {
		t.Fatalf("repo = %q, want the binding cleared", unbound.Repo)
	}
}

func TestUpdateTemplateReplacesWriteTargets(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x",
		Writes: []ui.WriteTarget{{Repo: "acme/docs"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}}
	updated, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Writes: &writes})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if len(updated.Writes) != 1 || updated.Writes[0].Repo != "acme/widgets-state" ||
		updated.Writes[0].Base != "deploy" {
		t.Fatalf("writes = %+v, want only the new one", updated.Writes)
	}
	// Left out of a request, they are left alone, like every other
	// field here.
	name := "y"
	again, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Writes) != 1 {
		t.Fatalf("writes = %+v, want them left alone", again.Writes)
	}
}

func TestCreateTemplateRejectsAnUnknownCapability(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Capabilities: []string{"not-a-real-capability"},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestListTemplatesReturnsNewestFirst(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	first, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "first", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "second", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list = ownTemplates(list)
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list = %+v, want [%s, %s]", list, second.ID, first.ID)
	}
}

func TestUpdateTemplateAppliesOnlyGivenFields(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "old title"})
	if err != nil {
		t.Fatal(err)
	}
	newTitle := "new title"
	updated, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Title: &newTitle})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if updated.Title != "new title" {
		t.Errorf("title = %q, want %q", updated.Title, "new title")
	}
	if updated.Name != "x" {
		t.Errorf("name = %q, want it left alone", updated.Name)
	}
}

func TestUpdateTemplateOnAnUnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.UpdateTemplate(ctx, "nope", ui.UpdateTemplateRequest{})
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestDeleteTemplateRemovesIt(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTemplate(ctx, tmpl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := c.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list = ownTemplates(list)
	if len(list) != 0 {
		t.Fatalf("list = %+v, want empty after delete", list)
	}
}

func TestDeleteTemplateOnAnUnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.DeleteTemplate(ctx, "nope")
	if err == nil {
		t.Fatal("want an error")
	}
}

// TestDeleteTemplateRefusesWhileASchedulePointsAtIt is
// ui.Client.DeleteTemplate's own safety net (its doc comment): deleting a
// template a schedule still fires from would silently strand that
// schedule's next firing, so this is rejected instead of allowed to
// succeed and orphan it.
func TestDeleteTemplateRefusesWhileASchedulePointsAtIt(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a template-backed schedule: %v", err)
	}
	err = c.DeleteTemplate(ctx, tmpl.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	// It names the schedule in the way, by title or, with none, by id.
	if want := strconv.Quote(firstNonEmptyString(sched.Title, sched.ID)); !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to name the schedule %s", err, want)
	}
}

// A template held by a schedule and by a suite at once is refused once,
// naming both: refusing on the schedule alone sent the human off to
// repoint it, only to be refused again on a suite nobody had mentioned
// (grain/task-2415).
func TestDeleteTemplateNamesEveryKindOfDependentAtOnce(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "Sweep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", TemplateID: tmpl.ID, Recurrence: everyDay,
	}); err != nil {
		t.Fatalf("creating a template-backed schedule: %v", err)
	}
	if _, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "Nightly sweep", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 1,
	}); err != nil {
		t.Fatalf("creating a suite: %v", err)
	}
	err = c.DeleteTemplate(ctx, tmpl.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	want := `template is used by 1 schedule(s) ("Sweep") and 1 suite(s) ("Nightly sweep"); ` +
		`repoint or delete the schedules and remove it from the suites first`
	if err.Error() != want {
		t.Errorf("error = %q\nwant      %q", err, want)
	}
}

func firstNonEmptyString(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// --- binding (grain/task-285) --------------------------------------------

func TestCreateTemplateBindsARepoAndBranch(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Repo: "acme/widgets", Base: "release",
	})
	if err != nil {
		t.Fatalf("creating a bound template: %v", err)
	}
	if tmpl.Repo != "acme/widgets" || tmpl.Base != "release" {
		t.Errorf("binding = (%q, %q), want acme/widgets on release", tmpl.Repo, tmpl.Base)
	}
}

// Binding is optional, and a template created without one says so:
// blank repo and base, not a repo nobody named.
func TestCreateTemplateLeavesTheBindingEmptyByDefault(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Repo != "" || tmpl.Base != "" {
		t.Errorf("binding = (%q, %q), want an unbound template", tmpl.Repo, tmpl.Base)
	}
}

func TestCreateTemplateRejectsAMalformedRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x", Repo: "widgets"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestUpdateTemplateBindsAndUnbinds(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	repo, base := "acme/widgets", "release"
	bound, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Repo: &repo, Base: &base})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	if bound.Repo != "acme/widgets" || bound.Base != "release" {
		t.Fatalf("binding = (%q, %q), want acme/widgets on release", bound.Repo, bound.Base)
	}

	// A branch on its own moves within the same binding.
	main := "main"
	rebased, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Base: &main})
	if err != nil {
		t.Fatalf("repinning the branch: %v", err)
	}
	if rebased.Repo != "acme/widgets" || rebased.Base != "main" {
		t.Fatalf("binding = (%q, %q), want the repo left alone on main", rebased.Repo, rebased.Base)
	}

	// An empty repo unbinds, and takes the pinned branch with it.
	none := ""
	unbound, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Repo: &none})
	if err != nil {
		t.Fatalf("unbinding: %v", err)
	}
	if unbound.Repo != "" || unbound.Base != "" {
		t.Fatalf("binding = (%q, %q), want it cleared", unbound.Repo, unbound.Base)
	}
}

func TestUpdateTemplateRejectsABranchWithNoRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	base := "release"
	_, err = c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Base: &base})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// Saving a template refreshes what its schedules show at once, not at
// each one's next firing: the schedules list, the Run now confirmation
// and a refused template delete all name a schedule by that copy, and
// before this they went on naming the template's old title while the
// preview and the next firing already used the new one (found in the
// 2026-10-12 recurring and composed UI walkthrough).
func TestUpdateTemplateRefreshesItsSchedulesStraightAway(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "rotate", Title: "Rotate {{secret}}", Description: "Rotate {{secret}} now.",
		Fields: []ui.TemplateField{{Name: "secret", Default: "api-token"}},
	})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	inline, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Inline", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("CreateSchedule inline: %v", err)
	}

	title, autoMerge := "Rotate {{secret}} (v2)", true
	if _, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Title: &title, AutoMerge: &autoMerge}); err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}

	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	for _, s := range list {
		switch s.ID {
		case sched.ID:
			if s.Title != "Rotate api-token (v2)" || !s.AutoMerge || s.Description != "Rotate api-token now." {
				t.Errorf("the template's schedule shows (%q, %q, autoMerge %v), want the edited template's content",
					s.Title, s.Description, s.AutoMerge)
			}
		case inline.ID:
			if s.Title != "Inline" || s.AutoMerge {
				t.Errorf("an inline schedule changed with an unrelated template: (%q, autoMerge %v)", s.Title, s.AutoMerge)
			}
		}
	}

	err = c.DeleteTemplate(ctx, tmpl.ID)
	if err == nil || !strings.Contains(err.Error(), `"Rotate api-token (v2)"`) {
		t.Errorf("DeleteTemplate = %v, want a refusal naming the schedule by its current title", err)
	}
}
