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

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/qa"
)

func TestResolveTaskQAAssignment_OffWritesNothingAndEnabledPersistsStickyArm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	now := time.Date(2026, 11, 12, 19, 0, 0, 0, time.UTC)
	task := model.Task{
		ID:         "701",
		Title:      "Add virtual memory page table allocator",
		Body:       "Implement buddy allocator and virtual memory page tables with concurrency tests.",
		WorkOrigin: "task:700",
	}

	// 1. Default off: zero DB rows written.
	offAssign, err := resolveTaskQAAssignment(ctx, store, Config{}, task, "run-1", now)
	if err != nil {
		t.Fatalf("resolveTaskQAAssignment off: %v", err)
	}
	if offAssign.Arm != model.QAArmOff {
		t.Fatalf("off Arm = %q, want %q", offAssign.Arm, model.QAArmOff)
	}
	stored, err := store.QAAssignment(ctx, "task:700", 1)
	if err != nil || stored != nil {
		t.Fatalf("expected nil stored assignment when off, got (%+v, %v)", stored, err)
	}

	// 2. Enabled 100% rollout: persists sticky assignment and appends JSONL profiler sample.
	perfPath := filepath.Join(t.TempDir(), "perf", "qa-checks.jsonl")
	cfgOn := Config{
		QA: model.QAConfig{
			Enabled:    true,
			FlowID:     model.QAFlowGoldenV1Minimal,
			RolloutPct: 100,
			Epoch:      1,
			RegimeID:   1,
		},
		QAProfiler: qa.NewProfiler(perfPath),
	}
	onAssign, err := resolveTaskQAAssignment(ctx, store, cfgOn, task, "run-2", now)
	if err != nil {
		t.Fatalf("resolveTaskQAAssignment on: %v", err)
	}
	if onAssign.Arm != model.QAArmTreatment || onAssign.ComplexityTier != model.QAComplexityHigh {
		t.Fatalf("unexpected assignment: %+v", onAssign)
	}
	storedOn, err := store.QAAssignment(ctx, "task:700", 1)
	if err != nil || storedOn == nil || storedOn.Arm != model.QAArmTreatment {
		t.Fatalf("expected persisted treatment assignment, got (%+v, %v)", storedOn, err)
	}
	perfBytes, err := os.ReadFile(perfPath)
	if err != nil || !strings.Contains(string(perfBytes), `"checkId":"arm-assign"`) {
		t.Fatalf("expected arm-assign JSONL perf sample, got (%q, %v)", string(perfBytes), err)
	}
}

func TestResolveTaskQAAssignment_OriginlessChildInheritsRootTreeArm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	// Pick deterministic root and child IDs whose standalone "task:<id>" buckets
	// land on opposite sides of 30% in epoch 1 so a broken fallback diverges.
	var rootID, childID string
	for i := 1; i < 500; i++ {
		id := strings.TrimSpace(string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10)))
		id = strings.TrimLeft(id, "0")
		if rootID == "" && qa.ComputeArmBucket("task:"+id, 1) < 30 {
			rootID = id
		} else if childID == "" && id != rootID && qa.ComputeArmBucket("task:"+id, 1) >= 30 {
			childID = id
		}
		if rootID != "" && childID != "" {
			break
		}
	}
	if rootID == "" || childID == "" {
		t.Fatalf("failed to pick opposite-bucket task IDs")
	}

	put := func(id string, links ...model.Link) {
		t.Helper()
		tk := model.Task{
			ID:      id,
			Intent:  model.IntentImplement,
			Title:   "task " + id,
			Origin:  model.Origin{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect},
			Binding: model.BindingDirective,
			Links:   links,
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", id, err)
		}
	}
	put(rootID)
	// Stored before its proposed-by link was added so WorkOrigin stays ""
	// (matching pre-origin / updated-link work trees).
	put(childID)
	put(childID, model.Link{Kind: model.LinkProposedBy, Target: rootID})

	rootTask, err := store.GetTask(ctx, rootID)
	if err != nil || rootTask == nil {
		t.Fatalf("GetTask(%s): %v", rootID, err)
	}
	childTask, err := store.GetTask(ctx, childID)
	if err != nil || childTask == nil || childTask.WorkOrigin != "" || childTask.Parent() != rootID {
		t.Fatalf("childTask = %+v (%v), want empty WorkOrigin and parent %s", childTask, err, rootID)
	}

	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	cfgOn := Config{
		QA: model.QAConfig{
			Enabled:    true,
			FlowID:     model.QAFlowGoldenV1Minimal,
			RolloutPct: 30,
			Epoch:      1,
			RegimeID:   1,
		},
	}

	rootAssign, err := resolveTaskQAAssignment(ctx, store, cfgOn, *rootTask, rootID+"-1", now)
	if err != nil {
		t.Fatalf("resolveTaskQAAssignment root: %v", err)
	}
	childAssign, err := resolveTaskQAAssignment(ctx, store, cfgOn, *childTask, childID+"-1", now)
	if err != nil {
		t.Fatalf("resolveTaskQAAssignment child: %v", err)
	}
	if rootAssign.RootOrigin != "task:"+rootID || childAssign.RootOrigin != "task:"+rootID {
		t.Fatalf("origins = (%q, %q), want both %q", rootAssign.RootOrigin, childAssign.RootOrigin, "task:"+rootID)
	}
	if rootAssign.Arm != model.QAArmTreatment || childAssign.Arm != rootAssign.Arm {
		t.Fatalf("arms = (root=%q, child=%q), want both %q", rootAssign.Arm, childAssign.Arm, model.QAArmTreatment)
	}
}
