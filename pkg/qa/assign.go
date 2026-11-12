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

// Package qa implements Grain's quality control assignment, stage profiling,
// and deterministic/semantic PR assurance checks.
package qa

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// AssignmentStore is the minimal persistence contract required by AssignOrLoadArm.
type AssignmentStore interface {
	QAAssignment(ctx context.Context, rootOrigin string, epoch int) (*model.QAAssignment, error)
	EnsureQAAssignment(ctx context.Context, candidate model.QAAssignment) (model.QAAssignment, error)
}

// ComputeArmBucket hashes (rootOrigin, epoch) into a uniform integer in [0, 99].
func ComputeArmBucket(rootOrigin string, epoch int) int {
	if epoch < 1 {
		epoch = 1
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", strings.TrimSpace(rootOrigin), epoch)))
	return int(binary.BigEndian.Uint32(sum[:4]) % 100)
}

// ClassifyTaskComplexity computes a pre-dispatch complexity tier ("low", "medium", "high")
// strictly from pre-filtrated task specification fields (title and body), never from
// post-treatment diff size or turn count so CUPED stratification stays unbiased.
func ClassifyTaskComplexity(title, body string) string {
	words := len(strings.Fields(title)) + len(strings.Fields(body))
	lower := strings.ToLower(title + "\n" + body)
	archSignals := 0
	for _, kw := range []string{
		"migration", "schema", "concurrency", "scheduler", "syscall",
		"filesystem", "virtual memory", "deadlock", "protocol", "security",
		"architecture", "multi-file", "end-to-end", "workstream",
	} {
		if strings.Contains(lower, kw) {
			archSignals++
		}
	}
	switch {
	case words >= 180 || archSignals >= 2:
		return model.QAComplexityHigh
	case words >= 45 || archSignals >= 1:
		return model.QAComplexityMedium
	default:
		return model.QAComplexityLow
	}
}

// ResolveRootOrigin returns the canonical sticky assignment key for a task:
// its work-tree origin when present, or "task:<id>" for standalone root tasks.
func ResolveRootOrigin(workOrigin, taskID string) string {
	if o := strings.TrimSpace(workOrigin); o != "" {
		return o
	}
	id := strings.TrimSpace(taskID)
	if id == "" {
		return ""
	}
	return "task:" + id
}

// AssignOrLoadArm resolves the QA arm for a task's work-tree root origin.
//
// When cfg.Enabled is false, AssignOrLoadArm returns Arm == "off" immediately without
// writing to the database so disabled deployments incur zero storage or runtime overhead.
// When cfg.Enabled is true, the arm is sticky per (rootOrigin, epoch) so child, review,
// and repair tasks in the same work tree always inherit their root's arm.
func AssignOrLoadArm(
	ctx context.Context,
	store AssignmentStore,
	rootOrigin string,
	complexityTier string,
	cfg model.QAConfig,
	now time.Time,
) (model.QAAssignment, error) {
	rootOrigin = strings.TrimSpace(rootOrigin)
	if !cfg.Enabled {
		return model.QAAssignment{
			RootOrigin:     rootOrigin,
			Epoch:          cfg.EpochOrDefault(),
			RegimeID:       cfg.RegimeIDOrDefault(),
			Arm:            model.QAArmOff,
			FlowID:         cfg.FlowIDOrDefault(),
			ComplexityTier: complexityTier,
		}, nil
	}
	if rootOrigin == "" {
		return model.QAAssignment{}, fmt.Errorf("qa: rootOrigin is required when QA is enabled")
	}

	epoch := cfg.EpochOrDefault()
	existing, err := store.QAAssignment(ctx, rootOrigin, epoch)
	if err != nil {
		return model.QAAssignment{}, err
	}
	if existing != nil {
		return *existing, nil
	}

	rollout := cfg.RolloutPct
	arm := model.QAArmControl
	switch {
	case rollout <= 0:
		arm = model.QAArmControl
	case rollout >= 100:
		arm = model.QAArmTreatment
	default:
		if ComputeArmBucket(rootOrigin, epoch) < rollout {
			arm = model.QAArmTreatment
		}
	}
	if strings.TrimSpace(complexityTier) == "" {
		complexityTier = model.QAComplexityMedium
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	return store.EnsureQAAssignment(ctx, model.QAAssignment{
		RootOrigin:         rootOrigin,
		Epoch:              epoch,
		RegimeID:           cfg.RegimeIDOrDefault(),
		Arm:                arm,
		FlowID:             cfg.FlowIDOrDefault(),
		RolloutPctAtAssign: rollout,
		ComplexityTier:     complexityTier,
		AssignedAt:         now.UTC(),
	})
}
