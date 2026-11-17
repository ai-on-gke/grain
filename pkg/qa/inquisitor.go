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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Theater rejection tags enforced by the S4 deterministic Go Inquisitor.
const (
	TheaterInventedArchitecture   = "INVENTED_ARCHITECTURE"
	TheaterContainedException     = "CONTAINED_EXCEPTION"
	TheaterPedanticEscalation     = "PEDANTIC_ESCALATION"
	TheaterDefensiveSlop          = "DEFENSIVE_SLOP"
	TheaterOutOfDiffPreexisting   = "OUT_OF_DIFF_PREEXISTING"
	TheaterStyleOnly              = "STYLE_ONLY"
	TheaterHallucinatedSymbolLine = "HALLUCINATED_SYMBOL_OR_LINE"
)

// S4RawFinding is the structured JSON finding shape requested from the fresh-context S4 verifier.
type S4RawFinding struct {
	File            string `json:"file"`
	Line            int    `json:"line"`
	Category        string `json:"category"`
	TheaterTag      string `json:"theater_tag,omitempty"`
	TriggerScenario string `json:"trigger_scenario"`
	FixHint         string `json:"fix_hint"`
}

// LineRange represents an inclusive 1-based line interval in a head file touched by a diff hunk
// or an S3 sibling-caller hazard card.
type LineRange struct {
	Start int
	End   int
}

// InquisitorContext supplies deterministic worktree facts used to validate S4 findings.
type InquisitorContext struct {
	// HeadFiles maps normalized relative file paths present at stageHeadSHA to their full text.
	HeadFiles map[string]string
	// DiffHunks maps normalized relative file paths to their modified head line ranges.
	DiffHunks map[string][]LineRange
	// HazardSites maps sibling-caller file paths surfaced by S3 to their line numbers.
	HazardSites map[string][]int
	// FullDiffText holds the raw unified diff text for symbol-grounding fallback.
	FullDiffText string
	// SeenFingerprints holds fingerprints already raised in an earlier round to prevent A->B->A oscillation.
	SeenFingerprints map[string]bool
}

// ValidatedS4Finding is an S4 finding that survived all 6 deterministic Inquisitor filters.
type ValidatedS4Finding struct {
	File        string
	Line        int
	Category    string
	Summary     string
	Evidence    string
	Fingerprint string
}

var backtickIdentRe = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]{3,})`")

var rejectedTheaterTags = map[string]bool{
	TheaterInventedArchitecture: true,
	TheaterContainedException:   true,
	TheaterPedanticEscalation:   true,
	TheaterDefensiveSlop:        true,
	TheaterOutOfDiffPreexisting: true,
	TheaterStyleOnly:            true,
}

// ExtractS4JSONFindings extracts a JSON array of S4RawFinding objects from raw model output
// (accepting either bare JSON or a ```json fenced block). Returns nil, nil when the verifier
// reports no findings ("[]" or "NO_FINDINGS").
func ExtractS4JSONFindings(raw string) ([]S4RawFinding, error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "NO_FINDINGS") || s == "[]" {
		return nil, nil
	}
	if idx := strings.Index(s, "```json"); idx >= 0 {
		rest := s[idx+len("```json"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			s = strings.TrimSpace(rest[:end])
		}
	} else if idx := strings.Index(s, "```"); idx >= 0 {
		rest := s[idx+3:]
		if end := strings.Index(rest, "```"); end >= 0 {
			s = strings.TrimSpace(rest[:end])
		}
	}
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end < start {
		if strings.Contains(strings.ToUpper(s), "NO_FINDINGS") {
			return nil, nil
		}
		return nil, fmt.Errorf("qa: S4 verifier output contained no JSON array")
	}
	var out []S4RawFinding
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil, fmt.Errorf("qa: decoding S4 verifier JSON findings: %w", err)
	}
	return out, nil
}

// FilterS4Findings runs the 6-filter deterministic Go Inquisitor over raw S4 findings,
// returning the surviving actionable findings and the count of rejected theater/ungrounded items.
func FilterS4Findings(raw []S4RawFinding, ictx InquisitorContext) (kept []ValidatedS4Finding, filtered int) {
	seen := make(map[string]bool, len(ictx.SeenFingerprints)+len(raw))
	for fp, v := range ictx.SeenFingerprints {
		if v {
			seen[fp] = true
		}
	}

	for _, item := range raw {
		relFile := strings.TrimPrefix(path.Clean(strings.TrimSpace(item.File)), "./")
		if relFile == "" || relFile == "." {
			filtered++
			continue
		}

		// Filter 1: Theater category & self-tag rejection.
		upperTag := strings.ToUpper(strings.TrimSpace(item.TheaterTag))
		upperCat := strings.ToUpper(strings.TrimSpace(item.Category))
		if rejectedTheaterTags[upperTag] || rejectedTheaterTags[upperCat] ||
			strings.Contains(upperCat, "STYLE") || strings.Contains(upperCat, "NIT") {
			filtered++
			continue
		}

		// Filter 2: Concrete failure scenario floor (must state input/state -> concrete failure).
		scenario := strings.TrimSpace(item.TriggerScenario)
		if len(scenario) < 20 {
			filtered++
			continue
		}

		// Filter 3: Worktree file existence at stageHeadSHA.
		fileText, fileExists := ictx.HeadFiles[relFile]
		if !fileExists {
			filtered++
			continue
		}

		// Filter 4: Line bounds & <= 15-line diff-hunk (or S3 sibling hazard site) grounding.
		lineCount := strings.Count(fileText, "\n") + 1
		line := item.Line
		if line <= 0 || line > lineCount {
			filtered++
			continue
		}
		if !withinDiffOrHazard(relFile, line, ictx.DiffHunks, ictx.HazardSites, 15) {
			filtered++
			continue
		}

		// Filter 5: Backticked identifier grounding (prevents hallucinated APIs/symbols).
		if hasHallucinatedBacktickIdent(item.TriggerScenario+" "+item.FixHint, fileText, ictx.FullDiffText, ictx.HeadFiles) {
			filtered++
			continue
		}

		// Filter 6: Fingerprint anti-oscillation deduplication.
		fp := FindingFingerprint(RuleS4OrthogonalVerifier, relFile, item.Category, scenario)
		if seen[fp] {
			filtered++
			continue
		}
		seen[fp] = true

		summary := fmt.Sprintf("[%s] %s", nonEmptyOr(strings.TrimSpace(item.Category), "defect"), scenario)
		evidence := strings.TrimSpace(item.FixHint)
		kept = append(kept, ValidatedS4Finding{
			File:        relFile,
			Line:        line,
			Category:    strings.TrimSpace(item.Category),
			Summary:     summary,
			Evidence:    evidence,
			Fingerprint: fp,
		})
	}
	return kept, filtered
}

// FindingFingerprint returns a stable 16-hex-char SHA-256 fingerprint for deduplication.
func FindingFingerprint(ruleID, filePath, category, detail string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(ruleID)) + "|" +
		strings.TrimSpace(filePath) + "|" +
		strings.ToLower(strings.TrimSpace(category)) + "|" +
		strings.ToLower(strings.TrimSpace(detail))))
	return hex.EncodeToString(sum[:8])
}

func withinDiffOrHazard(file string, line int, hunks map[string][]LineRange, hazards map[string][]int, margin int) bool {
	for _, r := range hunks[file] {
		if line >= r.Start-margin && line <= r.End+margin {
			return true
		}
	}
	for _, hLine := range hazards[file] {
		if line >= hLine-margin && line <= hLine+margin {
			return true
		}
	}
	return false
}

func hasHallucinatedBacktickIdent(prose, fileText, diffText string, allHeadFiles map[string]string) bool {
	matches := backtickIdentRe.FindAllStringSubmatch(prose, -1)
	for _, m := range matches {
		ident := m[1]
		if isCommonStdIdent(ident) {
			continue
		}
		if strings.Contains(fileText, ident) || strings.Contains(diffText, ident) {
			continue
		}
		foundElsewhere := false
		for _, body := range allHeadFiles {
			if strings.Contains(body, ident) {
				foundElsewhere = true
				break
			}
		}
		if !foundElsewhere {
			return true
		}
	}
	return false
}

func isCommonStdIdent(ident string) bool {
	switch ident {
	case "true", "false", "null", "undefined", "error", "string", "bool", "int64", "uint64",
		"context", "Context", "KnownFields", "DisallowUnknownFields", "TestingT":
		return true
	default:
		return false
	}
}

func nonEmptyOr(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}
