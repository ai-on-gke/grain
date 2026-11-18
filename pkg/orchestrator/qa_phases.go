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

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/safepath"
)

// PreCloneQAResult holds the result of pre-clone S0 EARS intent extraction.
type PreCloneQAResult struct {
	Prompt             string
	NeedsClarification bool
	Question           string
}

var pathTokenRe = regexp.MustCompile(`(?:[a-zA-Z0-9_.-]+/)*[a-zA-Z0-9_.-]+\.(?:go|ts|tsx|js|jsx|py|cc|cpp|h|hpp|md|yml|yaml|sql|json|html|css|scss)`)
var dirTokenRe = regexp.MustCompile(`(?:[a-zA-Z0-9_.-]+/)+[a-zA-Z0-9_.-]+`)
var goImportLineRe = regexp.MustCompile(`"([^"]+)"`)
var tsImportLineRe = regexp.MustCompile(`(?:from|require\()\s*['"]([^'"]+)['"]`)

func matchesComplexityTier(allowed []string, actual qa.ComplexityTier) bool {
	if len(allowed) == 0 {
		return true
	}
	act := strings.ToLower(strings.TrimSpace(string(actual)))
	for _, raw := range allowed {
		norm := strings.ToLower(strings.TrimSpace(raw))
		if norm == "large" {
			norm = string(qa.ComplexityHigh)
		}
		if norm == act {
			return true
		}
	}
	return false
}

func extractS0ClarificationQuestion(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "status:") && strings.Contains(lower, "needs_clarification") {
			if idx := strings.Index(lower, "needs_clarification"); idx >= 0 {
				rest := strings.TrimSpace(trimmed[idx+len("needs_clarification"):])
				rest = strings.TrimLeft(rest, ":- ")
				if rest != "" {
					kept = append(kept, rest)
				}
			}
			continue
		}
		if strings.HasPrefix(lower, "question:") {
			q := strings.TrimSpace(trimmed[len("question:"):])
			if q != "" {
				return q
			}
		}
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "- WHEN ") || strings.HasPrefix(upper, "* WHEN ") {
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == 0 {
		return "Clarification is required on ambiguous requirements before implementation proceeds."
	}
	return strings.Join(kept, "\n")
}

// ExecutePreCloneQAPhase runs S0 EARS intent extraction before git checkout so zero clone work is
// spent when the specification requires human clarification.
func ExecutePreCloneQAPhase(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	spec *qa.FlowSpec,
	expID, armID, flowHash string,
	currentPrompt string,
) PreCloneQAResult {
	if spec == nil || armID == model.QAArmControl {
		return PreCloneQAResult{Prompt: currentPrompt}
	}
	rt := LiveQARuntime()
	if rt == nil || rt.RunIsolatedPrompt == nil {
		return PreCloneQAResult{Prompt: currentPrompt}
	}

	comp := qa.AssessTaskComplexity(qa.ComplexityInput{
		Title:       task.Title,
		Body:        task.Body,
		ReadsCount:  len(task.Reads),
		WritesCount: len(task.Writes),
		PathHints:   extractCandidatePaths(task, spec, ""),
	}, rt.Profiler)

	finalPrompt := currentPrompt
	s0Enabled := spec.S0Intent || spec.S0IntentConfig.Enabled
	if s0Enabled && !comp.HasDesignItem && matchesComplexityTier(spec.S0IntentConfig.Complexities, comp.Tier) {
		var priorS0Hold bool
		if store != nil && task.ID != "" {
			if runs, rErr := store.ListQACheckRunsForTask(ctx, task.ID); rErr == nil {
				for _, r := range runs {
					if r.Stage == "S0" && r.Verdict == "hold" {
						priorS0Hold = true
						break
					}
				}
			}
		}
		timeout := time.Duration(spec.S0IntentConfig.MaxSeconds) * time.Second
		if timeout <= 0 || timeout > 30*time.Second {
			timeout = 30 * time.Second
		}
		intentGuidance := strings.TrimSpace(spec.IntentSkillText)
		if intentGuidance == "" {
			intentGuidance = "Extract 2 to 6 testable WHEN ... SHALL acceptance criteria and explicit non-goals strictly from the task title and body."
		}
		prompt := intentGuidance + "\n\nTask title: " + task.Title + "\n\nTask body:\n" + task.Body
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		out, inTok, outTok, err := rt.RunIsolatedPrompt(runCtx, task, task.ID+"-qa-s0", prompt, spec.S0IntentConfig.MaxTokens)
		duration := time.Since(start).Milliseconds()
		cancel()

		cr := model.QACheckRun{
			TaskID:         task.ID,
			RunID:          task.ID + "-qa-s0",
			Stage:          "S0",
			CheckID:        "qa:intent",
			Arm:            model.QAArmTreatment,
			Verdict:        "pass",
			DurationMS:     duration,
			TokensIn:       inTok,
			TokensOut:      outTok,
			ComplexityTier: string(comp.Tier),
			ExperimentID:   expID,
			ArmID:          armID,
			FlowHash:       flowHash,
			QAEngineRev:    qa.QAEngineRev,
			Model:          task.Model,
		}
		var needsClarification bool
		var question string
		if err != nil {
			cr.Verdict = "error"
		} else {
			checklist := strings.TrimSpace(out)
			if strings.Contains(strings.ToLower(checklist), "status: needs_clarification") {
				question = extractS0ClarificationQuestion(checklist)
				onClarify := strings.ToLower(strings.TrimSpace(spec.S0IntentConfig.OnClarification))
				detailBytes, _ := json.Marshal(map[string]any{
					"onClarification": onClarify,
					"question":        question,
					"output":          checklist,
				})
				cr.DetailJSON = string(detailBytes)
				if onClarify == "ask_human" && !priorS0Hold {
					cr.Verdict = "hold"
					needsClarification = true
				} else {
					cr.Verdict = "warn"
					if checklist != "" {
						finalPrompt += "\n\n## EARS Intent Checklist\n" + checklist
					}
				}
			} else if checklist != "" {
				finalPrompt += "\n\n## EARS Intent Checklist\n" + checklist
			}
		}
		if store != nil {
			_, _ = store.RecordQACheckRun(ctx, cr, nil)
		}
		if needsClarification {
			return PreCloneQAResult{
				Prompt:             finalPrompt,
				NeedsClarification: true,
				Question:           question,
			}
		}
	}

	return PreCloneQAResult{Prompt: finalPrompt}
}

// ExecutePostCloneWorkflowPhases runs post-checkout S1 repository & caller context discovery,
// S0b goal decomposition guidance, S2 fresh-context plan critique (with bounded REVISE_PLAN bounces),
// and ActiveCodingGuidance injection.
func ExecutePostCloneWorkflowPhases(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	worktreeDir string,
	spec *qa.FlowSpec,
	expID, armID, flowHash string,
	currentPrompt string,
) string {
	if spec == nil || armID == model.QAArmControl {
		return currentPrompt
	}
	rt := LiveQARuntime()
	var prof *qa.Profiler
	if rt != nil {
		prof = rt.Profiler
	}

	finalPrompt := currentPrompt
	candidatePaths := extractCandidatePaths(task, spec, worktreeDir)

	// 1. S1 (step:context-repo-discovery): deterministic repo & sibling importer context discovery.
	var codebaseContext string
	var siblingImporters []string
	s2Enabled := spec.S2PlanCritic || spec.S2PlanCriticConfig.Enabled
	if spec.Context.Enabled || s2Enabled {
		s1Start := time.Now()
		var existingPaths []string
		if strings.TrimSpace(worktreeDir) != "" {
			maxTokens := spec.Context.MaxTokens
			if maxTokens <= 0 || maxTokens > 2000 {
				maxTokens = 2000
			}
			codebaseContext, existingPaths, siblingImporters = discoverPostCloneCodebaseContext(worktreeDir, candidatePaths, maxTokens, 180*time.Millisecond)
			for _, ep := range existingPaths {
				if !slices.Contains(candidatePaths, ep) {
					candidatePaths = append(candidatePaths, ep)
				}
			}
		}
		s1Duration := time.Since(s1Start).Milliseconds()
		if spec.Context.Enabled || codebaseContext != "" {
			if codebaseContext != "" {
				finalPrompt += "\n\n## Codebase & Caller Context\n" + codebaseContext
			} else if spec.Context.Enabled && len(candidatePaths) > 0 {
				codebaseContext = "<codebase-context>\nCandidate paths: " + strings.Join(candidatePaths, ", ") + "\n</codebase-context>"
				finalPrompt += "\n\n## Codebase & Caller Context\n" + codebaseContext
			}
			if store != nil && (spec.Context.Enabled || codebaseContext != "") {
				detailBytes, _ := json.Marshal(map[string]any{
					"discoveredPaths":  existingPaths,
					"candidatePaths":   candidatePaths,
					"siblingImporters": siblingImporters,
					"contextBytes":     len(codebaseContext),
				})
				_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
					TaskID:       task.ID,
					RunID:        task.ID + "-qa-s1",
					Stage:        "S1",
					CheckID:      "qa:context-discovery",
					Arm:          model.QAArmTreatment,
					Verdict:      "pass",
					DurationMS:   s1Duration,
					DetailJSON:   string(detailBytes),
					ExperimentID: expID,
					ArmID:        armID,
					FlowHash:     flowHash,
					QAEngineRev:  qa.QAEngineRev,
					Model:        task.Model,
				}, nil)
			}
		}
	}

	comp := qa.AssessTaskComplexity(qa.ComplexityInput{
		Title:       task.Title,
		Body:        task.Body,
		ReadsCount:  len(task.Reads),
		WritesCount: len(task.Writes),
		PathHints:   candidatePaths,
	}, prof)

	// 2. S0b (step:goal-subtask-decomposer): complexity-gated slice plan / wave DAG guidance.
	if spec.Decompose.Enabled && spec.Decompose.Mode != "" && spec.Decompose.Mode != "off" {
		if matchesComplexityTier(spec.Decompose.Complexities, comp.Tier) {
			maxSub := spec.Decompose.MaxSubtasks
			if maxSub <= 0 {
				maxSub = 4
			}
			targetLines := spec.Decompose.TargetDiffLines
			if targetLines <= 0 {
				targetLines = 400
			}
			switch strings.ToLower(strings.TrimSpace(spec.Decompose.Mode)) {
			case "propose_dag":
				finalPrompt += fmt.Sprintf(
					"\n\n## Goal Subtask Decomposition (S0b — propose_dag)\n"+
						"Decompose multi-subsystem work into at most %d wave-ordered subtasks via `propose_task` "+
						"(target ~%d diff lines per subtask) with non-overlapping `writes[]` scopes and explicit `depends_on` edges.",
					maxSub, targetLines,
				)
			default:
				finalPrompt += fmt.Sprintf(
					"\n\n## Goal Subtask Decomposition (S0b — slice_plan)\n"+
						"Execute this change as at most %d ordered single-PR slice steps targeting ~%d diff lines per slice, "+
						"verifying compilation and targeted tests after each slice.",
					maxSub, targetLines,
				)
			}
			if store != nil {
				_, _ = store.RecordQACheckRun(ctx, model.QACheckRun{
					TaskID:         task.ID,
					RunID:          task.ID + "-qa-s0b",
					Stage:          "S0b",
					CheckID:        "step:goal-subtask-decomposer",
					Arm:            model.QAArmTreatment,
					Verdict:        "pass",
					ComplexityTier: string(comp.Tier),
					ExperimentID:   expID,
					ArmID:          armID,
					FlowHash:       flowHash,
					QAEngineRev:    qa.QAEngineRev,
					Model:          task.Model,
				}, nil)
			}
		}
	}

	// 3. S2 (qa:plan-critic): post-checkout plan critic honoring both Complexities and Tiers + MaxBounces.
	if s2Enabled && rt != nil && rt.RunIsolatedPrompt != nil {
		prelimRisk := qa.AssessDiffBlast(qa.DiffStats{
			Files:                 candidatePaths,
			SiblingCallerPackages: len(siblingImporters),
		}, spec.RiskOverrides, prof)

		hasComplexities := len(spec.S2PlanCriticConfig.Complexities) > 0
		hasTiers := len(spec.S2PlanCriticConfig.Tiers) > 0
		compMatch := matchesComplexityTier(spec.S2PlanCriticConfig.Complexities, comp.Tier)
		tierMatch := slices.Contains(spec.S2PlanCriticConfig.Tiers, string(prelimRisk.Tier))

		shouldRunS2 := false
		switch {
		case hasComplexities && hasTiers:
			shouldRunS2 = compMatch || tierMatch
		case hasComplexities:
			shouldRunS2 = compMatch
		case hasTiers:
			shouldRunS2 = tierMatch
		default:
			shouldRunS2 = comp.Tier == qa.ComplexityHigh || prelimRisk.Tier == qa.BlastT2 || prelimRisk.Tier == qa.BlastT3
		}

		if shouldRunS2 {
			timeout := time.Duration(spec.S2PlanCriticConfig.MaxSeconds) * time.Second
			if timeout <= 0 || timeout > 45*time.Second {
				timeout = 45 * time.Second
			}
			criticGuidance := strings.TrimSpace(spec.PlanCriticSkillText)
			if criticGuidance == "" {
				criticGuidance = "Inspect the proposed file plan and sibling importers. Flag missing caller updates, missing schema/route registrations, or scope creep beyond the task specification."
			}
			s2Prompt := criticGuidance + "\n\nTask title: " + task.Title + "\n\nTask body:\n" + task.Body
			if codebaseContext != "" {
				s2Prompt += "\n\n## Codebase & Caller Context\n" + codebaseContext
			}

			maxBounces := spec.S2PlanCriticConfig.MaxBounces
			if maxBounces < 0 {
				maxBounces = 0
			} else if maxBounces > 2 {
				maxBounces = 2
			}

			runCtx, cancel := context.WithTimeout(ctx, timeout)
			start := time.Now()
			out, inTok, outTok, err := rt.RunIsolatedPrompt(runCtx, task, task.ID+"-qa-s2", s2Prompt, spec.S2PlanCriticConfig.MaxTokens)
			totalInTok := inTok
			totalOutTok := outTok
			bounces := 0
			for bounces < maxBounces && err == nil && runCtx.Err() == nil && strings.Contains(out, "REVISE_PLAN:") {
				bounces++
				refinePrompt := fmt.Sprintf("%s\n\nPrevious Critique (Pass %d):\n%s\n\nRefine the implementation plan to resolve the REVISE_PLAN findings above.",
					s2Prompt, bounces, strings.TrimSpace(out))
				bOut, bIn, bOutTok, bErr := rt.RunIsolatedPrompt(runCtx, task, task.ID+"-qa-s2", refinePrompt, spec.S2PlanCriticConfig.MaxTokens)
				totalInTok += bIn
				totalOutTok += bOutTok
				if bErr != nil {
					break
				}
				if strings.TrimSpace(bOut) != "" {
					out = bOut
				}
			}
			duration := time.Since(start).Milliseconds()
			cancel()

			detailBytes, _ := json.Marshal(map[string]any{
				"bounces":          bounces,
				"maxBounces":       maxBounces,
				"prelimBlastTier":  prelimRisk.Tier,
				"siblingImporters": siblingImporters,
			})
			cr := model.QACheckRun{
				TaskID:         task.ID,
				RunID:          task.ID + "-qa-s2",
				Stage:          "S2",
				CheckID:        "qa:plan-critic",
				Arm:            model.QAArmTreatment,
				Verdict:        "pass",
				DurationMS:     duration,
				TokensIn:       totalInTok,
				TokensOut:      totalOutTok,
				DetailJSON:     string(detailBytes),
				BlastTier:      string(prelimRisk.Tier),
				BlastScore:     prelimRisk.Score,
				ComplexityTier: string(comp.Tier),
				ExperimentID:   expID,
				ArmID:          armID,
				FlowHash:       flowHash,
				QAEngineRev:    qa.QAEngineRev,
				Model:          task.Model,
			}
			if err != nil {
				cr.Verdict = "error"
			} else {
				critique := strings.TrimSpace(out)
				if critique != "" {
					finalPrompt += "\n\n## S2 Plan Critique\n" + critique
				}
			}
			if store != nil {
				_, _ = store.RecordQACheckRun(ctx, cr, nil)
			}
		}
	}

	// 4. Coding prompt assembly: inject ActiveCodingGuidance (ReadabilityPacks, SkillSteps, PRContract).
	if guidance := strings.TrimSpace(spec.ActiveCodingGuidance(candidatePaths)); guidance != "" {
		if !strings.Contains(finalPrompt, guidance) {
			finalPrompt += "\n\n## Active Workflow Coding & PR Guidance\n" + guidance
		}
	}

	return finalPrompt
}

// ExecutePreDispatchQAPhases executes pre-clone S0 and post-clone workflow stages for backward-compatible callers.
func ExecutePreDispatchQAPhases(ctx context.Context, store *model.Store, task model.Task, spec *qa.FlowSpec, expID, armID, flowHash string, initialPrompt string) string {
	pre := ExecutePreCloneQAPhase(ctx, store, task, spec, expID, armID, flowHash, initialPrompt)
	if pre.NeedsClarification {
		return pre.Prompt
	}
	return ExecutePostCloneWorkflowPhases(ctx, store, task, "", spec, expID, armID, flowHash, pre.Prompt)
}

func cleanRelativeRepoPath(raw string) (string, bool) {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, "`'\".,:;()[]{}")
	p = filepath.ToSlash(p)
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return "", false
	}
	p = strings.TrimPrefix(p, "./")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "" || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

func extractCandidatePaths(task model.Task, spec *qa.FlowSpec, worktreeDir string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(raw string) {
		p, ok := cleanRelativeRepoPath(raw)
		if !ok {
			return
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	var realWorktree string
	if strings.TrimSpace(worktreeDir) != "" {
		if resolved, err := safepath.Within(worktreeDir, "."); err == nil {
			realWorktree = resolved
		}
	}

	text := task.Title + "\n" + task.Body
	for _, m := range pathTokenRe.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range dirTokenRe.FindAllString(text, -1) {
		rel, ok := cleanRelativeRepoPath(m)
		if !ok {
			continue
		}
		if realWorktree != "" {
			if target, err := safepath.Within(realWorktree, rel); err == nil {
				if info, lErr := os.Lstat(target); lErr == nil && info.Mode()&os.ModeSymlink == 0 {
					add(rel)
					continue
				}
			}
		}
		if strings.HasPrefix(rel, "pkg/") || strings.HasPrefix(rel, "cmd/") || strings.HasPrefix(rel, "ui/") || strings.HasPrefix(rel, "ui-ng/") || strings.HasPrefix(rel, "internal/") || strings.HasPrefix(rel, "tests/") {
			add(rel)
		}
	}
	for _, w := range task.Writes {
		if w.Repo.Name != "" {
			add(w.Repo.Name)
		}
		if w.Base != "" && strings.Contains(w.Base, "/") {
			add(w.Base)
		}
	}
	if spec != nil {
		for _, ep := range spec.Context.ExtraPaths {
			cleaned := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(ep), "/**"), "/*")
			if cleaned != "" {
				add(cleaned)
			}
		}
	}
	return out
}

func discoverPostCloneCodebaseContext(
	worktreeDir string,
	candidatePaths []string,
	maxTokens int,
	maxDuration time.Duration,
) (contextBlock string, existingPaths []string, siblingImporters []string) {
	deadline := time.Now().Add(maxDuration)
	if maxTokens <= 0 || maxTokens > 2000 {
		maxTokens = 2000
	}
	maxChars := maxTokens * 4

	realRoot, err := safepath.Within(worktreeDir, ".")
	if err != nil {
		return "", nil, nil
	}
	root, err := os.OpenRoot(realRoot)
	if err != nil {
		return "", nil, nil
	}
	defer func() { _ = root.Close() }()

	seenExist := make(map[string]bool)
	targetDirs := make(map[string]bool)
	targetBasenames := make(map[string]bool)

	isSafeRegularFile := func(rel string) (string, bool) {
		cleanRel, ok := cleanRelativeRepoPath(rel)
		if !ok {
			return "", false
		}
		if _, sErr := safepath.Within(realRoot, cleanRel); sErr != nil {
			return "", false
		}
		info, lErr := root.Lstat(cleanRel)
		if lErr != nil || !info.Mode().IsRegular() {
			return "", false
		}
		return cleanRel, true
	}

	addExistingFile := func(rel string) {
		cleanRel, ok := isSafeRegularFile(rel)
		if !ok || seenExist[cleanRel] {
			return
		}
		seenExist[cleanRel] = true
		existingPaths = append(existingPaths, cleanRel)
		dir := path.Dir(cleanRel)
		if dir != "" && dir != "." {
			targetDirs[dir] = true
		}
		base := strings.TrimSuffix(path.Base(cleanRel), path.Ext(cleanRel))
		if base != "" {
			targetBasenames[base] = true
		}
	}

	for _, cp := range candidatePaths {
		if time.Now().After(deadline) {
			break
		}
		rel, ok := cleanRelativeRepoPath(cp)
		if !ok {
			continue
		}
		abs, sErr := safepath.Within(realRoot, rel)
		if sErr != nil {
			continue
		}
		info, err := root.Lstat(rel)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.Mode().IsRegular() {
			addExistingFile(rel)
			continue
		}
		if !info.IsDir() {
			continue
		}
		targetDirs[rel] = true
		entries, rErr := os.ReadDir(abs)
		if rErr != nil {
			continue
		}
		addedInDir := 0
		for _, ent := range entries {
			if ent.IsDir() || ent.Type()&fs.ModeSymlink != 0 {
				continue
			}
			name := ent.Name()
			if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".spec.ts") {
				continue
			}
			if strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".py") || strings.HasSuffix(name, ".md") {
				childRel := path.Join(rel, name)
				if _, ok := isSafeRegularFile(childRel); ok {
					addExistingFile(childRel)
					addedInDir++
					if addedInDir >= 3 {
						break
					}
				}
			}
		}
	}

	// If no candidate path resolved directly in worktreeDir, fall back to matching basenames or top-level source files.
	if len(existingPaths) == 0 && !time.Now().After(deadline) {
		candidateBases := make(map[string]bool)
		for _, cp := range candidatePaths {
			b := path.Base(filepath.ToSlash(cp))
			if b != "" && b != "." && b != ".." {
				candidateBases[b] = true
			}
		}
		_ = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
			if err != nil || time.Now().After(deadline) {
				return fs.SkipAll
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" {
					return fs.SkipDir
				}
				return nil
			}
			if len(candidateBases) > 0 && candidateBases[d.Name()] {
				addExistingFile(rel)
			}
			return nil
		})
	}

	// Discover direct Go/TS sibling importer directories within the 200ms budget.
	if (len(targetDirs) > 0 || len(targetBasenames) > 0) && !time.Now().After(deadline) {
		importerSet := make(map[string]bool)
		scannedFiles := 0
		_ = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
			if err != nil || time.Now().After(deadline) || scannedFiles >= 300 {
				return fs.SkipAll
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" {
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			isGo := strings.HasSuffix(name, ".go")
			isTS := strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".js")
			if !isGo && !isTS {
				return nil
			}
			cleanRel, ok := isSafeRegularFile(rel)
			if !ok {
				return nil
			}
			relDir := path.Dir(cleanRel)
			if targetDirs[relDir] || importerSet[relDir] {
				return nil
			}
			scannedFiles++
			raw, rErr := root.ReadFile(cleanRel)
			if rErr != nil {
				return nil
			}
			if len(raw) > 4096 {
				raw = raw[:4096]
			}
			snippet := string(raw)
			if isGo {
				for _, m := range goImportLineRe.FindAllStringSubmatch(snippet, -1) {
					imp := m[1]
					for td := range targetDirs {
						if imp == td || strings.HasSuffix(imp, "/"+td) {
							importerSet[relDir] = true
							break
						}
					}
				}
			} else if isTS {
				for _, m := range tsImportLineRe.FindAllStringSubmatch(snippet, -1) {
					imp := m[1]
					for td := range targetDirs {
						baseDir := path.Base(td)
						if strings.Contains(imp, td) || (baseDir != "" && baseDir != "." && strings.Contains(imp, baseDir)) {
							importerSet[relDir] = true
							break
						}
					}
					for tb := range targetBasenames {
						if tb != "" && (strings.HasSuffix(imp, "/"+tb) || imp == "./"+tb || imp == "../"+tb) {
							importerSet[relDir] = true
							break
						}
					}
				}
			}
			return nil
		})
		for impDir := range importerSet {
			siblingImporters = append(siblingImporters, impDir)
		}
		slices.Sort(siblingImporters)
		if len(siblingImporters) > 8 {
			siblingImporters = siblingImporters[:8]
		}
	}

	if len(existingPaths) == 0 && len(siblingImporters) == 0 {
		return "", nil, nil
	}

	var b strings.Builder
	b.WriteString("<codebase-context>\n")
	if len(siblingImporters) > 0 {
		b.WriteString("Direct sibling importer directories: ")
		b.WriteString(strings.Join(siblingImporters, ", "))
		b.WriteString("\n")
	}
	for _, rel := range existingPaths {
		if time.Now().After(deadline) || b.Len() >= maxChars-128 {
			break
		}
		cleanRel, ok := isSafeRegularFile(rel)
		if !ok {
			continue
		}
		raw, err := root.ReadFile(cleanRel)
		if err != nil {
			continue
		}
		snippet := extractBoundedFileHeader(string(raw), 18, 800)
		entry := fmt.Sprintf("--- %s ---\n%s\n", cleanRel, snippet)
		if b.Len()+len(entry)+32 > maxChars {
			remaining := maxChars - b.Len() - 32
			if remaining > 64 {
				b.WriteString(entry[:remaining])
				b.WriteString("\n")
			}
			break
		}
		b.WriteString(entry)
	}
	b.WriteString("</codebase-context>")
	return b.String(), existingPaths, siblingImporters
}

func extractBoundedFileHeader(content string, maxLines, maxBytes int) string {
	lines := strings.Split(content, "\n")
	var kept []string
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "// Copyright") || strings.HasPrefix(trimmed, "// Licensed") ||
			strings.HasPrefix(trimmed, "// you may not") || strings.HasPrefix(trimmed, "//     http") ||
			strings.HasPrefix(trimmed, "// Unless required") || strings.HasPrefix(trimmed, "// distributed under") ||
			strings.HasPrefix(trimmed, "// WITHOUT WARRANTIES") || strings.HasPrefix(trimmed, "// See the License") ||
			strings.HasPrefix(trimmed, "// limitations under") || trimmed == "//" {
			continue
		}
		kept = append(kept, ln)
		if len(kept) >= maxLines {
			break
		}
	}
	out := strings.TrimSpace(strings.Join(kept, "\n"))
	if len(out) > maxBytes {
		out = strings.TrimSpace(out[:maxBytes])
	}
	return out
}
