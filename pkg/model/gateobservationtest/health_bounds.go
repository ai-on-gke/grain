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

package gateobservationtest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

func RunHealthSourceBounds(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	changed := s.Changed()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for i := 0; i < 130; i++ {
		must(s.RecordGateWorkflowSource(ctx, gates.WorkflowHealthRun{ID: int64(i + 1), Attempt: 1, Repository: repo.String(), Ref: "main", CompletedAt: now.Add(time.Duration(i) * time.Second)}, now.Add(time.Duration(i)*time.Second)))
	}
	var n int
	must(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gate_health_workflow_source").Scan(&n))
	if n != 128 {
		t.Fatalf("per-repo source bound: %d", n)
	}
	must(s.RecordGateWorkflowSource(ctx, gates.WorkflowHealthRun{ID: 131, Attempt: 1, Repository: repo.String(), Ref: "main"}, now.Add(131*time.Second)))
	must(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gate_health_workflow_source WHERE run_id = 131").Scan(&n))
	if n != 1 {
		t.Fatal("missing completion time discarded incomplete newest marker")
	}
	oversized := gates.WorkflowHealthRun{ID: 200, Attempt: 1, Repository: repo.String(), CompletedAt: now, Jobs: make([]gates.HealthJob, 513)}
	if s.RecordGateWorkflowSource(ctx, oversized, now) == nil {
		t.Fatal("unbounded job proof")
	}
	for i := 0; i < 258; i++ {
		must(s.RecordGateBaseSource(ctx, repo, fmt.Sprintf("branch-%03d", i), model.GateBaseSource{At: now.Add(time.Duration(i) * time.Second)}))
	}
	must(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gate_health_base_source").Scan(&n))
	if n != 256 {
		t.Fatalf("base source bound: %d", n)
	}
	// A later write prunes old sources instead of presenting absent proof as met.
	later := now.Add(8 * 24 * time.Hour)
	must(s.RecordGateWorkflowSource(ctx, gates.WorkflowHealthRun{ID: 300, Attempt: 1, Repository: repo.String(), Ref: "main", CompletedAt: later}, later))
	must(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gate_health_workflow_source").Scan(&n))
	if n != 1 {
		t.Fatalf("source age bound: %d", n)
	}
	select {
	case <-changed:
		t.Fatal("source pruning signalled legacy Changed")
	default:
	}
}
