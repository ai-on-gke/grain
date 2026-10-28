package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

func openTestStoreForTuning(t *testing.T) *model.Store {
	t.Helper()
	st, _ := sqlitetest.Open(t)
	return st
}

func TestTuningProposalValidation(t *testing.T) {
	valid := []string{
		"max_ci_jobs",
		"max-ci-jobs",
		"max_workers",
		"max_mergers",
		"max_merges_per_repo",
		"sandbox_cpus",
		"sandbox_memory_mb",
		"package_cache_size_gb",
		"ci_run_history",
	}
	for _, p := range valid {
		if err := ValidateTuningProposal(p); err != nil {
			t.Errorf("ValidateTuningProposal(%q) returned error: %v", p, err)
		}
	}

	invalid := []string{
		"unrecognized_tuning_param",
		"quantum_cores",
		"",
		"   ",
	}
	for _, p := range invalid {
		if err := ValidateTuningProposal(p); err == nil {
			t.Errorf("ValidateTuningProposal(%q) succeeded, want error", p)
		}
	}
}

func TestExtractTuningProposal(t *testing.T) {
	// 1. Direct fields
	p1 := map[string]any{
		"target_parameter": "max_workers",
		"proposed_value":   "8",
	}
	param, val := ExtractTuningProposal(p1, "", "")
	if param != "max_workers" || val != "8" {
		t.Errorf("p1: got (%q, %q), want (max_workers, 8)", param, val)
	}

	// 2. Template fields
	p2 := map[string]any{
		"template_fields": map[string]any{
			"parameter": "merge_batch_size",
			"value":     "5",
		},
	}
	param, val = ExtractTuningProposal(p2, "", "")
	if param != "merge_batch_size" || val != "5" {
		t.Errorf("p2: got (%q, %q), want (merge_batch_size, 5)", param, val)
	}

	// 3. Custom fields
	p3 := map[string]any{
		"custom_fields": map[string]any{
			"target_parameter": "sandbox_cpus",
		},
		"body": "suggesting 4 cpus",
	}
	param, val = ExtractTuningProposal(p3, "", "")
	if param != "sandbox_cpus" {
		t.Errorf("p3: got param %q, want sandbox_cpus", param)
	}

	// 4. From Title
	p4 := map[string]any{}
	param, val = ExtractTuningProposal(p4, "Tune max_ci_jobs to 12", "")
	if param != "max_ci_jobs" || val != "12" {
		t.Errorf("p4: got (%q, %q), want (max_ci_jobs, 12)", param, val)
	}
}

func TestTuningRecommendationRecordAndRetrieve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStoreForTuning(t)

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cooldown := 7 * 24 * time.Hour

	id1, err := RecordTuningRecommendation(ctx, store, "task-1", "max_merges_per_repo", "4", cooldown, now)
	if err != nil {
		t.Fatalf("RecordTuningRecommendation 1 failed: %v", err)
	}
	if id1 <= 0 {
		t.Fatalf("expected positive id, got %d", id1)
	}

	id2, err := RecordTuningRecommendation(ctx, store, "task-2", "max_workers", "8", cooldown, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("RecordTuningRecommendation 2 failed: %v", err)
	}
	if id2 <= 0 {
		t.Fatalf("expected positive id, got %d", id2)
	}

	// Retrieve all
	all, err := RetrieveTuningRecommendations(ctx, store, "", 10)
	if err != nil {
		t.Fatalf("RetrieveTuningRecommendations failed: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 recommendations, got %d", len(all))
	}

	// Retrieve filtered
	filtered, err := RetrieveTuningRecommendations(ctx, store, "max_merges_per_repo", 10)
	if err != nil {
		t.Fatalf("RetrieveTuningRecommendations filtered failed: %v", err)
	}
	if len(filtered) != 1 || filtered[0].TargetParameter != "max_merges_per_repo" {
		t.Fatalf("expected 1 max_merges_per_repo recommendation, got %+v", filtered)
	}

	// Retrieve latest
	latest, err := RetrieveLatestTuningRecommendation(ctx, store, "max_merges_per_repo")
	if err != nil {
		t.Fatalf("RetrieveLatestTuningRecommendation failed: %v", err)
	}
	if latest == nil || latest.ProposedValue != "4" {
		t.Fatalf("unexpected latest recommendation: %+v", latest)
	}

	// Mark adopted
	if err := MarkTuningRecommendationAdopted(ctx, store, id1, true); err != nil {
		t.Fatalf("MarkTuningRecommendationAdopted failed: %v", err)
	}
	updated, err := store.GetTuningRecommendation(ctx, id1)
	if err != nil {
		t.Fatalf("GetTuningRecommendation failed: %v", err)
	}
	if !updated.Adopted {
		t.Fatalf("expected adopted=true, got false")
	}

	// Test CheckAndMarkTuningAdoption by task
	taskAdopt := model.Task{
		ID:    "task-2",
		Title: "Apply max_workers tuning",
	}
	if err := CheckAndMarkTuningAdoption(ctx, store, taskAdopt); err != nil {
		t.Fatalf("CheckAndMarkTuningAdoption failed: %v", err)
	}
	rec2, err := store.GetTuningRecommendation(ctx, id2)
	if err != nil {
		t.Fatalf("GetTuningRecommendation 2 failed: %v", err)
	}
	if !rec2.Adopted {
		t.Fatalf("expected rec2 adopted=true after CheckAndMarkTuningAdoption")
	}
}

func TestRelayProposedTasksTuningValidationAndRecording(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStoreForTuning(t)

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	infraTask := model.Task{
		ID:         "infra-review-run-1",
		TemplateID: model.InfraReviewTemplateID,
		Title:      "Nightly infra review",
	}

	// 1. Valid proposal: max_workers = 4
	validResult := &agent.Result{
		ToolCalls: []agent.ToolCall{
			{
				Name: "propose_task",
				Arguments: map[string]any{
					"title":            "Tune max_workers to 4",
					"body":             "Proposal to increase max_workers to 4 based on contention metrics",
					"target_parameter": "max_workers",
					"proposed_value":   "4",
				},
			},
		},
	}

	if err := relayProposedTasks(ctx, store, nil, infraTask, validResult, now); err != nil {
		t.Fatalf("relayProposedTasks with valid proposal failed: %v", err)
	}

	// Verify recommendation was recorded in the database
	recs, err := store.ListTuningRecommendations(ctx, "max_workers", 10)
	if err != nil {
		t.Fatalf("ListTuningRecommendations failed: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 recorded tuning recommendation, got %d", len(recs))
	}
	if recs[0].TargetParameter != "max_workers" || recs[0].ProposedValue != "4" {
		t.Errorf("recorded recommendation mismatch: %+v", recs[0])
	}
	if recs[0].TaskID != infraTask.ID {
		t.Errorf("expected TaskID %q, got %q", infraTask.ID, recs[0].TaskID)
	}

	// Verify the proposed task was actually filed
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task filed, got %d", len(tasks))
	}
	if tasks[0].Title != "Tune max_workers to 4" {
		t.Errorf("expected task title 'Tune max_workers to 4', got %q", tasks[0].Title)
	}

	// 2. Invalid proposal targeting an unrecognized parameter
	invalidResult := &agent.Result{
		ToolCalls: []agent.ToolCall{
			{
				Name: "propose_task",
				Arguments: map[string]any{
					"title":            "Tune made_up_parameter to 99",
					"body":             "LLM hallucinated parameter",
					"target_parameter": "made_up_parameter",
					"proposed_value":   "99",
				},
			},
		},
	}

	if err := relayProposedTasks(ctx, store, nil, infraTask, invalidResult, now); err != nil {
		t.Fatalf("relayProposedTasks with invalid proposal failed: %v", err)
	}

	// Verify no new recommendation was recorded
	invalidRecs, err := store.ListTuningRecommendations(ctx, "made_up_parameter", 10)
	if err != nil {
		t.Fatalf("ListTuningRecommendations failed: %v", err)
	}
	if len(invalidRecs) != 0 {
		t.Errorf("expected 0 recommendations for invalid parameter, got %d", len(invalidRecs))
	}

	// Verify no new task was filed (task count remains 1)
	tasksAfter, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if len(tasksAfter) != 1 {
		t.Errorf("expected task count to remain 1, got %d", len(tasksAfter))
	}

	// Verify the refusal is said on the runner's own task, naming the
	// proposal, the parameter and the allowlist's reason -- and only for
	// the refused batch: the valid one above posted nothing.
	comments, err := store.Comments(ctx, infraTask.ID)
	if err != nil {
		t.Fatalf("Comments failed: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment on the runner's task, got %d: %+v", len(comments), comments)
	}
	for _, want := range []string{"Tune made_up_parameter to 99", "`made_up_parameter`", "unrecognized grain_config parameter"} {
		if !strings.Contains(comments[0].Body, want) {
			t.Errorf("refusal comment does not name %q:\n%s", want, comments[0].Body)
		}
	}
}
