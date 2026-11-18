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
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

type postQAProbeReq struct {
	FlowHash       string   `json:"flowHash"`
	FlowYAML       string   `json:"flowYaml"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Files          []string `json:"files"`
	NonTestAdded   int      `json:"nonTestAdded"`
	NonTestDeleted int      `json:"nonTestDeleted"`
	SiblingCallers int      `json:"siblingCallers"`
	TestTampered   bool     `json:"testTampered"`
}

func (s *Server) handlePostQAProbe(w http.ResponseWriter, r *http.Request) {
	var req postQAProbeReq
	if !readJSON(w, r, &req) {
		return
	}

	var flowBundle qa.FlowVersionBundle
	if strings.TrimSpace(req.FlowYAML) != "" {
		b, err := qa.ResolveAnyFlowDeclaration([]byte(req.FlowYAML), 0, func(p string) (qa.FlowVersionBundle, bool) {
			found, _, _, _, ok, _ := s.lookupFlowBundle(r.Context(), p)
			return found, ok
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		flowBundle = b
	} else {
		flowRef := strings.TrimSpace(req.FlowHash)
		if flowRef == "" {
			flowRef = "golden-v3@1"
		}
		found, _, _, _, ok, err := s.lookupFlowBundle(r.Context(), flowRef)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("flow version %q not found", flowRef))
			return
		}
		flowBundle = found
	}
	flow := flowBundle.Resolved

	comp := qa.AssessTaskComplexity(qa.ComplexityInput{
		Title:       req.Title,
		Body:        req.Body,
		ReadsCount:  0,
		WritesCount: len(req.Files),
	}, nil)

	risk := qa.AssessDiffBlast(qa.DiffStats{
		Files:                 req.Files,
		NonTestAdded:          req.NonTestAdded,
		NonTestDeleted:        req.NonTestDeleted,
		SiblingCallerPackages: req.SiblingCallers,
		TestTampered:          req.TestTampered,
	}, flow.RiskOverrides, nil)

	stagesFired := []string{}
	if flow.S0Intent && !comp.HasDesignItem {
		if len(flow.S0IntentConfig.Complexities) == 0 || slices.Contains(flow.S0IntentConfig.Complexities, string(comp.Tier)) {
			stagesFired = append(stagesFired, "S0")
		}
	}
	stagesFired = append(stagesFired, "S1")

	if flow.S2PlanCritic {
		if comp.Tier == qa.ComplexityHigh || slices.Contains(flow.S2PlanCriticConfig.Complexities, string(comp.Tier)) {
			stagesFired = append(stagesFired, "S2")
		}
	}
	stagesFired = append(stagesFired, "S3")

	for _, stg := range flow.CustomStages {
		tierMatch := qa.BlastTierAtLeast(risk.Tier, qa.BlastTier(stg.MinTier))
		pathMatch := len(stg.WhenPaths) == 0 || qa.MatchesAnyGlob(stg.WhenPaths, req.Files)
		if tierMatch && pathMatch {
			stagesFired = append(stagesFired, stg.ID)
		}
	}

	if flow.RequiresS4ForTier(risk.Tier) {
		stagesFired = append(stagesFired, "S4")
	}

	var triage qa.TriageVerdict
	modeStr := flow.S5MergeTriage.Mode
	if modeStr == "" {
		modeStr = "off"
	}
	if modeStr != "off" {
		stagesFired = append(stagesFired, "S5")
		var autoTiers, humTiers []qa.BlastTier
		for _, t := range flow.S5MergeTriage.AutoMergeTiers {
			autoTiers = append(autoTiers, qa.BlastTier(t))
		}
		for _, t := range flow.S5MergeTriage.RequireHumanTiers {
			humTiers = append(humTiers, qa.BlastTier(t))
		}
		triage = qa.EvaluateTriage(qa.TriageInput{
			Mode:                       modeStr,
			AutoMergeTiers:             autoTiers,
			RequireHumanTiers:          humTiers,
			Assessment:                 risk,
			TrustBoundaryRequiresHuman: risk.TrustBoundaryHit || len(trustboundary.Classify(req.Files)) > 0,
			FilesTruncated:             len(req.Files) >= 3000,
			HasUnresolvedFindings:      false,
			OperatorAutoMergeEnabled:   true,
		}, nil)
	}

	res := map[string]any{
		"complexity": map[string]any{
			"tier":          comp.Tier,
			"score":         comp.Score,
			"signals":       comp.Signals,
			"hasDesignItem": comp.HasDesignItem,
		},
		"risk": map[string]any{
			"tier":          risk.Tier,
			"score":         risk.Score,
			"signals":       risk.Reasons,
			"testTampered":  risk.TestTampered,
			"trustBoundary": risk.TrustBoundaryHit,
		},
		"stagesFired": stagesFired,
		"triage": map[string]any{
			"decision":       string(triage.Decision),
			"enforced":       triage.Enforced,
			"mode":           modeStr,
			"tier":           string(triage.Tier),
			"blastScore":     triage.BlastScore,
			"reasons":        triage.Reasons,
			"blockedByFloor": triage.BlockedByFloor,
		},
	}
	writeJSON(w, http.StatusOK, res)
}
