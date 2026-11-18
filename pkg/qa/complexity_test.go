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
	"testing"
	"time"
)

type dummyProfiler struct{}

func (p dummyProfiler) Measure(meta PerfSample, fn func() (verdict string, err error)) (PerfSample, error) {
	_, _ = fn()
	return meta, nil
}

func TestAssessTaskComplexity(t *testing.T) {
	tests := []struct {
		name     string
		in       ComplexityInput
		wantTier ComplexityTier
	}{
		{
			name: "high from design item and writes",
			in: ComplexityInput{
				Body:        "Design-Item: true",
				WritesCount: 2,
				ReadsCount:  2,
				PathHints:   []string{"pkg/orchestrator"},
			},
			wantTier: ComplexityHigh,
		},
		{
			name: "low mechanical",
			in: ComplexityInput{
				Title: "rename stuff",
			},
			wantTier: ComplexityLow,
		},
		{
			name: "ui icon tweak with dispatch envelope stays below high",
			in: ComplexityInput{
				Title: "[ACME-10] Add little eye visibility icon glyph at bottom of card",
				Body: "Dispatched from **Issue Tracker** (`ACME-10` in project **Widgets**, owner `alice@example.com`).\n\n" +
					"> **Gate Override Note:** already checked\n\n" +
					"## Pinned Deliverables (Materialized in `./attachments/`)\n" +
					"- **SPEC** (`abc@v1`, file `./attachments/1-spec.md`): visibility auth note\n",
			},
			wantTier: ComplexityLow,
		},
		{
			name: "multi-deliverable cross-surface milestone feature scores high",
			in: ComplexityInput{
				Title: "Build attachment storage API and Angular inspector upload UI",
				Body: "## Pinned Deliverables\n" +
					"- **CONCEPT** (`./attachments/1-concept.md`)\n" +
					"- **SPEC** (`./attachments/2-spec.md`)\n\n" +
					"## Milestones\n- Wave 1: pkg/model/schema.go + internal/server\n- Wave 2: ui-ng/src/app/card.component.ts\n",
			},
			wantTier: ComplexityHigh,
		},
		{
			name: "short dispatch wrapper with multi-section attached markdown spec scores high",
			in: ComplexityInput{
				Title: "[ACME-13] UX polish",
				Body: "Dispatched from **Issue Tracker** (`ACME-13` in project **Widgets**, owner `alice@example.com`).\n\n" +
					"## Capability Summary\nLet's stress test this!\n\n" +
					"> **Gate Override Note:** Fast track\n\n" +
					"## Pinned Deliverables (Materialized in `./attachments/`)\n" +
					"- **CONCEPT** (`doc1@v2`, file `./attachments/1-concept.md`)\n" +
					"- **HTML_MOCK** (`doc2@v1`, file `./attachments/2-mock.html`)\n",
				MarkdownAttachmentCount: 2,
				AttachmentText: "---\ntitle: \"ACME-13 Inspector & Action Bar UX\"\nstatus: Approved\n---\n\n" +
					"# ACME-13: Card Inspector, Action Bar & Shaping Polish\n\n" +
					"## 1. Problem & Motivation\n" +
					"- Fix inspector resize jump when switching tabs.\n" +
					"- Pin action bar at bottom of drawer.\n\n" +
					"## 2. Surface & State Model\n" +
					"- Update `ui-ng/src/app/components/card-inspector/card-inspector.component.ts`.\n" +
					"- Update `ui-ng/src/app/components/modals/modals.component.ts`.\n\n" +
					"## 3. Backend & Persistence Contracts\n" +
					"- Extend `internal/store/sqlite.go` and `internal/server/server.go`.\n" +
					"- Validate stage transitions in `internal/playbook/playbook.go`.\n\n" +
					"## 4. Phased Implementation & Verification Plan\n" +
					"- Phase 1: Backend schema and store wiring.\n" +
					"- Phase 2: Angular inspector and pinned bar.\n" +
					"- Phase 3: End-to-end verification specs.\n" +
					"Additional architectural details covering error recovery, accessibility focus restoration, keyboard shortcuts, and responsive drawer breakpoints across desktop and mobile viewports so the full authored specification exceeds fifteen hundred bytes of concrete implementation requirements and acceptance criteria for multi-subsystem decomposition.\n" +
					"Further specification details covering optimistic locking, stale-version conflict banners, keyboard Escape handling in nested listboxes, and component CSS byte-budget checks across light and dark themes to mirror a complete multi-area feature specification.\n",
				PathHints: []string{
					"ui-ng/src/app/components/card-inspector/card-inspector.component.ts",
					"internal/store/sqlite.go",
					"internal/server/server.go",
					"internal/playbook/playbook.go",
				},
			},
			wantTier: ComplexityHigh,
		},
		{
			name: "single focused markdown attachment without multi-phase plan scores medium",
			in: ComplexityInput{
				Title:                   "[ACME-14] Add directory search to member invite input",
				Body:                    "Dispatched from **Issue Tracker** (`ACME-14`).\n\n## Pinned Deliverables\n- **CONCEPT** (`./attachments/1-concept.md`)\n",
				MarkdownAttachmentCount: 1,
				AttachmentText: "# ACME-14 Directory Autocomplete\n\n" +
					"## Context\nAdd autocomplete suggestions to the member invite dialog in `ui-ng/src/app/roster.component.ts` and wire `internal/server/directory.go` so users can look up teammates by handle across all member forms in the project settings modal without typing full email addresses manually.\n" +
					"- Support keyboard arrow navigation and Escape dismissal.\n" +
					"- Render profile avatars next to handles.\n",
			},
			wantTier: ComplexityMedium,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessTaskComplexity(tt.in, dummyProfiler{})
			if got.Tier != tt.wantTier {
				t.Errorf("got tier %s, want %s (score %d, signals %v)", got.Tier, tt.wantTier, got.Score, got.Signals)
			}
		})
	}
}

func BenchmarkAssessTaskComplexity(b *testing.B) {
	in := ComplexityInput{
		Title:       "Add auth to orchestrator",
		Body:        "Design-Item: 123\n- a\n- b\n- c\n- d\n- e\n- f",
		WritesCount: 5,
		ReadsCount:  10,
		PathHints:   []string{"pkg/orchestrator", "ui/app.ts"},
	}
	prof := dummyProfiler{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AssessTaskComplexity(in, prof)
	}
}

func TestAssessTaskComplexity_TimeLimit(t *testing.T) {
	in := ComplexityInput{
		Title:       "Add auth to orchestrator",
		Body:        "Design-Item: 123\n- a\n- b\n- c\n- d\n- e\n- f",
		WritesCount: 5,
		ReadsCount:  10,
		PathHints:   []string{"pkg/orchestrator", "ui/app.ts"},
	}
	prof := dummyProfiler{}

	start := time.Now()
	for i := 0; i < 100; i++ {
		AssessTaskComplexity(in, prof)
	}
	elapsed := time.Since(start)
	if avg := elapsed / 100; avg > 5*time.Millisecond {
		t.Errorf("execution took too long: avg %v (limit 5ms)", avg)
	}
}
