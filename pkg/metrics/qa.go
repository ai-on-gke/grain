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

package metrics

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
)

// MinTreatmentSampleForAutoDowngrade is the minimum assigned Treatment WorkOrigin count
// before the automatic safety circuit breaker evaluates latency/context/trust guardrails.
const MinTreatmentSampleForAutoDowngrade = 15

// QASummaryInput holds all store slices needed to compute a single-instance QA A/B summary
// without touching the database inside pkg/metrics.
type QASummaryInput struct {
	Window      Window
	Scope       Scope
	Config      model.QAConfig
	Assignments []model.QAAssignment
	CheckRuns   []model.QACheckRun
	Findings    []model.QAFinding
	Tasks       []model.Task
	Timings     []model.TaskTiming
	Runs        []model.RunTiming
	Prompts     []model.RunPrompt
	Usages      []model.RunUsage
	CIRuns      []model.CIRunOutcome
}

// QAArmMetrics summarizes Intention-to-Treat (ITT) outcomes for one experiment arm.
type QAArmMetrics struct {
	Arm                    string  `json:"arm"`
	AssignedOrigins        int     `json:"assignedOrigins"`
	TasksTotal             int     `json:"tasksTotal"`
	PRsOpened              int     `json:"prsOpened"`
	PRsMerged              int     `json:"prsMerged"`
	HeldByQAGate           int     `json:"heldByQaGate"`
	BlindEscapes           int     `json:"blindEscapes"`
	BlindEscapeRate        float64 `json:"blindEscapeRate"`
	BlindEscapeCILow       float64 `json:"blindEscapeCiLow"`
	BlindEscapeCIHigh      float64 `json:"blindEscapeCiHigh"`
	BlindReviewEscapes     int     `json:"blindReviewEscapes"`
	BlindReviewEscapeRate  float64 `json:"blindReviewEscapeRate"`
	FirstPushCIEvaluated   int     `json:"firstPushCiEvaluated"`
	FirstPushCIGreen       int     `json:"firstPushCiGreen"`
	FirstPushCIGreenRate   float64 `json:"firstPushCiGreenRate"`
	StackedFixes           int     `json:"stackedFixes"`
	StackedFixRate         float64 `json:"stackedFixRate"`
	PostMergeChurn7d       int     `json:"postMergeChurn7d"`
	PostMergeChurn7dRate   float64 `json:"postMergeChurn7dRate"`
	PromptsMeasured        int     `json:"promptsMeasured"`
	ThreadCuts             int     `json:"threadCuts"`
	ThreadCutShare         float64 `json:"threadCutShare"`
	QASkillPromptByteShare float64 `json:"qaSkillPromptByteShare"`
}

// QACheckRuleSummary aggregates runtime latency, heap allocations, catch rates, and operator
// helpful/not-helpful votes for one pre-PR check rule (S3 or S4).
type QACheckRuleSummary struct {
	RuleID               string  `json:"ruleId"`
	Stage                string  `json:"stage"`
	EnforcedRuns         int     `json:"enforcedRuns"`
	ShadowRuns           int     `json:"shadowRuns"`
	PassCount            int     `json:"passCount"`
	FailCount            int     `json:"failCount"`
	ErrorCount           int     `json:"errorCount"`
	CatchRate            float64 `json:"catchRate"`
	FindingsCount        int     `json:"findingsCount"`
	FilteredTheaterCount int     `json:"filteredTheaterCount"`
	P50DurationMS        int64   `json:"p50DurationMs"`
	P95DurationMS        int64   `json:"p95DurationMs"`
	MeanAllocBytes       int64   `json:"meanAllocBytes"`
	HelpfulVotes         int     `json:"helpfulVotes"`
	NotHelpfulVotes      int     `json:"notHelpfulVotes"`
	NotHelpfulRate       float64 `json:"notHelpfulRate"`
	NotHelpfulWilsonLB   float64 `json:"notHelpfulWilsonLb"`
}

// QAShadowRecall measures paired deterministic S3 recall on uncontaminated Control PRs.
type QAShadowRecall struct {
	ControlTasksChecked int     `json:"controlTasksChecked"`
	ShadowWouldBlock    int     `json:"shadowWouldBlock"`
	ShadowCatchRate     float64 `json:"shadowCatchRate"`
	ControlBlindEscapes int     `json:"controlBlindEscapes"`
	MatchedBlindEscapes int     `json:"matchedBlindEscapes"`
	PairedRecall        float64 `json:"pairedRecall"`
	MatchedEscapes      int     `json:"matchedEscapes"`
	MatchedRecall       float64 `json:"matchedRecall"`
}

// QACircuitBreaker reports sequential mSPRT safety status and whether an automatic rollout
// downgrade to 0% shadow-only mode is warranted.
type QACircuitBreaker struct {
	MinSampleMet         bool        `json:"minSampleMet"`
	TreatmentSample      int         `json:"treatmentSample"`
	MinRequiredSample    int         `json:"minRequiredSample"`
	ShouldAutoDowngrade  bool        `json:"shouldAutoDowngrade"`
	Reasons              []string    `json:"reasons"`
	FastLaneLatencyMSPRT MSPRTResult `json:"fastLaneLatencyMsprt"`
	BlindEscapeMSPRT     MSPRTResult `json:"blindEscapeMsprt"`
}

// QASummary is the complete single-instance A/B quality vs. overhead report served by GET /api/qa/summary.
type QASummary struct {
	CostMeasured       bool                 `json:"costMeasured"`
	Since              time.Time            `json:"since"`
	Until              time.Time            `json:"until"`
	WindowSeconds      float64              `json:"windowSeconds"`
	Repos              []string             `json:"repos,omitempty"`
	Enabled            bool                 `json:"enabled"`
	FlowID             string               `json:"flowId"`
	RolloutPct         int                  `json:"rolloutPct"`
	ShadowOnControl    bool                 `json:"shadowOnControl"`
	Epoch              int                  `json:"epoch"`
	RegimeID           int                  `json:"regimeId"`
	Treatment          QAArmMetrics         `json:"treatment"`
	Control            QAArmMetrics         `json:"control"`
	RegimeStrata       []TwoByTwoStratum    `json:"regimeStrata"`
	BlindEscapeCMH     CMHResult            `json:"blindEscapeCmh"`
	RelativeEscapeLift float64              `json:"relativeEscapeLift"`
	FisherExactP       float64              `json:"fisherExactP"`
	BarnardExactP      float64              `json:"barnardExactP"`
	CostCUPED          OnlineCUPEDResult    `json:"costCuped"`
	TokensCUPED        OnlineCUPEDResult    `json:"tokensCuped"`
	FastLaneTimeToPR   HLResult             `json:"fastLaneTimeToPrSeconds"`
	OverallTimeToPR    HLResult             `json:"overallTimeToPrSeconds"`
	OriginTimeToLand   HLResult             `json:"originTimeToLandSeconds"`
	ShadowRecall       QAShadowRecall       `json:"shadowRecall"`
	Checks             []QACheckRuleSummary `json:"checks"`
	CircuitBreaker     QACircuitBreaker     `json:"circuitBreaker"`
}

// ComputeQASummary joins sticky WorkOrigin assignments with blind post-PR every-pr.yml review
// findings, first-push CI verdicts, post-PR fix churn, Online-CUPED cost/tokens, Hodges-Lehmann
// latency shifts, Control shadow recall, and mSPRT safety guardrails.
func ComputeQASummary(in QASummaryInput) QASummary {
	epoch := in.Config.EpochOrDefault()
	flowID := in.Config.FlowIDOrDefault()
	skillByteLen := skillPromptLen(in.Config)

	repoAllow := map[string]bool{}
	for _, r := range in.Scope.Repos {
		trimmed := strings.ToLower(strings.TrimSpace(r))
		if trimmed != "" {
			repoAllow[trimmed] = true
		}
	}
	scopeByRepo := in.Scope.ReposBound || len(repoAllow) > 0
	matchesRepo := func(repo string) bool {
		if !scopeByRepo {
			return true
		}
		if len(repoAllow) == 0 {
			return false
		}
		trimmed := strings.ToLower(strings.TrimSpace(repo))
		if trimmed == "" {
			return false
		}
		return repoAllow[trimmed]
	}

	timingByTask := make(map[string]model.TaskTiming, len(in.Timings))
	taskRepoByID := make(map[string]string, len(in.Tasks)+len(in.Timings))
	for _, tm := range in.Timings {
		timingByTask[tm.TaskID] = tm
		if tm.Repo != "" {
			taskRepoByID[tm.TaskID] = tm.Repo
		}
	}

	taskByID := make(map[string]model.Task, len(in.Tasks))
	tasksByOrigin := map[string][]model.Task{}
	// Map reviewedTaskID -> whether a review task pushed fixes or opened a stacked fix PR onto it.
	reviewerFixedTask := map[string]bool{}
	rootOrigins := model.NewQARootOrigins(in.Tasks)
	for _, t := range in.Tasks {
		taskByID[t.ID] = t
		if t.Target != nil {
			taskRepoByID[t.ID] = t.Target.String()
		}
		originKey := rootOrigins.Of(t)
		if originKey != "" {
			tasksByOrigin[originKey] = append(tasksByOrigin[originKey], t)
		}
		var reviewedTarget string
		hasReviewerHead := false
		hasFixesLink := false
		for _, l := range t.Links {
			switch l.Kind {
			case model.LinkPushesReviewedBranch:
				if strings.TrimSpace(l.Target) != "" {
					reviewedTarget = strings.TrimSpace(l.Target)
				}
			case model.LinkReviewerHead:
				if strings.TrimSpace(l.Target) != "" {
					hasReviewerHead = true
				}
			case model.LinkFixes:
				if strings.TrimSpace(l.Target) != "" {
					hasFixesLink = true
				}
			}
		}
		if reviewedTarget != "" && hasReviewerHead {
			reviewerFixedTask[reviewedTarget] = true
		}
		if t.Filing.Reason == model.ReasonReview {
			if parentID := strings.TrimSpace(t.Parent()); parentID != "" {
				if hasFixesLink || timingByTask[t.ID].PrOpenedAt != nil {
					reviewerFixedTask[parentID] = true
				}
			}
		}
	}

	runsByTask := map[string][]model.RunTiming{}
	runToTask := make(map[string]string, len(in.Runs))
	for _, r := range in.Runs {
		runsByTask[r.TaskID] = append(runsByTask[r.TaskID], r)
		runToTask[r.RunID] = r.TaskID
	}

	promptByRun := make(map[string]model.RunPrompt, len(in.Prompts))
	for _, p := range in.Prompts {
		promptByRun[p.RunID] = p
	}

	usageByTask := map[string][]model.RunUsage{}
	for _, u := range in.Usages {
		tid := u.TaskID
		if tid == "" {
			tid = runToTask[u.RunID]
		}
		if tid != "" {
			usageByTask[tid] = append(usageByTask[tid], u)
		}
	}

	// Earliest finished non-recheck CI run per (lower(repo), branch ref).
	firstCIBranch := map[repoRefKey]string{}
	for _, ci := range in.CIRuns {
		if ci.Conclusion == "" || ci.Conclusion == "cancelled" || ci.Conclusion == "skipped" || ci.Recheck {
			continue
		}
		ref := strings.TrimPrefix(strings.TrimSpace(ci.Ref), "refs/heads/")
		if ref == "" {
			continue
		}
		key := repoRefKey{repo: strings.ToLower(strings.TrimSpace(ci.Repo)), ref: ref}
		if _, exists := firstCIBranch[key]; !exists {
			firstCIBranch[key] = ci.Conclusion
		}
	}

	checkRunsByTask := map[string][]model.QACheckRun{}
	for _, cr := range in.CheckRuns {
		checkRunsByTask[cr.TaskID] = append(checkRunsByTask[cr.TaskID], cr)
	}

	findingsByCheckRun := map[int64][]model.QAFinding{}
	findingsByRule := map[string][]model.QAFinding{}
	for _, f := range in.Findings {
		findingsByCheckRun[f.CheckRunID] = append(findingsByCheckRun[f.CheckRunID], f)
		findingsByRule[f.RuleID] = append(findingsByRule[f.RuleID], f)
	}

	out := QASummary{
		Since:           in.Window.Since,
		Until:           in.Window.Until,
		WindowSeconds:   in.Window.Duration().Seconds(),
		Repos:           slices.Clone(in.Scope.Repos),
		Enabled:         in.Config.Enabled,
		FlowID:          flowID,
		RolloutPct:      in.Config.RolloutPct,
		ShadowOnControl: in.Config.ShadowOnControlEnabled(),
		Epoch:           epoch,
		RegimeID:        in.Config.RegimeIDOrDefault(),
		Treatment:       QAArmMetrics{Arm: model.QAArmTreatment},
		Control:         QAArmMetrics{Arm: model.QAArmControl},
		RegimeStrata:    []TwoByTwoStratum{},
		Checks:          []QACheckRuleSummary{},
		CircuitBreaker: QACircuitBreaker{
			MinRequiredSample: MinTreatmentSampleForAutoDowngrade,
			Reasons:           []string{},
		},
	}

	strataByRegime := map[int]*TwoByTwoStratum{}
	var costSamples, tokenSamples []CUPEDSample
	var fastT, fastC []float64
	var overallPRT, overallPRC []float64
	var landT, landC []float64
	var escapeBinaryT, escapeBinaryC []float64
	var treatPromptBytesSum int64

	inWindow := func(assign model.QAAssignment, originTasks []model.Task) bool {
		if in.Window.Duration() <= 0 {
			return true
		}
		return in.Window.holds(&assign.AssignedAt)
	}

	for _, assign := range in.Assignments {
		if assign.Epoch != epoch {
			continue
		}
		if assign.Arm != model.QAArmTreatment && assign.Arm != model.QAArmControl {
			continue
		}
		rawOriginTasks := tasksByOrigin[assign.RootOrigin]
		scopedTasks := rawOriginTasks
		if scopeByRepo {
			scopedTasks = make([]model.Task, 0, len(rawOriginTasks))
			for _, t := range rawOriginTasks {
				if matchesRepo(taskRepoByID[t.ID]) {
					scopedTasks = append(scopedTasks, t)
				}
			}
			if len(scopedTasks) == 0 {
				continue
			}
		}
		if !inWindow(assign, scopedTasks) {
			continue
		}

		armStats := &out.Control
		if assign.Arm == model.QAArmTreatment {
			armStats = &out.Treatment
		}
		armStats.AssignedOrigins++

		st, exists := strataByRegime[assign.RegimeID]
		if !exists {
			st = &TwoByTwoStratum{
				RegimeID:   assign.RegimeID,
				RolloutPct: assign.RolloutPctAtAssign,
			}
			strataByRegime[assign.RegimeID] = st
		}
		if assign.Arm == model.QAArmTreatment {
			st.TreatN++
		} else {
			st.CtrlN++
		}

		var (
			originHasBlindEscape  bool
			originHasReviewEscape bool
			originHasStackedFix   bool
			originHasChurn7d      bool
			originHeldByQA        bool
			codingCostUSD         float64
			codingBillableTokens  float64
			s4Tokens              float64
			rootBodyBytes         int
			earliestApproved      *time.Time
			latestMerged          *time.Time
			eligibleCount         int
			allOpenedMerged       = true
			anyMerged             bool
		)

		// Attribute full WorkOrigin compute cost (including review, re-review, and S4 verification)
		// across all scoped tasks in this WorkOrigin before filtering PR denominators.
		for _, t := range scopedTasks {
			for _, u := range usageByTask[t.ID] {
				if isFinite(u.CostUSD) && u.CostUSD > 0 {
					codingCostUSD += u.CostUSD
				}
				if b := u.Billable(); b > 0 {
					codingBillableTokens += float64(b)
				}
			}
			for _, cr := range checkRunsByTask[t.ID] {
				if !cr.Shadow && cr.Stage == "S4" {
					s4Tokens += float64(max(int64(0), cr.TokensIn)) + float64(max(int64(0), cr.TokensOut))
				}
			}
		}

		for _, t := range scopedTasks {
			if !TaskEligibleForQARolloutMetric(t) {
				continue
			}
			eligibleCount++
			armStats.TasksTotal++
			if rootBodyBytes == 0 {
				rootBodyBytes = len(t.Body) + len(t.Title)
			}

			tm := timingByTask[t.ID]
			if tm.ApprovedAt != nil && (earliestApproved == nil || tm.ApprovedAt.Before(*earliestApproved)) {
				earliestApproved = tm.ApprovedAt
			} else if tm.CreatedAt != nil && earliestApproved == nil {
				earliestApproved = tm.CreatedAt
			}
			if tm.PrOpenedAt != nil {
				armStats.PRsOpened++
			}
			if tm.PrMergedAt != nil {
				armStats.PRsMerged++
				anyMerged = true
				if latestMerged == nil || tm.PrMergedAt.After(*latestMerged) {
					latestMerged = tm.PrMergedAt
				}
			} else {
				allOpenedMerged = false
			}

			taskReviewEscape := reviewerFixedTask[t.ID]
			hasLinkQAHold := false
			hasGateQA := false
			for _, l := range t.Links {
				switch l.Kind {
				case model.LinkQAHold:
					hasLinkQAHold = true
				case model.LinkReviewFindings:
					if rec, ok := model.ParseReviewFindingsRecord(l.Target); ok {
						if rec.Gate == model.GateQA {
							hasGateQA = true
						} else {
							taskReviewEscape = true
						}
					}
				case model.LinkReviewQuestions, model.LinkRelayedReviewThread, model.LinkDraftReview:
					taskReviewEscape = true
				}
			}
			// Distinguish transient in-flight LinkQAHold (awaiting 1-round S4 on an open task)
			// from unresolved QA holds (GateQA after retry cap or terminal closed/completed task still held).
			unresolvedQAHold := hasGateQA || (hasLinkQAHold && (tm.ClosedAt != nil || tm.CompletedAt != nil))
			if unresolvedQAHold {
				originHeldByQA = true
				originHasBlindEscape = true
			}
			if taskReviewEscape {
				originHasReviewEscape = true
				originHasBlindEscape = true
			}
			if tm.FixTaskFiled {
				originHasStackedFix = true
				originHasBlindEscape = true
			}
			if tm.PrMergedAt != nil && tm.MergeQueueRepairAt != nil && (tm.PrOpenedAt == nil || !tm.MergeQueueRepairAt.Before(*tm.PrOpenedAt)) {
				originHasChurn7d = true
			}

			// Check 7-day post-merge follow-up repair tasks in the same WorkOrigin filed after PrMergedAt.
			if tm.PrMergedAt != nil {
				horizon := tm.PrMergedAt.Add(7 * 24 * time.Hour)
				for _, other := range scopedTasks {
					if other.ID == t.ID {
						continue
					}
					otm := timingByTask[other.ID]
					if otm.CreatedAt != nil && otm.CreatedAt.After(*tm.PrMergedAt) && !otm.CreatedAt.After(horizon) {
						lowerTitle := strings.ToLower(other.Title)
						if strings.HasPrefix(lowerTitle, "fix") || strings.Contains(lowerTitle, "regression") || strings.Contains(lowerTitle, "repair") {
							originHasChurn7d = true
							originHasStackedFix = true
							originHasBlindEscape = true
						}
					}
				}
			}

			// First-push CI verdict on this task's branch.
			taskCI := firstTaskCIConclusion(t, tm, firstCIBranch)
			taskFirstCIFailed := false
			if taskCI != "" {
				armStats.FirstPushCIEvaluated++
				if taskCI == "success" {
					armStats.FirstPushCIGreen++
				} else {
					taskFirstCIFailed = true
					originHasBlindEscape = true
				}
			}

			// Control Shadow-Twin paired recall evaluation.
			if assign.Arm == model.QAArmControl {
				shadowRan := false
				shadowBlocked := false
				for _, cr := range checkRunsByTask[t.ID] {
					if cr.Shadow {
						shadowRan = true
						if cr.Verdict == "fail" {
							shadowBlocked = true
						}
					}
				}
				if shadowRan {
					out.ShadowRecall.ControlTasksChecked++
					taskBlindEscaped := taskReviewEscape || tm.FixTaskFiled || taskFirstCIFailed
					if shadowBlocked {
						out.ShadowRecall.ShadowWouldBlock++
					}
					if taskBlindEscaped {
						out.ShadowRecall.ControlBlindEscapes++
						if shadowBlocked {
							out.ShadowRecall.MatchedBlindEscapes++
						}

						var shadowFindings []model.QAFinding
						for _, cr := range checkRunsByTask[t.ID] {
							if cr.Shadow && cr.Verdict == "fail" {
								shadowFindings = append(shadowFindings, findingsByCheckRun[cr.ID]...)
							}
						}
						var escapeFindings []model.QAFinding
						for _, l := range t.Links {
							switch l.Kind {
							case model.LinkReviewFindings:
								if rec, ok := model.ParseReviewFindingsRecord(l.Target); ok && rec.Gate != model.GateQA && rec.ReviewTask != "" {
									for _, cr := range checkRunsByTask[rec.ReviewTask] {
										escapeFindings = append(escapeFindings, findingsByCheckRun[cr.ID]...)
									}
								}
							case model.LinkReviewQuestions, model.LinkRelayedReviewThread, model.LinkDraftReview:
								rtID := strings.TrimSpace(l.Target)
								if rtID != "" {
									for _, cr := range checkRunsByTask[rtID] {
										escapeFindings = append(escapeFindings, findingsByCheckRun[cr.ID]...)
									}
								}
							}
						}

						matchedAny := false
						if len(escapeFindings) == 0 && (tm.FixTaskFiled || taskFirstCIFailed) && shadowBlocked {
							// For non-review escapes where specific findings are absent but task escaped and shadow blocked,
							// we consider it a match if shadow found something, but wait: the prompt says
							// "Match shadow check findings against blind-review escape findings on the same PR by file + line within +-15 lines...
							// instead of mere PR co-occurrence!"
							// So if there are no escape findings, it's NOT a match.
						}
						for _, ef := range escapeFindings {
							for _, sf := range shadowFindings {
								if sf.FilePath != "" && ef.FilePath != "" && sf.FilePath == ef.FilePath {
									lineMatch := false
									if sf.Line > 0 && ef.Line > 0 {
										lineMatch = math.Abs(float64(sf.Line-ef.Line)) <= 15
									} else {
										lineMatch = true
									}
									if lineMatch {
										matchedAny = true
										break
									}
								}
							}
							if matchedAny {
								break
							}
						}
						if matchedAny {
							out.ShadowRecall.MatchedEscapes++
						}
					}
				}
			}

			// Prompt & ThreadCut metrics across runs of t.
			tRuns := runsByTask[t.ID]
			var firstRunStart *time.Time
			for _, r := range tRuns {
				if !r.StartedAt.IsZero() && (firstRunStart == nil || r.StartedAt.Before(*firstRunStart)) {
					ts := r.StartedAt
					firstRunStart = &ts
				}
				if p, hasP := promptByRun[r.RunID]; hasP && p.Bytes > 0 {
					armStats.PromptsMeasured++
					if assign.Arm == model.QAArmTreatment {
						treatPromptBytesSum += int64(p.Bytes)
					}
					if p.ThreadCut {
						armStats.ThreadCuts++
					}
				}
			}

			// Time-to-Open-PR latency per task.
			if tm.PrOpenedAt != nil && firstRunStart != nil && !tm.PrOpenedAt.Before(*firstRunStart) {
				sec := tm.PrOpenedAt.Sub(*firstRunStart).Seconds()
				isFastLane := isFastLaneTask(assign.ComplexityTier, checkRunsByTask[t.ID])
				if assign.Arm == model.QAArmTreatment {
					overallPRT = append(overallPRT, sec)
					if isFastLane {
						fastT = append(fastT, sec)
					}
				} else {
					overallPRC = append(overallPRC, sec)
					if isFastLane {
						fastC = append(fastC, sec)
					}
				}
			}
		}
		if eligibleCount == 0 {
			allOpenedMerged = false
		}

		originBillableTokens := codingBillableTokens + s4Tokens
		var originCostUSD float64
		if codingCostUSD > 0 {
			originCostUSD = codingCostUSD
			if codingBillableTokens > 0 && s4Tokens > 0 {
				originCostUSD += s4Tokens * (codingCostUSD / codingBillableTokens)
			}
		}

		if originHeldByQA {
			armStats.HeldByQAGate++
		}
		if originHasBlindEscape {
			armStats.BlindEscapes++
			if assign.Arm == model.QAArmTreatment {
				st.TreatEvents++
				escapeBinaryT = append(escapeBinaryT, 1)
			} else {
				st.CtrlEvents++
				escapeBinaryC = append(escapeBinaryC, 1)
			}
		} else {
			if assign.Arm == model.QAArmTreatment {
				escapeBinaryT = append(escapeBinaryT, 0)
			} else {
				escapeBinaryC = append(escapeBinaryC, 0)
			}
		}
		if originHasReviewEscape {
			armStats.BlindReviewEscapes++
		}
		if originHasStackedFix {
			armStats.StackedFixes++
		}
		if originHasChurn7d {
			armStats.PostMergeChurn7d++
		}

		if anyMerged && allOpenedMerged && earliestApproved != nil && latestMerged != nil && !latestMerged.Before(*earliestApproved) {
			landSec := latestMerged.Sub(*earliestApproved).Seconds()
			if assign.Arm == model.QAArmTreatment {
				landT = append(landT, landSec)
			} else {
				landC = append(landC, landSec)
			}
		}

		// Pre-dispatch covariate X_i: deterministic complexity tier score + log2(1 + spec bytes).
		covX := complexityCovariate(assign.ComplexityTier, rootBodyBytes)
		if originBillableTokens > 0 || originCostUSD > 0 {
			tokenSamples = append(tokenSamples, CUPEDSample{
				Arm: assign.Arm,
				X:   covX,
				Y:   originBillableTokens,
				At:  assign.AssignedAt,
			})
			costSamples = append(costSamples, CUPEDSample{
				Arm: assign.Arm,
				X:   covX,
				Y:   originCostUSD,
				At:  assign.AssignedAt,
			})
		}
	}

	finalizeArmRates(&out.Treatment, skillByteLen, treatPromptBytesSum)
	finalizeArmRates(&out.Control, 0, 0)

	if out.ShadowRecall.ControlTasksChecked > 0 {
		out.ShadowRecall.ShadowCatchRate = cleanFloat(float64(out.ShadowRecall.ShadowWouldBlock) / float64(out.ShadowRecall.ControlTasksChecked))
	}
	if out.ShadowRecall.ControlBlindEscapes > 0 {
		out.ShadowRecall.PairedRecall = cleanFloat(float64(out.ShadowRecall.MatchedBlindEscapes) / float64(out.ShadowRecall.ControlBlindEscapes))
		out.ShadowRecall.MatchedRecall = cleanFloat(float64(out.ShadowRecall.MatchedEscapes) / float64(out.ShadowRecall.ControlBlindEscapes))
	}

	regimeIDs := make([]int, 0, len(strataByRegime))
	for id := range strataByRegime {
		regimeIDs = append(regimeIDs, id)
	}
	sort.Ints(regimeIDs)
	for _, id := range regimeIDs {
		out.RegimeStrata = append(out.RegimeStrata, *strataByRegime[id])
	}

	out.BlindEscapeCMH = CMHStratified(out.RegimeStrata)
	out.FisherExactP = FisherExactTwoSided(
		out.Treatment.BlindEscapes, out.Treatment.AssignedOrigins,
		out.Control.BlindEscapes, out.Control.AssignedOrigins,
	)
	out.BarnardExactP = BarnardExactTwoSided(
		out.Treatment.BlindEscapes, out.Treatment.AssignedOrigins,
		out.Control.BlindEscapes, out.Control.AssignedOrigins,
	)
	if out.Control.BlindEscapeRate > 1e-9 && out.Treatment.AssignedOrigins > 0 {
		// Positive lift means Treatment reduced blind post-PR escapes relative to Control.
		out.RelativeEscapeLift = cleanFloat((out.Control.BlindEscapeRate - out.Treatment.BlindEscapeRate) / out.Control.BlindEscapeRate)
	}

	out.TokensCUPED = OnlineCUPED(tokenSamples)
	var totalCost float64
	for _, s := range costSamples {
		totalCost += s.Y
	}
	if totalCost > 0 {
		out.CostMeasured = true
		out.CostCUPED = OnlineCUPED(costSamples)
	} else {
		out.CostMeasured = false
		out.CostCUPED = OnlineCUPEDResult{} // Empty
	}

	out.FastLaneTimeToPR = HodgesLehmannShift(fastT, fastC)
	out.OverallTimeToPR = HodgesLehmannShift(overallPRT, overallPRC)
	out.OriginTimeToLand = HodgesLehmannShift(landT, landC)

	out.Checks = summarizeCheckRules(in.CheckRuns, findingsByCheckRun, findingsByRule, scopeByRepo, matchesRepo, taskRepoByID, in.Window)

	// Sequential mSPRT & automatic safety circuit-breaker evaluation.
	fastMSPRT := NormalMixtureSPRT(fastT, fastC, 0, 0.05)
	escapeMSPRT := NormalMixtureSPRT(escapeBinaryT, escapeBinaryC, 0.20, 0.01)
	cb := QACircuitBreaker{
		MinSampleMet:         out.Treatment.AssignedOrigins >= MinTreatmentSampleForAutoDowngrade,
		TreatmentSample:      out.Treatment.AssignedOrigins,
		MinRequiredSample:    MinTreatmentSampleForAutoDowngrade,
		Reasons:              []string{},
		FastLaneLatencyMSPRT: fastMSPRT,
		BlindEscapeMSPRT:     escapeMSPRT,
	}
	if cb.MinSampleMet {
		// Auto-downgrade ONLY on explicit-alpha (alpha = 0.01) one-sided mSPRT harm (blind escapes).
		if escapeMSPRT.Significant && escapeMSPRT.MeanDiff > 0 {
			cb.ShouldAutoDowngrade = true
			cb.Reasons = append(cb.Reasons, "Blind escape rate regressed with sequential mSPRT p < 0.01")
		}

		// Heuristic findings/co-occurrence signals are notify-only.
		if out.FastLaneTimeToPR.RelativeShift > 0.15 && fastMSPRT.Significant && fastMSPRT.MeanDiff > 0 {
			cb.Reasons = append(cb.Reasons, "T0/T1 fast-lane median Time-to-PR regressed > +15% with sequential mSPRT p < 0.05")
		}
		if out.Treatment.PromptsMeasured >= 10 && out.Control.PromptsMeasured >= 10 &&
			(out.Treatment.ThreadCutShare-out.Control.ThreadCutShare) > 0.05 {
			cb.Reasons = append(cb.Reasons, "Treatment ThreadCut truncation rate exceeded Control by > +5.0 percentage points")
		}
		for _, chk := range out.Checks {
			votes := chk.HelpfulVotes + chk.NotHelpfulVotes
			if votes >= 5 && chk.NotHelpfulWilsonLB > 0.10 {
				cb.Reasons = append(cb.Reasons, "Rule "+chk.RuleID+" human 'Not helpful' 95% Wilson lower bound exceeded 10%")
			}
		}
	}
	out.CircuitBreaker = cb
	return out
}

// TaskEligibleForQARolloutMetric excludes interactive chats, priority-dispatch/durable sessions, path-trigger tasks,
// and review sub-tasks from PR rollout denominators.
func TaskEligibleForQARolloutMetric(t model.Task) bool {
	if t.Interactive || t.HasPriorityDispatch() {
		return false
	}
	if t.Filing.Reason == model.ReasonReview || slices.Contains(t.Tags, model.PathTriggerMarkerTag) {
		return false
	}
	for _, l := range t.Links {
		switch l.Kind {
		case model.LinkPushesReviewedBranch, model.LinkReviewCommentsOnly, model.LinkReviewsPullRequest:
			return false
		}
	}
	return true
}

func finalizeArmRates(a *QAArmMetrics, skillBytes int, promptBytesSum int64) {
	if a.AssignedOrigins > 0 {
		n := float64(a.AssignedOrigins)
		a.BlindEscapeRate = cleanFloat(float64(a.BlindEscapes) / n)
		a.BlindEscapeCILow, a.BlindEscapeCIHigh = WilsonCI95(a.BlindEscapes, a.AssignedOrigins)
		a.BlindReviewEscapeRate = cleanFloat(float64(a.BlindReviewEscapes) / n)
		a.StackedFixRate = cleanFloat(float64(a.StackedFixes) / n)
	}
	if a.FirstPushCIEvaluated > 0 {
		a.FirstPushCIGreenRate = cleanFloat(float64(a.FirstPushCIGreen) / float64(a.FirstPushCIEvaluated))
	}
	if a.PRsMerged > 0 {
		a.PostMergeChurn7dRate = cleanFloat(float64(a.PostMergeChurn7d) / float64(a.PRsMerged))
	}
	if a.PromptsMeasured > 0 {
		a.ThreadCutShare = cleanFloat(float64(a.ThreadCuts) / float64(a.PromptsMeasured))
		if skillBytes > 0 && promptBytesSum > 0 {
			totalSkill := float64(skillBytes) * float64(a.PromptsMeasured)
			a.QASkillPromptByteShare = cleanFloat(math.Min(1.0, totalSkill/float64(promptBytesSum)))
		}
	}
}

type repoRefKey struct {
	repo string
	ref  string
}

func firstTaskCIConclusion(t model.Task, tm model.TaskTiming, firstCIBranch map[repoRefKey]string) string {
	repo := tm.Repo
	if repo == "" && t.Target != nil {
		repo = t.Target.String()
	}
	if repo == "" {
		return ""
	}
	branch := t.BranchName()
	if branch == "" {
		return ""
	}
	return firstCIBranch[repoRefKey{repo: strings.ToLower(strings.TrimSpace(repo)), ref: branch}]
}

func isFastLaneTask(complexityTier string, checkRuns []model.QACheckRun) bool {
	for _, r := range checkRuns {
		if r.Shadow || r.Stage != "S3" || r.DetailJSON == "" {
			continue
		}
		var meta struct {
			BlastTier string `json:"blastTier"`
		}
		if json.Unmarshal([]byte(r.DetailJSON), &meta) == nil && meta.BlastTier != "" {
			bt := qa.BlastTier(meta.BlastTier)
			return !bt.RequiresS4()
		}
	}
	return complexityTier == model.QAComplexityLow
}

func complexityCovariate(tier string, specBytes int) float64 {
	base := 2.0
	switch tier {
	case model.QAComplexityLow:
		base = 1.0
	case model.QAComplexityHigh:
		base = 3.5
	}
	return base + 0.25*math.Log2(1.0+float64(max(0, specBytes)))
}

func summarizeCheckRules(
	runs []model.QACheckRun,
	findingsByCheckRun map[int64][]model.QAFinding,
	findingsByRule map[string][]model.QAFinding,
	scopeByRepo bool,
	matchesRepo func(string) bool,
	taskRepoByID map[string]string,
	win Window,
) []QACheckRuleSummary {
	type ruleAcc struct {
		ruleID     string
		stage      string
		enforced   int
		shadow     int
		pass       int
		fail       int
		errored    int
		findings   int
		filteredS4 int
		durations  []int64
		allocSum   float64
		allocCount int64
	}
	byRule := map[string]*ruleAcc{}
	includedCheckRunIDs := make(map[int64]bool, len(runs))

	for _, r := range runs {
		if scopeByRepo && !matchesRepo(taskRepoByID[r.TaskID]) {
			continue
		}
		if win.Duration() > 0 && !win.holds(&r.CreatedAt) {
			continue
		}
		if r.ID > 0 {
			includedCheckRunIDs[r.ID] = true
		}
		acc, ok := byRule[r.CheckID]
		if !ok {
			acc = &ruleAcc{ruleID: r.CheckID, stage: r.Stage}
			byRule[r.CheckID] = acc
		}
		if r.Shadow {
			acc.shadow++
		} else {
			acc.enforced++
		}
		switch r.Verdict {
		case "pass":
			acc.pass++
		case "fail":
			acc.fail++
		default:
			acc.errored++
		}
		acc.findings += len(findingsByCheckRun[r.ID])
		if r.DurationMS >= 0 {
			acc.durations = append(acc.durations, r.DurationMS)
		}
		if r.AllocBytes > 0 {
			acc.allocSum += float64(r.AllocBytes)
			acc.allocCount++
		}
		if r.Stage == "S4" && r.DetailJSON != "" {
			var meta struct {
				FilteredTheaterCount int `json:"filteredTheaterCount"`
			}
			if json.Unmarshal([]byte(r.DetailJSON), &meta) == nil && meta.FilteredTheaterCount > 0 {
				acc.filteredS4 += meta.FilteredTheaterCount
			}
		}
	}

	keys := make([]string, 0, len(byRule))
	for k := range byRule {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]QACheckRuleSummary, 0, len(keys))
	for _, k := range keys {
		acc := byRule[k]
		total := acc.enforced + acc.shadow
		catchRate := 0.0
		if total > 0 {
			catchRate = cleanFloat(float64(acc.fail) / float64(total))
		}
		sort.Slice(acc.durations, func(i, j int) bool { return acc.durations[i] < acc.durations[j] })
		p50 := percentileInt64(acc.durations, 0.50)
		p95 := percentileInt64(acc.durations, 0.95)
		meanAlloc := int64(0)
		if acc.allocCount > 0 {
			meanAlloc = int64(math.Round(acc.allocSum / float64(acc.allocCount)))
		}

		var upVotes, downVotes int
		for _, f := range findingsByRule[k] {
			if f.CheckRunID > 0 {
				if !includedCheckRunIDs[f.CheckRunID] {
					continue
				}
			} else {
				if scopeByRepo && !matchesRepo(taskRepoByID[f.TaskID]) {
					continue
				}
				if win.Duration() > 0 && !win.holds(&f.CreatedAt) {
					continue
				}
			}
			if f.HumanVote > 0 {
				upVotes++
			} else if f.HumanVote < 0 {
				downVotes++
			}
		}
		voteTotal := upVotes + downVotes
		notHelpfulRate := 0.0
		notHelpfulLB := 0.0
		if voteTotal > 0 {
			notHelpfulRate = cleanFloat(float64(downVotes) / float64(voteTotal))
			notHelpfulLB, _ = WilsonCI95(downVotes, voteTotal)
		}

		out = append(out, QACheckRuleSummary{
			RuleID:               acc.ruleID,
			Stage:                acc.stage,
			EnforcedRuns:         acc.enforced,
			ShadowRuns:           acc.shadow,
			PassCount:            acc.pass,
			FailCount:            acc.fail,
			ErrorCount:           acc.errored,
			CatchRate:            catchRate,
			FindingsCount:        acc.findings,
			FilteredTheaterCount: acc.filteredS4,
			P50DurationMS:        p50,
			P95DurationMS:        p95,
			MeanAllocBytes:       meanAlloc,
			HelpfulVotes:         upVotes,
			NotHelpfulVotes:      downVotes,
			NotHelpfulRate:       notHelpfulRate,
			NotHelpfulWilsonLB:   notHelpfulLB,
		})
	}
	return out
}

func percentileInt64(sorted []int64, q float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func skillPromptLen(c model.QAConfig) int {
	flow, ok := qa.LookupFlow(c.FlowIDOrDefault())
	if !ok || !flow.InjectSkill {
		return 0
	}
	return len(flow.SkillPrompt())
}
