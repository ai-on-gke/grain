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

package checks

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/qa"
)

var unannotatedSuppressionRe = regexp.MustCompile(`(?i)(//\s*nolint(?::[a-z0-9_,-]+)?\s*$|@\s*ts-(?:ignore|expect-error)\s*$|eslint-disable(?:-next-line|-line)?(?:\s+[a-z0-9_/@,-]+)?\s*\*?/?\s*$|#\s*(?:noqa|type:\s*ignore)(?:\[[a-z0-9_,-]+\])?\s*$|//\s*NOLINT(?:\([a-z0-9_,-]+\))?\s*$)`)

// CheckPRDescriptionContract evaluates golden:pr-description-contract against optional PR subject/body metadata and added diff lines.
func CheckPRDescriptionContract(pd ParsedDiff, subject, body string, contract qa.PRContractDecl) ([]Finding, error) {
	var findings []Finding
	sev := strings.TrimSpace(contract.Mode)
	if sev != "blocking" && sev != "warn" && sev != "shadow" {
		sev = "warn"
	}

	maxSubject := contract.MaxSubjectChars
	if maxSubject <= 0 {
		maxSubject = 72
	}
	if subj := strings.TrimSpace(subject); subj != "" {
		if n := utf8.RuneCountInString(subj); n > maxSubject {
			findings = append(findings, Finding{
				RuleID:   qa.RulePRDescriptionContract,
				Stage:    "S3",
				Severity: sev,
				Summary:  fmt.Sprintf("PR subject line is %d characters, exceeding max-subject-chars %d.", n, maxSubject),
				Evidence: subj,
			})
		}
	}

	if trimmedBody := strings.TrimSpace(body); trimmedBody != "" && len(contract.RequiredSections) > 0 {
		if len(trimmedBody) > 65536 {
			trimmedBody = trimmedBody[:65536]
		}
		lowerBody := strings.ToLower(trimmedBody)
		for _, sec := range contract.RequiredSections {
			secTrim := strings.TrimSpace(sec)
			if secTrim == "" {
				continue
			}
			lowSec := strings.ToLower(secTrim)
			if !strings.Contains(lowerBody, "# "+lowSec) &&
				!strings.Contains(lowerBody, "## "+lowSec) &&
				!strings.Contains(lowerBody, "### "+lowSec) &&
				!strings.Contains(lowerBody, "**"+lowSec+"**") &&
				!strings.Contains(lowerBody, lowSec+":") {
				findings = append(findings, Finding{
					RuleID:   qa.RulePRDescriptionContract,
					Stage:    "S3",
					Severity: sev,
					Summary:  fmt.Sprintf("PR description is missing required section %q.", secTrim),
				})
			}
		}
	}

	auditSuppressions := contract.AuditLintSuppressions || !contract.Enabled
	if auditSuppressions {
		for _, df := range pd.Files {
			if df.IsDeleted {
				continue
			}
			p := df.EffectivePath()
			if isTestPath(p) {
				continue
			}
			for _, al := range df.AddedLines {
				trimmedLine := strings.TrimSpace(al.Text)
				if unannotatedSuppressionRe.MatchString(trimmedLine) {
					findings = append(findings, Finding{
						RuleID:   qa.RulePRDescriptionContract,
						Stage:    "S3",
						Severity: sev,
						FilePath: p,
						Line:     al.Line,
						Summary:  "Added lint/type-check suppression directive without an inline justification.",
						Evidence: trimmedLine,
					})
				}
			}
		}
	}

	return findings, nil
}
