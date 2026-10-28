package postgres_test

// Qualification's own store tests (bwsalmon/agents#518), against a real
// embedded SQLite database -- release_store_test.go's own reasoning for
// why this matters applies again here.

import (
	"context"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func buildTemplate(id string) model.Template {
	return model.Template{
		ID: id, Name: "Build", Title: "Build", Body: "go build ./...",
		Reads:  []model.RepoRef{gadgets},
		Grants: []model.Grant{{Capability: "run-tests", Via: model.GrantByLabel}}, CreatedAt: now,
	}
}

func unitTestTemplate(id string) model.Template {
	return model.Template{
		ID: id, Name: "Unit tests", Title: "Unit tests", Body: "go test ./...",
		CreatedAt: now,
	}
}

func testQualificationPlan() model.QualificationPlan {
	return model.QualificationPlan{
		Repo: widgets, Configured: true, RequireApproval: false, AutoPromote: true,
		Items: []model.QualificationItem{
			{TemplateID: "template-build", Repeat: 1},
			{TemplateID: "template-unit", Repeat: 2, DependsOn: []string{"template-build"}},
		},
	}
}

// cutTestCandidate creates a fresh release for widgets and returns its
// own first candidate -- CreateRelease's own "also cuts its first
// candidate" (bwsalmon/agents#571), which is all every qualification test
// below needs a candidate for.
func cutTestCandidate(t *testing.T, ctx context.Context, store *model.Store) model.Candidate {
	t.Helper()
	r, err := store.CreateRelease(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	c, err := store.CurrentCandidateForRelease(ctx, r.ID)
	if err != nil || c == nil {
		t.Fatalf("current candidate: (%+v, %v)", c, err)
	}
	return *c
}

func TestGetQualificationPlanReturnsNilOnAFreshDatabase(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	got, err := store.GetQualificationPlan(ctx, widgets)
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil) before anything has configured %s, got (%+v, %v)", widgets, got, err)
	}
}

func TestQualificationPlanRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := testQualificationPlan()
	if err := store.PutQualificationPlan(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetQualificationPlan(ctx, widgets)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, want %+v", *got, want)
	}
}

// PutQualificationPlan replaces a repo's items wholesale rather than
// accumulating them, the same discipline every other one-row-per-repo
// config in this package already holds its own single row to.
func TestPutQualificationPlanReplacesRatherThanAccumulating(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutQualificationPlan(ctx, testQualificationPlan()); err != nil {
		t.Fatalf("first put: %v", err)
	}
	updated := model.QualificationPlan{
		Repo: widgets, Configured: true, RequireApproval: true, AutoPromote: false,
		Items: []model.QualificationItem{{TemplateID: "template-smoke", Repeat: 1}},
	}
	if err := store.PutQualificationPlan(ctx, updated); err != nil {
		t.Fatalf("second put: %v", err)
	}
	got, err := store.GetQualificationPlan(ctx, widgets)
	if err != nil || got == nil {
		t.Fatalf("get: (%+v, %v)", got, err)
	}
	if !reflect.DeepEqual(*got, updated) {
		t.Fatalf("got %+v, want %+v", *got, updated)
	}
}

func TestCreateQualificationRunInstantiatesEveryItemWithDependencyLinks(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	// The build template says something about this deployment's own
	// declared fields (grain/task-105), which every task filed from it
	// should carry, exactly as its reads do.
	if err := store.PutCustomField(ctx, model.CustomField{Name: "team"}); err != nil {
		t.Fatalf("declaring a field: %v", err)
	}
	build := buildTemplate("template-build")
	build.CustomFields = map[string][]string{"team": {"infra"}}
	if err := store.PutTemplate(ctx, build); err != nil {
		t.Fatalf("put build template: %v", err)
	}
	if err := store.PutTemplate(ctx, unitTestTemplate("template-unit")); err != nil {
		t.Fatalf("put unit template: %v", err)
	}
	candidate := cutTestCandidate(t, ctx, store)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	candidate.Status = model.CandidateActive

	plan := testQualificationPlan()
	run, err := store.CreateQualificationRun(ctx, candidate, plan, now)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if len(run.Tasks) != 3 {
		t.Fatalf("got %d task instances, want 3 (1 build + 2 unit)", len(run.Tasks))
	}

	var buildID string
	unitCount := 0
	for _, ts := range run.Tasks {
		if !ts.Approved {
			t.Errorf("task %s: want approved (RequireApproval is false), got unapproved", ts.TaskID)
		}
		if ts.State != model.StateQueued {
			t.Errorf("task %s: got state %s, want queued", ts.TaskID, ts.State)
		}
		switch ts.TemplateID {
		case "template-build":
			buildID = ts.TaskID
			if ts.TemplateName != "Build" {
				t.Errorf("got template name %q, want Build", ts.TemplateName)
			}
		case "template-unit":
			unitCount++
		}
	}
	if buildID == "" {
		t.Fatal("no build instance found")
	}
	if unitCount != 2 {
		t.Fatalf("got %d unit instances, want 2", unitCount)
	}

	// Every unit instance should depend on the build instance, and
	// nothing should block build itself.
	for _, ts := range run.Tasks {
		task, err := store.GetTask(ctx, ts.TaskID)
		if err != nil || task == nil {
			t.Fatalf("get task %s: (%+v, %v)", ts.TaskID, task, err)
		}
		if task.Base != candidate.Branch {
			t.Errorf("task %s: got base %q, want candidate branch %q", ts.TaskID, task.Base, candidate.Branch)
		}
		if task.Target == nil || *task.Target != widgets {
			t.Errorf("task %s: got target %v, want %s", ts.TaskID, task.Target, widgets)
		}
		if task.Origin.Reason != model.ReasonQualification {
			t.Errorf("task %s: got origin reason %q, want qualification", ts.TaskID, task.Origin.Reason)
		}
		var deps []string
		for _, l := range task.Links {
			if l.Kind == model.LinkDependsOn {
				deps = append(deps, l.Target)
			}
		}
		sort.Strings(deps)
		switch ts.TemplateID {
		case "template-build":
			if len(deps) != 0 {
				t.Errorf("build task %s: got deps %v, want none", ts.TaskID, deps)
			}
			if len(task.Reads) != 1 || task.Reads[0] != gadgets {
				t.Errorf("build task %s: got reads %v, want [%s]", ts.TaskID, task.Reads, gadgets)
			}
			if !slices.Equal(task.CustomFields["team"], []string{"infra"}) {
				t.Errorf("build task %s: got customFields %v, want the template's own",
					ts.TaskID, task.CustomFields)
			}
		case "template-unit":
			if len(deps) != 1 || deps[0] != buildID {
				t.Errorf("unit task %s: got deps %v, want [%s]", ts.TaskID, deps, buildID)
			}
		}
	}

	// A second run for the same candidate is refused outright.
	if _, err := store.CreateQualificationRun(ctx, candidate, plan, now); err == nil {
		t.Fatal("want an error creating a second run for the same candidate")
	}
}

// An item with Repeat > 1 depending on another item with Repeat > 1 waits
// on the full cross product: every one of its own instances links to
// every instance the dependency produced, not just one of them -- the
// commit's own "an item with Repeat 3 depending on one with Repeat 2
// waits on both of the latter's instances before any of its own three
// start," exercised directly rather than trusted from a doc comment.
func TestCreateQualificationRunLinksEveryDependentInstanceToEveryDependencyInstance(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTemplate(ctx, buildTemplate("template-build")); err != nil {
		t.Fatalf("put build template: %v", err)
	}
	if err := store.PutTemplate(ctx, unitTestTemplate("template-unit")); err != nil {
		t.Fatalf("put unit template: %v", err)
	}
	candidate := cutTestCandidate(t, ctx, store)

	plan := model.QualificationPlan{
		Repo: widgets, Configured: true,
		Items: []model.QualificationItem{
			{TemplateID: "template-build", Repeat: 2},
			{TemplateID: "template-unit", Repeat: 3, DependsOn: []string{"template-build"}},
		},
	}
	run, err := store.CreateQualificationRun(ctx, candidate, plan, now)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if len(run.Tasks) != 5 {
		t.Fatalf("got %d task instances, want 5 (2 build + 3 unit)", len(run.Tasks))
	}

	var buildIDs []string
	var unitIDs []string
	for _, ts := range run.Tasks {
		switch ts.TemplateID {
		case "template-build":
			buildIDs = append(buildIDs, ts.TaskID)
		case "template-unit":
			unitIDs = append(unitIDs, ts.TaskID)
		}
	}
	if len(buildIDs) != 2 || len(unitIDs) != 3 {
		t.Fatalf("got %d build instances and %d unit instances, want 2 and 3", len(buildIDs), len(unitIDs))
	}

	for _, id := range unitIDs {
		task, err := store.GetTask(ctx, id)
		if err != nil || task == nil {
			t.Fatalf("get task %s: (%+v, %v)", id, task, err)
		}
		var deps []string
		for _, l := range task.Links {
			if l.Kind == model.LinkDependsOn {
				deps = append(deps, l.Target)
			}
		}
		sort.Strings(deps)
		want := append([]string(nil), buildIDs...)
		sort.Strings(want)
		if !reflect.DeepEqual(deps, want) {
			t.Fatalf("unit task %s: got deps %v, want every build instance %v", id, deps, want)
		}
	}
}

// CreateQualificationRun's own doc comment promises the unique index on
// candidate_id is "the backstop against creating two runs for the same
// candidate" when "a concurrent daemon racing the same candidate" beats
// the caller's own nil check -- exercised here with real goroutines
// rather than trusted from the two sequential calls
// TestCreateQualificationRunInstantiatesEveryItemWithDependencyLinks
// already makes.
func TestCreateQualificationRunUnderConcurrentRaceCreatesExactlyOneRun(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTemplate(ctx, buildTemplate("template-build")); err != nil {
		t.Fatalf("put build template: %v", err)
	}
	candidate := cutTestCandidate(t, ctx, store)
	plan := model.QualificationPlan{
		Repo: widgets, Items: []model.QualificationItem{{TemplateID: "template-build", Repeat: 1}},
	}

	const attempts = 8
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = store.CreateQualificationRun(ctx, candidate, plan, now)
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successful concurrent creations, want exactly 1 (errs: %v)", successes, errs)
	}

	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("candidate qualification run: (%+v, %v)", run, err)
	}
	if len(run.Tasks) != 1 {
		t.Fatalf("got %d task instances, want exactly 1 (no duplication)", len(run.Tasks))
	}
}

func TestCreateQualificationRunFailsWhenATemplateIsMissing(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	candidate := cutTestCandidate(t, ctx, store)
	plan := model.QualificationPlan{
		Repo: widgets, Items: []model.QualificationItem{{TemplateID: "does-not-exist", Repeat: 1}},
	}
	if _, err := store.CreateQualificationRun(ctx, candidate, plan, now); err == nil {
		t.Fatal("want an error when a plan's template no longer exists")
	}
}

// TestCreateQualificationRunFailsWhenATemplateIsBoundElsewhere is
// CreateQualificationRun's own backstop for grain/task-285: a run's
// tasks always target the candidate's repo and branch, so a template
// bound to another repo is one this run cannot honour -- it fails the
// run outright rather than quietly aiming the template somewhere its
// binding says it does not belong. ui.Client.PutQualificationPlan
// refuses to save such a plan in the first place; this catches a
// template bound after the plan was saved.
func TestCreateQualificationRunFailsWhenATemplateIsBoundElsewhere(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	bound := buildTemplate("template-build")
	bound.Target = &gadgets
	if err := store.PutTemplate(ctx, bound); err != nil {
		t.Fatalf("put build template: %v", err)
	}
	candidate := cutTestCandidate(t, ctx, store)
	plan := model.QualificationPlan{
		Repo: widgets, Items: []model.QualificationItem{{TemplateID: "template-build", Repeat: 1}},
	}
	if _, err := store.CreateQualificationRun(ctx, candidate, plan, now); err == nil {
		t.Fatal("want an error when a plan's template is bound to another repo")
	}
}

// A template bound to the repo being qualified is usable, and the
// candidate's own branch still decides what its tasks run against --
// the binding's branch never moves a qualification task.
func TestCreateQualificationRunHonoursTheCandidateBranchOverABinding(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	bound := buildTemplate("template-build")
	bound.Target, bound.Base = &widgets, "some-other-branch"
	if err := store.PutTemplate(ctx, bound); err != nil {
		t.Fatalf("put build template: %v", err)
	}
	candidate := cutTestCandidate(t, ctx, store)
	plan := model.QualificationPlan{
		Repo: widgets, Items: []model.QualificationItem{{TemplateID: "template-build", Repeat: 1}},
	}
	run, err := store.CreateQualificationRun(ctx, candidate, plan, now)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	for _, ts := range run.Tasks {
		task, err := store.GetTask(ctx, ts.TaskID)
		if err != nil || task == nil {
			t.Fatalf("get task %s: (%+v, %v)", ts.TaskID, task, err)
		}
		if task.Target == nil || *task.Target != widgets || task.Base != candidate.Branch {
			t.Errorf("task %s: target/base = %v/%q, want %s/%q",
				ts.TaskID, task.Target, task.Base, widgets, candidate.Branch)
		}
	}
}

func TestCreateQualificationRunLeavesTasksUnapprovedWhenPlanRequiresApproval(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTemplate(ctx, buildTemplate("template-build")); err != nil {
		t.Fatalf("put build template: %v", err)
	}
	if err := store.PutTemplate(ctx, unitTestTemplate("template-unit")); err != nil {
		t.Fatalf("put unit template: %v", err)
	}
	candidate := cutTestCandidate(t, ctx, store)

	plan := testQualificationPlan()
	plan.RequireApproval = true
	run, err := store.CreateQualificationRun(ctx, candidate, plan, now)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	for _, ts := range run.Tasks {
		if ts.Approved {
			t.Errorf("task %s: want unapproved, got approved", ts.TaskID)
		}
		if ts.State != model.StateProposed {
			t.Errorf("task %s: got state %s, want proposed", ts.TaskID, ts.State)
		}
	}
	if got := model.QualificationStatus(run.Tasks); got != model.QualificationPendingApproval {
		t.Fatalf("got status %v, want pending_approval", got)
	}

	if err := store.ApproveQualificationRun(ctx, run.ID, model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}, now); err != nil {
		t.Fatalf("approve: %v", err)
	}
	approved, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || approved == nil {
		t.Fatalf("reading run after approval: (%+v, %v)", approved, err)
	}
	for _, ts := range approved.Tasks {
		if !ts.Approved {
			t.Errorf("task %s: want approved after ApproveQualificationRun, got unapproved", ts.TaskID)
		}
	}
}

func TestQualifiableActiveCandidatesFindsAnActiveCandidateWithAnItemButNoRunYet(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	candidate := cutTestCandidate(t, ctx, store)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}

	got, err := store.QualifiableActiveCandidates(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates before any plan is configured, want 0", len(got))
	}

	if err := store.PutQualificationPlan(ctx, testQualificationPlan()); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	got, err = store.QualifiableActiveCandidates(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != candidate.ID {
		t.Fatalf("got %+v, want exactly candidate %d", got, candidate.ID)
	}
}

func TestQualificationPlansUsingTemplateFindsAPlanThatReferencesIt(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	got, err := store.QualificationPlansUsingTemplate(ctx, "template-build")
	if err != nil {
		t.Fatalf("list before any plan exists: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}

	if err := store.PutQualificationPlan(ctx, testQualificationPlan()); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	got, err = store.QualificationPlansUsingTemplate(ctx, "template-build")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0] != widgets {
		t.Fatalf("got %+v, want [%s]", got, widgets)
	}
}
