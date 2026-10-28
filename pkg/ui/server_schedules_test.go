package ui_test

// The /api/schedules HTTP surface -- server_test.go's own doc comment on
// why this only asserts status codes and JSON shape, leaving Client's own
// behaviour to schedules_test.go.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/ui"
)

func TestSchedulesCreateListUpdateDelete(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"nightly bump","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Schedule](t, rec)
	if created.ID == "" {
		t.Fatal("created schedule came back with no id")
	}
	if !created.Enabled {
		t.Error("want a freshly created schedule enabled")
	}

	rec = do(t, srv, http.MethodGet, "/api/schedules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	list := decode[[]ui.Schedule](t, rec)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v, want just %s", list, created.ID)
	}

	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", rec.Code, rec.Body)
	}
	updated := decode[ui.Schedule](t, rec)
	if updated.Enabled {
		t.Error("want the schedule paused after the update")
	}

	rec = do(t, srv, http.MethodDelete, "/api/schedules/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/schedules", "")
	list = decode[[]ui.Schedule](t, rec)
	if len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}
}

// The overlap policy over the wire: absent on the way in reads as
// "skip", never as blank on the way out, and a value this build does not
// know is a 400 rather than something the store carries around.
func TestSchedulesCarryTheirOverlapPolicyOverTheWire(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"nightly bump","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Schedule](t, rec)
	if created.Overlap != "skip" {
		t.Errorf("overlap = %q, want skip when the request said nothing", created.Overlap)
	}

	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, `{"overlap":"allow"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if updated := decode[ui.Schedule](t, rec); updated.Overlap != "allow" {
		t.Errorf("overlap = %q, want allow", updated.Overlap)
	}

	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, `{"overlap":"queue"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d, want 400 for an unknown policy: %s", rec.Code, rec.Body)
	}
}

func TestSchedulesCreateRejectsAMissingTitleWith400(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestSchedulesUpdateOnAnUnknownIDIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPatch, "/api/schedules/nope", `{"enabled":false}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestSchedulesDeleteOnAnUnknownIDIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodDelete, "/api/schedules/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The suite-backed half of the same surface: a schedule that runs a task
// suite goes through the very same four routes, with suiteId in place of
// the title and content fields (ui.CreateScheduleRequest's own doc
// comment on why they are mutually exclusive).
func TestSchedulesCreateFromASuite(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Find bugs","title":"Find and fix a bug"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/suites",
		`{"name":"Bug sweep","templateIds":["`+tmpl.ID+`"],"mode":"until_clean","maxPasses":5}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create suite status = %d, want 201: %s", rec.Code, rec.Body)
	}
	suite := decode[ui.Suite](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/schedules",
		`{"suiteId":"`+suite.ID+`","repo":"acme/widgets","base":"main",`+
			`"recurrence":{"kind":"daily","timeOfDay":"09:00"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Schedule](t, rec)
	if created.SuiteID != suite.ID || created.SuiteName != "Bug sweep" {
		t.Fatalf("created = %+v, want it naming the suite it runs", created)
	}

	// Deleting that suite while the schedule still runs it is a 400, not
	// a silent orphaning (ui.Client.DeleteSuite's own guard).
	rec = do(t, srv, http.MethodDelete, "/api/suites/"+suite.ID, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete-in-use status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// Content sent alongside a templateId is a 400 at the HTTP boundary,
// which is the level a UI actually meets it at -- and the body names the
// fields and the template that decides them, so an error banner is
// enough on its own (ui.refuseIgnoredScheduleContent). The client-level
// rules are schedule_ignored_content_test.go's.
func TestSchedulesRefuseContentAlongsideATemplateOverHTTP(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Dependency bump","title":"Bump dependencies"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/schedules",
		`{"templateId":"`+tmpl.ID+`","repo":"acme/widgets","title":"my own title",`+
			`"recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400: %s", rec.Code, rec.Body)
	}
	for _, want := range []string{"title", "Dependency bump"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body %s does not mention %q", rec.Body, want)
		}
	}

	// The same request without the title is the ordinary one, and still
	// goes through: the refusal is about content, not about templates.
	rec = do(t, srv, http.MethodPost, "/api/schedules",
		`{"templateId":"`+tmpl.ID+`","repo":"acme/widgets",`+
			`"recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Schedule](t, rec)

	// And a PATCH that resends what the overlay resends -- the template
	// it already fires, its repo, a new cadence -- is not content at all.
	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID,
		`{"templateId":"`+tmpl.ID+`","repo":"acme/widgets","base":"",`+
			`"recurrence":{"kind":"daily","timeOfDay":"09:00"},"overlap":"allow"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if updated := decode[ui.Schedule](t, rec); updated.Recurrence.Kind != "daily" {
		t.Errorf("recurrence = %q, want the new cadence taken", updated.Recurrence.Kind)
	}

	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, `{"description":"mine"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// "Run now" over the wire (grain/task-227): a 201 carrying the task it
// filed and the schedule as it now stands, a 409 while that firing is
// still open, and a 404 for a schedule that does not exist.
func TestSchedulesRunNow(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"nightly bump","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Schedule](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/schedules/"+created.ID+"/run", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run status = %d, want 201: %s", rec.Code, rec.Body)
	}
	run := decode[ui.ScheduleRun](t, rec)
	if run.TaskID == "" {
		t.Error("want the filed task's id in the response")
	}
	if run.Schedule.LastRunAt == nil {
		t.Error("want the schedule back with its lastRunAt set")
	}
	if !run.Schedule.NextRunAt.Equal(created.NextRunAt) {
		t.Errorf("nextRunAt = %v, want it left at %v", run.Schedule.NextRunAt, created.NextRunAt)
	}

	// The firing it just made is still open, and this schedule skips
	// rather than overlaps -- a conflict, not a bad request.
	rec = do(t, srv, http.MethodPost, "/api/schedules/"+created.ID+"/run", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second run status = %d, want 409: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodPost, "/api/schedules/nope/run", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown-id run status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The composition endpoints over the wire (grain/task-405): routed,
// answering with the tasks a firing would file, and 404 for an id
// nothing is behind. What each field's value and source should be is
// composition_test.go's, against the Client.
func TestScheduleAndSuiteCompositionOverTheWire(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Bump","title":"Bump the dependencies","autoMerge":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/schedules",
		`{"repo":"acme/widgets","templateId":"`+tmpl.ID+`","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule status = %d, want 201: %s", rec.Code, rec.Body)
	}
	sched := decode[ui.Schedule](t, rec)

	rec = do(t, srv, http.MethodGet, "/api/schedules/"+sched.ID+"/composition", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("schedule composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	composed := decode[ui.Composition](t, rec)
	if composed.Kind != "task" || len(composed.Tasks) != 1 {
		t.Fatalf("composition = %+v, want one task", composed)
	}
	if composed.Tasks[0].Title != "Bump the dependencies" {
		t.Errorf("title = %q, want the template's", composed.Tasks[0].Title)
	}

	rec = do(t, srv, http.MethodPost, "/api/suites",
		`{"name":"Nightly","templateIds":["`+tmpl.ID+`"],"mode":"count","count":2}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create suite status = %d, want 201: %s", rec.Code, rec.Body)
	}
	suite := decode[ui.Suite](t, rec)

	rec = do(t, srv, http.MethodGet,
		"/api/suites/"+suite.ID+"/composition?repo=acme/widgets&base=main", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("suite composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	composed = decode[ui.Composition](t, rec)
	if composed.Kind != "suite" || composed.Repo != "acme/widgets" || composed.Count != 2 {
		t.Fatalf("composition = %+v, want the suite run it describes", composed)
	}

	rec = do(t, srv, http.MethodGet, "/api/schedules/sched-404/composition", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown schedule status = %d, want 404: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodGet, "/api/suites/suite-404/composition", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown suite status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The same two endpoints for something that has not been saved
// (grain/task-426): the body a form would create the row with, composed
// without creating it. What the answer should say is composition_test.go's.
func TestDraftCompositionOverTheWire(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Bump","title":"Bump the dependencies","autoMerge":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)

	// A schedule that does not exist, and never will: no cadence, no id,
	// nothing written.
	rec = do(t, srv, http.MethodPost, "/api/schedules/composition",
		`{"repo":"acme/widgets","base":"main","templateId":"`+tmpl.ID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("draft schedule composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	composed := decode[ui.Composition](t, rec)
	if composed.Kind != "task" || len(composed.Tasks) != 1 {
		t.Fatalf("composition = %+v, want one task", composed)
	}
	if composed.Tasks[0].Title != "Bump the dependencies" || composed.ScheduleID != "" {
		t.Errorf("composition = %+v, want the template's title and no schedule id", composed)
	}

	rec = do(t, srv, http.MethodPost, "/api/suites/composition?repo=acme/widgets&base=main",
		`{"name":"Nightly","templateIds":["`+tmpl.ID+`"],"mode":"count","count":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("draft suite composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	composed = decode[ui.Composition](t, rec)
	if composed.Kind != "suite" || composed.Repo != "acme/widgets" || composed.Count != 2 {
		t.Fatalf("composition = %+v, want the suite run this draft describes", composed)
	}
	if composed.SuiteID != "" {
		t.Errorf("suiteId = %q, want none: this suite has not been saved", composed.SuiteID)
	}

	rec = do(t, srv, http.MethodGet, "/api/schedules", "")
	if list := decode[[]ui.Schedule](t, rec); len(list) != 0 {
		t.Errorf("schedules = %+v, want none: composing a draft writes nothing", list)
	}
	rec = do(t, srv, http.MethodGet, "/api/suites", "")
	if list := decode[[]ui.Suite](t, rec); len(list) != 0 {
		t.Errorf("suites = %+v, want none: composing a draft writes nothing", list)
	}

	// A draft naming something that is not there is a bad request rather
	// than a 404: the id is in the body somebody is typing.
	rec = do(t, srv, http.MethodPost, "/api/schedules/composition",
		`{"repo":"acme/widgets","base":"main","templateId":"template-404"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown template status = %d, want 400: %s", rec.Code, rec.Body)
	}
}
