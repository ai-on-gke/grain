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
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/qa"
)

// Finding is one deterministic pre-PR S3 finding produced by a check rule.
type Finding struct {
	RuleID   string `json:"ruleId"`
	Stage    string `json:"stage"`
	Severity string `json:"severity"`
	FilePath string `json:"filePath,omitempty"`
	Line     int    `json:"line,omitempty"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence,omitempty"`
}

// CheckResult records the per-rule execution outcome, latency, and heap allocation delta.
type CheckResult struct {
	CheckID    string    `json:"checkId"`
	Stage      string    `json:"stage"`
	Verdict    string    `json:"verdict"`
	DurationMS int64     `json:"durationMs"`
	AllocBytes int64     `json:"allocBytes"`
	Findings   []Finding `json:"findings,omitempty"`
}

// RunOptions configures one S3 suite execution.
type RunOptions struct {
	TaskID        string
	RunID         string
	Arm           string
	Shadow        bool
	Attempt       int
	CallerNotes   string
	TaskSpecBytes int
	ExecInSandbox func(ctx context.Context, dir string, argv []string, timeout time.Duration) (string, int, error)
}

// Report aggregates the full S3 check pass, blast-radius classification, and findings.
type Report struct {
	HeadSHA      string           `json:"headSha"`
	ChangedFiles []string         `json:"changedFiles,omitempty"`
	BlastTier    qa.BlastTier     `json:"blastTier"`
	BlastScore   int              `json:"blastScore"`
	BlastReasons []string         `json:"blastReasons,omitempty"`
	Passed       bool             `json:"passed"`
	Results      []CheckResult    `json:"results"`
	Findings     []Finding        `json:"findings,omitempty"`
	HazardSites  map[string][]int `json:"hazardSites,omitempty"`
	DurationMS   int64            `json:"durationMs"`
	AllocBytes   int64            `json:"allocBytes"`
	TestTampered bool             `json:"testTampered,omitempty"`
}

func (r Report) FormatAgentWarnings() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pre-PR quality gate completed with warnings (%d issue(s) on %s, blast tier %s):\n\n",
		len(r.Findings), shortRev(r.HeadSHA), r.BlastTier)
	for i, f := range r.Findings {
		loc := f.FilePath
		if loc != "" && f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		if loc != "" {
			fmt.Fprintf(&b, "%d. [%s] [%s] %s — %s\n", i+1, f.RuleID, f.Severity, loc, f.Summary)
		} else {
			fmt.Fprintf(&b, "%d. [%s] [%s] %s\n", i+1, f.RuleID, f.Severity, f.Summary)
		}
		if strings.TrimSpace(f.Evidence) != "" {
			fmt.Fprintf(&b, "   %s\n", strings.ReplaceAll(strings.TrimSpace(f.Evidence), "\n", "\n   "))
		}
	}
	b.WriteString("\nConsider addressing the warnings above.")
	return b.String()
}

// FormatAgentFailure formats blocking S3 findings as a concise, actionable plain-text response
// returned directly from `open_pull_request` so a live agent can repair them in-turn.
func (r Report) FormatAgentFailure() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pre-PR quality gate blocked pull request creation (%d issue(s) on %s, blast tier %s):\n\n",
		len(r.Findings), shortRev(r.HeadSHA), r.BlastTier)
	for i, f := range r.Findings {
		loc := f.FilePath
		if loc != "" && f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		if loc != "" {
			fmt.Fprintf(&b, "%d. [%s] %s — %s\n", i+1, f.RuleID, loc, f.Summary)
		} else {
			fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, f.RuleID, f.Summary)
		}
		if strings.TrimSpace(f.Evidence) != "" {
			fmt.Fprintf(&b, "   %s\n", strings.ReplaceAll(strings.TrimSpace(f.Evidence), "\n", "\n   "))
		}
	}
	b.WriteString("\nFix the issue(s) above, commit, push your branch, and call `open_pull_request` again.")
	return b.String()
}

// ResolveEffectiveFlow loads repo-custom overrides from `.grain/qa-flow.yml` when flowID is "repo-custom",
// or returns the built-in FlowSpec.
func ResolveEffectiveFlow(ctx context.Context, ws WorkspaceReader, flowID string) (qa.FlowSpec, error) {
	spec, ok := qa.LookupFlow(flowID)
	if !ok {
		spec, _ = qa.LookupFlow("")
	}
	if spec.ID != "repo-custom" {
		return spec, nil
	}
	for _, candidate := range qa.RepoFlowCandidatePaths {
		raw, err := ws.ReadFileAtBase(ctx, candidate)
		if err != nil {
			continue
		}
		return qa.ParseRepoFlowYAML(raw)
	}
	return spec, nil
}

// RunS3 executes all enabled S3 deterministic checks in flow against ws, recording per-check
// and aggregate profiling metrics into profiler when non-nil.
func RunS3(ctx context.Context, ws WorkspaceReader, flow qa.FlowSpec, opts RunOptions, profiler *qa.Profiler) (Report, error) {
	if opts.Attempt <= 0 {
		opts.Attempt = 1
	}

	var report Report
	totalSample, runErr := profiler.Measure(qa.PerfSample{
		TaskID:  opts.TaskID,
		RunID:   opts.RunID,
		Stage:   "S3",
		CheckID: "s3-suite",
	}, func() (string, error) {
		headSHA, err := ws.HeadSHA(ctx)
		if err != nil {
			return "error", err
		}
		rawDiff, err := ws.BaseHeadDiff(ctx)
		if err != nil {
			return "error", err
		}
		pd := ParseUnifiedDiff(rawDiff)
		report.HeadSHA = headSHA

		var siblingPkgCount int
		hazardSites := map[string][]int{}

		runOne := func(ruleID string, fn func() ([]Finding, error)) error {
			if !flow.HasRule(ruleID) {
				return nil
			}
			ruleSpec, _ := flow.Rule(ruleID)
			stage := ruleSpec.Stage
			if stage == "" {
				stage = "S3"
			}
			var ruleFindings []Finding
			sample, rErr := profiler.Measure(qa.PerfSample{
				TaskID:  opts.TaskID,
				RunID:   opts.RunID,
				Stage:   stage,
				CheckID: ruleID,
			}, func() (string, error) {
				fs, err := fn()
				if err != nil {
					return "error", err
				}
				for i := range fs {
					if ruleSpec.Severity != "" {
						fs[i].Severity = ruleSpec.Severity
					} else if fs[i].Severity == "" {
						fs[i].Severity = "blocking"
					}
				}
				ruleFindings = fs
				if len(fs) > 0 {
					if ruleSpec.Severity == "shadow" {
						return "shadow", nil
					} else if ruleSpec.Severity == "warn" {
						return "warn", nil
					}
					return "fail", nil
				}
				return "pass", nil
			})
			if rErr != nil {
				return rErr
			}
			report.Results = append(report.Results, CheckResult{
				CheckID:    ruleID,
				Stage:      stage,
				Verdict:    sample.Verdict,
				DurationMS: sample.DurationMS,
				AllocBytes: sample.AllocBytes,
				Findings:   ruleFindings,
			})
			report.Findings = append(report.Findings, ruleFindings...)
			return nil
		}

		if err := runOne(qa.RuleTestIntegrityGuard, func() ([]Finding, error) {
			return CheckTestIntegrity(ctx, ws, pd)
		}); err != nil {
			return "error", err
		}
		if err := runOne(qa.RuleStrictConfigDecoder, func() ([]Finding, error) {
			return CheckStrictConfigDecoder(ctx, ws, pd, flow.StrictDecoderPaths)
		}); err != nil {
			return "error", err
		}
		if flow.HasRule(qa.RuleSiblingExportHazard) {
			if err := runOne(qa.RuleSiblingExportHazard, func() ([]Finding, error) {
				fs, hz, pkgCnt, hErr := CheckSiblingExportHazard(ctx, ws, pd, opts.Attempt, opts.CallerNotes)
				hazardSites = hz
				siblingPkgCount = pkgCnt
				return fs, hErr
			}); err != nil {
				return "error", err
			}
		} else {
			_, hz, pkgCnt, _ := CheckSiblingExportHazard(ctx, ws, pd, 2, "disabled")
			hazardSites = hz
			siblingPkgCount = pkgCnt
		}
		if err := runOne(qa.RuleUIBudgetAndSpecFloor, func() ([]Finding, error) {
			return CheckUIBudgetAndSpecFloor(ctx, ws, pd)
		}); err != nil {
			return "error", err
		}
		if err := runOne(qa.RuleExportedSurfaceDelta, func() ([]Finding, error) {
			rSpec, _ := flow.Rule(qa.RuleExportedSurfaceDelta)
			return CheckExportedSurfaceDelta(ctx, ws, pd, opts, rSpec.Params)
		}); err != nil {
			return "error", err
		}
		if err := runOne(qa.RuleTestHygiene, func() ([]Finding, error) {
			return CheckTestHygiene(ctx, ws, pd)
		}); err != nil {
			return "error", err
		}
		if err := runOne(qa.RuleMutationHardening, func() ([]Finding, error) {
			rSpec, _ := flow.Rule(qa.RuleMutationHardening)
			return CheckMutationHardening(ctx, ws, pd, opts, rSpec.Params)
		}); err != nil {
			return "error", err
		}
		if err := runOne(qa.RuleBoundaryFuzz, func() ([]Finding, error) {
			rSpec, _ := flow.Rule(qa.RuleBoundaryFuzz)
			return CheckBoundaryFuzz(ctx, ws, pd, opts, rSpec.Params)
		}); err != nil {
			return "error", err
		}
		if err := runOne(qa.RulePRDescriptionContract, func() ([]Finding, error) {
			return CheckPRDescriptionContract(pd, "", "", flow.PRContract)
		}); err != nil {
			return "error", err
		}

		testTampered := false
		for _, res := range report.Results {
			if res.CheckID == qa.RuleTestIntegrityGuard && len(res.Findings) > 0 {
				testTampered = true
			}
		}

		for _, c := range flow.CustomChecks {
			findings, err := qa.EvaluateCustomCheck(c, qa.CustomCheckContext{
				ChangedFiles:    pd.AllPaths(),
				AddedLines:      pd.AddedLinesMap(),
				TotalLinesAdded: pd.NonTestAdded + pd.TestAdded,
				CallerNotes:     opts.CallerNotes,
				ReadFile: func(p string) ([]byte, error) {
					return ws.ReadFileAtHead(ctx, p)
				},
			})
			if err != nil {
				return "error", err
			}
			var ruleFindings []Finding
			for _, f := range findings {
				ruleFindings = append(ruleFindings, Finding{
					RuleID:   f.RuleID,
					Stage:    "S3",
					Severity: f.Severity,
					FilePath: f.FilePath,
					Line:     f.Line,
					Summary:  f.Summary,
					Evidence: f.Evidence,
				})
			}
			verdict := "pass"
			if len(ruleFindings) > 0 {
				if hasBlocking(ruleFindings) {
					verdict = "fail"
				} else {
					verdict = "warn"
				}
			}
			report.Results = append(report.Results, CheckResult{
				CheckID:  c.ID,
				Stage:    "S3",
				Verdict:  verdict,
				Findings: ruleFindings,
			})
			report.Findings = append(report.Findings, ruleFindings...)
		}

		allPaths := pd.AllPaths()
		riskAssessment := qa.AssessDiffBlast(qa.DiffStats{
			Files:                 allPaths,
			NonTestAdded:          pd.NonTestAdded,
			NonTestDeleted:        pd.NonTestDeleted,
			SiblingCallerPackages: siblingPkgCount,
			TestTampered:          testTampered,
		}, flow.RiskOverrides, profiler)

		report.ChangedFiles = allPaths
		report.BlastTier = riskAssessment.Tier
		report.BlastScore = riskAssessment.Score
		report.BlastReasons = riskAssessment.Reasons
		report.TestTampered = riskAssessment.TestTampered
		report.HazardSites = hazardSites
		report.Passed = !hasBlocking(report.Findings)
		if report.Passed {
			return "pass", nil
		}
		return "fail", nil
	})
	if runErr != nil {
		return Report{}, runErr
	}
	report.DurationMS = totalSample.DurationMS
	report.AllocBytes = totalSample.AllocBytes
	return report, nil
}

func hasBlocking(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == "" || f.Severity == "blocking" || f.Severity == "error" {
			return true
		}
	}
	return false
}

func shortRev(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "HEAD"
	}
	return sha
}
