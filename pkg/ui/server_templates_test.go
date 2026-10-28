package ui_test

// The /api/templates HTTP surface (bwsalmon/agents#516) --
// server_schedules_test.go's own doc comment on why this only asserts
// status codes and JSON shape, leaving Client's own behaviour to
// templates_test.go.

import (
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestTemplatesCreateListUpdateDelete(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Dependency bump","title":"Bump dependencies"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Template](t, rec)
	if created.ID == "" {
		t.Fatal("created template came back with no id")
	}

	rec = do(t, srv, http.MethodGet, "/api/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	list := ownTemplates(decode[[]ui.Template](t, rec))
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v, want just %s", list, created.ID)
	}

	rec = do(t, srv, http.MethodPatch, "/api/templates/"+created.ID, `{"title":"Bump dependencies (patch only)"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", rec.Code, rec.Body)
	}
	updated := decode[ui.Template](t, rec)
	if updated.Title != "Bump dependencies (patch only)" {
		t.Errorf("title = %q, want it updated", updated.Title)
	}

	rec = do(t, srv, http.MethodDelete, "/api/templates/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204: %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/templates", "")
	list = ownTemplates(decode[[]ui.Template](t, rec))
	if len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}
}

func TestTemplatesCreateRejectsAMissingNameWith400(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/templates", `{"title":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestTemplatesUpdateOnAnUnknownIDIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPatch, "/api/templates/nope", `{"title":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestTemplatesDeleteOnAnUnknownIDIs404(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodDelete, "/api/templates/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// TestScheduleCreatedFromATemplateOverHTTP is a thin end-to-end check
// that the /api/schedules and /api/templates surfaces compose: a
// schedule created over HTTP naming templateId comes back carrying that
// template's content, the same as ui.Client.CreateSchedule already
// checks directly (schedules_test.go). Repo is never among that content
// (model.Template's own doc comment on why a template carries no target
// of its own), so the schedule request supplies it directly.
func TestScheduleCreatedFromATemplateOverHTTP(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Dependency bump","title":"Bump dependencies"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/schedules",
		`{"templateId":"`+tmpl.ID+`","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule status = %d, want 201: %s", rec.Code, rec.Body)
	}
	sched := decode[ui.Schedule](t, rec)
	if sched.TemplateID != tmpl.ID {
		t.Errorf("templateId = %q, want %q", sched.TemplateID, tmpl.ID)
	}
	if sched.Title != "Bump dependencies" {
		t.Errorf("title = %q, want the template's own", sched.Title)
	}
	if sched.Repo != "acme/widgets" {
		t.Errorf("repo = %q, want the schedule request's own", sched.Repo)
	}
}

// TestTemplateBindingOverHTTP checks the binding survives the JSON
// surface in both directions (grain/task-285): repo/base go in on
// create, come back on read, and an empty repo unbinds through PATCH.
func TestTemplateBindingOverHTTP(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Dependency bump","title":"Bump dependencies","repo":"acme/widgets","base":"release"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Template](t, rec)
	if created.Repo != "acme/widgets" || created.Base != "release" {
		t.Fatalf("binding = (%q, %q), want acme/widgets on release", created.Repo, created.Base)
	}

	rec = do(t, srv, http.MethodPatch, "/api/templates/"+created.ID, `{"repo":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unbind status = %d, want 200: %s", rec.Code, rec.Body)
	}
	unbound := decode[ui.Template](t, rec)
	if unbound.Repo != "" || unbound.Base != "" {
		t.Fatalf("binding = (%q, %q), want it cleared", unbound.Repo, unbound.Base)
	}
}

// The two halves of a template's input fields over HTTP (grain/task-58):
// the declaration going in and coming back, and the composition endpoint
// a form asks as somebody fills those fields in -- one `field.<name>`
// query parameter per value, so a value with a comma in it needs nothing
// but ordinary escaping.
func TestTemplateInputFieldsOverHTTP(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Deploy","title":"Deploy {{service}} to {{environment}}","fields":[`+
			`{"name":"service","label":"Service","required":true},`+
			`{"name":"environment","options":["staging","production"],"default":"staging"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Template](t, rec)
	if len(created.Fields) != 2 || created.Fields[0].Name != "service" {
		t.Fatalf("fields = %+v, want both, in order", created.Fields)
	}

	rec = do(t, srv, http.MethodGet,
		"/api/templates/"+created.ID+"/composition?repo=acme%2Fwidgets&field.service=payments%2C+again", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("composition status = %d, want 200: %s", rec.Code, rec.Body)
	}
	comp := decode[ui.Composition](t, rec)
	if len(comp.Tasks) != 1 || comp.Tasks[0].Title != "Deploy payments, again to staging" {
		t.Fatalf("composition = %+v, want the values filled in", comp.Tasks)
	}

	// A value outside a selector's options is a 400 and not a task
	// filed with a choice the picker could not have produced.
	rec = do(t, srv, http.MethodGet,
		"/api/templates/"+created.ID+"/composition?field.service=x&field.environment=laptop", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad value status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// Filing from a template over HTTP: POST /api/tasks with nothing but the
// template, the values for its input fields and the choices the form
// itself makes.
func TestCreateTaskFromATemplateOverHTTP(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Deploy","title":"Deploy {{service}}","description":"Deploy {{service}} now.",`+
			`"fields":[{"name":"service"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Template](t, rec)

	rec = do(t, srv, http.MethodPost, "/api/tasks",
		`{"templateId":"`+created.ID+`","fields":{"service":"payments"},"approved":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task status = %d, want 201: %s", rec.Code, rec.Body)
	}
	task := decode[ui.Task](t, rec)
	if task.Title != "Deploy payments" {
		t.Errorf("title = %q, want the template's, filled in", task.Title)
	}
	if task.Description != "Deploy payments now." {
		t.Errorf("description = %q, want the template's, filled in", task.Description)
	}
}
