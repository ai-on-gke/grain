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

package grainclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The payloads below are verbatim from a live `grain demo` (the §7
// walkthrough of docs/test-plans/2026-11-07.md), so these tests pin the
// shapes grain actually speaks rather than this package's own fakes.

const liveTaskJSON = `{"id":"22","title":"PLC-7 implement billing","description":"Implement the billing spec.",` +
	`"state":"proposed","repo":"acme/widgets","externalRef":"PLC-7",` +
	`"designs":[".grain/context/planner/plc-042","acme/specs:plc/042@2879df476108b048b551d73be67151bac7671af2"],` +
	`"context":[{"repo":"acme/widgets","path":".grain/context/planner/plc-042","placement":"target"}],"filedVia":"plc-studio"}`

func TestLiveGrainTaskDecodes(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(liveTaskJSON), &task); err != nil {
		t.Fatalf("decoding a live grain task: %v", err)
	}
	if task.Body != "Implement the billing spec." {
		t.Errorf("Body = %q, want grain's description", task.Body)
	}
	want := DesignRefs{
		{Path: ".grain/context/planner/plc-042"},
		{Repo: "acme/specs", Path: "plc/042", Rev: "2879df476108b048b551d73be67151bac7671af2"},
	}
	if len(task.Designs) != 2 || task.Designs[0] != want[0] || task.Designs[1] != want[1] {
		t.Errorf("Designs = %+v, want %+v", task.Designs, want)
	}
}

func TestCreateTaskRequestSpeaksGrainsFields(t *testing.T) {
	data, err := json.Marshal(CreateTaskRequest{
		Title: "t", Body: "the body", Repo: "acme/app", ExternalRef: "PLC-7",
		Designs: DesignRefs{{Repo: "acme/specs", Path: "architecture.md", Rev: "main"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["description"] != "the body" {
		t.Errorf("description = %v, want the body (grain ignores body): %s", got["description"], data)
	}
	designs, _ := got["designs"].([]any)
	if len(designs) != 1 || designs[0] != "acme/specs:architecture.md@main" {
		t.Errorf("designs = %v, want grain's string form: %s", got["designs"], data)
	}
	patch, _ := json.Marshal(TaskPatch{Body: ptr("new")})
	if !strings.Contains(string(patch), `"description":"new"`) {
		t.Errorf("TaskPatch = %s, want description", patch)
	}
}

func ptr[T any](v T) *T { return &v }

func TestLiveRefSummaryDerivesCounts(t *testing.T) {
	const live = `{"externalRef":"PROJ-42","total":4,"byState":{"proposed":1,"queued":1,"awaiting_reply":1,"completed":1},` +
		`"checks":{"passed":0,"total":0},"openQuestions":[{"taskId":"3","summary":"Which?"}],` +
		`"pullRequests":{"opened":2,"merged":1,"open":1}}`
	var s RefSummary
	if err := json.Unmarshal([]byte(live), &s); err != nil {
		t.Fatal(err)
	}
	want := RefRollupCounts{Total: 4, Active: 2, Blocked: 1, Done: 1, Merged: 1}
	if s.Counts != want {
		t.Errorf("Counts = %+v, want %+v", s.Counts, want)
	}
	if len(s.OpenQuestions) != 1 || s.OpenQuestions[0].TaskID != "3" {
		t.Errorf("OpenQuestions = %+v", s.OpenQuestions)
	}
	// The package's own {counts} shape still reads as written.
	var own RefSummary
	_ = json.Unmarshal([]byte(`{"externalRef":"X","counts":{"total":3,"active":3}}`), &own)
	if own.Counts.Total != 3 || own.Counts.Active != 3 {
		t.Errorf("own shape Counts = %+v", own.Counts)
	}
}

func TestLiveEventStreamDecodesAndFilters(t *testing.T) {
	const stream = "id: 1\nevent: task.state\n" +
		`data: {"id":"1","type":"task.state","at":"2026-11-08T01:40:15Z","taskId":"1","repo":"acme/widgets","state":"queued"}` + "\n\n" +
		"id: 2\nevent: task.state\n" +
		`data: {"id":"2","type":"task.state","at":"2026-11-08T01:40:16Z","taskId":"22","externalRef":"PLC-7",` +
		`"previousState":"proposed","state":"queued","pullRequest":"acme/widgets#9"}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()
	inst := New(Config{GrainURL: srv.URL}).ForURL(srv.URL)
	var got []IntegrationEvent
	err := inst.StreamEvents(context.Background(), EventsFilter{ExternalRef: "PLC-7"}, func(ev IntegrationEvent) error {
		got = append(got, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamEvents over grain's live shape: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want only PLC-7's: %+v", len(got), got)
	}
	ev := got[0]
	if ev.ID != 2 || ev.TaskID != "22" || ev.PrevState != "proposed" || ev.PRURL != "acme/widgets#9" || ev.Time.IsZero() {
		t.Errorf("event = %+v", ev)
	}
}
