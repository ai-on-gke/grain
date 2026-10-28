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

// What a run's capability grants did for it (model.CapabilityAuditRecord),
// against a real database: records come back under their own attempt and
// in the order they were written, a second write numbers on rather than
// colliding, and another task's runs are not read along with them.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRunCapabilityAuditRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")
	seedFinishedRun(t, store, ctx, "r9", "c3d4")

	at := now.Add(time.Minute)
	first := []model.CapabilityAuditRecord{
		{Grant: "host-script#host-script", AttemptID: "e1-a1b2-1-0", At: at,
			Record: `{"time":"2026-01-01T00:00:00Z","script":"deploy","argv":["--dry-run","ünïcode \"quoted\""],"exit_code":0}`},
		{Grant: "host-script#host-script", AttemptID: "e1-a1b2-1-0", At: at, Note: "the server left out 3 records"},
	}
	if err := store.AppendRunCapabilityAudit(ctx, "r1", first); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRunCapabilityAudit(ctx, "r1", []model.CapabilityAuditRecord{
		{Grant: "other#m", AttemptID: "e1-a1b2-1-1", At: at, Record: `{"time":"2026-01-01T00:00:01Z"}`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRunCapabilityAudit(ctx, "r9", []model.CapabilityAuditRecord{
		{Grant: "other#m", AttemptID: "e1-c3d4-1-0", At: at, Record: `{"time":"2026-01-01T00:00:02Z"}`},
	}); err != nil {
		t.Fatal(err)
	}

	recs, ok, err := store.RunCapabilityAudit(ctx, "a1b2", 1)
	if err != nil || !ok {
		t.Fatalf("RunCapabilityAudit: %v, found %v", err, ok)
	}
	if len(recs) != 3 {
		t.Fatalf("audit = %+v, want three records", recs)
	}
	counts, err := store.RunCapabilityAuditCounts(ctx, "a1b2")
	if err != nil || len(counts) != 1 || counts[1] != 3 {
		t.Errorf("counts = %v, %v; want attempt 1 to have 3", counts, err)
	}
	if _, ok, err := store.RunCapabilityAudit(ctx, "a1b2", 2); ok || err != nil {
		t.Errorf("an attempt that never ran: found %v, %v", ok, err)
	}
	for i, r := range recs {
		if r.Seq != i+1 || r.RunID != "r1" || !r.At.Equal(at) {
			t.Errorf("record %d = %+v", i, r)
		}
	}
	if recs[0].Record != first[0].Record || recs[0].Note != "" || recs[1].Note != first[1].Note || recs[1].Record != "" {
		t.Errorf("records changed in the round trip: %+v", recs[:2])
	}
	if recs[2].Grant != "other#m" || recs[2].AttemptID != "e1-a1b2-1-1" {
		t.Errorf("second write = %+v", recs[2])
	}
}
