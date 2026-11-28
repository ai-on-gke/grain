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

package postgres_test

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestQASpecAttachmentsMonotonicTierAndLegacySummariesOnPostgres(t *testing.T) {
	t.Parallel()
	st, _, ctx := openStore(t)

	if err := st.PutConfig(ctx, model.Config{
		PollInterval: 30 * time.Second,
		GitHubHost:   "github.com",
		QA: model.QAConfig{
			Enabled:    true,
			FlowID:     model.QAFlowStandard,
			RolloutPct: 100,
			Epoch:      1,
			RegimeID:   1,
		},
	}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	repo := &model.RepoRef{Owner: "acme", Name: "widgets"}
	rootTask := model.Task{
		ID:     "pg-qa-root",
		Title:  "Hydrate oversized spec on PostgreSQL",
		Body:   "See attached spec.",
		Target: repo,
		Writes: []model.WriteTarget{{Repo: *repo, Base: "main"}},
	}
	if err := st.PutTask(ctx, rootTask); err != nil {
		t.Fatalf("PutTask root: %v", err)
	}

	// Build an oversized multi-byte UTF-8 attachment where 4-byte runes straddle both
	// DefaultTaskSpecAttachmentMaxBytes (131072) and DefaultTaskSpecAttachmentMaxBytes+4 (131076).
	prefix := []byte("# PostgreSQL Spec\n\n## 1. Overview\nTouch `pkg/model/qa.go`.\n")
	pad := (2 - (len(prefix) % 4) + 4) % 4
	var oversized bytes.Buffer
	oversized.Write(prefix)
	oversized.Write(bytes.Repeat([]byte{'A'}, pad))
	rocket := []byte("🚀")
	for oversized.Len() < model.DefaultTaskSpecAttachmentMaxBytes+4096 {
		oversized.Write(rocket)
	}
	rawOversized := oversized.Bytes()
	if (rawOversized[model.DefaultTaskSpecAttachmentMaxBytes] & 0xC0) != 0x80 {
		t.Fatalf("expected byte %d to land mid-rune", model.DefaultTaskSpecAttachmentMaxBytes)
	}

	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01, 0x02, 'P', 'N', 'G'}
	if _, err := st.AddAttachment(ctx, model.Attachment{
		TaskID:      rootTask.ID,
		Filename:    "1-diagram.png",
		ContentType: "image/png",
		Size:        int64(len(pngBytes)),
		Content:     pngBytes,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("AddAttachment png: %v", err)
	}
	if _, err := st.AddAttachment(ctx, model.Attachment{
		TaskID:      rootTask.ID,
		Filename:    "2-oversized.md",
		ContentType: "text/markdown; charset=utf-8",
		Size:        int64(len(rawOversized)),
		Content:     rawOversized,
		CreatedAt:   now.Add(time.Second),
	}); err != nil {
		t.Fatalf("AddAttachment oversized md: %v", err)
	}

	att, err := st.TaskSpecAttachments(ctx, rootTask.ID, model.DefaultTaskSpecAttachmentMaxBytes)
	if err != nil {
		t.Fatalf("TaskSpecAttachments: %v", err)
	}
	if att.MarkdownCount != 1 {
		t.Fatalf("MarkdownCount = %d, want 1", att.MarkdownCount)
	}
	if !strings.Contains(att.Text, "# PostgreSQL Spec") || strings.Contains(att.Text, "PNG") {
		t.Fatalf("unexpected hydrated text prefix/binary filtering: len=%d", len(att.Text))
	}
	if len(att.Text) > model.DefaultTaskSpecAttachmentMaxBytes {
		t.Fatalf("len(att.Text) = %d > max %d", len(att.Text), model.DefaultTaskSpecAttachmentMaxBytes)
	}
	if !utf8.ValidString(att.Text) || strings.ContainsRune(att.Text, utf8.RuneError) {
		t.Fatalf("truncated PostgreSQL attachment produced invalid UTF-8 or replacement rune")
	}

	// Verify monotonic UpdateQAAssignmentComplexityTier within an epoch and epoch isolation.
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:" + rootTask.ID,
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 100,
		ComplexityTier:     model.QAComplexityLow,
		AssignedAt:         now,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment epoch 1: %v", err)
	}
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:" + rootTask.ID,
		Epoch:              2,
		RegimeID:           2,
		Arm:                model.QAArmTreatment,
		FlowID:             model.QAFlowStandard,
		RolloutPctAtAssign: 100,
		ComplexityTier:     model.QAComplexityLow,
		AssignedAt:         now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("EnsureQAAssignment epoch 2: %v", err)
	}
	if err := st.UpdateQAAssignmentComplexityTier(ctx, "task:"+rootTask.ID, 1, model.QAComplexityHigh); err != nil {
		t.Fatalf("UpdateQAAssignmentComplexityTier low->high: %v", err)
	}
	if err := st.UpdateQAAssignmentComplexityTier(ctx, "task:"+rootTask.ID, 1, model.QAComplexityMedium); err != nil {
		t.Fatalf("UpdateQAAssignmentComplexityTier high->medium: %v", err)
	}
	ep1, err := st.QAAssignment(ctx, "task:"+rootTask.ID, 1)
	if err != nil || ep1 == nil || ep1.ComplexityTier != model.QAComplexityHigh {
		t.Fatalf("epoch 1 assignment = %+v (err=%v), want high", ep1, err)
	}
	ep2, err := st.QAAssignment(ctx, "task:"+rootTask.ID, 2)
	if err != nil || ep2 == nil || ep2.ComplexityTier != model.QAComplexityLow {
		t.Fatalf("epoch 2 assignment = %+v (err=%v), want untouched low", ep2, err)
	}

	// Verify legacy flow alias canonicalization and explicit QAFlowRef precedence on PostgreSQL.
	legacyTask := model.Task{ID: "pg-qa-legacy", Title: "Legacy v4 task", Target: repo}
	pinnedTask := model.Task{ID: "pg-qa-pinned", Title: "Pinned gates-only", Target: repo, QAFlowRef: model.QAFlowGatesOnly}
	for _, tk := range []model.Task{legacyTask, pinnedTask} {
		if err := st.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask %s: %v", tk.ID, err)
		}
	}
	if _, err := st.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         "task:" + legacyTask.ID,
		Epoch:              1,
		RegimeID:           1,
		Arm:                model.QAArmTreatment,
		FlowID:             "golden-v4-composable",
		RolloutPctAtAssign: 100,
		ComplexityTier:     model.QAComplexityMedium,
		AssignedAt:         now,
	}); err != nil {
		t.Fatalf("EnsureQAAssignment legacy: %v", err)
	}
	summaries, err := st.TaskQASummaries(ctx, []model.Task{legacyTask, pinnedTask}, map[string]model.State{
		legacyTask.ID: model.StateAwaitingSubmit,
		pinnedTask.ID: model.StateProposed,
	}, nil)
	if err != nil {
		t.Fatalf("TaskQASummaries: %v", err)
	}
	if s := summaries[legacyTask.ID]; s == nil || s.FlowID != model.QAFlowStandard || s.FlowLabel != "Standard" {
		t.Fatalf("legacyTask summary = %+v, want standard-workflow/Standard", s)
	}
	if s := summaries[pinnedTask.ID]; s == nil || !s.Pending || s.FlowID != model.QAFlowGatesOnly || s.FlowLabel != "Gates only" {
		t.Fatalf("pinnedTask summary = %+v, want pending gates-only/Gates only", s)
	}
}
