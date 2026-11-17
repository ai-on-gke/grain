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

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/expstats"
	"github.com/ai-on-gke/grain/pkg/qa/judge"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
	"github.com/ai-on-gke/grain/pkg/safepath"
)

type postQAExperimentReq struct {
	ID              string                       `json:"id,omitempty"`
	Name            string                       `json:"name"`
	ReferenceArmID  string                       `json:"referenceArmId,omitempty"`
	Hypothesis      string                       `json:"hypothesis,omitempty"`
	Arms            []model.QAExperimentArm      `json:"arms"`
	Scope           model.QAExperimentScope      `json:"scope,omitempty"`
	Allocation      model.QAExperimentAllocation `json:"allocation,omitempty"`
	PrimaryMetric   string                       `json:"primaryMetric,omitempty"`
	CoPrimaries     []string                     `json:"coPrimaries,omitempty"`
	PrimaryContrast string                       `json:"primaryContrast,omitempty"`
	Secondaries     []string                     `json:"secondaries,omitempty"`
	Guardrails      []string                     `json:"guardrails,omitempty"`
	Plan            model.QAExperimentPlan       `json:"plan,omitempty"`
	Options         model.QAExperimentOptions    `json:"options,omitempty"`
}

type postQAExperimentTransitionReq struct {
	Action          string                     `json:"action"`
	Revision        int                        `json:"revision,omitempty"`
	Reason          string                     `json:"reason,omitempty"`
	Arm             string                     `json:"arm,omitempty"`
	Decision        string                     `json:"decision,omitempty"`
	DryRunViewed    bool                       `json:"dryRunViewed,omitempty"`
	OverrideNotes   string                     `json:"overrideNotes,omitempty"`
	Name            string                     `json:"name,omitempty"`
	Hypothesis      *string                    `json:"hypothesis,omitempty"`
	Arms            []model.QAExperimentArm    `json:"arms,omitempty"`
	Scope           *model.QAExperimentScope   `json:"scope,omitempty"`
	PrimaryMetric   string                     `json:"primaryMetric,omitempty"`
	CoPrimaries     []string                   `json:"coPrimaries,omitempty"`
	PrimaryContrast string                     `json:"primaryContrast,omitempty"`
	Secondaries     []string                   `json:"secondaries,omitempty"`
	Guardrails      []string                   `json:"guardrails,omitempty"`
	Plan            *model.QAExperimentPlan    `json:"plan,omitempty"`
	Options         *model.QAExperimentOptions `json:"options,omitempty"`
	ReferenceArmID  string                     `json:"referenceArmId,omitempty"`
}

type qaPairEndpointOutcome struct {
	SpecKey             string    `json:"specKey,omitempty"`
	PairID              string    `json:"pairId,omitempty"`
	ArmID               string    `json:"armId"`
	RootOrigin          string    `json:"rootOrigin,omitempty"`
	TaskID              string    `json:"taskId,omitempty"`
	HiddenPassed        *bool     `json:"hiddenPassed,omitempty"`
	MutationScore       *float64  `json:"mutationScore,omitempty"`
	PanelScore          *float64  `json:"panelScore,omitempty"`
	RaterScores         []float64 `json:"raterScores,omitempty"`
	SeverityWeightedSum *float64  `json:"severityWeightedSum,omitempty"`
}

type postQAExperimentReadoutReq struct {
	Action           string                        `json:"action,omitempty"`
	Model            string                        `json:"model,omitempty"`
	Prompt           string                        `json:"prompt,omitempty"`
	ReadoutDir       string                        `json:"readoutDir,omitempty"`
	Manifest         *judge.ReadoutManifest        `json:"manifest,omitempty"`
	Scores           []judge.PacketScore           `json:"scores,omitempty"`
	RaterMatrix      [][]float64                   `json:"raterMatrix,omitempty"`
	Severities       []judge.FindingSeverityRecord `json:"severities,omitempty"`
	Probe            *judge.ProbeReport            `json:"probe,omitempty"`
	Outcomes         []qaPairEndpointOutcome       `json:"outcomes,omitempty"`
	HumanLabelsCount int                           `json:"humanLabelsCount,omitempty"`
	HumanAgreement   *float64                      `json:"humanAgreement,omitempty"`
}

type qaImportedReadout struct {
	ImportedAt       string                        `json:"importedAt"`
	JudgeModel       string                        `json:"judgeModel,omitempty"`
	Manifest         *judge.ReadoutManifest        `json:"manifest,omitempty"`
	Scores           []judge.PacketScore           `json:"scores,omitempty"`
	Severities       []judge.FindingSeverityRecord `json:"severities,omitempty"`
	ICC              float64                       `json:"icc"`
	ProbeAUC         float64                       `json:"probeAuc"`
	HumanLabelsCount int                           `json:"humanLabelsCount,omitempty"`
	HumanAgreement   *float64                      `json:"humanAgreement,omitempty"`
	Validated        bool                          `json:"validated"`
	Outcomes         []qaPairEndpointOutcome       `json:"outcomes,omitempty"`
}

type qaExperimentSummaryView struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	State         string                  `json:"state"`
	Arms          []model.QAExperimentArm `json:"arms"`
	PrimaryMetric string                  `json:"primaryMetric"`
	StartedAt     string                  `json:"startedAt,omitempty"`
	StoppedAt     string                  `json:"stoppedAt,omitempty"`
	Legacy        bool                    `json:"legacy,omitempty"`
}

type qaExperimentPowerEffectView struct {
	Power float64 `json:"power"`
	Label string  `json:"label"`
}

type qaExperimentPowerMDEView struct {
	Label string `json:"label"`
}

type qaExperimentPowerPreviewView struct {
	DecisionRule     string                      `json:"decisionRule"`
	SimulatedRuns    int                         `json:"simulatedRuns"`
	ObservedEffect   qaExperimentPowerEffectView `json:"observedEffect"`
	MeaningfulEffect qaExperimentPowerEffectView `json:"meaningfulEffect"`
	MDE80            qaExperimentPowerMDEView    `json:"mde80"`
}

type qaExperimentAllocationView struct {
	Mode     string   `json:"mode"`
	Design   string   `json:"design,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	ListHash string   `json:"listHash,omitempty"`
}

type qaExperimentPlanView struct {
	PlannedOriginsPerArm int                          `json:"plannedOriginsPerArm"`
	MaxDays              int                          `json:"maxDays"`
	Alpha                float64                      `json:"alpha"`
	MDEAck               bool                         `json:"mdeAck"`
	HarmRefTau           float64                      `json:"harmRefTau,omitempty"`
	Power                qaExperimentPowerPreviewView `json:"power"`
}

type qaExperimentAuditItemView struct {
	At      string `json:"at"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Summary string `json:"summary"`
}

type qaAllocationPairSlotView struct {
	Pair  int    `json:"pair"`
	KeyA  string `json:"keyA"`
	BaseA string `json:"baseA,omitempty"`
	KeyB  string `json:"keyB"`
	BaseB string `json:"baseB,omitempty"`
}

type qaExperimentDetailView struct {
	ID              string                            `json:"id"`
	Revision        int                               `json:"revision"`
	Name            string                            `json:"name"`
	Hypothesis      string                            `json:"hypothesis"`
	State           string                            `json:"state"`
	Arms            []model.QAExperimentArm           `json:"arms"`
	ReferenceArmID  string                            `json:"referenceArmId,omitempty"`
	Scope           model.QAExperimentScope           `json:"scope"`
	Allocation      qaExperimentAllocationView        `json:"allocation"`
	AllocationKeys  []string                          `json:"allocationKeys,omitempty"`
	AllocationPairs []qaAllocationPairSlotView        `json:"allocationPairs,omitempty"`
	AllocationList  []model.QAExplicitAllocationEntry `json:"allocationList,omitempty"`
	Salt            string                            `json:"salt,omitempty"`
	PrimaryMetric   string                            `json:"primaryMetric"`
	CoPrimaries     []string                          `json:"coPrimaries"`
	PrimaryContrast string                            `json:"primaryContrast"`
	Secondaries     []string                          `json:"secondaries"`
	Guardrails      []string                          `json:"guardrails"`
	Plan            qaExperimentPlanView              `json:"plan"`
	Options         model.QAExperimentOptions         `json:"options"`
	Segment         int                               `json:"segment"`
	EnrollmentHeld  string                            `json:"enrollmentHeld"`
	EnrolledByArm   map[string]int                    `json:"enrolledByArm"`
	StartedAt       string                            `json:"startedAt,omitempty"`
	StoppedAt       string                            `json:"stoppedAt,omitempty"`
	Decision        *model.QAExperimentDecision       `json:"decision,omitempty"`
	Readout         json.RawMessage                   `json:"readout,omitempty"`
	AuditTrail      []qaExperimentAuditItemView       `json:"auditTrail"`
}

var expSlugNonAlnum = regexp.MustCompile(`[^a-z0-9-]+`)

func slugifyExperimentID(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = expSlugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if s == "" {
		return "qa-exp"
	}
	return s
}

func formatTimeRFC3339(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func buildPowerPreviewView(exp model.QAExperiment) qaExperimentPowerPreviewView {
	n := exp.Plan.PlannedOriginsPerArm
	if n < 4 {
		n = 30
	}
	rho := 0.0
	rule := "Fixed-horizon work-tree permutation test"
	if strings.EqualFold(exp.Allocation.Design, "paired") {
		rho = 0.5
		rule = "Fixed-horizon paired sign-flip permutation test (Holm across co-primaries)"
	} else if len(exp.Arms) > 2 {
		rule = "Fixed-horizon work-tree permutation test (Holm over 2 contrasts)"
	}
	sim := expstats.SimulatePowerPreview(expstats.PowerConfig{
		PlannedOriginsPerArm: n,
		Design:               exp.Allocation.Design,
		PairedRho:            rho,
	})
	var obsLabel, meanLabel, mdeLabel string
	if exp.PrimaryMetric == "quality.panel_score" || strings.EqualFold(exp.Allocation.Design, "paired") {
		obsLabel = fmt.Sprintf("%.0f%% power at +0.44 pts", sim.PowerObserved*100)
		meanLabel = fmt.Sprintf("%.0f%% power at +0.50 pts", sim.PowerMeaningful*100)
		mdeLabel = fmt.Sprintf("+%.2f panel points at 80%% power", sim.MDE80)
	} else {
		obsLabel = fmt.Sprintf("%.0f%% power at observed v1 lift", sim.PowerObserved*100)
		meanLabel = fmt.Sprintf("%.0f%% power at -25%% findings/PR", sim.PowerMeaningful*100)
		mdePct := math.Min(95, math.Round(sim.MDE80*45))
		mdeLabel = fmt.Sprintf("-%.0f%% findings/PR at 80%% power", mdePct)
	}
	return qaExperimentPowerPreviewView{
		DecisionRule:     rule,
		SimulatedRuns:    2000,
		ObservedEffect:   qaExperimentPowerEffectView{Power: sim.PowerObserved, Label: obsLabel},
		MeaningfulEffect: qaExperimentPowerEffectView{Power: sim.PowerMeaningful, Label: meanLabel},
		MDE80:            qaExperimentPowerMDEView{Label: mdeLabel},
	}
}

func (s *Server) validateExperimentArms(ctx context.Context, arms []model.QAExperimentArm, referenceArmID string, mdeAck bool) ([]model.QAExperimentArm, string, error) {
	if len(arms) < 2 || len(arms) > 3 {
		return nil, "", errors.New("an experiment must declare control + 1 treatment arm (or 2 treatments with mdeAck)")
	}
	if len(arms) == 3 && !mdeAck {
		return nil, "", errors.New("adding a second treatment arm requires mdeAck: true")
	}
	seenIDs := map[string]bool{}
	hasControl := false
	treatmentCount := 0
	out := make([]model.QAExperimentArm, len(arms))
	for i, a := range arms {
		a.ID = strings.TrimSpace(a.ID)
		a.Label = strings.TrimSpace(a.Label)
		a.FlowHash = strings.TrimSpace(a.FlowHash)
		a.BaseBranch = strings.TrimSpace(a.BaseBranch)
		if a.ID == "" {
			return nil, "", errors.New("each experiment arm must have a non-empty id")
		}
		if seenIDs[a.ID] {
			return nil, "", fmt.Errorf("duplicate arm id %q", a.ID)
		}
		seenIDs[a.ID] = true
		if a.Label == "" {
			a.Label = a.ID
		}
		if a.Weight <= 0 {
			a.Weight = 100 / len(arms)
		}
		if a.ID == model.QAArmControl || strings.EqualFold(a.FlowHash, "control") {
			hasControl = true
			if a.FlowHash == "" {
				a.FlowHash = "control"
			}
		} else {
			treatmentCount++
			if a.FlowHash == "" {
				return nil, "", fmt.Errorf("treatment arm %q requires a flowHash", a.ID)
			}
			bundle, _, _, retiredAt, found, err := s.lookupFlowBundle(ctx, a.FlowHash)
			if err != nil {
				return nil, "", err
			}
			if !found {
				return nil, "", fmt.Errorf("flow version %q not found", a.FlowHash)
			}
			if retiredAt != nil {
				return nil, "", fmt.Errorf("flow version %q is retired and cannot be used in an experiment", a.FlowHash)
			}
			if bundle.Hash != "" && strings.Contains(a.FlowHash, "@") {
				// Keep canonical key@ver or hash usable; resolve.LookupFlowSpec handles both.
			}
		}
		out[i] = a
	}
	if !hasControl && len(arms) == 2 {
		if referenceArmID == "" {
			referenceArmID = out[0].ID
		}
	} else {
		if !hasControl || treatmentCount == 0 {
			return nil, "", errors.New("arms must include one control arm and at least one treatment arm")
		}
		if referenceArmID == "" {
			referenceArmID = model.QAArmControl
		}
	}
	return out, referenceArmID, nil
}

func generateAllocationForExperiment(exp *model.QAExperiment) error {
	mode := strings.ToLower(strings.TrimSpace(exp.Allocation.Mode))
	if mode == "" {
		mode = "hash"
	}
	if mode != "hash" && mode != "explicit" {
		return fmt.Errorf("unsupported allocation mode %q (want hash or explicit)", mode)
	}
	exp.Allocation.Mode = mode
	if mode == "hash" {
		exp.AllocationList = nil
		exp.Allocation.ListHash = ""
		return nil
	}
	design := strings.ToLower(strings.TrimSpace(exp.Allocation.Design))
	if design == "" {
		design = "paired"
	}
	if design != "paired" && design != "blocked" {
		return fmt.Errorf("unsupported explicit allocation design %q (want paired or blocked)", design)
	}
	exp.Allocation.Design = design
	if exp.Allocation.Seed == 0 {
		exp.Allocation.Seed = time.Now().UTC().UnixNano()
	}
	armIDs := make([]string, 0, len(exp.Arms))
	for _, a := range exp.Arms {
		armIDs = append(armIDs, a.ID)
	}
	n := exp.Plan.PlannedOriginsPerArm
	if n < 4 {
		n = 30
	}
	totalOrPairs := n
	if design == "blocked" {
		totalOrPairs = n * len(armIDs)
	}
	entries, listHash, err := expstats.GenerateExplicitList(design, armIDs, totalOrPairs, exp.Allocation.Strata, exp.Allocation.Seed)
	if err != nil {
		return err
	}
	mapped := make([]model.QAExplicitAllocationEntry, len(entries))
	for i, e := range entries {
		mapped[i] = model.QAExplicitAllocationEntry{
			Key:     e.Key,
			ArmID:   e.ArmID,
			Stratum: e.Stratum,
			PairID:  e.PairID,
		}
	}
	exp.AllocationList = mapped
	exp.Allocation.ListHash = listHash
	return nil
}

func (s *Server) buildExperimentDetailView(ctx context.Context, exp model.QAExperiment) qaExperimentDetailView {
	enrolledByArm := map[string]int{}
	for _, a := range exp.Arms {
		enrolledByArm[a.ID] = 0
	}
	if enrollments, err := s.tasks.Store.ListQAEnrollments(ctx, exp.ID); err == nil {
		for _, e := range enrollments {
			if e.ArmID != "excluded" && e.ArmID != "" {
				enrolledByArm[e.ArmID]++
			}
		}
	}

	var keys []string
	var pairs []qaAllocationPairSlotView
	armBase := map[string]string{}
	for _, a := range exp.Arms {
		armBase[a.ID] = strings.TrimSpace(a.BaseBranch)
	}
	if exp.Allocation.Mode == "explicit" && len(exp.AllocationList) > 0 {
		keys = make([]string, 0, len(exp.AllocationList))
		for _, e := range exp.AllocationList {
			keys = append(keys, e.Key)
		}
		if exp.Allocation.Design == "paired" && len(exp.Arms) == 2 {
			for i := 0; i+1 < len(exp.AllocationList); i += 2 {
				eA := exp.AllocationList[i]
				eB := exp.AllocationList[i+1]
				pairs = append(pairs, qaAllocationPairSlotView{
					Pair:  i / 2,
					KeyA:  eA.Key,
					BaseA: armBase[eA.ArmID],
					KeyB:  eB.Key,
					BaseB: armBase[eB.ArmID],
				})
			}
		}
	}

	var auditTrail []qaExperimentAuditItemView
	surface := "qa-experiment:" + exp.ID
	if audits, err := s.tasks.Store.ConfigAuditOfSurface(ctx, surface, 100); err == nil {
		for _, a := range audits {
			actor := strings.TrimSpace(a.By.ID)
			if actor == "" {
				actor = string(a.By.Kind)
			}
			auditTrail = append(auditTrail, qaExperimentAuditItemView{
				At:      a.At.UTC().Format(time.RFC3339),
				Actor:   actor,
				Action:  "audit",
				Summary: a.Summary,
			})
		}
	}
	if auditTrail == nil {
		auditTrail = []qaExperimentAuditItemView{}
	}

	out := qaExperimentDetailView{
		ID:             exp.ID,
		Revision:       exp.Revision,
		Name:           exp.Name,
		Hypothesis:     exp.Hypothesis,
		State:          exp.State,
		Arms:           exp.Arms,
		ReferenceArmID: exp.ReferenceArmID,
		Scope:          exp.Scope,
		Allocation: qaExperimentAllocationView{
			Mode:     exp.Allocation.Mode,
			Design:   exp.Allocation.Design,
			Keys:     keys,
			ListHash: exp.Allocation.ListHash,
		},
		AllocationKeys:  keys,
		AllocationPairs: pairs,
		PrimaryMetric:   exp.PrimaryMetric,
		CoPrimaries:     exp.CoPrimaries,
		PrimaryContrast: exp.PrimaryContrast,
		Secondaries:     exp.Secondaries,
		Guardrails:      exp.Guardrails,
		Plan: qaExperimentPlanView{
			PlannedOriginsPerArm: exp.Plan.PlannedOriginsPerArm,
			MaxDays:              exp.Plan.MaxDays,
			Alpha:                exp.Plan.Alpha,
			MDEAck:               exp.Plan.MDEAck,
			HarmRefTau:           exp.Plan.HarmRefTau,
			Power:                buildPowerPreviewView(exp),
		},
		Options:        exp.Options,
		Segment:        exp.Segment,
		EnrollmentHeld: exp.EnrollmentHeld,
		EnrolledByArm:  enrolledByArm,
		StartedAt:      formatTimeRFC3339(exp.StartedAt),
		StoppedAt:      formatTimeRFC3339(exp.StoppedAt),
		Decision:       exp.Decision,
		Readout:        exp.Readout,
		AuditTrail:     auditTrail,
	}
	// Salt and full explicit arm mapping stay hidden until stopped/archived (spec-v2.1 §5.2, §14).
	if exp.State == model.QAExpStateStopped || exp.State == model.QAExpStateArchived {
		out.Salt = exp.Salt
		out.AllocationList = exp.AllocationList
	}
	return out
}

func (s *Server) handleGetQAExperiments(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}
	exps, err := s.tasks.Store.ListQAExperiments(r.Context(), state, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]qaExperimentSummaryView, 0, len(exps)+2)
	for _, e := range exps {
		out = append(out, qaExperimentSummaryView{
			ID:            e.ID,
			Name:          e.Name,
			State:         e.State,
			Arms:          e.Arms,
			PrimaryMetric: e.PrimaryMetric,
			StartedAt:     formatTimeRFC3339(e.StartedAt),
			StoppedAt:     formatTimeRFC3339(e.StoppedAt),
		})
	}

	// Synthesize read-only legacy epoch summaries when space remains.
	if len(out) < limit && (state == "" || state == model.QAExpStateStopped || state == model.QAExpStateRunning) {
		qaCfg, _ := s.tasks.Store.ConfigQA(r.Context())
		if assigns, aErr := s.tasks.Store.ListQAAssignments(r.Context()); aErr == nil && len(assigns) > 0 {
			byEpoch := map[int]struct {
				first time.Time
				last  time.Time
				flow  string
				pct   int
			}{}
			for _, a := range assigns {
				cur, ok := byEpoch[a.Epoch]
				if !ok || a.AssignedAt.Before(cur.first) {
					cur.first = a.AssignedAt
				}
				if !ok || a.AssignedAt.After(cur.last) {
					cur.last = a.AssignedAt
				}
				if cur.flow == "" {
					cur.flow = a.FlowID
				}
				if a.RolloutPctAtAssign > 0 {
					cur.pct = a.RolloutPctAtAssign
				}
				byEpoch[a.Epoch] = cur
			}
			var epochs []int
			for ep := range byEpoch {
				epochs = append(epochs, ep)
			}
			sort.Sort(sort.Reverse(sort.IntSlice(epochs)))
			for _, ep := range epochs {
				if len(out) >= limit {
					break
				}
				meta := byEpoch[ep]
				st := model.QAExpStateStopped
				if qaCfg.Enabled && qaCfg.RolloutPct > 0 && qaCfg.EpochOrDefault() == ep {
					st = model.QAExpStateRunning
				}
				if state != "" && st != state {
					continue
				}
				pct := meta.pct
				if pct <= 0 {
					pct = 50
				}
				out = append(out, qaExperimentSummaryView{
					ID:    fmt.Sprintf("legacy-e%d", ep),
					Name:  fmt.Sprintf("Legacy Epoch %d (%s)", ep, firstNonEmptyStr(meta.flow, model.DefaultQAFlowID)),
					State: st,
					Arms: []model.QAExperimentArm{
						{ID: "control", Label: "Control", FlowHash: "control", Weight: 100 - pct},
						{ID: "treatment", Label: "Treatment", FlowHash: firstNonEmptyStr(meta.flow, model.DefaultQAFlowID), Weight: pct},
					},
					PrimaryMetric: "legacy.escape.blind",
					StartedAt:     meta.first.UTC().Format(time.RFC3339),
					StoppedAt:     meta.last.UTC().Format(time.RFC3339),
					Legacy:        true,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"experiments": out})
}

func (s *Server) handlePostQAExperiment(w http.ResponseWriter, r *http.Request) {
	var req postQAExperimentReq
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("experiment name is required"))
		return
	}
	arms, refArmID, err := s.validateExperimentArms(r.Context(), req.Arms, req.ReferenceArmID, req.Plan.MDEAck)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		base := slugifyExperimentID(name)
		id = base
		for suffix := 2; suffix < 1000; suffix++ {
			if _, gErr := s.tasks.Store.GetQAExperiment(r.Context(), id); errors.Is(gErr, model.ErrQAExperimentNotFound) {
				break
			}
			id = fmt.Sprintf("%s-%d", base, suffix)
		}
	} else {
		id = slugifyExperimentID(id)
	}

	salt, err := expstats.GenerateSalt()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	plannedN := req.Plan.PlannedOriginsPerArm
	if plannedN < 4 {
		plannedN = 30
	}
	maxDays := req.Plan.MaxDays
	if maxDays < 1 {
		maxDays = 21
	}
	alpha := req.Plan.Alpha
	if alpha <= 0 || alpha >= 0.5 {
		alpha = 0.05
	}
	memMode := strings.ToLower(strings.TrimSpace(req.Options.Memory))
	if memMode == "" {
		memMode = "record"
	}
	if memMode != "record" && memMode != "frozen" {
		writeError(w, http.StatusBadRequest, errors.New("options.memory must be 'record' or 'frozen'"))
		return
	}

	primaryMetric := strings.TrimSpace(req.PrimaryMetric)
	if primaryMetric == "" {
		if strings.EqualFold(req.Allocation.Design, "paired") {
			primaryMetric = "quality.panel_score"
		} else {
			primaryMetric = "review.findings_sev.per_pr"
		}
	}
	coPrimaries := req.CoPrimaries
	if len(coPrimaries) == 0 && primaryMetric == "quality.panel_score" {
		coPrimaries = []string{"quality.mutation_score.added_lines", "bench.hidden_acceptance_pass"}
	}
	primaryContrast := strings.TrimSpace(req.PrimaryContrast)
	if primaryContrast == "" {
		baseRef := refArmID
		if baseRef == "" {
			baseRef = model.QAArmControl
		}
		for _, a := range arms {
			if a.ID != baseRef {
				primaryContrast = a.ID + ":" + baseRef
				break
			}
		}
	}
	guardrails := req.Guardrails
	if len(guardrails) == 0 {
		guardrails = []string{"flow.time_to_pr", "qa.held_rate", "qa.check.precision", "flow.tokens.per_task", "flow.overhead", "flow.blindness_leaks"}
	}

	now := s.tasks.now()
	actor := s.tasks.requestActor(r.Context())
	exp := model.QAExperiment{
		ID:              id,
		Revision:        1,
		Name:            name,
		Hypothesis:      strings.TrimSpace(req.Hypothesis),
		State:           model.QAExpStateDraft,
		Arms:            arms,
		ReferenceArmID:  refArmID,
		Scope:           req.Scope,
		Allocation:      req.Allocation,
		Salt:            salt,
		PrimaryMetric:   primaryMetric,
		CoPrimaries:     coPrimaries,
		PrimaryContrast: primaryContrast,
		Secondaries:     req.Secondaries,
		Guardrails:      guardrails,
		Plan: model.QAExperimentPlan{
			PlannedOriginsPerArm: plannedN,
			MaxDays:              maxDays,
			Alpha:                alpha,
			MDEAck:               req.Plan.MDEAck,
			HarmRefTau:           req.Plan.HarmRefTau,
		},
		Options: model.QAExperimentOptions{
			ShadowOnControl: req.Options.ShadowOnControl,
			Memory:          memMode,
		},
		Engine: model.QAExperimentEngine{
			QAEngineRev: qa.QAEngineRev,
		},
		Segment:   1,
		CreatedBy: actor.Actor.ID,
		CreatedAt: now,
	}
	if err := generateAllocationForExperiment(&exp); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.tasks.Store.CreateQAExperiment(r.Context(), exp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, fmt.Errorf("experiment %q already exists", exp.ID))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = s.tasks.Store.RecordConfigAudit(r.Context(), model.ConfigAuditEntry{
		At:         now,
		By:         actor.Actor,
		OnBehalfOf: actor.OnBehalfOf,
		Surface:    "qa-experiment:" + exp.ID,
		Summary:    fmt.Sprintf("created draft QA experiment %s (%s, %d/arm)", exp.ID, exp.Allocation.Mode, exp.Plan.PlannedOriginsPerArm),
	})
	writeJSON(w, http.StatusCreated, map[string]any{"experiment": s.buildExperimentDetailView(r.Context(), exp)})
}

func (s *Server) handleGetQAExperiment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	exp, err := s.tasks.Store.GetQAExperiment(r.Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrQAExperimentNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.buildExperimentDetailView(r.Context(), exp))
}

func (s *Server) checkNotHelpfulStartGate(ctx context.Context) (string, error) {
	findings, err := s.tasks.Store.ListAllQAFindings(ctx)
	if err != nil || len(findings) == 0 {
		return "", nil
	}
	votesByRule := map[string]struct{ pos, neg int }{}
	for _, f := range findings {
		if f.Severity != "blocking" && f.Severity != "blocker" {
			continue
		}
		cur := votesByRule[f.RuleID]
		if f.HumanVote > 0 {
			cur.pos++
		} else if f.HumanVote < 0 {
			cur.neg++
		}
		votesByRule[f.RuleID] = cur
	}
	for ruleID, v := range votesByRule {
		tot := v.pos + v.neg
		if tot < 5 {
			continue
		}
		// Wilson score 95% lower bound on not-helpful rate.
		p := float64(v.neg) / float64(tot)
		z := 1.95996
		n := float64(tot)
		denom := 1.0 + z*z/n
		center := p + z*z/(2*n)
		rad := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n)
		lb := (center - rad) / denom
		if lb > 0.10 {
			return fmt.Sprintf("blocking rule %s had not-helpful Wilson LB %.1f%% (%d/%d votes); pass overrideNotes with a reason to start", ruleID, lb*100, v.neg, tot), nil
		}
	}
	return "", nil
}

func (s *Server) handlePostQAExperimentTransition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req postQAExperimentTransitionReq
	if !readJSON(w, r, &req) {
		return
	}
	expectedRev := req.Revision
	if ifMatch := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), "\""); ifMatch != "" && expectedRev == 0 {
		if parsed, err := strconv.Atoi(ifMatch); err == nil && parsed > 0 {
			expectedRev = parsed
		}
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	now := s.tasks.now()
	actor := s.tasks.requestActor(r.Context())
	var promoteReason string
	var auditSummary string

	// Pre-validate start checks outside the write transaction.
	if action == "start" {
		cur, gErr := s.tasks.Store.GetQAExperiment(r.Context(), id)
		if gErr != nil {
			if errors.Is(gErr, model.ErrQAExperimentNotFound) {
				writeError(w, http.StatusNotFound, gErr)
				return
			}
			writeError(w, http.StatusInternalServerError, gErr)
			return
		}
		if _, _, vErr := s.validateExperimentArms(r.Context(), cur.Arms, cur.ReferenceArmID, cur.Plan.MDEAck); vErr != nil {
			writeError(w, http.StatusBadRequest, vErr)
			return
		}
		if strings.TrimSpace(req.OverrideNotes) == "" {
			if blockMsg, nErr := s.checkNotHelpfulStartGate(r.Context()); nErr == nil && blockMsg != "" {
				writeError(w, http.StatusConflict, errors.New(blockMsg))
				return
			}
		}
	}

	var validatedArms []model.QAExperimentArm
	var validatedRefArmID string
	if action == "edit" && len(req.Arms) > 0 {
		mdeAck := false
		if req.Plan != nil {
			mdeAck = req.Plan.MDEAck
		}
		arms, refID, vErr := s.validateExperimentArms(r.Context(), req.Arms, req.ReferenceArmID, mdeAck)
		if vErr != nil {
			writeError(w, http.StatusBadRequest, vErr)
			return
		}
		validatedArms = arms
		validatedRefArmID = refID
	}

	updated, err := s.tasks.Store.UpdateQAExperiment(r.Context(), id, expectedRev, func(exp *model.QAExperiment) error {
		switch action {
		case "edit":
			if exp.State != model.QAExpStateDraft {
				return fmt.Errorf("%w: only draft experiments can be edited (state is %s)", model.ErrQAExperimentConflict, exp.State)
			}
			regenAlloc := false
			if strings.TrimSpace(req.Name) != "" {
				exp.Name = strings.TrimSpace(req.Name)
			}
			if req.Hypothesis != nil {
				exp.Hypothesis = strings.TrimSpace(*req.Hypothesis)
			}
			if len(validatedArms) > 0 {
				exp.Arms = validatedArms
				exp.ReferenceArmID = validatedRefArmID
				regenAlloc = true
			} else if strings.TrimSpace(req.ReferenceArmID) != "" {
				exp.ReferenceArmID = strings.TrimSpace(req.ReferenceArmID)
			}
			if req.Scope != nil {
				exp.Scope = *req.Scope
			}
			if strings.TrimSpace(req.PrimaryMetric) != "" {
				exp.PrimaryMetric = strings.TrimSpace(req.PrimaryMetric)
			}
			if req.CoPrimaries != nil {
				exp.CoPrimaries = req.CoPrimaries
			}
			if strings.TrimSpace(req.PrimaryContrast) != "" {
				exp.PrimaryContrast = strings.TrimSpace(req.PrimaryContrast)
			}
			if req.Secondaries != nil {
				exp.Secondaries = req.Secondaries
			}
			if req.Guardrails != nil {
				exp.Guardrails = req.Guardrails
			}
			if req.Plan != nil {
				if req.Plan.PlannedOriginsPerArm >= 4 && req.Plan.PlannedOriginsPerArm != exp.Plan.PlannedOriginsPerArm {
					exp.Plan.PlannedOriginsPerArm = req.Plan.PlannedOriginsPerArm
					regenAlloc = true
				}
				if req.Plan.MaxDays >= 1 {
					exp.Plan.MaxDays = req.Plan.MaxDays
				}
				if req.Plan.Alpha > 0 && req.Plan.Alpha < 0.5 {
					exp.Plan.Alpha = req.Plan.Alpha
				}
				exp.Plan.MDEAck = req.Plan.MDEAck
				if req.Plan.HarmRefTau > 0 {
					exp.Plan.HarmRefTau = req.Plan.HarmRefTau
				}
			}
			if req.Options != nil {
				exp.Options = *req.Options
			}
			if regenAlloc && exp.Allocation.Mode == "explicit" {
				if err := generateAllocationForExperiment(exp); err != nil {
					return err
				}
			}
			auditSummary = fmt.Sprintf("edited draft QA experiment %s", exp.ID)

		case "start":
			if exp.State != model.QAExpStateDraft {
				return fmt.Errorf("%w: only draft experiments can start (state is %s)", model.ErrQAExperimentConflict, exp.State)
			}
			exp.State = model.QAExpStateRunning
			exp.StartedAt = &now
			exp.Engine.QAEngineRev = qa.QAEngineRev
			exp.Engine.DryRunViewed = req.DryRunViewed
			exp.Engine.OverrideNotes = strings.TrimSpace(req.OverrideNotes)
			auditSummary = fmt.Sprintf("started QA experiment %s (engine rev %d, listHash %s)", exp.ID, qa.QAEngineRev, exp.Allocation.ListHash)

		case "halt-arm":
			if exp.State != model.QAExpStateRunning {
				return fmt.Errorf("%w: only running experiments can halt an arm", model.ErrQAExperimentConflict)
			}
			targetArm := strings.TrimSpace(req.Arm)
			if targetArm == "" || targetArm == model.QAArmControl {
				return errors.New("halt-arm requires a treatment arm id")
			}
			activeTreatments := 0
			foundIdx := -1
			for i, a := range exp.Arms {
				if a.ID != model.QAArmControl && !a.Halted {
					activeTreatments++
				}
				if a.ID == targetArm && !a.Halted {
					foundIdx = i
				}
			}
			if activeTreatments < 2 {
				return fmt.Errorf("%w: halt-arm requires at least 2 active treatment arms (stop the experiment instead)", model.ErrQAExperimentConflict)
			}
			if foundIdx < 0 {
				return fmt.Errorf("active treatment arm %q not found", targetArm)
			}
			reason := firstNonEmptyStr(req.Reason, "manual operator arm halt")
			exp.Arms[foundIdx].Halted = true
			exp.Arms[foundIdx].HaltReason = reason
			exp.Arms[foundIdx].HaltedAt = now.Format(time.RFC3339)
			promoteReason = fmt.Sprintf("arm %s halted: %s", targetArm, reason)
			auditSummary = fmt.Sprintf("halted arm %s on QA experiment %s (%s)", targetArm, exp.ID, reason)

		case "resume-enrollment":
			if exp.State != model.QAExpStateRunning {
				return fmt.Errorf("%w: experiment %s is not running", model.ErrQAExperimentConflict, exp.ID)
			}
			if strings.TrimSpace(exp.EnrollmentHeld) == "" {
				return fmt.Errorf("%w: enrollment is not currently held on %s", model.ErrQAExperimentConflict, exp.ID)
			}
			exp.EnrollmentHeld = ""
			exp.Segment++
			exp.Engine.QAEngineRev = qa.QAEngineRev
			auditSummary = fmt.Sprintf("resumed enrollment on QA experiment %s at segment %d (engine rev %d)", exp.ID, exp.Segment, qa.QAEngineRev)

		case "stop":
			if exp.State != model.QAExpStateRunning {
				return fmt.Errorf("%w: only running experiments can be stopped (state is %s)", model.ErrQAExperimentConflict, exp.State)
			}
			decision := strings.ToLower(strings.TrimSpace(req.Decision))
			if decision == "" {
				decision = "inconclusive"
			}
			switch decision {
			case "ship", "reject", "inconclusive", "invalid":
			default:
				return fmt.Errorf("decision must be ship, reject, inconclusive, or invalid (got %q)", decision)
			}
			reason := firstNonEmptyStr(req.Reason, "stopped by operator")
			exp.State = model.QAExpStateStopped
			exp.StoppedAt = &now
			exp.EnrollmentHeld = ""
			exp.Decision = &model.QAExperimentDecision{
				Outcome:   decision,
				Arm:       strings.TrimSpace(req.Arm),
				Reason:    reason,
				DecidedBy: actor.Actor.ID,
				DecidedAt: now.Format(time.RFC3339),
			}
			promoteReason = fmt.Sprintf("experiment %s stopped (%s)", exp.ID, decision)
			auditSummary = fmt.Sprintf("stopped QA experiment %s: decision=%s reason=%s", exp.ID, decision, reason)

		case "archive", "discard":
			if exp.State != model.QAExpStateDraft && exp.State != model.QAExpStateStopped {
				return fmt.Errorf("%w: running experiments must be stopped before archiving", model.ErrQAExperimentConflict)
			}
			exp.State = model.QAExpStateArchived
			exp.ArchivedAt = &now
			auditSummary = fmt.Sprintf("archived QA experiment %s", exp.ID)

		default:
			return fmt.Errorf("unsupported transition action %q", action)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, model.ErrQAExperimentNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, model.ErrQAExperimentConflict):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusBadRequest, err)
		}
		return
	}

	// Starting an experiment enables the QA master switch and zeroes legacy rolloutPct (spec-v2.1 §8.1).
	if action == "start" {
		_ = s.tasks.Store.UpdateConfig(r.Context(), func(cfg *model.Config) error {
			cfg.QA.Enabled = true
			cfg.QA.RolloutPct = 0
			return nil
		})
	}

	if promoteReason != "" {
		if hooks := resolve.GetRuntimeHooks(); hooks != nil && hooks.PromoteExperimentStages != nil {
			hooks.PromoteExperimentStages(r.Context(), updated.ID, promoteReason)
		}
	}

	_, _ = s.tasks.Store.RecordConfigAudit(r.Context(), model.ConfigAuditEntry{
		At:         now,
		By:         actor.Actor,
		OnBehalfOf: actor.OnBehalfOf,
		Surface:    "qa-experiment:" + updated.ID,
		Summary:    auditSummary,
	})
	writeJSON(w, http.StatusOK, map[string]any{"experiment": s.buildExperimentDetailView(r.Context(), updated)})
}

func (s *Server) handlePostQAExperimentReadout(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req postQAExperimentReadoutReq
	if !readJSON(w, r, &req) {
		return
	}
	exp, err := s.tasks.Store.GetQAExperiment(r.Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrQAExperimentNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "import"
	}

	// Guard: refuse judge execution while the experiment is running with any enrolled task in flight (spec-v2.1 §12.1).
	if action == "judge-prompt" {
		if exp.State == model.QAExpStateRunning {
			enrollments, _ := s.tasks.Store.ListQAEnrollments(r.Context(), exp.ID)
			enrolledOrigins := map[string]bool{}
			for _, e := range enrollments {
				if e.ArmID != "excluded" {
					enrolledOrigins[e.RootOrigin] = true
				}
			}
			if len(enrolledOrigins) > 0 {
				tasks, _ := s.tasks.Store.ListTasks(r.Context())
				runTimings, _ := s.tasks.Store.RunTimings(r.Context())
				enrolledTasks := map[string]bool{}
				for _, t := range tasks {
					if enrolledOrigins[model.ResolveQARootOrigin(t.WorkOrigin, t.ID)] {
						enrolledTasks[t.ID] = true
					}
				}
				for _, rt := range runTimings {
					if enrolledTasks[rt.TaskID] && rt.FinishedAt == nil {
						writeError(w, http.StatusConflict, fmt.Errorf("judge prompt refused while enrolled task %s is running in experiment %s", rt.TaskID, exp.ID))
						return
					}
				}
			}
		}
		hooks := resolve.GetRuntimeHooks()
		if hooks == nil || hooks.RunJudgeModel == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("live judge model runtime is not configured on this server"))
			return
		}
		if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Prompt) == "" {
			writeError(w, http.StatusBadRequest, errors.New("model and prompt are required for judge-prompt"))
			return
		}
		out, inTok, outTok, jErr := hooks.RunJudgeModel(r.Context(), req.Model, req.Prompt)
		if jErr != nil {
			writeError(w, http.StatusBadGateway, jErr)
			return
		}
		now := s.tasks.now()
		actor := s.tasks.requestActor(r.Context())
		_, _ = s.tasks.Store.RecordConfigAudit(r.Context(), model.ConfigAuditEntry{
			At:         now,
			By:         actor.Actor,
			OnBehalfOf: actor.OnBehalfOf,
			Surface:    "qa-experiment:" + exp.ID,
			Summary:    fmt.Sprintf("executed readout judge prompt on %s using model %s (%d+%d tokens)", exp.ID, req.Model, inTok, outTok),
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"output":    out,
			"tokensIn":  inTok,
			"tokensOut": outTok,
		})
		return
	}

	if action != "import" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported readout action %q", action))
		return
	}

	imported := qaImportedReadout{
		ImportedAt:       s.tasks.now().UTC().Format(time.RFC3339),
		JudgeModel:       strings.TrimSpace(req.Model),
		Manifest:         req.Manifest,
		Scores:           req.Scores,
		Severities:       req.Severities,
		Outcomes:         req.Outcomes,
		HumanLabelsCount: req.HumanLabelsCount,
		HumanAgreement:   req.HumanAgreement,
	}
	if strings.TrimSpace(req.ReadoutDir) != "" {
		if strings.TrimSpace(s.tasks.Config.DataDir) == "" {
			writeError(w, http.StatusBadRequest, errors.New("readoutDir import requires a configured server dataDir; submit inline readout JSON instead"))
			return
		}
		readoutsRoot := filepath.Join(s.tasks.Config.DataDir, "qa", "readouts")
		if mkErr := os.MkdirAll(readoutsRoot, 0o750); mkErr != nil {
			writeError(w, http.StatusInternalServerError, mkErr)
			return
		}
		safeDir, spErr := safepath.Within(readoutsRoot, strings.TrimSpace(req.ReadoutDir))
		if spErr != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("readoutDir must reside within server qa/readouts directory: %w", spErr))
			return
		}
		manifest, scores, sevs, probe, _, lErr := judge.LoadReadoutBundle(safeDir)
		if lErr != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("loading readout bundle: %w", lErr))
			return
		}
		imported.Manifest = &manifest
		imported.Scores = scores
		imported.Severities = sevs
		imported.ProbeAUC = probe.AUC
	}
	if req.Probe != nil {
		imported.ProbeAUC = req.Probe.AUC
	}
	if len(req.RaterMatrix) > 0 {
		imported.ICC = judge.ComputeICC1K(req.RaterMatrix)
	} else {
		var matrix [][]float64
		for _, o := range imported.Outcomes {
			if len(o.RaterScores) >= 2 {
				matrix = append(matrix, o.RaterScores)
			}
		}
		if len(matrix) >= 2 {
			imported.ICC = judge.ComputeICC1K(matrix)
		}
	}
	probeOK := imported.ProbeAUC == 0 || imported.ProbeAUC <= 0.60
	imported.Validated = imported.ICC >= 0.70 && probeOK && imported.HumanLabelsCount >= 20

	rawBytes, err := json.Marshal(imported)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated, err := s.tasks.Store.UpdateQAExperiment(r.Context(), exp.ID, 0, func(cur *model.QAExperiment) error {
		cur.Readout = rawBytes
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := s.tasks.now()
	actor := s.tasks.requestActor(r.Context())
	_, _ = s.tasks.Store.RecordConfigAudit(r.Context(), model.ConfigAuditEntry{
		At:         now,
		By:         actor.Actor,
		OnBehalfOf: actor.OnBehalfOf,
		Surface:    "qa-experiment:" + exp.ID,
		Summary:    fmt.Sprintf("imported readout for QA experiment %s (ICC=%.2f, probeAUC=%.2f, outcomes=%d)", exp.ID, imported.ICC, imported.ProbeAUC, len(imported.Outcomes)),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"experiment": s.buildExperimentDetailView(r.Context(), updated),
		"readout":    imported,
	})
}

func (s *Server) handleGetQAExperimentResults(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	ctx := r.Context()
	exp, err := s.tasks.Store.GetQAExperiment(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrQAExperimentNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	refArmID := exp.ReferenceArmID
	var treatArmID string
	if len(exp.Arms) >= 2 {
		foundRef := false
		for _, a := range exp.Arms {
			if a.ID == refArmID {
				foundRef = true
				break
			}
		}
		if !foundRef {
			refArmID = exp.Arms[0].ID
			treatArmID = exp.Arms[1].ID
		} else {
			for _, a := range exp.Arms {
				if a.ID != refArmID {
					treatArmID = a.ID
					break
				}
			}
		}
	}

	enrollments, _ := s.tasks.Store.ListQAEnrollments(ctx, exp.ID)
	counts := map[string]int{}
	weightsMap := map[string]float64{}
	var totalWeight float64
	for _, a := range exp.Arms {
		counts[a.ID] = 0
		if !a.Halted && a.Weight > 0 {
			weightsMap[a.ID] = float64(a.Weight)
			totalWeight += float64(a.Weight)
		}
	}
	if totalWeight > 0 {
		for k, v := range weightsMap {
			weightsMap[k] = v / totalWeight
		}
	}

	auditEnrollments := make([]expstats.Enrollment, 0, len(enrollments))
	armByOrigin := map[string]string{}
	segmentByOrigin := map[string]int{}
	pairByOrigin := map[string]string{}
	keyToPair := map[string]string{}
	explicitEntries := make([]expstats.ExplicitEntry, len(exp.AllocationList))
	for i, e := range exp.AllocationList {
		explicitEntries[i] = expstats.ExplicitEntry{
			Key:     e.Key,
			ArmID:   e.ArmID,
			Stratum: e.Stratum,
			PairID:  e.PairID,
		}
		if e.PairID != "" {
			keyToPair[e.Key] = e.PairID
		}
	}

	for _, e := range enrollments {
		// Include only origins enrolled between StartedAt and StoppedAt when available.
		if exp.StartedAt != nil && e.AssignedAt.Before(exp.StartedAt.Add(-time.Second)) {
			continue
		}
		if exp.StoppedAt != nil && e.AssignedAt.After(exp.StoppedAt.Add(time.Second)) {
			continue
		}
		excluded := e.ArmID == "excluded"
		auditEnrollments = append(auditEnrollments, expstats.Enrollment{
			RootOrigin: e.RootOrigin,
			Key:        e.AllocationKey,
			ArmID:      e.ArmID,
			Excluded:   excluded,
		})
		if !excluded {
			counts[e.ArmID]++
			armByOrigin[e.RootOrigin] = e.ArmID
			segmentByOrigin[e.RootOrigin] = e.Segment
			if pID := keyToPair[e.AllocationKey]; pID != "" {
				pairByOrigin[e.RootOrigin] = pID
			}
		}
	}

	auditRep := expstats.AuditAssignments(
		exp.Allocation.Mode,
		exp.Salt,
		resolve.NormalizedBasisWeights(exp.Arms),
		explicitEntries,
		auditEnrollments,
	)
	mismatches := make([]map[string]string, 0, len(auditRep.Mismatches))
	for _, m := range auditRep.Mismatches {
		mismatches = append(mismatches, map[string]string{
			"rootOrigin":  m.RootOrigin,
			"expectedArm": m.Expected,
			"actualArm":   m.Actual,
		})
	}
	srmP := expstats.ExactMultinomialSRM(counts, weightsMap)

	tasks, _ := s.tasks.Store.ListTasks(ctx)
	timings, _ := s.tasks.Store.TaskTimings(ctx)
	runTimings, _ := s.tasks.Store.RunTimings(ctx)
	usages, _ := s.tasks.Store.RunUsagesSince(ctx, time.Time{})
	allCheckRuns, _ := s.tasks.Store.ListAllQACheckRuns(ctx)
	allFindings, _ := s.tasks.Store.ListAllQAFindings(ctx)

	firstRunByTask := map[string]time.Time{}
	for _, rt := range runTimings {
		if rt.StartedAt.IsZero() {
			continue
		}
		if prev, ok := firstRunByTask[rt.TaskID]; !ok || rt.StartedAt.Before(prev) {
			firstRunByTask[rt.TaskID] = rt.StartedAt
		}
	}
	prOpenedByTask := map[string]time.Time{}
	for _, tm := range timings {
		if tm.PrOpenedAt != nil && !tm.PrOpenedAt.IsZero() {
			prOpenedByTask[tm.TaskID] = *tm.PrOpenedAt
		}
	}
	tokensByRun := map[string]int64{}
	tokensByTask := map[string]int64{}
	for _, u := range usages {
		b := u.Billable()
		if b <= 0 {
			b = u.InputTokens + u.OutputTokens
		}
		tokensByRun[u.RunID] += b
		if u.TaskID != "" {
			tokensByTask[u.TaskID] += b
		}
	}
	for _, rt := range runTimings {
		if tokensByTask[rt.TaskID] == 0 {
			tokensByTask[rt.TaskID] += tokensByRun[rt.RunID]
		}
	}

	// Map blind review tasks ("Review owner/repo#N") and task aggregates per root origin.
	type originAgg struct {
		armID        string
		segment      int
		prs          int
		escapes      int
		sevSum       float64
		held         bool
		timesToPR    []float64
		tokens       int64
		prodAdded    int
		testAdded    int
		leakDetected bool
	}
	aggs := map[string]*originAgg{}
	for root, arm := range armByOrigin {
		aggs[root] = &originAgg{armID: arm, segment: segmentByOrigin[root]}
	}

	modelByArm := map[string]map[string]int{}
	for _, a := range exp.Arms {
		modelByArm[a.ID] = map[string]int{}
	}
	enrolledTaskIDs := map[string]string{} // taskID -> rootOrigin
	for _, t := range tasks {
		root := model.ResolveQARootOrigin(t.WorkOrigin, t.ID)
		arm := armByOrigin[root]
		if arm == "" {
			continue
		}
		if exp.StoppedAt != nil {
			if fs, ok := firstRunByTask[t.ID]; ok && fs.After(*exp.StoppedAt) {
				continue
			}
		}
		enrolledTaskIDs[t.ID] = root
		mName := strings.TrimSpace(t.Model)
		if mName == "" {
			mName = "default"
		}
		if modelByArm[arm] == nil {
			modelByArm[arm] = map[string]int{}
		}
		modelByArm[arm][mName]++

		ag := aggs[root]
		ag.tokens += tokensByTask[t.ID]
		if _, hasPR := prOpenedByTask[t.ID]; hasPR {
			ag.prs++
			if strings.Contains(strings.ToLower(t.Title), "grain-qa-stage/") || strings.Contains(strings.ToLower(t.Body), "grain-qa-stage/") {
				ag.leakDetected = true
			}
		}
		for _, l := range t.Links {
			switch l.Kind {
			case model.LinkQAHold:
				ag.held = true
			case model.LinkReviewFindings:
				if rec, ok := model.ParseReviewFindingsRecord(l.Target); ok {
					if rec.Gate == model.GateQA {
						ag.held = true
					} else if strings.TrimSpace(rec.Summary) != "" {
						ag.escapes++
						ag.sevSum += 2.0
					}
				}
			case model.LinkReviewQuestions, model.LinkRelayedReviewThread, model.LinkDraftReview:
				ag.escapes++
				ag.sevSum += 1.0
			}
		}
		if st, okS := firstRunByTask[t.ID]; okS {
			if op, okP := prOpenedByTask[t.ID]; okP && !op.Before(st) {
				ag.timesToPR = append(ag.timesToPR, op.Sub(st).Seconds())
			}
		}
	}

	modelMixP, modelFlagged := expstats.ModelMixBalanceTest(modelByArm)

	// Parse imported readout if present.
	var imported *qaImportedReadout
	if len(exp.Readout) > 0 {
		var parsed qaImportedReadout
		if json.Unmarshal(exp.Readout, &parsed) == nil {
			imported = &parsed
		}
	}

	plannedN := exp.Plan.PlannedOriginsPerArm
	if plannedN < 1 {
		plannedN = 30
	}
	minArmCount := math.MaxInt32
	for _, a := range exp.Arms {
		if !a.Halted && counts[a.ID] < minArmCount {
			minArmCount = counts[a.ID]
		}
	}
	if minArmCount == math.MaxInt32 {
		minArmCount = 0
	}
	provisional := exp.State == model.QAExpStateRunning && minArmCount < plannedN

	// Build forest rows and verdicts.
	var verdicts []map[string]any
	var forestRows []map[string]any

	treatArm := exp.Arms[0]
	for _, a := range exp.Arms {
		if a.ID != refArmID {
			treatArm = a
			break
		}
	}

	buildClusterPair := func(extract func(*originAgg) (num, den float64, ok bool)) (ctrlObs, treatObs []expstats.ClusterObs) {
		for _, ag := range aggs {
			num, den, ok := extract(ag)
			if !ok {
				continue
			}
			obs := expstats.ClusterObs{Num: num, Den: den, Segment: strconv.Itoa(ag.segment)}
			if ag.armID == refArmID {
				ctrlObs = append(ctrlObs, obs)
			} else if ag.armID == treatArm.ID {
				treatObs = append(treatObs, obs)
			}
		}
		return
	}

	clampPct := func(v, span float64) float64 {
		// Maps [-span, +span] improvement onto [5%, 95%] with 50% = neutral 0.
		if span <= 0 {
			span = 1.0
		}
		p := 50.0 + (v/span)*40.0
		if p < 4 {
			p = 4
		}
		if p > 96 {
			p = 96
		}
		return math.Round(p*10) / 10
	}

	// If paired readout outcomes exist, evaluate paired primary/co-primaries; otherwise online cluster metrics.
	primaryP := 1.0
	primaryDelta := 0.0
	hasPrimaryData := false

	if imported != nil && len(imported.Outcomes) > 0 {
		byKey := map[string]map[string]qaPairEndpointOutcome{}
		for _, o := range imported.Outcomes {
			k := firstNonEmptyStr(o.SpecKey, o.PairID)
			if k == "" {
				continue
			}
			if byKey[k] == nil {
				byKey[k] = map[string]qaPairEndpointOutcome{}
			}
			byKey[k][o.ArmID] = o
		}
		var panelDiffs, mutDiffs, hidDiffs []float64
		var keys []string
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pair := byKey[k]
			cOut, okC := pair[refArmID]
			tOut, okT := pair[treatArm.ID]
			if !okC || !okT {
				continue
			}
			if cOut.PanelScore != nil && tOut.PanelScore != nil {
				panelDiffs = append(panelDiffs, *tOut.PanelScore-*cOut.PanelScore)
			}
			if cOut.MutationScore != nil && tOut.MutationScore != nil {
				mutDiffs = append(mutDiffs, (*tOut.MutationScore-*cOut.MutationScore)*100.0)
			}
			if cOut.HiddenPassed != nil && tOut.HiddenPassed != nil {
				cv, tv := 0.0, 0.0
				if *cOut.HiddenPassed {
					cv = 100.0
				}
				if *tOut.HiddenPassed {
					tv = 100.0
				}
				hidDiffs = append(hidDiffs, tv-cv)
			}
		}

		var rawPs []float64
		type epRow struct {
			id, label, sub, tip, minL, maxL, valFmt string
			mean, lo, hi, span, p                   float64
		}
		var epRows []epRow
		if len(panelDiffs) >= 2 {
			m, lo, hi, p, rho := expstats.PairedSignFlipTest(panelDiffs, 5000, 42)
			rawPs = append(rawPs, p)
			epRows = append(epRows, epRow{
				id: "quality.panel_score", label: "Senior review score (1–10)",
				sub:  fmt.Sprintf("Primary · %d matched pairs (ρ=%.2f)", len(panelDiffs), rho),
				tip:  "Mean paired difference (treatment minus control) across 3 blind raters.",
				minL: "-2.0 pts", maxL: "+2.0 pts",
				valFmt: fmt.Sprintf("%+.2f pts [%.2f, %.2f]", m, lo, hi),
				mean:   m, lo: lo, hi: hi, span: 2.0, p: p,
			})
		}
		if len(mutDiffs) >= 2 {
			m, lo, hi, p, _ := expstats.PairedSignFlipTest(mutDiffs, 5000, 43)
			rawPs = append(rawPs, p)
			epRows = append(epRows, epRow{
				id: "quality.mutation_score.added_lines", label: "Test strength on new code",
				sub:  fmt.Sprintf("Co-primary · %d matched pairs", len(mutDiffs)),
				tip:  "Paired difference in mutation kill rate (percentage points) on PR-added lines.",
				minL: "-30 pp", maxL: "+30 pp",
				valFmt: fmt.Sprintf("%+.1f pp [%.1f, %.1f]", m, lo, hi),
				mean:   m, lo: lo, hi: hi, span: 30.0, p: p,
			})
		}
		if len(hidDiffs) >= 2 {
			m, lo, hi, p, _ := expstats.PairedSignFlipTest(hidDiffs, 5000, 44)
			rawPs = append(rawPs, p)
			epRows = append(epRows, epRow{
				id: "bench.hidden_acceptance_pass", label: "Hidden acceptance tests passed",
				sub:  fmt.Sprintf("Co-primary · %d matched pairs", len(hidDiffs)),
				tip:  "Paired pass-rate difference (percentage points) on spec-derived hidden acceptance tests.",
				minL: "-40 pp", maxL: "+40 pp",
				valFmt: fmt.Sprintf("%+.1f pp [%.1f, %.1f]", m, lo, hi),
				mean:   m, lo: lo, hi: hi, span: 40.0, p: p,
			})
		}
		adjPs := expstats.HolmAdjust(rawPs)
		for i, r := range epRows {
			loPct := clampPct(r.lo, r.span)
			hiPct := clampPct(r.hi, r.span)
			val := r.valFmt
			if !provisional && i < len(adjPs) {
				val = fmt.Sprintf("%s · Holm p=%.3f", val, adjPs[i])
			}
			forestRows = append(forestRows, map[string]any{
				"id":         r.id,
				"label":      r.label,
				"sub":        r.sub,
				"tip":        r.tip,
				"ciLowPct":   loPct,
				"ciWidthPct": math.Max(1.5, hiPct-loPct),
				"meanPct":    clampPct(r.mean, r.span),
				"minLabel":   r.minL,
				"maxLabel":   r.maxL,
				"valueStr":   val,
			})
		}
		if len(epRows) > 0 {
			hasPrimaryData = true
			primaryDelta = epRows[0].mean
			if len(adjPs) > 0 {
				primaryP = adjPs[0]
			}
		}
	}

	// Online work-tree cluster rows (always included when PRs exist).
	ctrlFind, treatFind := buildClusterPair(func(ag *originAgg) (float64, float64, bool) {
		if ag.prs == 0 {
			return 0, 0, false
		}
		return ag.sevSum, float64(ag.prs), true
	})
	if len(ctrlFind) >= 1 && len(treatFind) >= 1 {
		// Lower findings/PR is better -> improvement = -(treat - ctrl).
		delta, lo, hi := expstats.ClusterRatioEstimate(treatFind, ctrlFind, "lower", 2000, 42)
		p := expstats.ClusterPermutationTest(treatFind, ctrlFind, exp.Segment > 1, 2000, 42)
		impMean, impLo, impHi := -delta, -hi, -lo
		loPct := clampPct(impLo, 2.0)
		hiPct := clampPct(impHi, 2.0)
		subLabel := "Online work-tree ratio"
		if provisional {
			subLabel = "Provisional · valid at planned n only"
		}
		valStr := fmt.Sprintf("%+.2f findings/PR [%.2f, %.2f]", delta, lo, hi)
		if !provisional {
			valStr = fmt.Sprintf("%s · p=%.3f", valStr, p)
		}
		forestRows = append(forestRows, map[string]any{
			"id":         "review.findings_sev.per_pr",
			"label":      "Review findings per PR",
			"sub":        subLabel,
			"tip":        "Clustered ratio of review findings per PR across enrolled work trees (right = fewer findings).",
			"ciLowPct":   loPct,
			"ciWidthPct": math.Max(1.5, hiPct-loPct),
			"meanPct":    clampPct(impMean, 2.0),
			"minLabel":   "+2.0 worse",
			"maxLabel":   "-2.0 better",
			"valueStr":   valStr,
		})
		if !hasPrimaryData {
			hasPrimaryData = true
			primaryDelta = impMean
			primaryP = p
		}
	}

	// Build plain-language verdict per treatment arm.
	for _, a := range exp.Arms {
		if a.ID == refArmID {
			continue
		}
		nArm := counts[a.ID]
		need := max(0, plannedN-nArm)
		switch {
		case a.Halted:
			verdicts = append(verdicts, map[string]any{
				"armId":   a.ID,
				"status":  "Halted",
				"title":   fmt.Sprintf("%s · Halted", a.Label),
				"summary": fmt.Sprintf("Arm %s was halted (%s).", a.Label, firstNonEmptyStr(a.HaltReason, "operator action")),
			})
		case !auditRep.Passed:
			verdicts = append(verdicts, map[string]any{
				"armId":   a.ID,
				"status":  "Invalid",
				"title":   fmt.Sprintf("%s · Assignment check failed", a.Label),
				"summary": fmt.Sprintf("%d enrollment mismatch(es) detected; readout decision is blocked.", len(auditRep.Mismatches)),
			})
		case provisional:
			status := "Provisional"
			if hasPrimaryData && primaryDelta > 0.05 {
				status = "Leading"
			} else if hasPrimaryData && primaryDelta < -0.05 {
				status = "Trailing"
			}
			verdicts = append(verdicts, map[string]any{
				"armId":  a.ID,
				"status": status,
				"title":  fmt.Sprintf("%s · %s (%d of %d work trees)", a.Label, status, nArm, plannedN),
				"summary": fmt.Sprintf("%d more work trees needed in %s before the fixed-horizon decision test runs.",
					need, a.Label),
			})
		default:
			status := "Inconclusive at horizon"
			summary := fmt.Sprintf("At %d work trees, %s did not cross the pre-registered Holm threshold (p=%.3f).", nArm, a.Label, primaryP)
			if hasPrimaryData && primaryP < exp.Plan.Alpha {
				if primaryDelta > 0 {
					status = "Better"
					summary = fmt.Sprintf("%s outperformed Control on the pre-registered primary endpoint (Holm p=%.3f).", a.Label, primaryP)
				} else if primaryDelta < 0 {
					status = "Worse"
					summary = fmt.Sprintf("%s underperformed Control on the pre-registered primary endpoint (Holm p=%.3f).", a.Label, primaryP)
				}
			}
			verdicts = append(verdicts, map[string]any{
				"armId":   a.ID,
				"status":  status,
				"title":   fmt.Sprintf("%s · %s", a.Label, status),
				"summary": summary,
			})
		}
	}
	if verdicts == nil {
		verdicts = []map[string]any{}
	}
	if forestRows == nil {
		forestRows = []map[string]any{}
	}

	// Guardrails table.
	var ctrlTimes, treatTimes []float64
	heldTrees, treatTrees, leaksCount := 0, 0, 0
	var ctrlTokens, treatTokens []float64
	for _, ag := range aggs {
		if ag.leakDetected {
			leaksCount++
		}
		if len(ag.timesToPR) > 0 {
			durs := slices.Clone(ag.timesToPR)
			slices.Sort(durs)
			med := durs[len(durs)/2]
			if ag.armID == refArmID {
				ctrlTimes = append(ctrlTimes, med)
			} else if ag.armID == treatArm.ID {
				treatTimes = append(treatTimes, med)
			}
		}
		if ag.armID == refArmID {
			ctrlTokens = append(ctrlTokens, float64(ag.tokens))
		} else if ag.armID == treatArm.ID {
			treatTrees++
			if ag.held {
				heldTrees++
			}
			treatTokens = append(treatTokens, float64(ag.tokens))
		}
	}
	tau := exp.Plan.HarmRefTau
	if tau <= 0 {
		tau = 300.0
	}
	timeVal := "awaiting >= 15 work trees/arm"
	timeState := "ok"
	if len(treatTimes) >= 1 && len(ctrlTimes) >= 1 {
		_, _, harmed, hlShift := expstats.NormalMixtureSPRTOneSided(treatTimes, ctrlTimes, tau, 0.01)
		timeVal = fmt.Sprintf("%+.1f%% HL shift (%d vs %d trees)", hlShift*100, len(treatTimes), len(ctrlTimes))
		if len(treatTimes) >= 15 && len(ctrlTimes) >= 15 && harmed && hlShift > 0.25 {
			timeState = "tripped"
		} else if hlShift > 0.25 {
			timeState = "watch"
		}
	}
	heldPct := 0.0
	if treatTrees > 0 {
		heldPct = 100.0 * float64(heldTrees) / float64(treatTrees)
	}
	heldState := "ok"
	if heldPct > 25.0 {
		heldState = "watch"
	}
	leakState := "ok"
	if leaksCount > 0 {
		leakState = "tripped"
	}
	tokVal := "0 tokens"
	tokState := "ok"
	if len(treatTokens) > 0 && len(ctrlTokens) > 0 {
		cMed, tMed := medianFloat(ctrlTokens), medianFloat(treatTokens)
		tokVal = fmt.Sprintf("%.0fk vs %.0fk median tokens", tMed/1000.0, cMed/1000.0)
		if len(treatTokens) >= 15 && cMed > 0 && tMed > 1.5*cMed {
			tokState = "watch"
		}
	}
	guardrails := []map[string]any{
		{"id": "flow.time_to_pr", "label": "Time to pull request (harm mSPRT)", "value": timeVal, "threshold": "mSPRT α=0.01 & >+25%", "state": timeState},
		{"id": "qa.held_rate", "label": "Work trees held by checks", "value": fmt.Sprintf("%d / %d (%.1f%%)", heldTrees, treatTrees, heldPct), "threshold": "<= 25%", "state": heldState},
		{"id": "flow.tokens.per_task", "label": "Tokens per work tree", "value": tokVal, "threshold": "<= 1.5x control", "state": tokState},
		{"id": "flow.blindness_leaks", "label": "Arm leaks in commits/PRs", "value": strconv.Itoa(leaksCount), "threshold": "0 leaks", "state": leakState},
	}

	// Per-check health over enrolled tasks.
	type ruleStat struct {
		runs, fails int
		durations   []int64
		posV, negV  int
	}
	ruleStats := map[string]*ruleStat{}
	checkRunEnrolled := map[int64]bool{}
	for _, cr := range allCheckRuns {
		if enrolledTaskIDs[cr.TaskID] == "" && cr.ExperimentID != exp.ID {
			continue
		}
		if cr.Stage == "S1" || cr.Stage == "S0" {
			continue
		}
		checkRunEnrolled[cr.ID] = true
		rs := ruleStats[cr.CheckID]
		if rs == nil {
			rs = &ruleStat{}
			ruleStats[cr.CheckID] = rs
		}
		rs.runs++
		if cr.Verdict == "fail" {
			rs.fails++
		}
		if cr.DurationMS > 0 {
			rs.durations = append(rs.durations, cr.DurationMS)
		}
	}
	for _, f := range allFindings {
		if !checkRunEnrolled[f.CheckRunID] && enrolledTaskIDs[f.TaskID] == "" {
			continue
		}
		rs := ruleStats[f.RuleID]
		if rs == nil {
			continue
		}
		if f.HumanVote > 0 {
			rs.posV++
		} else if f.HumanVote < 0 {
			rs.negV++
		}
	}
	var ruleIDs []string
	for idKey := range ruleStats {
		ruleIDs = append(ruleIDs, idKey)
	}
	sort.Strings(ruleIDs)
	checkHealth := make([]map[string]any, 0, len(ruleIDs))
	for _, rid := range ruleIDs {
		rs := ruleStats[rid]
		slices.Sort(rs.durations)
		p95 := int64(0)
		if len(rs.durations) > 0 {
			idx := int(math.Ceil(0.95*float64(len(rs.durations)))) - 1
			if idx < 0 {
				idx = 0
			}
			p95 = rs.durations[idx]
		}
		prec := "not voted"
		if rs.posV+rs.negV > 0 {
			prec = fmt.Sprintf("%.0f%% (%d/%d)", 100*float64(rs.posV)/float64(rs.posV+rs.negV), rs.posV, rs.posV+rs.negV)
		}
		checkHealth = append(checkHealth, map[string]any{
			"id":        rid,
			"precision": prec,
			"applies":   fmt.Sprintf("%d runs (%d flagged)", rs.runs, rs.fails),
			"p95":       fmt.Sprintf("%d ms", p95),
		})
	}

	// PR shape per arm.
	prShape := make([]map[string]any, 0, len(exp.Arms))
	for _, a := range exp.Arms {
		var prsList, prodList, testList []float64
		for _, ag := range aggs {
			if ag.armID == a.ID {
				prsList = append(prsList, float64(ag.prs))
				prodList = append(prodList, float64(ag.prodAdded))
				testList = append(testList, float64(ag.testAdded))
			}
		}
		medPRs := medianFloat(prsList)
		medProd := medianFloat(prodList)
		medTest := medianFloat(testList)
		ratioStr := "—"
		if medProd > 0 && medTest > 0 {
			ratioStr = fmt.Sprintf("%.2f", medTest/medProd)
		}
		prShape = append(prShape, map[string]any{
			"armId":          a.ID,
			"prsPerWorkTree": fmt.Sprintf("%.1f", medPRs),
			"prodLines":      fmt.Sprintf("%.0f", medProd),
			"testLines":      fmt.Sprintf("%.0f", medTest),
			"testProdRatio":  ratioStr,
		})
	}

	// Rubric summary when readout exists.
	var rubric any
	if imported != nil && (len(imported.Scores) > 0 || len(imported.Outcomes) > 0) {
		scoresByArm := map[string][]float64{}
		for _, sc := range imported.Scores {
			scoresByArm[sc.ArmID] = append(scoresByArm[sc.ArmID], sc.Score)
		}
		if len(scoresByArm) == 0 {
			for _, o := range imported.Outcomes {
				if o.PanelScore != nil {
					scoresByArm[o.ArmID] = append(scoresByArm[o.ArmID], *o.PanelScore)
				}
			}
		}
		var armRows []map[string]any
		for _, a := range exp.Arms {
			vals := scoresByArm[a.ID]
			m, lo, hi := meanAndCI(vals)
			armRows = append(armRows, map[string]any{
				"armId":  a.ID,
				"mean":   math.Round(m*100) / 100,
				"ciLow":  math.Round(lo*100) / 100,
				"ciHigh": math.Round(hi*100) / 100,
			})
		}
		rubric = map[string]any{
			"validated": imported.Validated,
			"icc":       math.Round(imported.ICC*1000) / 1000,
			"probeAuc":  math.Round(imported.ProbeAUC*1000) / 1000,
			"dimensions": []map[string]any{
				{
					"id":    "overall",
					"label": "Overall senior review score (1–10)",
					"arms":  armRows,
				},
			},
		}
	}

	// Limitations & period label.
	sharedMain := true
	distinctBases := map[string]bool{}
	for _, a := range exp.Arms {
		if strings.TrimSpace(a.BaseBranch) != "" {
			distinctBases[strings.TrimSpace(a.BaseBranch)] = true
		}
	}
	if len(distinctBases) == len(exp.Arms) {
		sharedMain = false
	}

	memWrites := map[string]int{}
	for _, a := range exp.Arms {
		memWrites[a.ID] = 0
	}
	if mems, mErr := s.tasks.Store.ListMemories(ctx); mErr == nil {
		for _, m := range mems {
			if m.UpdatedBy.Kind == model.PrincipalAgent {
				if root := enrolledTaskIDs[m.UpdatedBy.ID]; root != "" {
					memWrites[armByOrigin[root]]++
				}
			}
		}
	}

	periodLabel := "Draft (not started)"
	if exp.StartedAt != nil {
		end := s.tasks.now()
		endStr := "now"
		if exp.StoppedAt != nil {
			end = *exp.StoppedAt
			endStr = exp.StoppedAt.UTC().Format("Jan 2")
		}
		days := max(1, int(end.Sub(*exp.StartedAt).Hours()/24)+1)
		periodLabel = fmt.Sprintf("Experiment period: %s – %s (day %d of %d)",
			exp.StartedAt.UTC().Format("Jan 2"), endStr, days, max(1, exp.Plan.MaxDays))
	}

	// Suppress unused import warning on metrics if only constants/helpers referenced elsewhere.
	_ = metrics.MinTreatmentSampleForAutoDowngrade

	var declarationDiff any
	armFlowSpec := map[string]qa.FlowSpec{}
	for _, a := range exp.Arms {
		if a.FlowHash != "" && !strings.EqualFold(a.FlowHash, "control") {
			if b, _, _, _, ok, _ := s.lookupFlowBundle(ctx, a.FlowHash); ok {
				armFlowSpec[a.ID] = b.Resolved
			}
		}
	}
	if treatArmID != "" {
		var refHash, candHash string
		for _, a := range exp.Arms {
			if a.ID == refArmID {
				refHash = a.FlowHash
			}
			if a.ID == treatArmID {
				candHash = a.FlowHash
			}
		}
		if candHash != "" && !strings.EqualFold(candHash, "control") {
			cB, _, _, _, cOK, _ := s.lookupFlowBundle(ctx, candHash)
			if cOK {
				var rPtr *qa.FlowVersionBundle
				if refHash != "" && !strings.EqualFold(refHash, "control") {
					if rB, _, _, _, rOK, _ := s.lookupFlowBundle(ctx, refHash); rOK {
						rPtr = &rB
					}
				}
				diff := qa.ComputeDeclarationDiff(rPtr, &cB)
				declarationDiff = &diff
			}
		}
	}

	escapedOrigins := map[string]bool{}
	for root, ag := range aggs {
		if ag.escapes > 0 {
			escapedOrigins[root] = true
		}
	}
	if imported != nil {
		for _, o := range imported.Outcomes {
			root := strings.TrimSpace(o.RootOrigin)
			if root == "" && o.TaskID != "" {
				root = enrolledTaskIDs[o.TaskID]
			}
			if root == "" {
				continue
			}
			if (o.HiddenPassed != nil && !*o.HiddenPassed) || (o.SeverityWeightedSum != nil && *o.SeverityWeightedSum > 0) {
				escapedOrigins[root] = true
			}
		}
	}

	stageFunnel := computeExperimentStageFunnel(exp, enrolledTaskIDs, armByOrigin, allCheckRuns, allFindings)
	triageMetrics := computeExperimentTriageMetrics(exp, armFlowSpec, enrolledTaskIDs, armByOrigin, escapedOrigins, allCheckRuns)

	writeJSON(w, http.StatusOK, map[string]any{
		"experimentId":    exp.ID,
		"referenceArmId":  refArmID,
		"declarationDiff": declarationDiff,
		"stageFunnel":     stageFunnel,
		"triageMetrics":   triageMetrics,
		"state":           exp.State,
		"periodLabel":     periodLabel,
		"scopeRepos":      exp.Scope.Repos,
		"verdicts":        verdicts,
		"forestRows":      forestRows,
		"provisional":     provisional,
		"assignmentAudit": map[string]any{
			"passed":       auditRep.Passed,
			"agreementPct": auditRep.AgreementPct,
			"checkedCount": auditRep.Total,
			"mismatches":   mismatches,
		},
		"srm": map[string]any{
			"pValue": srmP,
			"counts": counts,
		},
		"modelMix": map[string]any{
			"balanced": !modelFlagged,
			"pValue":   modelMixP,
			"byArm":    modelByArm,
		},
		"guardrails":  guardrails,
		"checkHealth": checkHealth,
		"rubric":      rubric,
		"prShape":     prShape,
		"limitations": map[string]any{
			"sharedMain":        sharedMain,
			"memoryMode":        exp.Options.Memory,
			"memoryWritesByArm": memWrites,
			"exposureBreaks":    0,
		},
		"pairByOrigin": pairByOrigin,
	})
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func medianFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := slices.Clone(vals)
	slices.Sort(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return 0.5 * (cp[n/2-1] + cp[n/2])
}

func meanAndCI(vals []float64) (mean, lo, hi float64) {
	n := len(vals)
	if n == 0 {
		return 0, 0, 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean = sum / float64(n)
	if n < 2 {
		return mean, mean, mean
	}
	var sq float64
	for _, v := range vals {
		d := v - mean
		sq += d * d
	}
	se := math.Sqrt(sq/float64(n-1)) / math.Sqrt(float64(n))
	return mean, mean - 1.96*se, mean + 1.96*se
}

func computeExperimentStageFunnel(
	exp model.QAExperiment,
	enrolledTaskIDs map[string]string,
	armByOrigin map[string]string,
	allCheckRuns []model.QACheckRun,
	allFindings []model.QAFinding,
) []map[string]any {
	out := []map[string]any{}
	armTasks := map[string]map[string]bool{}
	for _, a := range exp.Arms {
		armTasks[a.ID] = map[string]bool{}
	}
	for taskID, root := range enrolledTaskIDs {
		arm := armByOrigin[root]
		if arm != "" {
			if armTasks[arm] == nil {
				armTasks[arm] = map[string]bool{}
			}
			armTasks[arm][taskID] = true
		}
	}

	type stageKey struct {
		armID string
		stage string
	}
	type stageAcc struct {
		tasks        map[string]bool
		durations    []int64
		tokens       []int64
		failedRuns   int
		repairedRuns int
		findings     int
		helpful      int
		notHelpful   int
	}
	accs := map[stageKey]*stageAcc{}
	getAcc := func(armID, stage string) *stageAcc {
		k := stageKey{armID: armID, stage: stage}
		if a := accs[k]; a != nil {
			return a
		}
		a := &stageAcc{tasks: map[string]bool{}}
		accs[k] = a
		return a
	}

	for _, r := range allCheckRuns {
		root := enrolledTaskIDs[r.TaskID]
		if r.ExperimentID != exp.ID && root == "" {
			continue
		}
		armID := strings.TrimSpace(r.ArmID)
		if armID == "" && root != "" {
			armID = armByOrigin[root]
		}
		if armID == "" || r.Shadow {
			continue
		}
		stg := strings.TrimSpace(r.Stage)
		if stg == "" {
			stg = "S3"
		}
		acc := getAcc(armID, stg)
		acc.tasks[r.TaskID] = true
		if r.DurationMS > 0 {
			acc.durations = append(acc.durations, r.DurationMS)
		}
		tok := r.TokensIn + r.TokensOut
		if tok > 0 {
			acc.tokens = append(acc.tokens, tok)
		}
		if r.Verdict == "fail" || r.Verdict == "warn" {
			acc.failedRuns++
			if r.RepairedInNextSha > 0 {
				acc.repairedRuns++
			}
		}
	}

	for _, f := range allFindings {
		root := enrolledTaskIDs[f.TaskID]
		if root == "" {
			continue
		}
		armID := armByOrigin[root]
		if armID == "" {
			continue
		}
		stg := strings.TrimSpace(f.Stage)
		if stg == "" {
			stg = "S3"
		}
		acc := accs[stageKey{armID: armID, stage: stg}]
		if acc == nil {
			continue
		}
		acc.findings++
		if f.HumanVote > 0 {
			acc.helpful++
		} else if f.HumanVote < 0 {
			acc.notHelpful++
		}
	}

	stageOrder := func(stg string) int {
		switch stg {
		case "S0":
			return 0
		case "S1":
			return 1
		case "S2":
			return 2
		case "S3":
			return 3
		case "S4":
			return 5
		case "S5":
			return 6
		default:
			return 4
		}
	}

	keys := make([]stageKey, 0, len(accs))
	for k := range accs {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b stageKey) int {
		if a.armID != b.armID {
			return strings.Compare(a.armID, b.armID)
		}
		oa, ob := stageOrder(a.stage), stageOrder(b.stage)
		if oa != ob {
			return oa - ob
		}
		return strings.Compare(a.stage, b.stage)
	})

	for _, k := range keys {
		acc := accs[k]
		runs := len(acc.tasks)
		denomTasks := max(1, len(armTasks[k.armID]))
		appPct := math.Round(float64(runs)*1000.0/float64(denomTasks)) / 10.0
		var repairedPct float64
		if acc.failedRuns > 0 {
			repairedPct = math.Round(float64(acc.repairedRuns)*1000.0/float64(acc.failedRuns)) / 10.0
		}
		p50Ms, p95Ms := percentileInt64(acc.durations, 0.50), percentileInt64(acc.durations, 0.95)
		medTok := percentileInt64(acc.tokens, 0.50)
		var wilsonLB float64
		votes := acc.helpful + acc.notHelpful
		if votes > 0 {
			p := float64(acc.notHelpful) / float64(votes)
			z := 1.96
			n := float64(votes)
			wilsonLB = math.Max(0, (p+z*z/(2*n)-z*math.Sqrt((p*(1-p)+z*z/(4*n))/n))/(1+z*z/n))
			wilsonLB = math.Round(wilsonLB*1000) / 1000
		}
		out = append(out, map[string]any{
			"armId":                k.armID,
			"stage":                k.stage,
			"runs":                 runs,
			"applicablePct":        appPct,
			"findingsCount":        acc.findings,
			"repairedInNextShaPct": repairedPct,
			"durationP50Ms":        p50Ms,
			"durationP95Ms":        p95Ms,
			"tokensMedian":         medTok,
			"notHelpfulWilsonLB":   wilsonLB,
		})
	}
	return out
}

func computeExperimentTriageMetrics(
	exp model.QAExperiment,
	armFlowSpec map[string]qa.FlowSpec,
	enrolledTaskIDs map[string]string,
	armByOrigin map[string]string,
	escapedOrigins map[string]bool,
	allCheckRuns []model.QACheckRun,
) []map[string]any {
	out := []map[string]any{}
	obsByArm := map[string][]expstats.TriageObservation{}
	seen := map[string]bool{}
	for _, r := range allCheckRuns {
		if r.Stage != "S5" {
			continue
		}
		root := enrolledTaskIDs[r.TaskID]
		if r.ExperimentID != exp.ID && root == "" {
			continue
		}
		armID := strings.TrimSpace(r.ArmID)
		if armID == "" && root != "" {
			armID = armByOrigin[root]
		}
		if armID == "" {
			continue
		}
		originKey := root
		if originKey == "" {
			originKey = r.TaskID
		}
		dedup := armID + "|" + originKey
		if seen[dedup] {
			continue
		}
		seen[dedup] = true
		escaped := escapedOrigins[root]
		obsByArm[armID] = append(obsByArm[armID], expstats.TriageObservation{
			RootOrigin:                originKey,
			Decision:                  r.TriageDecision,
			Escaped:                   escaped,
			HadHumanFindingOrBoundary: escaped || r.BlastTier == "T3",
		})
	}

	for _, a := range exp.Arms {
		spec, hasSpec := armFlowSpec[a.ID]
		mode := ""
		if hasSpec {
			mode = spec.S5MergeTriage.Mode
		}
		obs := obsByArm[a.ID]
		if len(obs) == 0 && (mode == "" || mode == "off") {
			continue
		}
		if mode == "" {
			mode = "shadow"
		}
		sum := expstats.ComputeTriageSummary(obs)
		out = append(out, map[string]any{
			"armId":                       a.ID,
			"mode":                        mode,
			"autoMergeEligiblePct":        math.Round(sum.AutoMergeEligiblePct*1000) / 10,
			"humanRoutedPct":              math.Round(sum.HumanRoutedPct*1000) / 10,
			"autoMergedEscapeRate":        math.Round(sum.AutoMergedEscapeRate*1000) / 10,
			"humanRoutedTruePositiveRate": math.Round(sum.HumanRoutedTruePositiveRate*1000) / 10,
		})
	}
	return out
}

func percentileInt64(vals []int64, q float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	cp := slices.Clone(vals)
	slices.Sort(cp)
	idx := int(math.Ceil(q*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}
