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

package resolve

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa"
	"github.com/ai-on-gke/grain/pkg/qa/expstats"
)

// AllocTagPrefix prefixes explicit allocation key tags on root benchmark tasks.
const AllocTagPrefix = "qa-alloc:"

// RuntimeHooks exposes controller callbacks to HTTP/CLI experiment transitions and readout runners
// without introducing import cycles between pkg/ui and pkg/orchestrator.
type RuntimeHooks struct {
	DataDir                 string
	PromoteExperimentStages func(ctx context.Context, experimentID, reason string)
	RunJudgeModel           func(ctx context.Context, modelID, prompt string) (output string, tokensIn, tokensOut int64, err error)
	ReadRepoFileAtCommit    func(ctx context.Context, repo, commit, filePath string) ([]byte, error)
}

var liveRuntimeHooks atomic.Pointer[RuntimeHooks]

// SetRuntimeHooks registers live daemon callbacks for experiment fail-open promotion and judge execution.
func SetRuntimeHooks(h *RuntimeHooks) {
	liveRuntimeHooks.Store(h)
}

// GetRuntimeHooks returns the registered runtime hooks, or nil in standalone unit tests.
func GetRuntimeHooks() *RuntimeHooks {
	return liveRuntimeHooks.Load()
}

func init() {
	RegistryResolver = resolveEnrolledExperiment
	RegistryDispatchResolver = dispatchOrEnrollExperiment
}

// NormalizedBasisWeights converts arm weights (e.g. 50/50 or 34/33/33) into 10,000-basis-point
// weights over non-halted arms so expstats.AllocateHashArm and expstats.AuditAssignments agree.
func NormalizedBasisWeights(arms []model.QAExperimentArm) []expstats.ArmWeight {
	var active []model.QAExperimentArm
	total := 0
	for _, a := range arms {
		if !a.Halted && a.Weight > 0 {
			active = append(active, a)
			total += a.Weight
		}
	}
	if len(active) == 0 || total <= 0 {
		return nil
	}
	out := make([]expstats.ArmWeight, len(active))
	if total == 10000 {
		for i, a := range active {
			out[i] = expstats.ArmWeight{ID: a.ID, Weight: a.Weight}
		}
		return out
	}
	scaledSum := 0
	for i, a := range active {
		w := int(int64(a.Weight) * 10000 / int64(total))
		out[i] = expstats.ArmWeight{ID: a.ID, Weight: w}
		scaledSum += w
	}
	out[0].Weight += 10000 - scaledSum
	return out
}

func repoInExperimentScope(repos []string, target *model.RepoRef) bool {
	if len(repos) == 0 {
		return true
	}
	if target == nil {
		return false
	}
	full := strings.TrimSpace(target.String())
	short := strings.TrimSpace(target.Owner + "/" + target.Name)
	for _, r := range repos {
		rt := strings.TrimSpace(r)
		if strings.EqualFold(rt, full) || strings.EqualFold(rt, short) {
			return true
		}
	}
	return false
}

func extractAllocationKey(tags []string) string {
	for _, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		if strings.HasPrefix(strings.ToLower(trimmed), AllocTagPrefix) {
			return strings.TrimSpace(trimmed[len(AllocTagPrefix):])
		}
	}
	return ""
}

func primaryTreatmentFlow(arms []model.QAExperimentArm) string {
	for _, a := range arms {
		if a.ID != model.QAArmControl && !strings.EqualFold(a.FlowHash, "control") && strings.TrimSpace(a.FlowHash) != "" {
			return strings.TrimSpace(a.FlowHash)
		}
	}
	return ""
}

func resolveEnrolledExperiment(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	rootOrigin string,
	_ model.QAConfig,
) (*Resolution, error) {
	enr, err := store.GetQAEnrollment(ctx, rootOrigin)
	if err != nil || enr == nil {
		return nil, err
	}
	exp, err := store.GetQAExperiment(ctx, enr.ExperimentID)
	if err != nil {
		return &Resolution{
			Source:       "registry",
			ExperimentID: enr.ExperimentID,
			ArmID:        "baseline",
		}, nil
	}
	if enr.ArmID == "excluded" {
		return &Resolution{
			Source:       "registry",
			ExperimentID: exp.ID,
			ArmID:        "excluded",
			ExcludedWhy:  enr.ExcludedReason,
		}, nil
	}
	if exp.State != model.QAExpStateRunning || !repoInExperimentScope(exp.Scope.Repos, task.Target) {
		return &Resolution{
			Source:       "registry",
			ExperimentID: exp.ID,
			ArmID:        "baseline",
		}, nil
	}

	var matchedArm *model.QAExperimentArm
	for i := range exp.Arms {
		if exp.Arms[i].ID == enr.ArmID {
			matchedArm = &exp.Arms[i]
			break
		}
	}
	if matchedArm == nil || matchedArm.Halted {
		return &Resolution{
			Source:       "registry",
			ExperimentID: exp.ID,
			ArmID:        "baseline",
		}, nil
	}

	flowRef := strings.TrimSpace(enr.FlowHash)
	if flowRef == "" {
		flowRef = strings.TrimSpace(matchedArm.FlowHash)
	}
	isControl := matchedArm.ID == model.QAArmControl || flowRef == "" || strings.EqualFold(flowRef, "control")
	if isControl {
		var spec *qa.FlowSpec
		var shadowFlowID, shadowHash string
		shadow := false
		if exp.Options.ShadowOnControl {
			shadowFlowID = primaryTreatmentFlow(exp.Arms)
			if shadowFlowID != "" {
				spec, shadowHash, _ = LookupFlowSpec(ctx, store, shadowFlowID)
				shadow = spec != nil
			}
		}
		return &Resolution{
			Source:       "registry",
			ExperimentID: exp.ID,
			ArmID:        model.QAArmControl,
			FlowID:       shadowFlowID,
			FlowHash:     shadowHash,
			QAEngineRev:  qa.QAEngineRev,
			Spec:         spec,
			Shadow:       shadow,
		}, nil
	}

	spec, hash, _ := LookupFlowSpec(ctx, store, flowRef)
	if hash == "" {
		hash = flowRef
	}
	return &Resolution{
		Source:       "registry",
		ExperimentID: exp.ID,
		ArmID:        matchedArm.ID,
		FlowID:       flowRef,
		FlowHash:     hash,
		QAEngineRev:  qa.QAEngineRev,
		Spec:         spec,
		Shadow:       false,
	}, nil
}

func dispatchOrEnrollExperiment(
	ctx context.Context,
	store *model.Store,
	task model.Task,
	rootOrigin string,
	hasPriorRuns bool,
	qaCfg model.QAConfig,
	now time.Time,
) (*Resolution, error) {
	// 1. Existing registry enrollment always wins for the origin's lifetime.
	existing, err := store.GetQAEnrollment(ctx, rootOrigin)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return resolveEnrolledExperiment(ctx, store, task, rootOrigin, qaCfg)
	}

	// 2. Origin previously enrolled in legacy rollout stays in legacy rollout.
	if legacy, lErr := store.EarliestQAAssignment(ctx, rootOrigin); lErr == nil && legacy != nil {
		return nil, nil
	}

	// 3. Check whether a registry experiment is currently running.
	running, err := store.GetRunningQAExperiment(ctx)
	if err != nil || running == nil {
		return nil, err
	}
	if !repoInExperimentScope(running.Scope.Repos, task.Target) {
		return &Resolution{Source: "none", ArmID: "baseline"}, nil
	}

	enrollExcluded := func(reason, key string) (*Resolution, error) {
		enr, _, iErr := store.InsertQAEnrollment(ctx, model.QAEnrollment{
			RootOrigin:     rootOrigin,
			ExperimentID:   running.ID,
			Segment:        running.Segment,
			ArmID:          "excluded",
			QAEngineRev:    qa.QAEngineRev,
			Model:          task.Model,
			AllocationKey:  key,
			ExcludedReason: reason,
			AssignedAt:     now,
		})
		if iErr != nil {
			return nil, iErr
		}
		if enr.ArmID != "excluded" {
			return resolveEnrolledExperiment(ctx, store, task, rootOrigin, qaCfg)
		}
		return &Resolution{
			Source:       "registry",
			ExperimentID: running.ID,
			ArmID:        "excluded",
			ExcludedWhy:  enr.ExcludedReason,
		}, nil
	}

	if hasPriorRuns {
		return enrollExcluded("origin had runs before experiment started", "")
	}
	if strings.TrimSpace(running.EnrollmentHeld) != "" {
		return enrollExcluded("enrollment held: "+running.EnrollmentHeld, "")
	}

	var chosenArm *model.QAExperimentArm
	var allocKey string
	var bucketPtr *int

	if strings.EqualFold(running.Allocation.Mode, "explicit") {
		allocKey = extractAllocationKey(task.Tags)
		if allocKey == "" {
			return enrollExcluded("missing qa-alloc:<key> tag", "")
		}
		var matchedEntry *model.QAExplicitAllocationEntry
		for i := range running.AllocationList {
			if running.AllocationList[i].Key == allocKey {
				matchedEntry = &running.AllocationList[i]
				break
			}
		}
		if matchedEntry == nil {
			return enrollExcluded("unknown allocation key", allocKey)
		}
		used, uErr := store.IsAllocationKeyUsed(ctx, running.ID, allocKey)
		if uErr != nil {
			return nil, uErr
		}
		if used {
			return enrollExcluded("allocation key already used", allocKey)
		}
		for i := range running.Arms {
			if running.Arms[i].ID == matchedEntry.ArmID {
				chosenArm = &running.Arms[i]
				break
			}
		}
		if chosenArm == nil || chosenArm.Halted {
			return enrollExcluded("assigned arm is halted or missing", allocKey)
		}
	} else {
		weights := NormalizedBasisWeights(running.Arms)
		if len(weights) == 0 {
			return enrollExcluded("no active experiment arms", "")
		}
		armID, b := expstats.AllocateHashArm(running.Salt, rootOrigin, weights)
		bucketPtr = &b
		for i := range running.Arms {
			if running.Arms[i].ID == armID {
				chosenArm = &running.Arms[i]
				break
			}
		}
		if chosenArm == nil {
			return enrollExcluded("allocated arm missing", "")
		}
	}

	if expectedBase := strings.TrimSpace(chosenArm.BaseBranch); expectedBase != "" {
		actualBase := strings.TrimSpace(task.Base)
		if actualBase != expectedBase {
			return enrollExcluded(
				fmt.Sprintf("task base %q does not match arm baseBranch %q", actualBase, expectedBase),
				allocKey,
			)
		}
	}

	_, _, err = store.InsertQAEnrollment(ctx, model.QAEnrollment{
		RootOrigin:    rootOrigin,
		ExperimentID:  running.ID,
		Segment:       running.Segment,
		ArmID:         chosenArm.ID,
		FlowHash:      chosenArm.FlowHash,
		QAEngineRev:   qa.QAEngineRev,
		Model:         task.Model,
		AllocationKey: allocKey,
		Bucket:        bucketPtr,
		AssignedAt:    now,
	})
	if err != nil {
		return nil, err
	}
	return resolveEnrolledExperiment(ctx, store, task, rootOrigin, qaCfg)
}
