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
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A repository's own schedule is written by whoever can merge to the
// repository (insider profile B, task 3965), so a firing carrying a grant
// only a person may accept -- a GCP key, a host script, a dangerous api
// scope -- is filed for a person's approval instead of landing approved.
// Its ordinary grants, and the same grant on a schedule an operator
// wrote, still land approved.
func TestARepoDeclaredScheduleGrantingAGCPKeyFilesUnapproved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	declared := func(stem string) string { return model.RepoDeclaredID(model.DeclaredSchedule, nightlyRepo, stem) }
	for _, tc := range []struct {
		name, id, grant string
		held            bool
	}{
		{"declared gcp key", declared("publish"), `gcp#gcp-key={"permission_set":"ar-writer"}`, true},
		{"declared host script", declared("deploy"), `host-script#run={"script":"deploy"}`, true},
		{"declared dangerous api scope", declared("admin"), "api:capabilities:admin", true},
		{"declared ci read", declared("nightly"), "api:ci-read@acme/widgets", false},
		{"operator's gcp key", "sched-operator-gcp", `gcp#gcp-key={"permission_set":"ar-writer"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := fireForTask(t, store, ctx, model.Schedule{
				ID:         tc.id,
				Title:      "Publish",
				Target:     nightlyRepo,
				Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
				Enabled:    true,
				NextRunAt:  firstNight,
				CreatedAt:  firstNight.Add(-time.Hour),
				Grants:     []model.Grant{{Capability: tc.grant}},
			})
			if held := task.Approval == nil; held != tc.held {
				t.Fatalf("%s: filed unapproved = %v, want %v (approval %+v)", tc.grant, held, tc.held, task.Approval)
			}
			if tc.held && !strings.Contains(task.Body, "filed for a person's approval") {
				t.Errorf("a held firing does not say why: %q", task.Body)
			}
		})
	}
}

func TestDeclaredGrantNeedsPerson(t *testing.T) {
	for grant, want := range map[string]bool{
		`gcp#gcp-key={"permission_set":"ar-writer"}`: true,
		"host-script#run":          true,
		"github-credential#token":  true,
		"api:secrets:write":        true,
		"api:ci-read@acme/widgets": false,
		"grain-ci":                 false,
		"":                         false,
	} {
		if got := model.DeclaredGrantNeedsPerson(grant); got != want {
			t.Errorf("DeclaredGrantNeedsPerson(%q) = %v, want %v", grant, got, want)
		}
	}
}

// A person's acceptance of a declared schedule's grant set (task 3971)
// lets its firings land approved for as long as the set stays within
// what was accepted: the first firing is held and asks, an accepted one
// fires approved, and a schedule widened past the accepted set -- here
// by a host script beside the GCP key -- is held again and shows as
// awaiting acceptance.
func TestAnAcceptedDeclaredScheduleFiresApprovedUntilItWidens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	id := model.RepoDeclaredID(model.DeclaredSchedule, nightlyRepo, "publish")
	gcp := `gcp#gcp-key={"permission_set":"ar-writer"}`
	sched := model.Schedule{
		ID:         id,
		Title:      "Publish",
		Target:     nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true,
		NextRunAt:  firstNight,
		CreatedAt:  firstNight.Add(-time.Hour),
		Overlap:    model.OverlapAllow,
		Grants:     []model.Grant{{Capability: gcp}},
	}

	if task := fireForTask(t, store, ctx, sched); task.Approval != nil {
		t.Fatalf("the first firing, before anybody accepted, landed approved: %+v", task.Approval)
	}
	acc, err := store.GetDeclarationAcceptance(ctx, id)
	if err != nil || acc == nil || !acc.Awaiting() || !slices.Equal(acc.Requested, []string{gcp}) {
		t.Fatalf("after a held firing the declaration's record is %+v (%v), want it awaiting acceptance of %s", acc, err, gcp)
	}

	if _, err := store.AcceptDeclaration(ctx, id, "alice@example.com", []string{"capabilities:admin"}, []string{gcp}, firstNight); err != nil {
		t.Fatalf("accepting: %v", err)
	}
	task := fireForTask(t, store, ctx, sched)
	if task.Approval == nil {
		t.Fatalf("a firing within the accepted set was held: %q", task.Body)
	}
	if strings.Contains(task.Body, "filed for a person's approval") {
		t.Errorf("an accepted firing still says it is held: %q", task.Body)
	}

	widened := sched
	widened.Grants = append(slices.Clone(sched.Grants), model.Grant{Capability: `host-script#run={"script":"deploy"}`})
	task = fireForTask(t, store, ctx, widened)
	if task.Approval != nil {
		t.Fatalf("a firing widened past the accepted set landed approved")
	}
	if !strings.Contains(task.Body, "more than a person last accepted") {
		t.Errorf("a widened firing does not say it widened: %q", task.Body)
	}
	acc, err = store.GetDeclarationAcceptance(ctx, id)
	if err != nil || acc == nil || !acc.Awaiting() || !slices.Equal(acc.Unaccepted(), []string{`host-script#run={"script":"deploy"}`}) {
		t.Fatalf("after widening the record is %+v (%v), want it awaiting the host script alone", acc, err)
	}

	// Accepting a set other than the one the declaration now asks for
	// -- a page left open across the widening -- is refused.
	if _, err := store.AcceptDeclaration(ctx, id, "alice@example.com", nil, []string{gcp}, firstNight); !errors.Is(err, model.ErrDeclarationMoved) {
		t.Fatalf("accepting a stale set: %v, want ErrDeclarationMoved", err)
	}
}

// Revoking a person's acceptance of a declared schedule's set (task 4604)
// withdraws it: the next firing is held again and says nobody has
// accepted the set, the record shows as awaiting acceptance with what it
// asked for kept, and a firing filed approved before the revocation no
// longer has a person standing behind it at dispatch.
func TestARevokedDeclaredScheduleIsHeldAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	id := model.RepoDeclaredID(model.DeclaredSchedule, nightlyRepo, "rotate")
	secrets := "api:secrets:write"
	sched := model.Schedule{
		ID:         id,
		Title:      "Rotate",
		Target:     nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true,
		NextRunAt:  firstNight,
		CreatedAt:  firstNight.Add(-time.Hour),
		Overlap:    model.OverlapAllow,
		Grants:     []model.Grant{{Capability: secrets}},
	}
	fireForTask(t, store, ctx, sched)
	acc, err := store.GetDeclarationAcceptance(ctx, id)
	if err != nil || acc == nil {
		t.Fatalf("no record after the first firing: %+v, %v", acc, err)
	}
	if _, err := store.AcceptDeclaration(ctx, id, "alice@example.com", []string{"secrets:write"}, acc.Requested, firstNight); err != nil {
		t.Fatalf("accepting: %v", err)
	}
	approved := fireForTask(t, store, ctx, sched)
	if approved.Approval == nil {
		t.Fatalf("a firing within the accepted set was held: %q", approved.Body)
	}

	accepted, err := store.GetDeclarationAcceptance(ctx, id)
	if err != nil || accepted == nil {
		t.Fatalf("the accepted record: %+v, %v", accepted, err)
	}
	// A revocation checked against a set other than the one accepted --
	// one accepted wider since it was read -- is refused and withdraws
	// nothing.
	if _, err := store.RevokeDeclarationAcceptance(ctx, id, nil); !errors.Is(err, model.ErrAcceptanceMoved) {
		t.Fatalf("revoking a set other than the accepted one: err = %v, want ErrAcceptanceMoved", err)
	}
	if still, _ := store.GetDeclarationAcceptance(ctx, id); still == nil || still.AcceptedAt == nil {
		t.Fatalf("a refused revocation cleared the acceptance: %+v", still)
	}
	got, err := store.RevokeDeclarationAcceptance(ctx, id, accepted.Accepted)
	if err != nil {
		t.Fatalf("revoking: %v", err)
	}
	if _, err := store.RevokeDeclarationAcceptance(ctx, id, accepted.Accepted); !errors.Is(err, model.ErrAcceptanceMoved) {
		t.Fatalf("revoking twice: err = %v, want ErrAcceptanceMoved", err)
	}
	if got.AcceptedAt != nil || got.AcceptedBy != "" || len(got.Accepted) != 0 || !got.Awaiting() ||
		!slices.Equal(got.Requested, acc.Requested) {
		t.Fatalf("after revoking the record is %+v, want nothing accepted and %v still asked for", got, acc.Requested)
	}

	task := fireForTask(t, store, ctx, sched)
	if task.Approval != nil {
		t.Fatalf("a firing after the revocation landed approved: %+v", task.Approval)
	}
	if !strings.Contains(task.Body, "Nobody has accepted that set yet") {
		t.Errorf("a firing after the revocation does not say nobody has accepted: %q", task.Body)
	}
	if stands, err := store.PersonStandsBehind(ctx, *approved, []string{secrets}); err != nil || stands {
		t.Errorf("PersonStandsBehind(the firing approved before the revocation) = %v, %v; want false", stands, err)
	}
}

// A declared template's grants are accepted under the template when an
// operator's schedule fires it: the same rule, keyed by what was
// declared.
func TestAnOperatorScheduleFiringADeclaredTemplateIsAcceptedUnderTheTemplate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, nightlyRepo, "publish")
	gcp := `gcp#gcp-key={"permission_set":"ar-writer"}`
	if err := store.PutTemplate(ctx, model.Template{ID: tmplID, Name: "Publish", Title: "Publish", Body: "Publish it.",
		Grants: []model.Grant{{Capability: gcp}}}); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	sched := model.Schedule{
		ID: "sched-operator-publish", TemplateID: &tmplID, Title: "Publish", Target: nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true, NextRunAt: firstNight, CreatedAt: firstNight.Add(-time.Hour), Overlap: model.OverlapAllow,
	}
	if task := fireForTask(t, store, ctx, sched); task.Approval != nil {
		t.Fatalf("a declared template's GCP key landed approved before anybody accepted it")
	}
	if _, err := store.AcceptDeclaration(ctx, tmplID, "alice@example.com", nil, []string{gcp}, firstNight); err != nil {
		t.Fatalf("accepting the template: %v", err)
	}
	if task := fireForTask(t, store, ctx, sched); task.Approval == nil {
		t.Fatalf("a firing of the accepted template was held: %q", task.Body)
	}
}

// A repository's declared suite that files its tasks approved
// (`require_approval: false`) has its items' grants held as a declared
// schedule's are, and accepted the same way: as a set, with the suite's
// own require_approval in it.
func TestADeclaredAutoApprovedSuiteIsHeldUntilAccepted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, nightlyRepo, "smoke")
	suiteID := model.RepoDeclaredID(model.DeclaredSuite, nightlyRepo, "release")
	gcp := `gcp#gcp-key={"permission_set":"ar-writer"}`
	if err := store.PutTemplate(ctx, model.Template{ID: tmplID, Name: "Smoke", Title: "Smoke", Body: "Smoke it.",
		Grants: []model.Grant{{Capability: gcp}}}); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	suite := model.Suite{ID: suiteID, Name: "Release", Items: []model.SuiteItem{{TemplateID: tmplID}},
		Mode: model.SuiteCount, Count: 1, RequireApproval: false, CreatedAt: firstNight}
	if err := store.PutSuite(ctx, suite); err != nil {
		t.Fatalf("writing the suite: %v", err)
	}
	approved := func() bool {
		t.Helper()
		run, err := store.CreateSuiteRun(ctx, suite, nightlyRepo, "main", firstNight)
		if err != nil || len(run.Tasks) != 1 {
			t.Fatalf("running the suite: (%+v, %v)", run, err)
		}
		task, err := store.GetTask(ctx, run.Tasks[0].TaskID)
		if err != nil || task == nil {
			t.Fatalf("reading the suite's task: (%+v, %v)", task, err)
		}
		return task.Approval != nil
	}
	if approved() {
		t.Fatalf("a declared suite's GCP key landed approved before anybody accepted it")
	}
	want := []string{gcp, model.DeclaredAutoApprovedSuiteItem}
	slices.Sort(want)
	acc, err := store.GetDeclarationAcceptance(ctx, suiteID)
	if err != nil || acc == nil || !slices.Equal(acc.Requested, want) {
		t.Fatalf("the suite's record is %+v (%v), want it asking for %v", acc, err, want)
	}
	if _, err := store.AcceptDeclaration(ctx, suiteID, "alice@example.com", nil, want, firstNight); err != nil {
		t.Fatalf("accepting the suite: %v", err)
	}
	if !approved() {
		t.Fatalf("a run of the accepted suite was held")
	}
}

func TestDeclaredItemScopes(t *testing.T) {
	for item, want := range map[string]string{
		`gcp#gcp-key={"permission_set":"ar-writer"}`: "capabilities:admin",
		"api:secrets:write":                          "secrets:write",
		model.DeclaredNetworkOpenItem:                "capabilities:admin",
		model.DeclaredAutoApprovedSuiteItem:          "capabilities:admin",
	} {
		got := model.DeclaredItemScopes(item)
		if len(got) != 1 || string(got[0]) != want {
			t.Errorf("DeclaredItemScopes(%q) = %v, want [%s]", item, got, want)
		}
	}
}

// A `.grain/workflows` job's declared capabilities are a request too: a
// GCP key is refused until a person accepts it for that job, accepted
// from then on, and refused again once the job declares more.
func TestACIJobsDeclaredGCPKeyNeedsAcceptance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	gcp := `gcp#gcp-key={"permission_set":"ar-writer"}`
	note := func(caps ...string) bool {
		t.Helper()
		ok, _, err := store.NoteCIJobDeclaration(ctx, nightlyRepo, "publish", "abc123", caps, firstNight)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !note("api:ci-read@acme/widgets") {
		t.Fatal("a job declaring nothing held was refused")
	}
	if note(gcp) {
		t.Fatal("a job's GCP key was accepted before anybody accepted it")
	}
	id := model.CIJobDeclarationID(nightlyRepo, "publish")
	if _, err := store.AcceptDeclaration(ctx, id, "alice@example.com", []string{"capabilities:admin"}, []string{gcp}, firstNight); err != nil {
		t.Fatal(err)
	}
	if !note(gcp) {
		t.Fatal("an accepted job was refused")
	}
	if note(gcp, "host-script#run") {
		t.Fatal("a job widened past its accepted set was accepted")
	}
}

// PersonStandsBehind is how the dispatch-time catalogue hold tells a
// firing a person stands behind from one nobody does: an accepted
// declared schedule's firing has one, an unaccepted one does not.
func TestPersonStandsBehindAnAcceptedDeclaredFiring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	id := model.RepoDeclaredID(model.DeclaredSchedule, nightlyRepo, "publish")
	gcp := `gcp#gcp-key={"permission_set":"ar-writer"}`
	sched := model.Schedule{ID: id, Title: "Publish", Target: nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true, NextRunAt: firstNight, CreatedAt: firstNight.Add(-time.Hour), Overlap: model.OverlapAllow,
		Grants: []model.Grant{{Capability: gcp}}}
	fireForTask(t, store, ctx, sched)
	if _, err := store.AcceptDeclaration(ctx, id, "alice@example.com", nil, []string{gcp}, firstNight); err != nil {
		t.Fatal(err)
	}
	task := fireForTask(t, store, ctx, sched)
	if ok, err := store.PersonStandsBehind(ctx, *task, []string{gcp}); err != nil || !ok {
		t.Fatalf("PersonStandsBehind(accepted firing) = %v, %v; want true", ok, err)
	}
	if ok, _ := store.PersonStandsBehind(ctx, *task, []string{gcp, "api:secrets:write"}); ok {
		t.Fatal("PersonStandsBehind covered a grant nobody accepted")
	}
}
