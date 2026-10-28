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

// grain/task-127: what a bulk edit is, from the API's side.
//
// The UI's bulk edit form (BulkEditOverlay.jsx) is the only write in the
// list-working surface that touches many rows at once, and there is no
// endpoint for it: it is one PATCH /api/tasks/{id} per selected task,
// fired in parallel, each carrying the same body of ticked fields. That
// makes "some of them were rejected" an ordinary outcome rather than an
// exotic one -- the fields it sets are checked per task, so the same
// body can be a legal edit of four rows and an illegal edit of the
// fifth -- and it makes what the server does with the fifth the thing
// worth pinning: a rejected row has to be refused *whole*, so an
// operator retrying it is retrying a task that never moved, and the
// other four have to be untouched by its refusal.
//
// App.jsx's actBatch is the other half of this (Promise.allSettled, an
// error naming how many of how many failed, the selection kept so the
// rows can be retried); App.test.jsx's own bulk-edit case covers what
// the form reports.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// bulkEdit is the form's own apply: the same body PATCHed to every
// selected task, in the order they were selected, with nothing aborting
// on the first refusal -- so the answers can be read one per task the
// way actBatch reads its settled promises.
func bulkEdit(t *testing.T, srv *ui.Server, ids []string, body string) []*httptest.ResponseRecorder {
	t.Helper()
	recs := make([]*httptest.ResponseRecorder, 0, len(ids))
	for _, id := range ids {
		recs = append(recs, do(t, srv, http.MethodPatch, "/api/tasks/"+id, body))
	}
	return recs
}

func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the error response %q: %v", rec.Body.String(), err)
	}
	message, ok := body["error"]
	if !ok {
		t.Fatalf("the error response %q has no \"error\" key", rec.Body.String())
	}
	return message
}

func taskByID(t *testing.T, srv *ui.Server, id string) ui.Task {
	t.Helper()
	rec := do(t, srv, http.MethodGet, "/api/tasks/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks/%s = %d, want 200: %s", id, rec.Code, rec.Body)
	}
	return decode[ui.TaskDetail](t, rec).Task
}

// TestABulkEditAppliesToTheRowsItCanAndRefusesTheRestWhole is the
// partial failure itself: three tasks moved onto one repo at once, one
// of which already lists that repo among the other repos its work may
// land in -- the contradiction refuseTargetAmongWrites exists to catch,
// and one that depends on the task rather than on the request, which is
// what makes the same body land twice and be refused once.
func TestABulkEditAppliesToTheRowsItCanAndRefusesTheRestWhole(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	newTask := func(body string) string {
		t.Helper()
		rec := do(t, srv, http.MethodPost, "/api/tasks", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("creating a task = %d, want 201: %s", rec.Code, rec.Body)
		}
		return decode[ui.Task](t, rec).ID
	}
	first := newTask(`{"title":"fix the build","repo":"acme/widgets"}`)
	second := newTask(`{"title":"fix the docs","repo":"acme/widgets"}`)
	// The odd one out: its work may also land in acme/gadgets on its default branch, which is
	// where this edit is about to move it.
	third := newTask(`{"title":"fix the docs there too","repo":"acme/widgets","writes":[{"repo":"acme/gadgets"}]}`)

	ids := []string{first, second, third}
	recs := bulkEdit(t, srv, ids, `{"repo":"acme/gadgets","autoMerge":true}`)

	for i, id := range []string{first, second} {
		if recs[i].Code != http.StatusOK {
			t.Fatalf("PATCH on %s = %d, want 200: %s", id, recs[i].Code, recs[i].Body)
		}
		task := decode[ui.Task](t, recs[i])
		if task.Repo != "acme/gadgets" || !task.AutoMerge {
			t.Fatalf("task %s after the edit = %+v, want both ticked fields changed", id, task)
		}
	}

	// The refusal: a 400 rather than a 500, since it is a request the
	// store was never asked to make, and a sentence naming the repo --
	// which is the whole of what the form has to report with, since
	// nothing else tells an operator which of the selected rows this was
	// about.
	refused := recs[2]
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("PATCH on %s = %d, want 400: %s", third, refused.Code, refused.Body)
	}
	if message := errorMessage(t, refused); !strings.Contains(message, "acme/gadgets") {
		t.Fatalf("the refusal says %q, want it to name the repo it is about", message)
	}

	// Refused whole. The request carried two fields and the check that
	// failed is made after both have been applied to the copy being
	// edited, so a row that kept its auto-merge tick and lost its repo --
	// or the other way round -- would be a task in a state nobody asked
	// for, on the one row an operator has been told to look at.
	after := taskByID(t, srv, third)
	if after.Repo != "acme/widgets" {
		t.Fatalf("the refused task's repo = %q, want it left at acme/widgets", after.Repo)
	}
	if after.AutoMerge {
		t.Fatal("the refused task came out with auto-merge on: the other field of a refused edit landed")
	}
	if len(after.Writes) != 1 || after.Writes[0].Repo != "acme/gadgets" {
		t.Fatalf("the refused task's writes = %+v, want the one it was filed with", after.Writes)
	}

	// And the refusal reached nothing else: the two that landed are
	// still as they landed, read back off the list the form refreshes
	// from rather than out of the PATCH responses.
	rec := do(t, srv, http.MethodGet, "/api/tasks", "")
	listed := decode[[]ui.Task](t, rec)
	if len(listed) != len(ids) {
		t.Fatalf("listed %d tasks, want %d", len(listed), len(ids))
	}
	edited := 0
	for _, task := range listed {
		if task.Repo == "acme/gadgets" && task.AutoMerge {
			edited++
		}
	}
	if edited != 2 {
		t.Fatalf("%d of %d tasks came out edited, want 2", edited, len(listed))
	}
}

// TestABulkEditOnAStaleSelectionIs404ForThatRowAlone is the other way
// one row of a selection fails: the list behind the form is re-fetched
// every few seconds and a selection outlives it, so an id can name a
// task that is no longer there by the time Apply is pressed. That row is
// a 404 -- told apart from a refusal on purpose, since one is worth
// retrying and the other is not -- and the rest of the batch is
// unaffected.
func TestABulkEditOnAStaleSelectionIs404ForThatRowAlone(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix the build","repo":"acme/widgets"}`)
	id := decode[ui.Task](t, rec).ID

	recs := bulkEdit(t, srv, []string{id, "does-not-exist"}, `{"base":"release/2"}`)
	if recs[0].Code != http.StatusOK {
		t.Fatalf("PATCH on %s = %d, want 200: %s", id, recs[0].Code, recs[0].Body)
	}
	if got := decode[ui.Task](t, recs[0]).Base; got != "release/2" {
		t.Fatalf("base after the edit = %q, want release/2", got)
	}
	if recs[1].Code != http.StatusNotFound {
		t.Fatalf("PATCH on a task that is gone = %d, want 404: %s", recs[1].Code, recs[1].Body)
	}
	if message := errorMessage(t, recs[1]); !strings.Contains(message, "does-not-exist") {
		t.Fatalf("the 404 says %q, want it to name the task it could not find", message)
	}
}

// TestEveryFieldTheBulkEditFormSendsIsOneTheAPITakes walks the body the
// form builds, field by field, against a selection of two. It is the
// same agreement list_vocabulary_test.go keeps for the filter
// vocabulary, on the write side: the form names six fields of
// UpdateTaskRequest and sends only the ticked ones, so a field renamed
// on one side and not the other is a tick that silently does nothing --
// a PATCH the server accepts, with a body it ignores, reported to the
// operator as done.
func TestEveryFieldTheBulkEditFormSendsIsOneTheAPITakes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		// filed is what each of the two tasks is created with, so the
		// edit below is a change rather than a restatement -- clearing a
		// field nothing had set would pass whatever the server did with
		// the request.
		filed string
		// body is the PATCH the form builds for this field, and
		// needsTemplate asks for a review template first, %s in `body`
		// being its id.
		body          string
		needsTemplate bool
		want          func(ui.Task) bool
	}{
		{field: "repo", filed: `"repo":"acme/widgets"`, body: `{"repo":"acme/gadgets"}`,
			want: func(task ui.Task) bool { return task.Repo == "acme/gadgets" }},
		{field: "base", filed: `"base":"main"`, body: `{"base":"release/2"}`,
			want: func(task ui.Task) bool { return task.Base == "release/2" }},
		{field: "autoMerge", filed: `"autoMerge":false`, body: `{"autoMerge":true}`,
			want: func(task ui.Task) bool { return task.AutoMerge }},
		{field: "agentFramework", filed: `"agentFramework":"codex"`, body: `{"agentFramework":"claude"}`,
			want: func(task ui.Task) bool { return task.AgentFramework == "claude" }},
		{field: "reviewTemplateId", filed: `"repo":"acme/widgets"`, body: `{"reviewTemplateId":"%s"}`, needsTemplate: true,
			want: func(task ui.Task) bool { return task.ReviewTemplateName == "Careful review" }},
		// Clearing an override across a selection: an empty string is a
		// real edit here rather than a field nobody meant to change,
		// which is the whole reason the form ticks fields instead of
		// inferring intent from emptiness (BulkEditOverlay's own doc
		// comment).
		{field: "promptExtension", filed: `"promptExtension":"be careful"`, body: `{"promptExtension":""}`,
			want: func(task ui.Task) bool { return task.PromptExtension == "" }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			srv, _ := testServer(t)
			body := tc.body
			if tc.needsTemplate {
				rec := do(t, srv, http.MethodPost, "/api/templates",
					`{"name":"Careful review","title":"Review it"}`)
				if rec.Code != http.StatusCreated {
					t.Fatalf("creating a review template = %d, want 201: %s", rec.Code, rec.Body)
				}
				body = fmt.Sprintf(tc.body, decode[ui.Template](t, rec).ID)
			}
			var ids []string
			for i := 0; i < 2; i++ {
				rec := do(t, srv, http.MethodPost, "/api/tasks",
					fmt.Sprintf(`{"title":"fix it %d",%s}`, i, tc.filed))
				if rec.Code != http.StatusCreated {
					t.Fatalf("creating a task = %d, want 201: %s", rec.Code, rec.Body)
				}
				ids = append(ids, decode[ui.Task](t, rec).ID)
			}
			for i, rec := range bulkEdit(t, srv, ids, body) {
				if rec.Code != http.StatusOK {
					t.Fatalf("PATCH %s on %s = %d, want 200: %s", body, ids[i], rec.Code, rec.Body)
				}
				if !tc.want(taskByID(t, srv, ids[i])) {
					t.Fatalf("PATCH %s on %s came back 200 and changed nothing", body, ids[i])
				}
			}
		})
	}
}

// TestABulkEditOfOneDeclaredFieldLeavesTheOthersStanding is the
// agreement behind the form's Fields rows (grain/task-273): a bulk edit
// of one declared field has to be a body that names the *rest* of them
// too, per task, or it is a batch that quietly empties the fields nobody
// ticked.
//
// PATCH takes `customFields` as the task's whole answer about the fields
// this deployment declares -- one it does not name is one the task no
// longer has a value for (mergeCustomFields) -- which is right for the
// task pane, where every declared field is in front of the person
// pressing save, and a trap for a form where only one of them is ticked.
// So BulkEditOverlay builds a body per selected task: the ticked field
// as the form says, and each other declared field restated with what
// that task already holds. Both halves are asserted here, the second by
// sending the naive body against the same fixture and watching it
// clear -- if that ever stops being true this test says so rather than
// leaving the form carrying values it no longer needs to.
func TestABulkEditOfOneDeclaredFieldLeavesTheOthersStanding(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	declareFields(t, store, ctx,
		model.CustomField{Name: "priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}},
		model.CustomField{Name: "customer"},
		model.CustomField{Name: "teams", Multiple: true},
	)

	// Two tasks that agree on nothing but their customer -- the mixed
	// selection the form is written for, where "these are all critical"
	// is the one thing being said and everything else has to survive it.
	newTask := func(body string) string {
		t.Helper()
		rec := do(t, srv, http.MethodPost, "/api/tasks", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("creating a task = %d, want 201: %s", rec.Code, rec.Body)
		}
		return decode[ui.Task](t, rec).ID
	}
	first := newTask(`{"title":"fix the build","repo":"acme/widgets",` +
		`"customFields":{"priority":"low","customer":"globex","teams":["ui","infra"]}}`)
	second := newTask(`{"title":"fix the docs","repo":"acme/widgets",` +
		`"customFields":{"customer":"globex"}}`)

	// The body the form builds, one per task: `priority` is the ticked
	// row, and what follows it on each line is that task's own.
	recs := []*httptest.ResponseRecorder{
		do(t, srv, http.MethodPatch, "/api/tasks/"+first,
			`{"customFields":{"priority":"high","customer":"globex","teams":["ui","infra"]}}`),
		do(t, srv, http.MethodPatch, "/api/tasks/"+second,
			`{"customFields":{"priority":"high","customer":"globex"}}`),
	}
	for i, id := range []string{first, second} {
		if recs[i].Code != http.StatusOK {
			t.Fatalf("PATCH on %s = %d, want 200: %s", id, recs[i].Code, recs[i].Body)
		}
	}

	after := taskByID(t, srv, first)
	if !slices.Equal(after.CustomFields["priority"], []string{"high"}) {
		t.Errorf("priority after the edit = %v, want the ticked value", after.CustomFields)
	}
	if !slices.Equal(after.CustomFields["customer"], []string{"globex"}) {
		t.Errorf("customer after the edit = %v, want it left alone", after.CustomFields)
	}
	// Sorted, as a set is on the way in (model.CustomField.NormalizeValues).
	if !slices.Equal(after.CustomFields["teams"], []string{"infra", "ui"}) {
		t.Errorf("teams after the edit = %v, want the set left alone", after.CustomFields)
	}
	if second := taskByID(t, srv, second); !slices.Equal(second.CustomFields["customer"], []string{"globex"}) {
		t.Errorf("the second task's customer = %v, want it left alone", second.CustomFields)
	}

	// And the naive body, which is what the form would send if it ever
	// stopped restating: the same edit, saying only the ticked field.
	rec := do(t, srv, http.MethodPatch, "/api/tasks/"+first, `{"customFields":{"priority":"low"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH on %s = %d, want 200: %s", first, rec.Code, rec.Body)
	}
	naive := taskByID(t, srv, first)
	if len(naive.CustomFields) != 1 || !slices.Equal(naive.CustomFields["priority"], []string{"low"}) {
		t.Fatalf("after a body naming one field the task says %v, want only that field: "+
			"the unstated ones are meant to be cleared, which is why the form restates them",
			naive.CustomFields)
	}
}
