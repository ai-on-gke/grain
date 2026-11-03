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

package sqlite_test

// Which hostd a task is pinned to (model.SetTaskHostd), which one each
// run went to (model.TaskRunHostd), and the hostd a run's capability
// audit records carry, against a real database.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestTaskHostdPinRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if got, err := store.TaskHostd(ctx, "a1b2"); err != nil || got != "" {
		t.Fatalf("TaskHostd before a pin = %q, %v; want the default", got, err)
	}
	if err := store.SetTaskHostd(ctx, "a1b2", " zone=b , gpu=a100 "); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.TaskHostd(ctx, "a1b2"); got != "gpu=a100,zone=b" {
		t.Errorf("TaskHostd = %q, want the labels sorted", got)
	}
	if err := store.SetTaskHostd(ctx, "a1b2", "laptop"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.TaskHostd(ctx, "a1b2"); got != "laptop" {
		t.Errorf("TaskHostd = %q, want laptop", got)
	}
	if err := store.SetTaskHostd(ctx, "a1b2", "Not A Name"); err == nil {
		t.Error("SetTaskHostd accepted a name that is not a hostd name")
	}
	if err := store.SetTaskHostd(ctx, "a1b2", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.TaskHostd(ctx, "a1b2"); got != "" {
		t.Errorf("TaskHostd after clearing = %q, want the default", got)
	}
}

func TestTaskRunHostdAndCapabilityAuditRoundTrip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")
	seedFinishedRun(t, store, ctx, "r9", "c3d4")

	at := now.Add(time.Minute)
	if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{
		RunID: "r1", TaskID: "a1b2", Hostd: "laptop", Instance: "first.example.com", Owner: "alice@example.com", At: at,
	}); err != nil {
		t.Fatal(err)
	}
	// A second resolution of the same run replaces the first.
	if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{
		RunID: "r1", TaskID: "a1b2", Hostd: "laptop", Instance: "laptop.example.com", Owner: "alice@example.com", At: at,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.RunHostd(ctx, "r1")
	if err != nil || got == nil {
		t.Fatalf("RunHostd = %+v, %v", got, err)
	}
	if got.Hostd != "laptop" || got.Instance != "laptop.example.com" || got.Owner != "alice@example.com" || !got.At.Equal(at) {
		t.Errorf("RunHostd = %+v", got)
	}
	if none, err := store.RunHostd(ctx, "r9"); err != nil || none != nil {
		t.Errorf("RunHostd(r9) = %+v, %v; want none", none, err)
	}
	byRun, err := store.TaskRunHostds(ctx, "a1b2")
	if err != nil || len(byRun) != 1 || byRun["r1"].Instance != "laptop.example.com" {
		t.Errorf("TaskRunHostds = %+v, %v", byRun, err)
	}

	// Each record carries the hostd its capability server ran through,
	// as the orchestrator says it (orchestrator.Deps.CapabilityHostd).
	hostdOf := map[string]string{"r1": "laptop", "r9": ""}
	for _, run := range []string{"r1", "r9"} {
		if err := store.AppendRunCapabilityAudit(ctx, run, []model.CapabilityAuditRecord{
			{Grant: "host-script#host-script", AttemptID: "e1-" + run, At: at, Record: `{"time":"2026-01-01T00:00:00Z"}`, Hostd: hostdOf[run]},
		}); err != nil {
			t.Fatal(err)
		}
	}
	recs, _, err := store.RunCapabilityAudit(ctx, "a1b2", 1)
	if err != nil || len(recs) != 1 || recs[0].Hostd != "laptop" {
		t.Errorf("a1b2's audit = %+v, %v; want its record on hostd laptop", recs, err)
	}
	recs, _, err = store.RunCapabilityAudit(ctx, "c3d4", 1)
	if err != nil || len(recs) != 1 || recs[0].Hostd != "" {
		t.Errorf("c3d4's audit = %+v, %v; want its record on no hostd", recs, err)
	}
}
