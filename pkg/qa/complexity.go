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
	"path"
	"regexp"
	"strings"
)

var (
	specURLRe       = regexp.MustCompile("https?://[^\\s)>\\]\"'`]+")
	specPathTokenRe = regexp.MustCompile(`(?:[a-zA-Z0-9_.-]+/)*[a-zA-Z0-9_.-]+\.(?:tsx|jsx|hpp|cpp|scss|html|yaml|json|go|ts|js|py|cc|md|yml|sql|css|h)`)
	specNumberedAtt = regexp.MustCompile(`^\d+-[a-zA-Z0-9_.-]+\.(?:html|md|markdown|txt)$`)
)

func isSpecPathContByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

// ExtractSpecPathHints extracts clean repository-relative path hints from specification prose,
// ignoring URLs, domain suffixes, secondary backup extensions, and sandbox ./attachments/ files.
func ExtractSpecPathHints(texts ...string) []string {
	joined := strings.Join(texts, "\n")
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	cleanText := specURLRe.ReplaceAllString(joined, " ")
	seen := make(map[string]bool)
	var out []string
	for _, loc := range specPathTokenRe.FindAllStringIndex(cleanText, -1) {
		start, end := loc[0], loc[1]
		if end < len(cleanText) {
			if isSpecPathContByte(cleanText[end]) {
				continue
			}
			if cleanText[end] == '.' && end+1 < len(cleanText) && isSpecPathContByte(cleanText[end+1]) {
				continue
			}
		}
		tok := strings.TrimSpace(cleanText[start:end])
		tok = strings.Trim(tok, "`'\"()[]{}")
		tok = strings.TrimRight(tok, ".,:;")
		for strings.HasPrefix(tok, "./") {
			tok = strings.TrimPrefix(tok, "./")
		}
		tok = strings.TrimLeft(tok, "-")
		if tok == "" || strings.HasPrefix(tok, "/") {
			continue
		}
		cleaned := path.Clean(tok)
		if cleaned == "" || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
			continue
		}
		if cleaned == "attachments" || strings.HasPrefix(cleaned, "attachments/") {
			continue
		}
		if !strings.Contains(cleaned, "/") && specNumberedAtt.MatchString(cleaned) {
			continue
		}
		if !seen[cleaned] {
			seen[cleaned] = true
			out = append(out, cleaned)
		}
	}
	return out
}

type ComplexityTier string

const (
	ComplexityLow    ComplexityTier = "low"
	ComplexityMedium ComplexityTier = "medium"
	ComplexityHigh   ComplexityTier = "high"
)

type ComplexityInput struct {
	Title                   string
	Body                    string
	AttachmentText          string
	MarkdownAttachmentCount int
	ReadsCount              int
	WritesCount             int
	PathHints               []string
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
			if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
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

func stripYAMLFrontmatter(s string) string {
	trimmed := strings.TrimLeft(s, "\r\n ")
	if !strings.HasPrefix(trimmed, "---\n") && !strings.HasPrefix(trimmed, "---\r\n") {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	for i := 1; i < len(lines) && i <= 40; i++ {
		fence := strings.TrimSpace(lines[i])
		if fence == "---" || fence == "..." {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return s
}

func countMarkdownSectionHeadings(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "#### ") {
			n++
		}
	}
	return n
}

func assessTaskComplexityInternal(in ComplexityInput) ComplexityAssessment {
	cleanBody := strings.TrimSpace(stripDispatchEnvelope(stripYAMLFrontmatter(in.Body)))
	cleanAttachment := strings.TrimSpace(stripDispatchEnvelope(stripYAMLFrontmatter(in.AttachmentText)))
	combinedSpec := cleanBody
	if cleanAttachment != "" {
		if combinedSpec != "" {
			combinedSpec = combinedSpec + "\n\n" + cleanAttachment
		} else {
			combinedSpec = cleanAttachment
		}
	}

	deliverableRefs := countDeliverableRefs(in.Body)
	sectionHeadings := countMarkdownSectionHeadings(combinedSpec)
	hasExplicitDesignItem := strings.Contains(strings.ToLower(in.Body), "design-item:")
	hasAuthoredSpec := len(cleanAttachment) >= 1500 ||
		(in.MarkdownAttachmentCount > 0 && countMarkdownSectionHeadings(cleanAttachment) >= 4 && len(cleanAttachment) >= 800) ||
		(deliverableRefs >= 1 && len(combinedSpec) >= 1500 && sectionHeadings >= 4)
	hasDesignItem := hasExplicitDesignItem || hasAuthoredSpec

	score := 0
	var signals []string

	if hasDesignItem || (in.MarkdownAttachmentCount > 0 && len(cleanAttachment) >= 250) || (deliverableRefs >= 1 && len(combinedSpec) >= 350) {
		score += 20
		signals = append(signals, "has design item")
	}

	if deliverableRefs >= 2 || in.MarkdownAttachmentCount >= 3 {
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

	pathHints := in.PathHints
	if len(pathHints) == 0 {
		pathHints = ExtractSpecPathHints(in.Title, cleanBody, cleanAttachment)
	}

	highRiskMatched := false
	for _, hint := range pathHints {
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
	lowBody := strings.ToLower(combinedSpec)

	if highRiskMatched {
		score += 25
		signals = append(signals, "high-risk path hint")
	} else {
		if strings.Contains(lowTitle, "migrations.go") || strings.Contains(lowBody, "migrations.go") ||
			strings.Contains(lowTitle, "schema.go") || strings.Contains(lowBody, "schema.go") ||
			strings.Contains(lowTitle, "trustboundary") || strings.Contains(lowBody, "trustboundary") ||
			containsWordBoundary(lowTitle, "security") || containsWordBoundary(lowTitle, "auth") ||
			strings.Contains(lowTitle, "pkg/orchestrator") || strings.Contains(lowBody, "pkg/orchestrator") ||
			strings.Contains(lowTitle, "staterepo") || strings.Contains(lowBody, "staterepo") {
			score += 25
			signals = append(signals, "high-risk path hint")
		}
	}

	hasBackend := false
	hasFrontend := false
	for _, hint := range pathHints {
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

	if len(pathHints) >= 4 {
		dirs := make(map[string]struct{}, len(pathHints))
		for _, h := range pathHints {
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
		strings.Contains(lowBody, "wave 1") ||
		strings.Contains(lowBody, "phased implementation") ||
		strings.Contains(lowBody, "## implementation plan") ||
		(strings.Contains(lowBody, "phase 1") && strings.Contains(lowBody, "phase 2")) ||
		(strings.Contains(lowBody, "requirement #1") && strings.Contains(lowBody, "requirement #2")) {
		score += 20
		signals = append(signals, "multi-milestone plan")
	}

	bullets := strings.Count(combinedSpec, "\n- ") + strings.Count(combinedSpec, "\n* ") +
		strings.Count(combinedSpec, "\r\n- ") + strings.Count(combinedSpec, "\r\n* ")
	if strings.HasPrefix(combinedSpec, "- ") || strings.HasPrefix(combinedSpec, "* ") {
		bullets++
	}
	if bullets > 5 {
		score += 15
		signals = append(signals, "high bullet density")
	}
	if len(combinedSpec) > 1500 {
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
