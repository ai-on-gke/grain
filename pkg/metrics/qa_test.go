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

package metrics_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCMHStratifiedPreventsSimpsonsParadoxAcrossRolloutShifts(t *testing.T) {
	// Regime 1 (20% rollout, low-difficulty week):
	//   Treatment: 1 / 10 (10.0% escape rate) vs Control: 8 / 40 (20.0% escape rate) -> Treatment better (-10 pp)
	// Regime 2 (80% rollout, high-difficulty week):
	//   Treatment: 16 / 40 (40.0% escape rate) vs Control: 6 / 10 (60.0% escape rate) -> Treatment better (-20 pp)
	// Naive pooled rates:
	//   Treatment = 17 / 50 = 34.0% vs Control = 14 / 50 = 28.0% (+6 pp, WRONG sign due to Simpson's paradox!).
	strata := []metrics.TwoByTwoStratum{
		{RegimeID: 1, RolloutPct: 20, TreatEvents: 1, TreatN: 10, CtrlEvents: 8, CtrlN: 40},
		{RegimeID: 2, RolloutPct: 80, TreatEvents: 16, TreatN: 40, CtrlEvents: 6, CtrlN: 10},
	}

	cmh := metrics.CMHStratified(strata)
	if cmh.Strata != 2 {
		t.Fatalf("Strata = %d, want 2", cmh.Strata)
	}
	if cmh.RiskDiff >= -0.12 {
		t.Fatalf("CMH RiskDiff = %.4f, want negative (< -0.12) reflecting true within-regime improvement", cmh.RiskDiff)
	}
	if cmh.OddsRatio >= 0.60 {
		t.Fatalf("CMH OddsRatio = %.4f, want < 0.60", cmh.OddsRatio)
	}
}

func TestOnlineCUPEDIsPreFiltratedAndReducesVariance(t *testing.T) {
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	var prefix []metrics.CUPEDSample
	// Use ABBA counterbalanced assignment with period-5 covariate X so pre-dispatch covariate X
	// has zero linear-trend confounding during burn-in and overall, matching randomized assignment.
	for i := 0; i < 40; i++ {
		arm := model.QAArmControl
		armShift := 0.0
		if i%4 == 1 || i%4 == 2 {
			arm = model.QAArmTreatment
			armShift = 2.0
		}
		x := float64((i%5)+1) * 10.0
		noise := float64((i%3)-1) * 0.5
		y := 100.0 + 8.0*x + armShift + noise
		prefix = append(prefix, metrics.CUPEDSample{
			Arm: arm,
			X:   x,
			Y:   y,
			At:  base.Add(time.Duration(i) * time.Minute),
		})
	}

	resPrefix := metrics.OnlineCUPED(prefix)
	if resPrefix.VarianceReduction < 0.75 {
		t.Fatalf("OnlineCUPED VarianceReduction = %.3f, want >= 0.75 on correlated covariate", resPrefix.VarianceReduction)
	}
	if math.Abs(resPrefix.AdjDelta-2.0) > 2.5 {
		t.Fatalf("OnlineCUPED AdjDelta = %.3f, want near true +2.0", resPrefix.AdjDelta)
	}

	// True martingale / non-anticipating check: appending an extreme future outlier at t = 40
	// must NEVER alter any already-emitted score Y*_0..Y*_39 when OnlineCUPEDScores runs over the full stream.
	scoresPrefix := metrics.OnlineCUPEDScores(prefix)
	withFuture := append([]metrics.CUPEDSample(nil), prefix...)
	withFuture = append(withFuture, metrics.CUPEDSample{
		Arm: model.QAArmControl,
		X:   9999,
		Y:   9999999,
		At:  base.Add(40 * time.Minute),
	})
	scoresFull := metrics.OnlineCUPEDScores(withFuture)
	if len(scoresFull) != len(scoresPrefix)+1 {
		t.Fatalf("len(scoresFull) = %d, want %d", len(scoresFull), len(scoresPrefix)+1)
	}
	for i := range scoresPrefix {
		if scoresFull[i] != scoresPrefix[i] {
			t.Fatalf("score[%d] mutated after future outlier: got %+v, want %+v", i, scoresFull[i], scoresPrefix[i])
		}
	}
}

func TestExactTestsHodgesLehmannAndMSPRTJSONSafety(t *testing.T) {
	lo, hi := metrics.WilsonCI95(0, 0)
	if lo != 0 || hi != 0 {
		t.Fatalf("empty WilsonCI95 = (%v, %v), want (0, 0)", lo, hi)
	}
	pFisher := metrics.FisherExactTwoSided(1, 25, 11, 25)
	pBarnard := metrics.BarnardExactTwoSided(1, 25, 11, 25)
	if pFisher >= 0.005 || pBarnard >= 0.005 {
		t.Fatalf("expected strong significance for 1/25 vs 11/25: fisher=%v barnard=%v", pFisher, pBarnard)
	}

	hl := metrics.HodgesLehmannShift(
		[]float64{101, 102, 103, 104, 105, 106},
		[]float64{100, 101, 102, 103, 104, 105},
	)
	if math.Abs(hl.Shift-1.0) > 0.01 {
		t.Fatalf("HodgesLehmannShift = %+v, want shift ~ 1.0", hl)
	}

	sprt := metrics.NormalMixtureSPRT(
		[]float64{200, 210, 205, 215, 220, 208, 212, 218, 204, 216, 211, 209, 214, 217, 213},
		[]float64{100, 102, 98, 101, 99, 103, 100, 97, 102, 101, 99, 100, 102, 98, 101},
		15.0, 0.05,
	)
	if !sprt.Significant || sprt.PSeq >= 0.05 {
		t.Fatalf("NormalMixtureSPRT = %+v, want Significant=true and PSeq < 0.05", sprt)
	}

	if _, err := json.Marshal(struct {
		HL   metrics.HLResult
		SPRT metrics.MSPRTResult
	}{hl, sprt}); err != nil {
		t.Fatalf("json.Marshal failed (NaN/Inf leak): %v", err)
	}
}

func TestComputeQASummaryEndToEndITTShadowAndCircuitBreaker(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	win := metrics.Window{Since: now.Add(-24 * time.Hour), Until: now.Add(time.Hour)}
	repo := model.RepoRef{Owner: "acme", Name: "kernel"}
	shadowTrue := true

	cfg := model.QAConfig{
		Enabled:         true,
		FlowID:          model.QAFlowGoldenV1Minimal,
		RolloutPct:      50,
		ShadowOnControl: &shadowTrue,
		Epoch:           1,
		RegimeID:        1,
	}

	var assignments []model.QAAssignment
	var tasks []model.Task
	var timings []model.TaskTiming
	var runs []model.RunTiming
	var prompts []model.RunPrompt
	var usages []model.RunUsage
	var ciRuns []model.CIRunOutcome
	var checkRuns []model.QACheckRun
	var findings []model.QAFinding

	var nextCheckRunID int64 = 1
	var nextFindingID int64 = 1

	// Seed 16 Treatment origins and 16 Control origins (ITT = 16 per arm).
	for i := 1; i <= 16; i++ {
		// --- Treatment task ---
		tID := fmt.Sprintf("%d", i)
		tOrigin := "task:" + tID
		assignedAt := now.Add(-6*time.Hour + time.Duration(i)*time.Minute)
		assignments = append(assignments, model.QAAssignment{
			RootOrigin:         tOrigin,
			Epoch:              1,
			RegimeID:           1,
			Arm:                model.QAArmTreatment,
			FlowID:             model.QAFlowGoldenV1Minimal,
			RolloutPctAtAssign: 50,
			ComplexityTier:     model.QAComplexityLow,
			AssignedAt:         assignedAt,
		})

		tCreated := assignedAt.Add(-time.Minute)
		tStart := assignedAt
		tPROpen := assignedAt.Add(30 * time.Second)
		tMerged := assignedAt.Add(3 * time.Minute)

		tTask := model.Task{
			ID:         tID,
			Title:      "Implement scheduler tick " + tID,
			Body:       "Add tick counter and boundary tests.",
			Target:     &repo,
			WorkOrigin: model.WorkOrigin(tOrigin),
			CreatedAt:  &tCreated,
			ApprovedAt: &tCreated,
		}
		// Origin 16 is held by GateQA (never merged, no blind escape) -> must remain in ITT AssignedOrigins!
		if i == 16 {
			tTask.Links = append(tTask.Links, model.ReviewFindingsRecord{
				Gate:    model.GateQA,
				Summary: "pre-PR S3 held after retry cap",
				HeadSHA: "deadbeef",
				At:      tPROpen,
			}.Link())
			tMerged = time.Time{}
		}
		tasks = append(tasks, tTask)

		var mergedPtr *time.Time
		if !tMerged.IsZero() {
			m := tMerged
			mergedPtr = &m
		}
		timings = append(timings, model.TaskTiming{
			TaskID:     tID,
			CreatedAt:  &tCreated,
			ApprovedAt: &tCreated,
			PrOpenedAt: &tPROpen,
			PrMergedAt: mergedPtr,
			Targeted:   true,
			Repo:       repo.String(),
		})
		runID := "run-t-" + tID
		runs = append(runs, model.RunTiming{
			RunID:     runID,
			TaskID:    tID,
			Attempt:   1,
			StartedAt: tStart,
		})
		prompts = append(prompts, model.RunPrompt{
			RunID:     runID,
			Bytes:     80_000,
			ThreadCut: false,
		})
		usages = append(usages, model.RunUsage{
			RunID:        runID,
			TaskID:       tID,
			InputTokens:  12_000,
			OutputTokens: 2_000,
			CostUSD:      0.06,
			StartedAt:    tStart,
			FinishedAt:   tPROpen,
		})
		ciRuns = append(ciRuns, model.CIRunOutcome{
			ID:         "ci-t-" + tID,
			Repo:       repo.String(),
			Ref:        tTask.BranchName(),
			Conclusion: "success",
			StartedAt:  &tPROpen,
			FinishedAt: &tPROpen,
		})

		crID := nextCheckRunID
		nextCheckRunID++
		verdict := "pass"
		if i <= 4 {
			verdict = "fail" // 4 pre-PR catches fixed in-turn!
		}
		checkRuns = append(checkRuns, model.QACheckRun{
			ID:         crID,
			TaskID:     tID,
			RunID:      runID,
			Stage:      "S3",
			CheckID:    "golden:strict-config-decoder",
			Arm:        model.QAArmTreatment,
			Shadow:     false,
			Verdict:    verdict,
			DurationMS: int64(10 + i),
			AllocBytes: 4096,
			DetailJSON: `{"blastTier":"T1"}`,
			CreatedAt:  tPROpen,
		})
		if verdict == "fail" {
			findings = append(findings, model.QAFinding{
				ID:         nextFindingID,
				CheckRunID: crID,
				TaskID:     tID,
				Stage:      "S3",
				RuleID:     "golden:strict-config-decoder",
				Severity:   "blocking",
				Summary:    "missing KnownFields(true)",
				HumanVote:  1,
				CreatedAt:  tPROpen,
			})
			nextFindingID++
		}

		// --- Control task ---
		cID := fmt.Sprintf("%d", 100+i)
		cOrigin := "task:" + cID
		cAssignAt := assignedAt.Add(15 * time.Second)
		assignments = append(assignments, model.QAAssignment{
			RootOrigin:         cOrigin,
			Epoch:              1,
			RegimeID:           1,
			Arm:                model.QAArmControl,
			FlowID:             model.QAFlowGoldenV1Minimal,
			RolloutPctAtAssign: 50,
			ComplexityTier:     model.QAComplexityLow,
			AssignedAt:         cAssignAt,
		})
		cStart := cAssignAt
		cPROpen := cAssignAt.Add(29 * time.Second)
		cMerged := cAssignAt.Add(4 * time.Minute)
		cTask := model.Task{
			ID:         cID,
			Title:      "Implement config loader " + cID,
			Body:       "Add YAML loader.",
			Target:     &repo,
			WorkOrigin: model.WorkOrigin(cOrigin),
			CreatedAt:  &cStart,
			ApprovedAt: &cStart,
		}
		// 8 of 16 Control origins suffer blind post-PR every-pr.yml escapes!
		controlEscaped := i <= 8
		if controlEscaped {
			cTask.Links = append(cTask.Links, model.ReviewFindingsRecord{
				Gate:       model.GateReview,
				ReviewTask: "rev-" + cID,
				Summary:    "every-pr.yml caught silent unknown YAML field drop",
				HeadSHA:    "cafebabe",
				At:         cPROpen.Add(time.Minute),
			}.Link())
		}
		tasks = append(tasks, cTask)
		timings = append(timings, model.TaskTiming{
			TaskID:       cID,
			CreatedAt:    &cStart,
			ApprovedAt:   &cStart,
			PrOpenedAt:   &cPROpen,
			PrMergedAt:   &cMerged,
			Targeted:     true,
			Repo:         repo.String(),
			FixTaskFiled: i <= 3,
		})
		cRunID := "run-c-" + cID
		runs = append(runs, model.RunTiming{
			RunID:     cRunID,
			TaskID:    cID,
			Attempt:   1,
			StartedAt: cStart,
		})
		prompts = append(prompts, model.RunPrompt{
			RunID:     cRunID,
			Bytes:     79_000,
			ThreadCut: false,
		})
		usages = append(usages, model.RunUsage{
			RunID:        cRunID,
			TaskID:       cID,
			InputTokens:  13_000,
			OutputTokens: 2_500,
			CostUSD:      0.07,
			StartedAt:    cStart,
			FinishedAt:   cPROpen,
		})
		ciConc := "success"
		if i == 1 || i == 2 {
			ciConc = "failure"
		}
		ciRuns = append(ciRuns, model.CIRunOutcome{
			ID:         "ci-c-" + cID,
			Repo:       repo.String(),
			Ref:        cTask.BranchName(),
			Conclusion: ciConc,
			StartedAt:  &cPROpen,
			FinishedAt: &cPROpen,
		})

		// Shadow S3 ran on Control and would have caught 6 of the 8 blind escapes!
		sCrID := nextCheckRunID
		nextCheckRunID++
		shadowVerdict := "pass"
		if i <= 6 {
			shadowVerdict = "fail"
		}
		checkRuns = append(checkRuns, model.QACheckRun{
			ID:         sCrID,
			TaskID:     cID,
			Stage:      "S3",
			CheckID:    "golden:strict-config-decoder",
			Arm:        model.QAArmControl,
			Shadow:     true,
			Verdict:    shadowVerdict,
			DurationMS: 12,
			AllocBytes: 2048,
			CreatedAt:  cPROpen,
		})
	}

	sum := metrics.ComputeQASummary(metrics.QASummaryInput{
		Window:      win,
		Config:      cfg,
		Assignments: assignments,
		CheckRuns:   checkRuns,
		Findings:    findings,
		Tasks:       tasks,
		Timings:     timings,
		Runs:        runs,
		Prompts:     prompts,
		Usages:      usages,
		CIRuns:      ciRuns,
	})

	if sum.Treatment.AssignedOrigins != 16 || sum.Control.AssignedOrigins != 16 {
		t.Fatalf("ITT AssignedOrigins = (%d, %d), want (16, 16)", sum.Treatment.AssignedOrigins, sum.Control.AssignedOrigins)
	}
	if sum.Treatment.HeldByQAGate != 1 {
		t.Fatalf("HeldByQAGate = %d, want 1", sum.Treatment.HeldByQAGate)
	}
	// Conservative ITT: Origin 16 (unresolved GateQA hold) counts in Treatment.BlindEscapes (1 vs 8 on Control -> 87.5% lift).
	if sum.Treatment.BlindEscapes != 1 || sum.Control.BlindEscapes != 8 {
		t.Fatalf("BlindEscapes = (T=%d, C=%d), want (1, 8)", sum.Treatment.BlindEscapes, sum.Control.BlindEscapes)
	}
	if math.Abs(sum.RelativeEscapeLift-0.875) > 0.01 {
		t.Fatalf("RelativeEscapeLift = %.3f, want 0.875", sum.RelativeEscapeLift)
	}
	if sum.ShadowRecall.ControlTasksChecked != 16 || sum.ShadowRecall.MatchedBlindEscapes != 6 {
		t.Fatalf("ShadowRecall = %+v, want Checked=16, Matched=6 (75%% paired recall)", sum.ShadowRecall)
	}
	if math.Abs(sum.ShadowRecall.PairedRecall-0.75) > 0.01 {
		t.Fatalf("PairedRecall = %.3f, want 0.75", sum.ShadowRecall.PairedRecall)
	}
	if sum.CircuitBreaker.ShouldAutoDowngrade {
		t.Fatalf("healthy rollout unexpectedly triggered auto-downgrade: %+v", sum.CircuitBreaker)
	}
	if sum.Treatment.QASkillPromptByteShare <= 0 || sum.Treatment.QASkillPromptByteShare > 0.03 {
		t.Fatalf("QASkillPromptByteShare = %.5f, want > 0 and <= 0.03", sum.Treatment.QASkillPromptByteShare)
	}

	// Now verify circuit breaker trips when 6 negative human votes push NotHelpfulWilsonLB > 10%.
	for v := 0; v < 6; v++ {
		findings = append(findings, model.QAFinding{
			ID:         nextFindingID,
			CheckRunID: 1,
			TaskID:     "1",
			Stage:      "S3",
			RuleID:     "golden:strict-config-decoder",
			Severity:   "blocking",
			Summary:    "noisy check",
			HumanVote:  -1,
			CreatedAt:  now,
		})
		nextFindingID++
	}
	tripped := metrics.ComputeQASummary(metrics.QASummaryInput{
		Window:      win,
		Config:      cfg,
		Assignments: assignments,
		CheckRuns:   checkRuns,
		Findings:    findings,
		Tasks:       tasks,
		Timings:     timings,
		Runs:        runs,
		Prompts:     prompts,
		Usages:      usages,
		CIRuns:      ciRuns,
	})
	if tripped.CircuitBreaker.ShouldAutoDowngrade || len(tripped.CircuitBreaker.Reasons) == 0 {
		t.Fatalf("expected circuit breaker to NOT auto-downgrade but notify on high NotHelpfulWilsonLB, got %+v", tripped.CircuitBreaker)
	}
}

func TestComputeQASummaryRepoIsolationNilPROpenedAtAndS4Cost(t *testing.T) {
	now := time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)
	win := metrics.Window{Since: now.Add(-24 * time.Hour), Until: now.Add(time.Hour)}
	allowedRepo := model.RepoRef{Owner: "acme", Name: "allowed"}
	secretRepo := model.RepoRef{Owner: "acme", Name: "secret"}

	// NaN / Inf stream poisoning checks on OnlineCUPEDScores, HodgesLehmannShift, and NormalMixtureSPRT.
	cupedRes := metrics.OnlineCUPED([]metrics.CUPEDSample{
		{Arm: model.QAArmControl, X: 1, Y: 10, At: now.Add(-5 * time.Minute)},
		{Arm: model.QAArmTreatment, X: math.NaN(), Y: math.Inf(1), At: now.Add(-4 * time.Minute)},
		{Arm: model.QAArmTreatment, X: 1, Y: 12, At: now.Add(-3 * time.Minute)},
		{Arm: model.QAArmControl, X: 2, Y: 20, At: now.Add(-2 * time.Minute)},
		{Arm: model.QAArmTreatment, X: 2, Y: 22, At: now.Add(-1 * time.Minute)},
	})
	if cupedRes.TreatN != 2 || cupedRes.CtrlN != 2 || math.IsNaN(cupedRes.AdjDelta) {
		t.Fatalf("OnlineCUPED after NaN/Inf sample = %+v, want TreatN=2 CtrlN=2 finite", cupedRes)
	}
	hlRes := metrics.HodgesLehmannShift([]float64{10, math.Inf(1), 20}, []float64{5, math.NaN(), 15})
	if hlRes.TreatN != 2 || hlRes.CtrlN != 2 || math.IsNaN(hlRes.Shift) {
		t.Fatalf("HodgesLehmannShift after NaN/Inf = %+v", hlRes)
	}
	msprtRes := metrics.NormalMixtureSPRT([]float64{10, math.Inf(1), 12, 11}, []float64{10, math.NaN(), 9, 11}, 0, 0.05)
	if msprtRes.N != 6 || math.IsNaN(msprtRes.LikelihoodRatio) {
		t.Fatalf("NormalMixtureSPRT after NaN/Inf = %+v", msprtRes)
	}

	mergedAt := now.Add(-10 * time.Minute)
	repairAt := now.Add(-8 * time.Minute)
	createdAt := now.Add(-30 * time.Minute)

	// Multi-repo WorkOrigin ("ws:multi"): tAllowed on acme/allowed (with nil PrOpenedAt + LinkDraftReview
	// + ReasonReview child + S4 tokens), tSecret on acme/secret (with custom check rule & negative vote).
	tasks := []model.Task{
		{
			ID:         "t-allowed",
			Title:      "Feature on allowed repo",
			Body:       "Spec",
			Target:     &allowedRepo,
			WorkOrigin: "ws:multi",
			Links: []model.Link{
				{Kind: model.LinkDraftReview, Target: "991"},
			},
		},
		{
			ID:         "t-review-child",
			Title:      "Review acme/allowed#1",
			Body:       "Review",
			Target:     &allowedRepo,
			WorkOrigin: "ws:multi",
			Filing:     model.Filing{Reason: model.ReasonReview},
			Tags:       []string{model.PathTriggerMarkerTag},
			Links: []model.Link{
				{Kind: model.LinkProposedBy, Target: "t-allowed"},
			},
		},
		{
			ID:         "t-secret",
			Title:      "Secret feature",
			Body:       "Confidential",
			Target:     &secretRepo,
			WorkOrigin: "ws:multi",
		},
	}
	timings := []model.TaskTiming{
		{
			TaskID:             "t-allowed",
			CreatedAt:          &createdAt,
			PrOpenedAt:         nil, // Adopted/legacy PR with nil PrOpenedAt must NEVER panic!
			PrMergedAt:         &mergedAt,
			MergeQueueRepairAt: &repairAt,
			Repo:               allowedRepo.String(),
		},
		{
			TaskID:    "t-review-child",
			CreatedAt: &createdAt,
			Repo:      allowedRepo.String(),
		},
		{
			TaskID:    "t-secret",
			CreatedAt: &createdAt,
			Repo:      secretRepo.String(),
		},
	}
	runs := []model.RunTiming{
		{RunID: "r-zero", TaskID: "t-allowed", Attempt: 1}, // Zero StartedAt must be ignored cleanly.
		{RunID: "r-1", TaskID: "t-allowed", Attempt: 2, StartedAt: createdAt},
		{RunID: "r-rev", TaskID: "t-review-child", Attempt: 1, StartedAt: createdAt},
		{RunID: "r-sec", TaskID: "t-secret", Attempt: 1, StartedAt: createdAt},
	}
	usages := []model.RunUsage{
		{RunID: "r-1", TaskID: "t-allowed", InputTokens: 10_000, OutputTokens: 0, CostUSD: 0.10},       // $10 / 1M blended rate
		{RunID: "r-rev", TaskID: "t-review-child", InputTokens: 5_000, OutputTokens: 0, CostUSD: 0.05}, // Review child spend included!
		{RunID: "r-sec", TaskID: "t-secret", InputTokens: 900_000, OutputTokens: 0, CostUSD: 99.0},     // Must NOT leak into acme/allowed!
	}
	checkRuns := []model.QACheckRun{
		{
			ID:        10,
			TaskID:    "t-allowed",
			Stage:     "S4",
			CheckID:   "s4-fresh-verifier",
			Arm:       model.QAArmTreatment,
			Verdict:   "pass",
			TokensIn:  4_000,
			TokensOut: 1_000, // +5,000 S4 tokens -> +$0.05 imputed at $10/1M rate -> total $0.20!
			CreatedAt: mergedAt,
		},
		{
			ID:        20,
			TaskID:    "t-secret",
			Stage:     "S3",
			CheckID:   "secret-corp-rule",
			Arm:       model.QAArmTreatment,
			Verdict:   "fail",
			CreatedAt: mergedAt,
		},
	}
	findings := []model.QAFinding{
		{
			ID:         1,
			CheckRunID: 20,
			TaskID:     "t-secret",
			Stage:      "S3",
			RuleID:     "secret-corp-rule",
			HumanVote:  -1,
			CreatedAt:  mergedAt,
		},
	}
	assignments := []model.QAAssignment{
		{
			RootOrigin: "ws:multi",
			Epoch:      1,
			RegimeID:   1,
			Arm:        model.QAArmTreatment,
			FlowID:     model.QAFlowGoldenV1Minimal,
			AssignedAt: createdAt,
		},
	}
	cfg := model.QAConfig{Enabled: true, FlowID: model.QAFlowGoldenV1Minimal, RolloutPct: 50, Epoch: 1, RegimeID: 1}

	// 1. Empty repository scope ("acme/empty") MUST return 0 checks and 0 votes (zero cross-tenant leak).
	emptyScopeSum := metrics.ComputeQASummary(metrics.QASummaryInput{
		Window:      win,
		Scope:       metrics.Scope{Repos: []string{"acme/empty"}},
		Config:      cfg,
		Assignments: assignments,
		CheckRuns:   checkRuns,
		Findings:    findings,
		Tasks:       tasks,
		Timings:     timings,
		Runs:        runs,
		Usages:      usages,
	})
	if len(emptyScopeSum.Checks) != 0 || emptyScopeSum.Treatment.AssignedOrigins != 0 {
		t.Fatalf("acme/empty leaked cross-repo data: %+v", emptyScopeSum)
	}

	// 2. ReposBound=true with empty Repos allowlist MUST return empty summary.
	boundEmptySum := metrics.ComputeQASummary(metrics.QASummaryInput{
		Window:      win,
		Scope:       metrics.Scope{ReposBound: true},
		Config:      cfg,
		Assignments: assignments,
		CheckRuns:   checkRuns,
		Findings:    findings,
		Tasks:       tasks,
		Timings:     timings,
	})
	if len(boundEmptySum.Checks) != 0 || boundEmptySum.Treatment.AssignedOrigins != 0 {
		t.Fatalf("ReposBound empty allowlist leaked data: %+v", boundEmptySum)
	}

	// 3. Scoped to acme/allowed: includes t-allowed (1 eligible PR task, LinkDraftReview escape, churn from nil PrOpenedAt),
	// excludes ReasonReview child from TasksTotal while including its $0.05 + S4 $0.05 = $0.20 total cost, and hides secret-corp-rule!
	allowedSum := metrics.ComputeQASummary(metrics.QASummaryInput{
		Window:      win,
		Scope:       metrics.Scope{Repos: []string{"acme/allowed"}},
		Config:      cfg,
		Assignments: assignments,
		CheckRuns:   checkRuns,
		Findings:    findings,
		Tasks:       tasks,
		Timings:     timings,
		Runs:        runs,
		Usages:      usages,
	})
	if allowedSum.Treatment.TasksTotal != 1 {
		t.Fatalf("TasksTotal = %d, want 1 (ReasonReview/PathTrigger child and acme/secret task excluded)", allowedSum.Treatment.TasksTotal)
	}
	if allowedSum.Treatment.BlindReviewEscapes != 1 || allowedSum.Treatment.PostMergeChurn7d != 1 {
		t.Fatalf("expected LinkDraftReview escape=1 and PostMergeChurn7d=1, got %+v", allowedSum.Treatment)
	}
	if len(allowedSum.Checks) != 1 || allowedSum.Checks[0].RuleID != "s4-fresh-verifier" {
		t.Fatalf("unexpected Checks on acme/allowed (leaked secret-corp-rule?): %+v", allowedSum.Checks)
	}
	if math.Abs(allowedSum.CostCUPED.RawTreatMean-0.20) > 1e-6 || math.Abs(allowedSum.TokensCUPED.RawTreatMean-20_000) > 1e-6 {
		t.Fatalf("expected Cost=$0.20 (coding $0.10 + review $0.05 + S4 $0.05) and Tokens=20000, got Cost=%+v Tokens=%+v",
			allowedSum.CostCUPED, allowedSum.TokensCUPED)
	}
}

func TestNormalMixtureSPRTAlphaAndUnmeasuredCost(t *testing.T) {
	// Construct treatment vs control samples with LikelihoodRatio strictly between 20 and 100
	// so alpha=0.05 (threshold 20) triggers Significant=true while alpha=0.01 (threshold 100) stays false.
	treat := []float64{2.9, 1.1, 3.1, 1.3, 2.8, 1.4, 3.0, 1.2}
	ctrl := []float64{1.2, 0.0, 1.4, 0.2, 1.1, 0.1, 1.3, 0.1}
	res05 := metrics.NormalMixtureSPRT(treat, ctrl, 0.5, 0.05)
	res01 := metrics.NormalMixtureSPRT(treat, ctrl, 0.5, 0.01)
	if res05.LikelihoodRatio < 20 || res05.LikelihoodRatio >= 100 {
		t.Fatalf("expected 20 <= LR < 100 for threshold separation, got LR=%v", res05.LikelihoodRatio)
	}
	if !res05.Significant {
		t.Fatalf("expected Significant=true at alpha=0.05 for LR=%v", res05.LikelihoodRatio)
	}
	if res01.Significant {
		t.Fatalf("expected Significant=false at alpha=0.01 for LR=%v", res01.LikelihoodRatio)
	}

	// Verify CostMeasured is false when token usage is positive (e.g. ambient login) but CostUSD == 0.
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-20 * time.Minute)
	mergedAt := now.Add(-5 * time.Minute)
	repo := model.RepoRef{Owner: "acme", Name: "ambient"}
	sum := metrics.ComputeQASummary(metrics.QASummaryInput{
		Window: metrics.Window{Since: now.Add(-24 * time.Hour), Until: now.Add(time.Hour)},
		Config: model.QAConfig{Enabled: true, FlowID: model.QAFlowGoldenV1Minimal, RolloutPct: 50, Epoch: 1, RegimeID: 1},
		Assignments: []model.QAAssignment{
			{RootOrigin: "task:t1", Epoch: 1, RegimeID: 1, Arm: model.QAArmTreatment, FlowID: model.QAFlowGoldenV1Minimal, AssignedAt: createdAt},
			{RootOrigin: "task:c1", Epoch: 1, RegimeID: 1, Arm: model.QAArmControl, FlowID: model.QAFlowGoldenV1Minimal, AssignedAt: createdAt},
		},
		Tasks: []model.Task{
			{ID: "t1", Title: "Treat", Target: &repo, WorkOrigin: "task:t1"},
			{ID: "c1", Title: "Ctrl", Target: &repo, WorkOrigin: "task:c1"},
		},
		Timings: []model.TaskTiming{
			{TaskID: "t1", CreatedAt: &createdAt, PrOpenedAt: &mergedAt, PrMergedAt: &mergedAt, Repo: repo.String()},
			{TaskID: "c1", CreatedAt: &createdAt, PrOpenedAt: &mergedAt, PrMergedAt: &mergedAt, Repo: repo.String()},
		},
		Runs: []model.RunTiming{
			{RunID: "rt1", TaskID: "t1", Attempt: 1, StartedAt: createdAt},
			{RunID: "rc1", TaskID: "c1", Attempt: 1, StartedAt: createdAt},
		},
		Usages: []model.RunUsage{
			{RunID: "rt1", TaskID: "t1", InputTokens: 50_000, OutputTokens: 10_000, CostUSD: 0},
			{RunID: "rc1", TaskID: "c1", InputTokens: 40_000, OutputTokens: 8_000, CostUSD: 0},
		},
		CheckRuns: []model.QACheckRun{
			{ID: 1, TaskID: "t1", Stage: "S4", CheckID: "s4-fresh-verifier", Arm: model.QAArmTreatment, Verdict: "pass", TokensIn: 5_000, TokensOut: 1_000, CreatedAt: mergedAt},
		},
	})
	if sum.CostMeasured {
		t.Fatalf("CostMeasured = true when all CostUSD == 0, want false (got CostCUPED=%+v)", sum.CostCUPED)
	}
	if sum.TokensCUPED.RawTreatMean <= 0 || sum.TokensCUPED.RawCtrlMean <= 0 {
		t.Fatalf("expected positive TokensCUPED despite unmeasured cost, got %+v", sum.TokensCUPED)
	}
}
