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

package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestValidateTuningParameter(t *testing.T) {
	valid := []string{
		"max_workers",
		"max-workers",
		"max_ci_jobs",
		"max-ci-jobs",
		"max_merges_per_repo",
		"sandbox_cpus",
		"sandbox_memory_mb",
		"sandbox_disk_gb",
		"sandbox_cpu_overcommit",
		"package_cache_size_gb",
		"ci_run_history",
		"poll_interval_ms",
	}
	for _, p := range valid {
		if err := model.ValidateTuningParameter(p); err != nil {
			t.Errorf("ValidateTuningParameter(%q) = %v, want nil", p, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"unknown_parameter",
		"quantum_cores",
		"arbitrary_flag",
		"merge_batch_size",
		"merge_gather_window_ms",
	}
	for _, p := range invalid {
		if err := model.ValidateTuningParameter(p); err == nil {
			t.Errorf("ValidateTuningParameter(%q) succeeded, want error", p)
		}
	}
}

func TestTuningRecommendationStoreOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cooldown := now.Add(48 * time.Hour)

	rec := model.TuningRecommendation{
		TargetParameter: "max_merges_per_repo",
		ProposedValue:   "4",
		CooldownExpiry:  cooldown,
		Adopted:         false,
		TaskID:          "task-123",
		CreatedAt:       now,
	}

	id, err := st.RecordTuningRecommendation(ctx, rec)
	if err != nil {
		t.Fatalf("RecordTuningRecommendation failed: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// Retrieve by ID
	fetched, err := st.GetTuningRecommendation(ctx, id)
	if err != nil {
		t.Fatalf("GetTuningRecommendation failed: %v", err)
	}
	if fetched == nil {
		t.Fatalf("expected recommendation %d to exist", id)
	}
	if fetched.TargetParameter != "max_merges_per_repo" || fetched.ProposedValue != "4" {
		t.Errorf("mismatched recommendation: %+v", fetched)
	}
	if fetched.Adopted {
		t.Errorf("expected adopted=false, got true")
	}

	// Check cooldown: at now + 24h, should still be in cooldown
	inCooldown, latest, err := st.IsTuningParameterInCooldown(ctx, "max_merges_per_repo", now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("IsTuningParameterInCooldown failed: %v", err)
	}
	if !inCooldown || latest == nil {
		t.Errorf("expected inCooldown=true, got %v (latest: %+v)", inCooldown, latest)
	}

	// At now + 50h, should no longer be in cooldown
	inCooldownAfter, _, err := st.IsTuningParameterInCooldown(ctx, "max_merges_per_repo", now.Add(50*time.Hour))
	if err != nil {
		t.Fatalf("IsTuningParameterInCooldown failed: %v", err)
	}
	if inCooldownAfter {
		t.Errorf("expected inCooldownAfter=false, got true")
	}

	// Update adopted status
	if err := st.UpdateTuningRecommendationAdopted(ctx, id, true); err != nil {
		t.Fatalf("UpdateTuningRecommendationAdopted failed: %v", err)
	}
	updated, err := st.GetTuningRecommendation(ctx, id)
	if err != nil {
		t.Fatalf("GetTuningRecommendation after update failed: %v", err)
	}
	if !updated.Adopted {
		t.Errorf("expected adopted=true after update, got false")
	}

	// List recommendations
	list, err := st.ListTuningRecommendations(ctx, "max_merges_per_repo", 10)
	if err != nil {
		t.Fatalf("ListTuningRecommendations failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListTuningRecommendations length = %d, want 1", len(list))
	}
}
