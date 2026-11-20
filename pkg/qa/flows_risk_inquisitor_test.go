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

package qa

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestBuiltInFlowsAndEmbeddedSkillContract(t *testing.T) {
	flows := BuiltInFlows()
	if len(flows) != 3 {
		t.Fatalf("len(BuiltInFlows()) = %d, want 3", len(flows))
	}
	golden, ok := LookupFlow(model.QAFlowGoldenV1Minimal)
	if !ok {
		t.Fatal("LookupFlow(golden-v1-minimal) = false")
	}
	skill := golden.SkillPrompt()
	if skill == "" {
		t.Fatal("expected non-empty embedded SkillPrompt for golden-v1-minimal")
	}
	// Guard prompt-budget ceiling: <= 1400 bytes (~280 tokens) and zero QA-* commit watermarks.
	if len(skill) > 1400 {
		t.Fatalf("SkillPrompt length = %d bytes, want <= 1400 bytes", len(skill))
	}
	if strings.Contains(skill, "QA-") || strings.Contains(skill, "Trait:") {
		t.Fatalf("SkillPrompt must not introduce git commit watermarks: %q", skill)
	}
	if !strings.Contains(skill, "caller_notes") {
		t.Fatalf("SkillPrompt must reference caller_notes parameter: %q", skill)
	}

	s3Only, ok := LookupFlow(model.QAFlowDeterministicS3Only)
	if !ok || s3Only.InjectSkill || s3Only.EnableS4 || s3Only.SkillPrompt() != "" {
		t.Fatalf("deterministic-s3-only must disable skill and S4: %+v", s3Only)
	}
}

func TestParseRepoFlowYAMLStrictSchemaAndMandatoryFloors(t *testing.T) {
	// Unknown field must fail due to KnownFields(true).
	if _, err := ParseRepoFlowYAML([]byte("unknown-key: true\n")); err == nil {
		t.Fatal("expected ParseRepoFlowYAML to reject unknown YAML fields")
	}
	// Disabling mandatory test-integrity floor must fail.
	if _, err := ParseRepoFlowYAML([]byte("disable-rules:\n  - golden:test-integrity-guard\n")); err == nil {
		t.Fatal("expected ParseRepoFlowYAML to reject disabling golden:test-integrity-guard")
	}
	// Disabling mandatory S4 or strict-decoder floors must fail (tighten-only).
	if _, err := ParseRepoFlowYAML([]byte("enable-s4: false\n")); err == nil {
		t.Fatal("expected ParseRepoFlowYAML to reject disabling enable-s4")
	}
	// Valid tighten-only overlay succeeds.
	spec, err := ParseRepoFlowYAML([]byte("strict-decoder-paths:\n  - internal/cfg/\n"))
	if err != nil {
		t.Fatalf("ParseRepoFlowYAML valid overlay: %v", err)
	}
	if spec.ID != model.QAFlowRepoCustom || !spec.EnableS4 || !spec.InjectSkill {
		t.Fatalf("unexpected parsed spec: %+v", spec)
	}
	if len(spec.StrictDecoderPaths) != 1 || spec.StrictDecoderPaths[0] != "internal/cfg/" {
		t.Fatalf("StrictDecoderPaths = %v, want [internal/cfg/]", spec.StrictDecoderPaths)
	}
}

func TestClassifyDiffBlastTiers(t *testing.T) {
	cases := []struct {
		name  string
		stats DiffStats
		want  BlastTier
	}{
		{
			name:  "docs_and_tests_only_is_T0",
			stats: DiffStats{Files: []string{"docs/arch.md", "pkg/foo/foo_test.go"}},
			want:  BlastT0,
		},
		{
			name:  "single_package_small_edit_is_T1",
			stats: DiffStats{Files: []string{"pkg/calc/add.go", "pkg/calc/add_test.go"}, NonTestAdded: 35, SiblingCallerPackages: 0},
			want:  BlastT1,
		},
		{
			name:  "sibling_fanin_2_is_T2",
			stats: DiffStats{Files: []string{"pkg/calc/add.go"}, NonTestAdded: 12, SiblingCallerPackages: 2},
			want:  BlastT2,
		},
		{
			name:  "ui_ng_component_is_T2",
			stats: DiffStats{Files: []string{"ui-ng/src/app/tasks/tasks-page.component.ts"}, NonTestAdded: 20},
			want:  BlastT2,
		},
		{
			name:  "schema_migration_is_T2",
			stats: DiffStats{Files: []string{"pkg/model/migrations.go"}, NonTestAdded: 18},
			want:  BlastT2,
		},
		{
			name:  "trust_boundary_gitproxy_is_T3",
			stats: DiffStats{Files: []string{"pkg/gitproxy/authorize.go"}, NonTestAdded: 5},
			want:  BlastT3,
		},
		{
			name:  "qa_engine_itself_is_T3",
			stats: DiffStats{Files: []string{"pkg/qa/flows.go"}, NonTestAdded: 5},
			want:  BlastT3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reasons := ClassifyDiffBlast(tc.stats)
			if got != tc.want {
				t.Fatalf("ClassifyDiffBlast(%+v) = %s (%v), want %s", tc.stats, got, reasons, tc.want)
			}
		})
	}
}

func TestInquisitorSixFilterValidation(t *testing.T) {
	headSrc := strings.Join([]string{
		"package scheduler",
		"func RunQueue(items []Item) error {",
		"    for _, it := range items {",
		"        if err := dispatchItem(it); err != nil {",
		"            return nil",
		"        }",
		"    }",
		"    return nil",
		"}",
	}, "\n")

	ictx := InquisitorContext{
		HeadFiles: map[string]string{
			"pkg/scheduler/queue.go": headSrc,
		},
		DiffHunks: map[string][]LineRange{
			"pkg/scheduler/queue.go": {{Start: 2, End: 7}},
		},
		HazardSites:      map[string][]int{},
		FullDiffText:     "+        if err := dispatchItem(it); err != nil {\n+            return nil",
		SeenFingerprints: map[string]bool{},
	}

	rawText := "```json\n" + `[
		{
			"file": "pkg/scheduler/queue.go",
			"line": 5,
			"category": "silent_error_drop",
			"trigger_scenario": "When ` + "`dispatchItem`" + ` returns a non-nil error on item 0, ` + "`RunQueue`" + ` returns nil and aborts remaining items silently.",
			"fix_hint": "Return ` + "`err`" + ` instead of nil when ` + "`dispatchItem`" + ` fails."
		},
		{
			"file": "pkg/scheduler/queue.go",
			"line": 5,
			"category": "INVENTED_ARCHITECTURE",
			"theater_tag": "INVENTED_ARCHITECTURE",
			"trigger_scenario": "Should introduce a distributed dead-letter queue service for failed items.",
			"fix_hint": "Add DLQ worker."
		},
		{
			"file": "pkg/scheduler/nonexistent.go",
			"line": 10,
			"category": "logic_bug",
			"trigger_scenario": "Missing file should be dropped deterministically by worktree check.",
			"fix_hint": "Fix it."
		},
		{
			"file": "pkg/scheduler/queue.go",
			"line": 5,
			"category": "logic_bug",
			"trigger_scenario": "Calling ` + "`NonExistentHallucinatedAPI`" + ` crashes at startup with nil dereference.",
			"fix_hint": "Remove ` + "`NonExistentHallucinatedAPI`" + `."
		},
		{
			"file": "pkg/scheduler/queue.go",
			"line": 5,
			"category": "nit",
			"trigger_scenario": "Rename variable it to item for style consistency across package.",
			"fix_hint": "Rename variable."
		}
	]` + "\n```"

	parsed, err := ExtractS4JSONFindings(rawText)
	if err != nil {
		t.Fatalf("ExtractS4JSONFindings: %v", err)
	}
	if len(parsed) != 5 {
		t.Fatalf("len(parsed) = %d, want 5", len(parsed))
	}

	kept, filtered := FilterS4Findings(parsed, ictx)
	if len(kept) != 1 || filtered != 4 {
		t.Fatalf("FilterS4Findings = (%d kept, %d filtered), want (1 kept, 4 filtered); kept=%+v", len(kept), filtered, kept)
	}
	if kept[0].Line != 5 || !strings.Contains(kept[0].Summary, "dispatchItem") {
		t.Fatalf("unexpected surviving finding: %+v", kept[0])
	}

	// Second pass with SeenFingerprints must drop the duplicate to prevent oscillation.
	ictx.SeenFingerprints[kept[0].Fingerprint] = true
	kept2, filtered2 := FilterS4Findings(parsed[:1], ictx)
	if len(kept2) != 0 || filtered2 != 1 {
		t.Fatalf("expected SeenFingerprints to suppress duplicate finding, got kept=%d filtered=%d", len(kept2), filtered2)
	}
}
