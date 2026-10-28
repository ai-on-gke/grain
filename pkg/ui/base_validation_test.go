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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// A base branch the orchestrator could never check out is refused with a
// 400 on every path that writes one -- task, schedule and template, on
// create and on patch -- rather than accepted and left to fail at
// dispatch (grain/task-1433).
func TestUnusableBaseBranchIsRefusedOnEveryWritePath(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	refused := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "not a usable branch name") {
			t.Fatalf("body = %s, want it to say the base is not a usable branch name", rec.Body)
		}
	}

	t.Run("create task", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/widgets","base":"no such branch ..lock"}`)
		refused(t, rec)
	})

	t.Run("create task with a bad write-target base", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/widgets","writes":[{"repo":"acme/widgets","base":"x y"}]}`)
		refused(t, rec)
	})

	t.Run("patch task", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"t","description":"d","repo":"acme/widgets","base":"release/1.2"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
		}
		task := decode[ui.Task](t, rec)
		rec = do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"base":"also bad ~^:"}`)
		refused(t, rec)
		rec = do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"base":"main"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch to a usable base: status = %d, want 200: %s", rec.Code, rec.Body)
		}
		rec = do(t, srv, http.MethodPatch, "/api/tasks/"+task.ID, `{"base":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch to the default branch: status = %d, want 200: %s", rec.Code, rec.Body)
		}
	})

	t.Run("create schedule", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/schedules",
			`{"title":"nightly","repo":"acme/widgets","base":"bad ref ..x","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
		refused(t, rec)
	})

	t.Run("patch schedule", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/schedules",
			`{"title":"nightly","repo":"acme/widgets","base":"main","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
		}
		sched := decode[ui.Schedule](t, rec)
		rec = do(t, srv, http.MethodPatch, "/api/schedules/"+sched.ID, `{"base":"feature//x"}`)
		refused(t, rec)
	})

	t.Run("create template", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/templates",
			`{"name":"n","title":"t","repo":"acme/widgets","base":"x y"}`)
		refused(t, rec)
	})

	t.Run("patch template", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/templates",
			`{"name":"n2","title":"t","repo":"acme/widgets","base":"main"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
		}
		tmpl := decode[ui.Template](t, rec)
		rec = do(t, srv, http.MethodPatch, "/api/templates/"+tmpl.ID, `{"base":"trailing/"}`)
		refused(t, rec)
	})
}
