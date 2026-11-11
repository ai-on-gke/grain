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
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// accessItems is the request every test here parks on: a read, a write,
// network access, and a dangerous api grant only a person may make.
func accessItems() []model.AccessRequestItem {
	return []model.AccessRequestItem{
		{Kind: model.AccessRead, Repo: "acme/lib"},
		{Kind: model.AccessWrite, Repo: "acme/other", Base: "dev"},
		{Kind: model.AccessNetwork, Access: "open"},
		{Kind: model.AccessGrant, Capability: "api:tasks:admin"},
	}
}

// parkOnAccess leaves task id as a run's request_access call does
// (orchestrator.relayParkingCalls): a comment, the task pointed at it,
// and the request recorded beside it.
func parkOnAccess(t *testing.T, c *ui.Client, ctx context.Context, id string, items []model.AccessRequestItem) {
	t.Helper()
	commentID, err := c.Store.AddComment(ctx, model.Comment{
		TaskID: id, Body: "**This run asked for more access:**", CreatedAt: time.Now(),
		Summary: "Grant access to acme/lib",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.ObserveField(ctx, id, time.Now(), func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
		o.PendingAccess = &model.PendingAccessRequest{
			Summary: "Grant access to acme/lib", Reason: "the fix spans both repositories",
			Items: items, CreatedAt: time.Now(),
		}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAccessRequestRefusesWhatCannotBeGranted(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Reads = []model.RepoRef{{Owner: "acme", Name: "held"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := c.CheckAccessRequest(ctx, task.ID, accessItems()); err != nil {
		t.Fatalf("a well-formed request was refused: %v", err)
	}
	for name, item := range map[string]model.AccessRequestItem{
		"unknown capability": {Kind: model.AccessGrant, Capability: "no-such-capability"},
		"held read":          {Kind: model.AccessRead, Repo: "acme/held"},
		"own repository":     {Kind: model.AccessRead, Repo: "acme/widgets"},
		"bad repository":     {Kind: model.AccessRead, Repo: "not a repo"},
		"bad base":           {Kind: model.AccessWrite, Repo: "acme/other", Base: "../x"},
	} {
		_, err := c.CheckAccessRequest(ctx, task.ID, []model.AccessRequestItem{item})
		var invalid *ui.ValidationError
		if !errors.As(err, &invalid) {
			t.Errorf("%s: CheckAccessRequest = %v, want a validation error", name, err)
		} else if !strings.Contains(err.Error(), "requests[0]") {
			t.Errorf("%s: the refusal does not say which item: %v", name, err)
		}
	}
}

func TestAPendingAccessRequestParksAndShowsOnTheTask(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)
	parkOnAccess(t, c, ctx, task.ID, accessItems())

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", detail.State)
	}
	view := detail.PendingAccess
	if view == nil || len(view.Items) != 4 || view.Reason == "" {
		t.Fatalf("PendingAccess = %+v, want the four items and the reason", view)
	}
	if grant := view.Items[3]; !grant.Dangerous || grant.Grant == "" {
		t.Errorf("the api:tasks:admin item = %+v, want it normalized and marked dangerous", grant)
	}
	for _, it := range view.Items {
		if it.Error != "" {
			t.Errorf("item %s carries an error: %s", it.Label(), it.Error)
		}
	}
}

func TestGrantAccessRequestAppliesExactlyTheChosenItemsAndRequeues(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	parkOnAccess(t, c, ctx, task.ID, accessItems())

	chosen := []int{0, 2, 3} // not the write
	if err := c.GrantAccessRequest(ctx, task.ID, ui.AccessGrantDecision{Items: &chosen}); err != nil {
		t.Fatalf("GrantAccessRequest: %v", err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Reads) != 1 || got.Reads[0].String() != "acme/lib" {
		t.Errorf("reads = %v, want acme/lib", got.Reads)
	}
	if len(got.Writes) != 0 {
		t.Errorf("writes = %v, want none: the write was not chosen", got.Writes)
	}
	if got.NetworkAccess != "open" {
		t.Errorf("network access = %q, want open", got.NetworkAccess)
	}
	if !slices.ContainsFunc(got.Grants, func(g model.Grant) bool { return strings.Contains(g.Capability, "tasks:admin") }) {
		t.Errorf("grants = %v, want api:tasks:admin granted by the person", got.Grants)
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State == model.StateAwaitingReply || detail.PendingAccess != nil {
		t.Fatalf("after granting, state = %q and pending = %+v: want re-queued", detail.State, detail.PendingAccess)
	}
	last := detail.Comments[len(detail.Comments)-1]
	for _, want := range []string{"granted", "read acme/lib", "Declined", "write acme/other"} {
		if !strings.Contains(last.Body, want) {
			t.Errorf("the decision comment does not say %q:\n%s", want, last.Body)
		}
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.AccessDecisions) != 1 || len(obs.AccessDecisions[0].Granted) != 3 ||
		len(obs.AccessDecisions[0].Declined) != 1 || obs.AccessDecisions[0].By.Kind != model.PrincipalHuman {
		t.Errorf("decisions = %+v, want one by a person granting three and declining one", obs.AccessDecisions)
	}
	events, err := store.TaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(e model.TaskEvent) bool {
		return e.Kind == model.TaskEventAccessGranted && strings.Contains(e.Detail, "acme/lib")
	}) {
		t.Errorf("no access-granted event recorded: %+v", events)
	}
}

func TestDenyAccessRequestNeedsAReasonAndShowsOnARepeat(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	parkOnAccess(t, c, ctx, task.ID, accessItems())

	if err := c.DenyAccessRequest(ctx, task.ID, ui.AccessDenyDecision{Reason: "  "}); err == nil {
		t.Fatal("a denial with no reason was accepted")
	}
	if err := c.DenyAccessRequest(ctx, task.ID, ui.AccessDenyDecision{Reason: "use the vendored copy instead"}); err != nil {
		t.Fatalf("DenyAccessRequest: %v", err)
	}
	got, _ := store.GetTask(ctx, task.ID)
	if len(got.Reads) != 0 || got.NetworkAccess != "" || len(got.Writes) != 0 {
		t.Errorf("a denial changed the task: %+v", got)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State == model.StateAwaitingReply {
		t.Fatal("a denied task is still parked")
	}
	last := detail.Comments[len(detail.Comments)-1]
	if !strings.Contains(last.Body, "denied") || !strings.Contains(last.Body, "use the vendored copy instead") ||
		!strings.Contains(last.Body, "Do not ask for this access again") {
		t.Errorf("the denial comment = %q, want the denial, its reason and not to ask again", last.Body)
	}

	// The same request again shows the earlier denial on its card.
	reordered := accessItems()
	slices.Reverse(reordered)
	parkOnAccess(t, c, ctx, task.ID, reordered)
	detail, err = c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PendingAccess == nil || len(detail.PendingAccess.Earlier) != 1 ||
		!detail.PendingAccess.Earlier[0].Denied || detail.PendingAccess.Earlier[0].Reason != "use the vendored copy instead" {
		t.Fatalf("a repeated request's card = %+v, want the earlier denial", detail.PendingAccess)
	}
}

func TestOnlyAPersonDecidesAnAccessRequest(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	parkOnAccess(t, c, ctx, task.ID, accessItems())

	var forbidden *ui.ForbiddenError
	for _, who := range []model.Principal{
		{Kind: model.PrincipalAgent, ID: "task-" + task.ID},
		{Kind: model.PrincipalAutomation, ID: "grain"},
		{Kind: model.PrincipalService, ID: "ci-bot@my-proj.iam.gserviceaccount.com", Source: model.SourceIAP},
	} {
		actor := ui.WithPrincipal(ctx, who)
		if err := c.GrantAccessRequest(actor, task.ID, ui.AccessGrantDecision{}); !errors.As(err, &forbidden) {
			t.Errorf("%s granting = %v, want forbidden", who.Kind, err)
		}
		if err := c.DenyAccessRequest(actor, task.ID, ui.AccessDenyDecision{Reason: "no"}); !errors.As(err, &forbidden) {
			t.Errorf("%s denying = %v, want forbidden", who.Kind, err)
		}
	}
	got, _ := store.GetTask(ctx, task.ID)
	if len(got.Grants) != 0 && slices.ContainsFunc(got.Grants, func(g model.Grant) bool { return strings.Contains(g.Capability, "tasks:admin") }) {
		t.Fatalf("a refused decision granted the dangerous scope: %v", got.Grants)
	}
}

func TestAnAgentCannotApproveItsOwnAccessRequest(t *testing.T) {
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:admin")
	parkOnAccess(t, client, context.Background(), "1", accessItems())

	for _, path := range []string{"/api/tasks/1/access-request/grant", "/api/tasks/1/access-request/deny"} {
		if code, body := asAgent(t, srv, "1", http.MethodPost, path, `{"reason":"mine"}`, ""); code != http.StatusForbidden {
			t.Errorf("an agent holding api:tasks:admin reached %s: %d %s", path, code, body)
		}
	}
	obs, err := client.Store.GetObservation(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingAccess == nil {
		t.Fatal("an agent's refused decision cleared its own request")
	}
	// A person, through the same route, may.
	if rec := do(t, srv, http.MethodPost, "/api/tasks/1/access-request/grant", `{"items":[0]}`); rec.Code != http.StatusOK {
		t.Fatalf("a person granting: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSecondAccessRequestAfterPartialGrantShowsEarlierAnswer(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)
	items := []model.AccessRequestItem{
		{Kind: model.AccessRead, Repo: "acme/docs"},
		{Kind: model.AccessNetwork, Access: "open"},
		{Kind: model.AccessGrant, Capability: "api:tasks:admin"},
	}
	parkOnAccess(t, c, ctx, task.ID, items)

	picked := []int{0, 1}
	if err := c.GrantAccessRequest(ctx, task.ID, ui.AccessGrantDecision{Items: &picked}); err != nil {
		t.Fatalf("GrantAccessRequest: %v", err)
	}
	// Run 2 cannot re-ask for items 0 and 1 because the task now holds them;
	// asking again for the declined item 2 must still show the earlier answer.
	parkOnAccess(t, c, ctx, task.ID, []model.AccessRequestItem{
		{Kind: model.AccessGrant, Capability: "api:tasks:admin"},
	})
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PendingAccess == nil || len(detail.PendingAccess.Earlier) != 1 {
		t.Fatalf("PendingAccess.Earlier = %+v, want the earlier partial grant decision", detail.PendingAccess)
	}
	earlier := detail.PendingAccess.Earlier[0]
	if earlier.Denied || len(earlier.Granted) != 2 || len(earlier.Declined) != 1 || earlier.Declined[0].Capability != "api:tasks:admin" {
		t.Fatalf("earlier decision = %+v, want 2 granted and api:tasks:admin declined", earlier)
	}

	// An unrelated later request does not show a decision whose items do not overlap.
	parkOnAccess(t, c, ctx, task.ID, []model.AccessRequestItem{
		{Kind: model.AccessWrite, Repo: "acme/lib", Base: "dev"},
	})
	detail, err = c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PendingAccess == nil || len(detail.PendingAccess.Earlier) != 0 {
		t.Fatalf("unrelated request Earlier = %+v, want none", detail.PendingAccess)
	}
}

func TestCheckAccessRequestHonorsTemplateAndSessionCatalogueContext(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	now := time.Now().UTC()
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tmpl-release", Name: "release-audit", Title: "Audit", Body: "Check", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{
			InProcess: map[string]model.CatalogueInProcess{
				"read-issues": {
					Restrictions: &model.CapabilityRestrictions{
						Templates:      model.Patterns{"release-audit"},
						RequiresPerson: true,
					},
				},
				"github-sandbox": {
					Restrictions: &model.CapabilityRestrictions{
						Kinds: model.Patterns{model.CatalogueKindSession},
					},
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	adhoc := create(t, c, ctx)
	if _, err := c.CheckAccessRequest(ctx, adhoc.ID, []model.AccessRequestItem{{Kind: model.AccessGrant, Capability: "read-issues"}}); err == nil {
		t.Fatal("ad-hoc task admitted template-restricted capability in CheckAccessRequest")
	}

	fromTemplate := model.Task{
		ID: "tmpl-task", Title: "Audit", Body: "Check",
		Target:     &model.RepoRef{Owner: "acme", Name: "widgets"},
		TemplateID: "tmpl-release", ApprovedAt: &now, CreatedAt: &now,
	}
	if err := store.PutTask(ctx, fromTemplate); err != nil {
		t.Fatal(err)
	}
	views, err := c.CheckAccessRequest(ctx, fromTemplate.ID, []model.AccessRequestItem{{Kind: model.AccessGrant, Capability: "read-issues"}})
	if err != nil {
		t.Fatalf("template task CheckAccessRequest: %v", err)
	}
	if len(views) != 1 || !views[0].RequiresPerson {
		t.Fatalf("views = %+v, want RequiresPerson=true on template-matched capability", views)
	}

	sessionTask := model.Task{
		ID: "sess-task", Title: "Console", Body: "Session",
		Interactive: true, Tags: []string{model.ConsoleSessionTag("ops")},
		Origin:     model.Origin{Reason: model.ReasonConsoleSession},
		ApprovedAt: &now, CreatedAt: &now,
	}
	if err := store.PutTask(ctx, sessionTask); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CheckAccessRequest(ctx, sessionTask.ID, []model.AccessRequestItem{{Kind: model.AccessGrant, Capability: "github-sandbox"}}); err != nil {
		t.Fatalf("session task CheckAccessRequest for session-restricted capability: %v", err)
	}
	// The same session-only capability stays refused to an ordinary task:
	// judging in the task's own context must not admit it everywhere.
	if _, err := c.CheckAccessRequest(ctx, adhoc.ID, []model.AccessRequestItem{{Kind: model.AccessGrant, Capability: "github-sandbox"}}); err == nil {
		t.Fatal("ad-hoc task admitted session-restricted capability in CheckAccessRequest")
	}
}

// grainCIItem is a request_access item for grain#ci, as task 3950's run
// asked for it.
func grainCIItem() model.AccessRequestItem {
	return model.AccessRequestItem{Kind: model.AccessGrant, Capability: model.GrainCIRef}
}

// A request for grain#ci is judged by the same check the grants editor
// makes, which refuses it: on a ci_mode: github repository the refusal
// names api:ci-read@<repo> and the commands that need no grant, never the
// CI Mode switch the repository deliberately does not use.
func TestCheckAccessRequestRefusesGrainCIOnAGitHubModeRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []model.AccessRequestItem{grainCIItem(), {Kind: model.AccessGrant, Capability: model.GrainCICapability}} {
		_, err := c.CheckAccessRequest(ctx, task.ID, []model.AccessRequestItem{item})
		var invalid *ui.ValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: CheckAccessRequest = %v, want a validation error", item.Capability, err)
		}
		for _, want := range []string{"requests[0]", "api#ci-read", `{repo: "acme/widgets"}`, "`grain ci wait`", "`grain pr open`"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal %q does not name %q", item.Capability, err, want)
			}
		}
		if strings.Contains(err.Error(), "Set the CI Mode") {
			t.Errorf("%s: refusal %q tells a ci_mode: github repo to switch to grain CI", item.Capability, err)
		}
	}
	// What the refusal says to ask for instead is accepted.
	capability, with := grainci.SubstituteRequest("acme/widgets")
	if _, err := c.CheckAccessRequest(ctx, task.ID, []model.AccessRequestItem{{Kind: model.AccessGrant, Capability: capability, With: with}}); err != nil {
		t.Errorf("api#ci-read {repo: acme/widgets} was refused: %v", err)
	}
}

// On a repository grain CI gates, the task holds grain#ci already, and a
// request for it is refused as one for what it has.
func TestCheckAccessRequestRefusesGrainCIOnAGrainCIRepoAsHeld(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	task := create(t, c, ctx)
	_, err := c.CheckAccessRequest(ctx, task.ID, []model.AccessRequestItem{grainCIItem()})
	var invalid *ui.ValidationError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "holds this already") {
		t.Fatalf("CheckAccessRequest = %v, want it refused as already held", err)
	}
}

// The grants editor's own check says no to grain#ci in either spelling,
// as attaching it does: request_access's check is that check.
func TestCheckCapabilityGrantsRefusesGrainCI(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	checks, err := c.CheckCapabilityGrants(ctx, []ui.GrantRequest{{Capability: model.GrainCIRef}, {Capability: model.GrainCICapability}})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		if check.Error == "" || check.Recorded != "" {
			t.Errorf("check of %s = %+v, want it refused", check.Grant, check)
		}
	}
}

// A request parked on grain#ci before its check refused it -- task 3950's
// -- shows why it cannot be granted and offers api:ci-read@<repo> in its
// place, and Grant grants that rather than failing.
func TestAParkedGrainCIRequestOffersCIReadAndGrantsIt(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	parkOnAccess(t, c, ctx, task.ID, []model.AccessRequestItem{grainCIItem()})

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PendingAccess == nil || len(detail.PendingAccess.Items) != 1 {
		t.Fatalf("PendingAccess = %+v, want the one item", detail.PendingAccess)
	}
	item := detail.PendingAccess.Items[0]
	want := grainci.SubstituteGrant("acme/widgets")
	if item.Error == "" || item.Substitute == nil || item.SubstituteGrant != want {
		t.Fatalf("item = %+v, want an error and the substitute %s", item, want)
	}

	if err := c.GrantAccessRequest(ctx, task.ID, ui.AccessGrantDecision{}); err != nil {
		t.Fatalf("GrantAccessRequest: %v", err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Grants, func(g model.Grant) bool { return g.Capability == want }) {
		t.Errorf("grants = %v, want %s", got.Grants, want)
	}
	if grainci.Granted(got.Grants) {
		t.Errorf("grants = %v, want no grain#ci", got.Grants)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingAccess != nil || len(obs.AccessDecisions) != 1 ||
		len(obs.AccessDecisions[0].Granted) != 1 || obs.AccessDecisions[0].Granted[0].Capability != model.APIRef("ci-read") {
		t.Errorf("decisions = %+v, want api#ci-read recorded as granted", obs.AccessDecisions)
	}
}
