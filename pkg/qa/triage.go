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

type TriageMode string

const (
	TriageOff      TriageMode = "off"
	TriageShadow   TriageMode = "shadow"
	TriageEnforced TriageMode = "enforced"
)

type TriageDecision string

const (
	TriageAutoMerge    TriageDecision = "auto_merge"
	TriageRequireHuman TriageDecision = "require_human"
)

type TriageInput struct {
	Mode                       string
	AutoMergeTiers             []BlastTier
	RequireHumanTiers          []BlastTier
	Assessment                 RiskAssessment
	TrustBoundaryRequiresHuman bool
	MergeRulesRequireHuman     bool
	GateTriggerMatched         bool
	FilesTruncated             bool
	CIRedOrPending             bool
	HasUnresolvedFindings      bool
	OperatorAutoMergeEnabled   bool
}

type TriageVerdict struct {
	Decision          TriageDecision
	Mode              string
	Enforced          bool
	WithdrawAutoMerge bool
	SetTriageHold     bool
	Tier              BlastTier
	BlastScore        int
	Reasons           []string
	BlockedByFloor    bool
}

func EvaluateTriage(in TriageInput, prof perfProfiler) TriageVerdict {
	var out TriageVerdict
	fn := func() (string, error) {
		out = evaluateTriageInternal(in)
		return string(out.Decision), nil
	}
	if prof != nil {
		prof.Measure(PerfSample{Stage: "evaluate-triage", CheckID: "triage"}, fn)
	} else {
		fn()
	}
	return out
}

func evaluateTriageInternal(in TriageInput) TriageVerdict {
	if len(in.AutoMergeTiers) == 0 {
		in.AutoMergeTiers = []BlastTier{BlastT0, BlastT1}
	}
	if len(in.RequireHumanTiers) == 0 {
		in.RequireHumanTiers = []BlastTier{BlastT2, BlastT3}
	}

	tier := in.Assessment.Tier

	blockedByFloor := false
	if in.Assessment.TrustBoundaryHit || in.TrustBoundaryRequiresHuman || in.MergeRulesRequireHuman ||
		in.GateTriggerMatched || in.FilesTruncated || in.CIRedOrPending || in.HasUnresolvedFindings ||
		!in.OperatorAutoMergeEnabled {
		blockedByFloor = true
	}

	decision := TriageAutoMerge
	if blockedByFloor {
		decision = TriageRequireHuman
	} else {
		requireHuman := false
		for _, t := range in.RequireHumanTiers {
			if tier == t {
				requireHuman = true
				break
			}
		}

		isAutoMerge := false
		for _, t := range in.AutoMergeTiers {
			if tier == t {
				isAutoMerge = true
				break
			}
		}

		if requireHuman || !isAutoMerge {
			decision = TriageRequireHuman
		}
	}

	enforced := false
	withdrawAutoMerge := false
	setTriageHold := false

	if in.Mode == string(TriageEnforced) && decision == TriageRequireHuman {
		tierRequiresHuman := false
		for _, t := range in.RequireHumanTiers {
			if t == tier {
				tierRequiresHuman = true
				break
			}
		}
		isAuto := false
		for _, t := range in.AutoMergeTiers {
			if t == tier {
				isAuto = true
				break
			}
		}
		if !isAuto {
			tierRequiresHuman = true
		}

		// Fail closed: whenever AutoMerge is enabled and Decision == require_human
		// (whether from tier or floor checks such as trustboundary, merge-rules,
		// truncated file list, or red CI), enforced triage withdraws AutoMerge.
		if in.OperatorAutoMergeEnabled {
			withdrawAutoMerge = true
		}
		if !blockedByFloor || (in.OperatorAutoMergeEnabled && (tierRequiresHuman || in.FilesTruncated)) {
			enforced = true
			withdrawAutoMerge = true
			setTriageHold = true
		}
	}

	return TriageVerdict{
		Decision:          decision,
		Mode:              in.Mode,
		Enforced:          enforced,
		WithdrawAutoMerge: withdrawAutoMerge,
		SetTriageHold:     setTriageHold,
		Tier:              tier,
		BlastScore:        in.Assessment.Score,
		Reasons:           in.Assessment.Reasons,
		BlockedByFloor:    blockedByFloor,
	}
}
