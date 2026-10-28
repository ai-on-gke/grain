package ui_test

// The operator surface for a repository's work ownership
// (grain/task-542): both levels of model.WorkOwnership, and the three
// states each of them has.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ownershipServer is testServer with one repository declaring two
// schedules -- one running, one paused -- and one schedule created here
// that targets it, which is the list this pane exists to draw.
func ownershipServer(t *testing.T) (*ui.Server, *model.Store, context.Context, string) {
	t.Helper()
	client, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	nightly := model.RepoDeclaredID(model.DeclaredSchedule, repo, "nightly")
	weekly := model.RepoDeclaredID(model.DeclaredSchedule, repo, "weekly")
	every := model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24}
	if _, err := store.SyncRepoDeclarations(ctx, repo, "abc123", model.RepoDeclarations{
		Schedules: []model.DeclaredScheduleFile{
			{
				Path: model.ScheduleDeclarationDir + "/nightly.yml",
				Schedule: model.Schedule{
					ID: nightly, Title: "Nightly sweep", Target: repo,
					Recurrence: every, Enabled: true,
				},
			},
			{
				Path: model.ScheduleDeclarationDir + "/weekly.yml",
				Schedule: model.Schedule{
					ID: weekly, Title: "Weekly report", Target: repo,
					Recurrence: every, Enabled: false,
				},
			},
		},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "local-chore", Title: "Housekeeping", Target: repo,
		Recurrence: every, Enabled: true,
	}); err != nil {
		t.Fatalf("creating a schedule here: %v", err)
	}
	// A schedule targeting somebody else, which must not appear on this
	// repository's list however this deployment answers for it.
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "elsewhere", Title: "Not ours", Target: model.RepoRef{Owner: "acme", Name: "gadgets"},
		Recurrence: every, Enabled: true,
	}); err != nil {
		t.Fatalf("creating a schedule elsewhere: %v", err)
	}
	return ui.NewServerWithClient(client), store, ctx, nightly
}

func itemOf(t *testing.T, got ui.RepoWorkOwnership, id string) ui.RepoWorkItem {
	t.Helper()
	for _, item := range got.Items {
		if item.Item == id {
			return item
		}
	}
	t.Fatalf("item %q is not on the list: %+v", id, got.Items)
	return ui.RepoWorkItem{}
}

// A repository nobody has said anything about reads as silence that
// resolves to "yes" -- and the two halves are reported separately,
// which is the whole of what a checkbox could not have said.
func TestRepoWorkOwnershipDrawsSilenceAsItsOwnState(t *testing.T) {
	t.Parallel()
	srv, _, _, nightly := ownershipServer(t)

	rec := do(t, srv, http.MethodGet, "/api/repos/acme/widgets/work-ownership", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoWorkOwnership](t, rec)
	if got.Repo != "acme/widgets" || got.Default != "" || !got.DefaultRuns {
		t.Fatalf("default = %+v, want nobody having said and work running anyway", got)
	}
	release := itemOf(t, got, string(model.ReleaseItem))
	if release.Kind != ui.WorkItemKindRelease || release.Override != "" || !release.Runs {
		t.Fatalf("release row = %+v", release)
	}
	if got.Items[0].Item != string(model.ReleaseItem) {
		t.Errorf("first row = %q, want the release", got.Items[0].Item)
	}
	// The repository's own declared schedules are rows, with the file
	// they come from: an operator asking "which of these is this
	// deployment running" should not have to type an id.
	sweep := itemOf(t, got, nightly)
	if sweep.Kind != ui.WorkItemKindSchedule || sweep.Title != "Nightly sweep" || sweep.Paused {
		t.Fatalf("declared schedule row = %+v", sweep)
	}
	if sweep.Declaration == nil || sweep.Declaration.Path != model.ScheduleDeclarationDir+"/nightly.yml" {
		t.Fatalf("declaration = %+v, want the file it was declared in", sweep.Declaration)
	}
	// A schedule's own pause switch is a different fact from this
	// deployment not owning it, and is drawn as one.
	weekly := itemOf(t, got, model.RepoDeclaredID(model.DeclaredSchedule,
		model.RepoRef{Owner: "acme", Name: "widgets"}, "weekly"))
	if !weekly.Paused || !weekly.Runs {
		t.Fatalf("paused schedule row = %+v, want paused and still owned", weekly)
	}
	// A schedule created here that fires into the repository is its
	// standing work too, and carries no declaration.
	local := itemOf(t, got, "local-chore")
	if local.Declaration != nil || local.Kind != ui.WorkItemKindSchedule {
		t.Fatalf("locally created schedule row = %+v", local)
	}
	for _, item := range got.Items {
		if item.Item == "elsewhere" {
			t.Fatalf("a schedule targeting another repository is on this list: %+v", got.Items)
		}
	}
}

// The repository default, and the three states it is written through.
func TestRepoWorkOwnershipDefaultIsWrittenAndCleared(t *testing.T) {
	t.Parallel()
	srv, store, ctx, nightly := ownershipServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership", `{"default":"off"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoWorkOwnership](t, rec)
	if got.Default != "off" || got.DefaultRuns {
		t.Fatalf("default = %+v, want an explicit off", got)
	}
	// Every item that was following the default moved with it, which is
	// the point of having a default at all.
	if itemOf(t, got, nightly).Runs || itemOf(t, got, string(model.ReleaseItem)).Runs {
		t.Fatalf("items still run with the repository's default off: %+v", got.Items)
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil || cfg == nil || cfg.Work.Default != model.OwnershipOff {
		t.Fatalf("stored default = %+v, %v", cfg, err)
	}

	// An explicit "on" is not the same write as clearing it, and the
	// store keeps the difference.
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership", `{"default":"on"}`)
	if got = decode[ui.RepoWorkOwnership](t, rec); rec.Code != http.StatusOK || got.Default != "on" || !got.DefaultRuns {
		t.Fatalf("PUT on: %d %+v", rec.Code, got)
	}
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership", `{"default":""}`)
	if got = decode[ui.RepoWorkOwnership](t, rec); rec.Code != http.StatusOK || got.Default != "" || !got.DefaultRuns {
		t.Fatalf("PUT clearing the default: %d %+v", rec.Code, got)
	}
	cfg, err = store.GetRepoConfig(ctx, repo)
	if err != nil || (cfg != nil && cfg.Work.Default != model.OwnershipUnset) {
		t.Fatalf("stored default after clearing = %+v, %v", cfg, err)
	}

	// Anything that is not one of the three is a typo rather than a
	// third answer, and is refused instead of read as "off".
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership", `{"default":"false"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT of a nonsense default: %d %s", rec.Code, rec.Body)
	}
	// And a request about neither level is a mistake worth reporting.
	if rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with nothing to set: %d %s", rec.Code, rec.Body)
	}
}

// One item's override: set against the default, cleared back to
// following it, and both of those distinguishable in what comes back.
func TestRepoWorkOwnershipItemOverridesTheDefault(t *testing.T) {
	t.Parallel()
	srv, store, ctx, nightly := ownershipServer(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership",
		`{"default":"off"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT default: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership",
		`{"item":"`+nightly+`","state":"on"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT item: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoWorkOwnership](t, rec)
	sweep := itemOf(t, got, nightly)
	if sweep.Override != "on" || !sweep.Runs {
		t.Fatalf("overridden row = %+v, want an explicit on that runs", sweep)
	}
	if itemOf(t, got, string(model.ReleaseItem)).Runs {
		t.Fatalf("one item's exception moved another item: %+v", got.Items)
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil || cfg == nil || !model.RunsWorkItem(cfg, model.ScheduleItem(nightly)) {
		t.Fatalf("stored config = %+v, %v", cfg, err)
	}

	// Clearing the override is "follow the default again" rather than
	// "off", which is exactly the distinction the empty state carries.
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership",
		`{"item":"`+nightly+`","state":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT clearing the override: %d %s", rec.Code, rec.Body)
	}
	if sweep = itemOf(t, decode[ui.RepoWorkOwnership](t, rec), nightly); sweep.Override != "" || sweep.Runs {
		t.Fatalf("cleared row = %+v, want it following a default that is off", sweep)
	}

	// An item without a state is a request missing a field rather than a
	// request to clear it.
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership",
		`{"item":"`+nightly+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT of an item with no state: %d %s", rec.Code, rec.Body)
	}
	// And an id the storage encoding could not survive is refused rather
	// than written and silently dropped on the way back in.
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership",
		`{"item":"a,b","state":"off"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT of an id holding a separator: %d %s", rec.Code, rec.Body)
	}
}

// An override outlives whatever it named -- a withdrawn declaration
// leaves one behind -- and it has to stay visible, or nobody can clear
// the decision that is still being made on their behalf.
func TestRepoWorkOwnershipListsAnOverrideWithNoItemLeft(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := ownershipServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership",
		`{"item":"withdrawn-schedule","state":"off"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	got := itemOf(t, decode[ui.RepoWorkOwnership](t, rec), "withdrawn-schedule")
	if got.Kind != ui.WorkItemKindUnknown || got.Override != "off" || got.Runs {
		t.Fatalf("row for an item nothing matches = %+v", got)
	}
}

// The release item is the one this record already resolves today, and
// the Release pane writes it. Both surfaces write the same override, so
// what one does the other reports.
func TestRepoWorkOwnershipAndTheReleasePaneWriteTheSameItem(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := ownershipServer(t)

	if rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/release", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT release: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, srv, http.MethodGet, "/api/repos/acme/widgets/work-ownership", "")
	got := itemOf(t, decode[ui.RepoWorkOwnership](t, rec), string(model.ReleaseItem))
	if got.Override != "off" || got.Runs {
		t.Fatalf("release row after the release pane switched it off = %+v", got)
	}

	if rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership",
		`{"item":"release","state":"on"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT item: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodGet, "/api/repos/acme/widgets/release", "")
	if settings := decode[ui.RepoReleaseSettings](t, rec); !settings.Enabled {
		t.Fatalf("release settings = %+v, want the item's override read back there", settings)
	}
}

// Every reserved item the standing-work switch covers is on the page
// (grain/task-3332) -- the base-branch check and ports included -- with
// what the repository default resolves it to, and an item explicitly on
// beneath a default off reads as running.
func TestRepoWorkOwnershipListsEveryStandingWorkItem(t *testing.T) {
	t.Parallel()
	srv, _, _, nightly := ownershipServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership", `{"default":"off"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT default: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/work-ownership", `{"item":"ports","state":"on"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT ports: %d %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoWorkOwnership](t, rec)
	if got.DefaultRuns {
		t.Fatal("a repository default of off resolves to running")
	}
	for i, id := range model.StandingWorkItems {
		if got.Items[i].Item != string(id) {
			t.Fatalf("row %d = %q, want the reserved items first, in order: %+v", i, got.Items[i].Item, got.Items)
		}
	}
	for _, id := range []model.WorkItem{model.ReleaseItem, model.BaseBranchCIItem, model.PostMergeItem, model.TriageItem} {
		row := itemOf(t, got, string(id))
		if row.Runs || row.Override != "" || row.Title == "" {
			t.Errorf("%s = %+v, want a titled row following the default to off", id, row)
		}
	}
	if ports := itemOf(t, got, string(model.PortsItem)); !ports.Runs || ports.Override != "on" ||
		ports.Kind != ui.WorkItemKindStanding {
		t.Errorf("ports = %+v, want a standing item explicitly on and running", ports)
	}
	if sweep := itemOf(t, got, nightly); sweep.Runs {
		t.Errorf("the nightly schedule = %+v, want it following the default to off", sweep)
	}
}
