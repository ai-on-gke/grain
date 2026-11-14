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
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A recorded blame is on both tasks' JSON, as blames and blamedBy, which
// is what a report counting change failures reads.
func TestTaskJSONCarriesBlamesAndBlamedBy(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	culprit, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Change the widget", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	repair, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Fix grain CI on main", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordBlame(ctx, repair.ID, culprit.ID); err != nil {
		t.Fatal(err)
	}
	gotRepair, err := client.Task(ctx, repair.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotRepair.Blames, []string{culprit.ID}) {
		t.Errorf("repair.Blames = %v, want [%s]", gotRepair.Blames, culprit.ID)
	}
	gotCulprit, err := client.Task(ctx, culprit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotCulprit.BlamedBy, []string{repair.ID}) {
		t.Errorf("culprit.BlamedBy = %v, want [%s]", gotCulprit.BlamedBy, repair.ID)
	}
	raw, err := json.Marshal(gotCulprit)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"blamedBy":["` + repair.ID + `"]`; !strings.Contains(string(raw), want) {
		t.Errorf("culprit JSON has no %s: %s", want, raw)
	}
	stored, err := store.GetTask(ctx, culprit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := model.TaskLinkTargets(*stored, model.LinkBlamedBy); !slices.Equal(got, []string{repair.ID}) {
		t.Errorf("stored blamed-by links = %v", got)
	}
}
