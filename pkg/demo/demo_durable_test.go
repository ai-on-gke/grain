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

package demo

import "testing"

// TestDemoDurableTaskHasEveryContextLine is the demo's durable task as
// the task page's Context panel and brief read it (DT-15): compacted,
// with a twice-edited brief and a stale handover.
func TestDemoDurableTaskHasEveryContextLine(t *testing.T) {
	client, _, ctx, ids := demoCLs(t)
	id, ok := ids[DemoDurableTaskTitle]
	if !ok {
		t.Fatalf("no %q task in the demo", DemoDurableTaskTitle)
	}
	view, err := client.TaskContext(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if view.CompactedThrough == 0 || view.Summarised != 4 {
		t.Errorf("compacted through %d with %d summarised, want 4 summarised", view.CompactedThrough, view.Summarised)
	}
	if view.CurrentBrief == "" || len(view.BriefEdits) != 2 {
		t.Errorf("brief %q with %d edits, want a brief and 2 edits", view.CurrentBrief, len(view.BriefEdits))
	}
	if !view.HandoverStale || view.HandoverVersion != 1 {
		t.Errorf("handover v%d stale=%v (%+v), want v1 stale", view.HandoverVersion, view.HandoverStale, view.HandoverAge)
	}
}
