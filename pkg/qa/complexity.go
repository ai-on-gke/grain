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
)

type ComplexityTier string

const (
	ComplexityLow    ComplexityTier = "low"
	ComplexityMedium ComplexityTier = "medium"
	ComplexityHigh   ComplexityTier = "high"
)

type ComplexityInput struct {
	Title       string
	Body        string
	ReadsCount  int
	WritesCount int
	PathHints   []string
}

type ComplexityAssessment struct {
	Tier          ComplexityTier
	Score         int
	Signals       []string
	HasDesignItem bool
}

type perfProfiler interface {
	Measure(meta PerfSample, fn func() (verdict string, err error)) (PerfSample, error)
}

func AssessTaskComplexity(in ComplexityInput, prof perfProfiler) ComplexityAssessment {
	var out ComplexityAssessment
	fn := func() (string, error) {
		out = assessTaskComplexityInternal(in)
		return string(out.Tier), nil
	}
	if prof != nil {
		prof.Measure(PerfSample{Stage: "assess-complexity", CheckID: "complexity"}, fn)
	} else {
		fn()
	}
	return out
}

func stripDispatchEnvelope(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	inPinnedSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		low := strings.ToLower(trimmed)
		if strings.HasPrefix(low, "dispatched from ") || (strings.HasPrefix(low, "> **") && strings.Contains(low, "override note:")) {
			continue
		}
		if strings.HasPrefix(low, "## pinned deliverables") {
			inPinnedSection = true
			continue
		}
		if inPinnedSection {
			if strings.HasPrefix(trimmed, "## ") {
				inPinnedSection = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func containsWordBoundary(lowText, word string) bool {
	idx := 0
	for {
		pos := strings.Index(lowText[idx:], word)
		if pos < 0 {
			return false
		}
		start := idx + pos
		end := start + len(word)
		leftOK := start == 0 || !isAlphaNum(lowText[start-1])
		rightOK := end == len(lowText) || !isAlphaNum(lowText[end])
		if leftOK && rightOK {
			return true
		}
		idx = start + 1
	}
}

func isAlphaNum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func countDeliverableRefs(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		low := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(low, "./attachments/") &&
			(strings.HasPrefix(low, "- **") || strings.HasPrefix(low, "* **")) {
			n++
		}
	}
	return n
}

func assessTaskComplexityInternal(in ComplexityInput) ComplexityAssessment {
	cleanBody := stripDispatchEnvelope(in.Body)
	hasDesignItem := strings.Contains(strings.ToLower(in.Body), "design-item:")

	score := 0
	var signals []string

	if hasDesignItem {
		score += 20
		signals = append(signals, "has design item")
	}

	if countDeliverableRefs(in.Body) >= 2 {
		score += 25
		signals = append(signals, "multi-deliverable bundle")
	}

	if in.WritesCount > 1 {
		score += 15
		signals = append(signals, "multiple writes")
	}
	if in.ReadsCount >= 2 {
		score += 10
		signals = append(signals, "multiple reads")
	}

	highRiskMatched := false
	for _, hint := range in.PathHints {
		lowHint := strings.ToLower(hint)
		if strings.Contains(lowHint, "pkg/orchestrator") ||
			strings.Contains(lowHint, "migrations.go") ||
			strings.Contains(lowHint, "schema.go") ||
			strings.Contains(lowHint, "trustboundary") ||
			containsWordBoundary(lowHint, "security") ||
			containsWordBoundary(lowHint, "auth") ||
			strings.Contains(lowHint, "staterepo") {
			highRiskMatched = true
			break
		}
	}

	lowTitle := strings.ToLower(in.Title)
	lowBody := strings.ToLower(cleanBody)

	if highRiskMatched {
		score += 25
		signals = append(signals, "high-risk path hint")
	} else {
		if strings.Contains(lowTitle, "migrations.go") || strings.Contains(lowBody, "migrations.go") ||
			strings.Contains(lowTitle, "schema.go") || strings.Contains(lowBody, "schema.go") ||
			strings.Contains(lowTitle, "trustboundary") || strings.Contains(lowBody, "trustboundary") ||
			containsWordBoundary(lowTitle, "security") || containsWordBoundary(lowBody, "security") ||
			containsWordBoundary(lowTitle, "auth") || containsWordBoundary(lowBody, "auth") ||
			strings.Contains(lowTitle, "pkg/orchestrator") || strings.Contains(lowBody, "pkg/orchestrator") ||
			strings.Contains(lowTitle, "staterepo") || strings.Contains(lowBody, "staterepo") {
			score += 25
			signals = append(signals, "high-risk path hint")
		}
	}

	hasBackend := false
	hasFrontend := false
	for _, hint := range in.PathHints {
		if strings.HasPrefix(hint, "pkg/") || strings.HasPrefix(hint, "internal/") || strings.HasPrefix(hint, "cmd/") {
			hasBackend = true
		}
		if strings.HasPrefix(hint, "ui/") || strings.HasPrefix(hint, "ui-ng/") || strings.HasPrefix(hint, "web/") {
			hasFrontend = true
		}
	}
	if !hasBackend && (strings.Contains(lowBody, "pkg/") || strings.Contains(lowBody, "internal/") ||
		containsWordBoundary(lowTitle, "backend") || containsWordBoundary(lowTitle, "api")) {
		hasBackend = true
	}
	if !hasFrontend && (strings.Contains(lowBody, "ui-ng/") || strings.Contains(lowBody, "component.ts") ||
		containsWordBoundary(lowTitle, "ui") || containsWordBoundary(lowTitle, "frontend")) {
		hasFrontend = true
	}
	if hasBackend && hasFrontend {
		score += 20
		signals = append(signals, "cross-surface")
	}

	if len(in.PathHints) >= 4 {
		dirs := make(map[string]struct{}, len(in.PathHints))
		for _, h := range in.PathHints {
			clean := strings.Trim(strings.TrimSpace(h), "/")
			if idx := strings.LastIndexByte(clean, '/'); idx > 0 {
				dirs[clean[:idx]] = struct{}{}
			} else if clean != "" {
				dirs[clean] = struct{}{}
			}
		}
		if len(dirs) >= 3 {
			score += 20
			signals = append(signals, "broad path footprint")
		}
	}

	if strings.Contains(lowBody, "## milestones") ||
		strings.Contains(lowBody, "### milestone ") ||
		(strings.Contains(lowBody, "milestone 1") && strings.Contains(lowBody, "milestone 2")) ||
		strings.Contains(lowBody, "wave 1") {
		score += 20
		signals = append(signals, "multi-milestone plan")
	}

	bullets := strings.Count(cleanBody, "\n- ") + strings.Count(cleanBody, "\n* ") +
		strings.Count(cleanBody, "\r\n- ") + strings.Count(cleanBody, "\r\n* ")
	if strings.HasPrefix(cleanBody, "- ") || strings.HasPrefix(cleanBody, "* ") {
		bullets++
	}
	if bullets > 5 {
		score += 15
		signals = append(signals, "high bullet density")
	}
	if len(cleanBody) > 1500 {
		score += 10
		signals = append(signals, "long spec")
	}

	if strings.Contains(lowTitle, "typo") || strings.Contains(lowTitle, "rename") ||
		((containsWordBoundary(lowTitle, "icon") || containsWordBoundary(lowTitle, "glyph") || containsWordBoundary(lowTitle, "tooltip")) && !hasBackend) {
		score -= 20
		signals = append(signals, "small-scope cue")
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	var tier ComplexityTier
	if score < 30 {
		tier = ComplexityLow
	} else if score < 65 {
		tier = ComplexityMedium
	} else {
		tier = ComplexityHigh
	}

	return ComplexityAssessment{
		Tier:          tier,
		Score:         score,
		Signals:       signals,
		HasDesignItem: hasDesignItem,
	}
}
