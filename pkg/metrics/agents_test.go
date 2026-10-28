package metrics_test

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/metrics"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestAgentsSectionGroupsByTemplateAndComputesReReadsAndReportChecks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	w := metrics.Window{Since: now.Add(-7 * 24 * time.Hour), Until: now}

	fin1 := now.Add(-2 * time.Hour)
	fin2 := now.Add(-1 * time.Hour)
	fin3 := now.Add(-30 * time.Minute)

	rep := metrics.Compute(metrics.Input{
		Window: w,
		Tasks: []model.TaskTiming{
			{TaskID: "768", TemplateID: model.PipelineStewardTemplateID},
			{TaskID: "800", TemplateID: model.InfraReviewTemplateID},
		},
		Runs: []model.RunTiming{
			{RunID: "768-1", TaskID: "768", Attempt: 1, StartedAt: fin1.Add(-5 * time.Minute), FinishedAt: &fin1, Outcome: "succeeded"},
			{RunID: "768-2", TaskID: "768", Attempt: 2, StartedAt: fin2.Add(-5 * time.Minute), FinishedAt: &fin2, Outcome: "succeeded"},
			{RunID: "800-1", TaskID: "800", Attempt: 1, StartedAt: fin3.Add(-5 * time.Minute), FinishedAt: &fin3, Outcome: "succeeded"},
		},
		ToolUses: []model.RunToolUse{
			{RunID: "768-1", Tool: "read_grain_task", Calls: 10, Elided: 2, ResultBytes: 12000},
			{RunID: "768-2", Tool: "read_grain_task", Calls: 30, Elided: 4, ResultBytes: 48000},
			{RunID: "800-1", Tool: "read_grain_metrics", Calls: 5, Elided: 0, ResultBytes: 8000},
		},
		APICalls: []model.RunAPICall{
			// 768-1 reads task 832 three times (> 2 -> 1 re-read) and task 833 twice (not a re-read).
			{RunID: "768-1", Seq: 1, Tool: "read_grain_task", TargetIDs: "832", ResultBytes: 1000},
			{RunID: "768-1", Seq: 2, Tool: "read_grain_task", TargetIDs: "832", ResultBytes: 1000},
			{RunID: "768-1", Seq: 3, Tool: "read_grain_task", TargetIDs: "832", ResultBytes: 1000},
			{RunID: "768-1", Seq: 4, Tool: "read_grain_task", TargetIDs: "833", ResultBytes: 1000},
			{RunID: "768-1", Seq: 5, Tool: "read_grain_task", TargetIDs: "833", ResultBytes: 1000},
		},
		ReportChecks: []model.RunReportCheck{
			{RunID: "768-1", Claims: 6, Uncited: 1, Unread: 1, Mismatched: 2},
			{RunID: "768-2", Claims: 4, Uncited: 0, Unread: 0, Mismatched: 0},
		},
	})

	if len(rep.Agents.Templates) != 2 {
		t.Fatalf("Agents.Templates = %+v, want 2 templates", rep.Agents.Templates)
	}
	steward := rep.Agents.Templates[0]
	if steward.TemplateID != model.PipelineStewardTemplateID || steward.Wakes != 2 {
		t.Fatalf("steward row = %+v, want template %s with 2 wakes", steward, model.PipelineStewardTemplateID)
	}
	if steward.CallsP50 != 10 || steward.CallsP95 != 30 {
		t.Errorf("steward CallsP50=%d CallsP95=%d, want 10 and 30", steward.CallsP50, steward.CallsP95)
	}
	if steward.BytesP50 != 12000 || steward.BytesP95 != 48000 {
		t.Errorf("steward BytesP50=%d BytesP95=%d, want 12000 and 48000", steward.BytesP50, steward.BytesP95)
	}
	if steward.Elisions != 6 || steward.ElisionsPerWake != 3.0 {
		t.Errorf("steward Elisions=%d ElisionsPerWake=%v, want 6 and 3.0", steward.Elisions, steward.ElisionsPerWake)
	}
	if steward.ReReads != 1 || steward.ReReadsPerWake != 0.5 {
		t.Errorf("steward ReReads=%d ReReadsPerWake=%v, want 1 and 0.5", steward.ReReads, steward.ReReadsPerWake)
	}
	if steward.ReportCheck.Claims != 10 || steward.ReportCheck.Mismatched != 2 || steward.ReportCheck.MismatchRate != 0.2 {
		t.Errorf("steward ReportCheck = %+v, want Claims=10 Mismatched=2 MismatchRate=0.2", steward.ReportCheck)
	}
}
