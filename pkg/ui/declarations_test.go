package ui_test

// What grain's own settings surface does with a template, suite or
// schedule a repository declares in its `.grain` directory
// (grain/task-60): it shows it, it will not edit it, and it says where
// the file is instead.

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

var declared = model.RepoRef{Owner: "acme", Name: "widgets"}

// declaringServer is testServer with one repository's declarations
// already in the store behind it.
func declaringServer(t *testing.T) (*ui.Server, string, string) {
	t.Helper()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	target := declared
	templateID := model.RepoDeclaredID(model.DeclaredTemplate, declared, "nightly")
	scheduleID := model.RepoDeclaredID(model.DeclaredSchedule, declared, "nightly")
	if _, err := store.SyncRepoDeclarations(ctx, declared, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path: model.TemplateDeclarationDir + "/nightly.yml",
			Template: model.Template{
				ID: templateID, Name: "nightly", Title: "Bump dependencies", Target: &target,
			},
		}},
		Schedules: []model.DeclaredScheduleFile{{
			Path: model.ScheduleDeclarationDir + "/nightly.yml",
			Schedule: model.Schedule{
				ID: scheduleID, Title: "nightly", Target: declared, TemplateID: &templateID,
				Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
				Enabled:    true,
			},
		}},
		Triggers: []model.PathTrigger{{
			Name: "docs", Path: model.TriggerDeclarationDir + "/docs.yml", On: model.TriggerOnMerge,
			Paths: []string{"docs"}, Template: templateID,
		}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}
	return srv, templateID, scheduleID
}

func TestADeclaredTemplateCannotBeEditedHere(t *testing.T) {
	t.Parallel()
	srv, templateID, _ := declaringServer(t)

	rec := do(t, srv, http.MethodPatch, "/api/templates/"+templateID, `{"title":"Something else"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, ".grain/templates/nightly.yml") {
		t.Errorf("refusal = %s, want it to name the file to change instead", body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "acme/widgets") {
		t.Errorf("refusal = %s, want it to name the repository", body)
	}
}

func TestADeclaredTemplateCannotBeDeletedHere(t *testing.T) {
	t.Parallel()
	srv, templateID, _ := declaringServer(t)

	rec := do(t, srv, http.MethodDelete, "/api/templates/"+templateID, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestADeclaredScheduleCannotBeEditedOrDeletedHere(t *testing.T) {
	t.Parallel()
	srv, _, scheduleID := declaringServer(t)

	rec := do(t, srv, http.MethodPatch, "/api/schedules/"+scheduleID, `{"enabled":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, ".grain/schedules/nightly.yml") {
		t.Errorf("refusal = %s, want it to name the file", body)
	}
	rec = do(t, srv, http.MethodDelete, "/api/schedules/"+scheduleID, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// Run now is a firing, not an edit, so a declared schedule still takes
// it -- and the firing keeps the name the repository gave the schedule.
// A firing refreshes a schedule's title from what it fires, which for a
// declared schedule renamed the row to its template's title until the
// next poll of the repository put the file's name back (grain/task-1358).
func TestRunningADeclaredScheduleKeepsItsDeclaredName(t *testing.T) {
	t.Parallel()
	srv, _, scheduleID := declaringServer(t)

	rec := do(t, srv, http.MethodPost, "/api/schedules/"+scheduleID+"/run", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run now status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"title":"nightly"`) {
		t.Errorf("run now answered %s, want the schedule still titled \"nightly\"", body)
	}
	rec = do(t, srv, http.MethodGet, "/api/schedules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"title":"nightly"`) || strings.Contains(body, `"title":"Bump dependencies"`) {
		t.Errorf("schedules after run now = %s, want the declared schedule still titled \"nightly\"", body)
	}
	if !strings.Contains(body, `"lastRunAt"`) {
		t.Errorf("schedules after run now = %s, want the firing recorded as lastRunAt", body)
	}
}

// An ordinary template is still an ordinary template: the refusal is
// about where a row came from, not about templates.
func TestATemplateSomebodyCreatedHereIsStillEditable(t *testing.T) {
	t.Parallel()
	srv, _, _ := declaringServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates", `{"name":"By hand","title":"Bump dependencies"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Template](t, rec)
	rec = do(t, srv, http.MethodPatch, "/api/templates/"+created.ID, `{"title":"Something else"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// The switch and what has come of it, on the repository's own page.
func TestRepoSettingsCarryTheDeclarationsSwitchAndItsStatus(t *testing.T) {
	t.Parallel()
	srv, _, _ := declaringServer(t)

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/declarations", `{"declarations":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	defaults := decode[ui.RepoDefaults](t, rec)
	if !defaults.Declarations {
		t.Fatal("declarations = false after turning them on")
	}
	if defaults.DeclarationsStatus == nil {
		t.Fatal("no declarations status for a repository grain has read")
	}
	status := defaults.DeclarationsStatus
	if status.Commit != "abc123" {
		t.Errorf("commit = %q, want the one the declarations were read at", status.Commit)
	}
	if len(status.Templates) != 1 || len(status.Schedules) != 1 {
		t.Errorf("status = %+v, want the files it found listed", status)
	}
	// Triggers are stored apart from the other three, and were left out
	// of the status until grain/task-1829.
	if len(status.Triggers) != 1 || status.Triggers[0] != ".grain/triggers/docs.yml" {
		t.Errorf("status.Triggers = %v, want the trigger file listed", status.Triggers)
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/declarations", `{"declarations":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if decode[ui.RepoDefaults](t, rec).Declarations {
		t.Fatal("declarations = true after turning them off")
	}
}

// A merge-rules file the sync took with warnings says so on the status,
// rather than only in the daemon log (grain/task-2107).
func TestRepoDeclarationsStatusCarriesMergeRulesWarnings(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	warning := "both " + model.MergeRulesPath + " and " + model.MergeRulesAliasPath +
		" exist; sensitive.yml is ignored"
	if _, err := store.SyncRepoDeclarations(ctx, declared, "abc123", model.RepoDeclarations{
		MergeRules: &model.MergeRules{Path: model.MergeRulesPath, Warnings: []string{warning}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}

	rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/declarations", `{"declarations":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	status := decode[ui.RepoDefaults](t, rec).DeclarationsStatus
	if status == nil {
		t.Fatal("no declarations status for a repository grain has read")
	}
	if status.MergeRules != model.MergeRulesPath {
		t.Errorf("status.MergeRules = %q, want %q", status.MergeRules, model.MergeRulesPath)
	}
	if len(status.Warnings) != 1 || status.Warnings[0] != warning {
		t.Errorf("status.Warnings = %v, want [%q]", status.Warnings, warning)
	}
}

// The operator's port settings and the repository's own are both on the
// repository's settings, with the filter they compose into and any
// declared entry that has no effect.
func TestRepoSettingsReportPortPairsFiltersAndIgnoredDeclarations(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	if _, err := store.SyncRepoDeclarations(ctx, declared, "abc123", model.RepoDeclarations{
		Settings: &model.DeclaredSettingsFile{Path: model.SettingsDeclarationPath, Settings: model.RepoSettings{
			PortBranches: []string{"main", "candidate"},
			PortTypes:    map[string][]string{"main->candidate": {"fix"}, "main->release": {"fix"}},
		}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}
	rc, err := store.GetRepoConfig(ctx, declared)
	if err != nil || rc == nil {
		t.Fatalf("GetRepoConfig = %v, %v", rc, err)
	}
	rc.PortBranches = []string{"main", "candidate"}
	if err := store.PutRepoConfig(ctx, *rc); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodGet, "/api/repos/acme/widgets/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.RepoDefaults](t, rec)
	if !reflect.DeepEqual(got.PortBranches, []string{"main", "candidate"}) ||
		!reflect.DeepEqual(got.DeclaredPortBranches, []string{"main", "candidate"}) ||
		!reflect.DeepEqual(got.PairedBranches, []string{"main", "candidate"}) {
		t.Errorf("port branches = %v / %v / %v", got.PortBranches, got.DeclaredPortBranches, got.PairedBranches)
	}
	want := []ui.RepoPortDirection{
		{From: "candidate", To: "main", Paired: true},
		// The operator pairs both with no list, so the declared [fix] is
		// reported and composes into no filter at all.
		{From: "main", To: "candidate", Paired: true, DeclaredTypes: []string{"fix"}},
		{From: "main", To: "release", DeclaredTypes: []string{"fix"}, Types: []string{"fix"}},
	}
	if !reflect.DeepEqual(got.PortDirections, want) {
		t.Errorf("port directions = %+v, want %+v", got.PortDirections, want)
	}
	if len(got.PortWarnings) != 2 {
		t.Fatalf("port warnings = %q, want two", got.PortWarnings)
	}
	if got.DeclarationsStatus == nil || !reflect.DeepEqual(got.DeclarationsStatus.Warnings, got.PortWarnings) {
		t.Errorf("declarations status %+v, want its warnings to carry %q", got.DeclarationsStatus, got.PortWarnings)
	}
}

// A listing marks its declared rows, so a UI can show one as belonging
// to a repository rather than to this deployment's settings.
func TestListingsSayWhichRowsARepositoryDeclares(t *testing.T) {
	t.Parallel()
	srv, templateID, scheduleID := declaringServer(t)

	rec := do(t, srv, http.MethodGet, "/api/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rec.Code, rec.Body)
	}
	templates := ownTemplates(decode[[]ui.Template](t, rec))
	if len(templates) != 1 || templates[0].ID != templateID {
		t.Fatalf("templates = %+v", templates)
	}
	if templates[0].DeclaredIn == nil ||
		templates[0].DeclaredIn.Path != model.TemplateDeclarationDir+"/nightly.yml" {
		t.Errorf("declaredIn = %+v, want the repository and file", templates[0].DeclaredIn)
	}

	rec = do(t, srv, http.MethodGet, "/api/schedules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rec.Code, rec.Body)
	}
	schedules := decode[[]ui.Schedule](t, rec)
	if len(schedules) != 1 || schedules[0].ID != scheduleID {
		t.Fatalf("schedules = %+v", schedules)
	}
	if schedules[0].DeclaredIn == nil || schedules[0].DeclaredIn.Repo != "acme/widgets" {
		t.Errorf("declaredIn = %+v, want the repository and file", schedules[0].DeclaredIn)
	}
}
