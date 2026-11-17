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

func assessTaskComplexityInternal(in ComplexityInput) ComplexityAssessment {
	hasDesignItem := strings.Contains(strings.ToLower(in.Body), "design-item:")

	score := 0
	var signals []string

	if hasDesignItem {
		score += 20
		signals = append(signals, "has design item")
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
			strings.Contains(lowHint, "security") ||
			strings.Contains(lowHint, "auth") ||
			strings.Contains(lowHint, "staterepo") {
			highRiskMatched = true
			break
		}
	}

	if highRiskMatched {
		score += 25
		signals = append(signals, "high-risk path hint")
	} else {
		lowTitle := strings.ToLower(in.Title)
		lowBody := strings.ToLower(in.Body)
		if strings.Contains(lowTitle, "migrations.go") || strings.Contains(lowBody, "migrations.go") ||
			strings.Contains(lowTitle, "schema.go") || strings.Contains(lowBody, "schema.go") ||
			strings.Contains(lowTitle, "trustboundary") || strings.Contains(lowBody, "trustboundary") ||
			strings.Contains(lowTitle, "security") || strings.Contains(lowTitle, "auth") ||
			strings.Contains(lowTitle, "pkg/orchestrator") || strings.Contains(lowBody, "pkg/orchestrator") ||
			strings.Contains(lowTitle, "staterepo") || strings.Contains(lowBody, "staterepo") {
			score += 25
			signals = append(signals, "high-risk path hint")
		}
	}

	hasBackend := false
	hasFrontend := false
	for _, hint := range in.PathHints {
		if strings.HasPrefix(hint, "pkg/") {
			hasBackend = true
		}
		if strings.HasPrefix(hint, "ui/") || strings.HasPrefix(hint, "ui-ng/") {
			hasFrontend = true
		}
	}
	if hasBackend && hasFrontend {
		score += 20
		signals = append(signals, "cross-surface")
	}

	bullets := strings.Count(in.Body, "\n- ") + strings.Count(in.Body, "\n* ") +
		strings.Count(in.Body, "\r\n- ") + strings.Count(in.Body, "\r\n* ")
	if strings.HasPrefix(in.Body, "- ") || strings.HasPrefix(in.Body, "* ") {
		bullets++
	}
	if bullets > 5 {
		score += 15
		signals = append(signals, "high bullet density")
	}
	if len(in.Body) > 1000 {
		score += 10
		signals = append(signals, "long spec")
	}

	lowTitle := strings.ToLower(in.Title)
	if strings.Contains(lowTitle, "typo") || strings.Contains(lowTitle, "rename") {
		score -= 20
		signals = append(signals, "mechanical cue")
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
