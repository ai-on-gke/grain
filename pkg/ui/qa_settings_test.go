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

package ui_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestQASettingsShipOffPlanCleanlyAndBumpRegimeOnRolloutChange(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	initial, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings initial: %v", err)
	}
	if initial.QAEnabled || initial.QARolloutPct != 0 || initial.QAFlowID != model.DefaultQAFlowID || !initial.QAShadowOnControl {
		t.Fatalf("unexpected initial QA settings: enabled=%v rollout=%d flow=%q shadow=%v",
			initial.QAEnabled, initial.QARolloutPct, initial.QAFlowID, initial.QAShadowOnControl)
	}

	enable := true
	rollout30 := 30
	flow := model.QAFlowGoldenV1Minimal
	shadow := true
	req30 := ui.UpdateSettingsRequest{
		QAEnabled:         &enable,
		QAFlowID:          &flow,
		QARolloutPct:      &rollout30,
		QAShadowOnControl: &shadow,
	}

	planned30, err := c.PlanSettings(ctx, req30)
	if err != nil {
		t.Fatalf("PlanSettings 30%%: %v", err)
	}
	if !planned30.QAEnabled || planned30.QARolloutPct != 30 || planned30.QARegimeID != 1 {
		t.Fatalf("unexpected planned 30%%: enabled=%v rollout=%d regime=%d",
			planned30.QAEnabled, planned30.QARolloutPct, planned30.QARegimeID)
	}

	// PlanSettings must not write to store.
	stillOff, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stillOff.QAEnabled {
		t.Fatalf("PlanSettings mutated stored QAEnabled=true")
	}

	saved30, err := c.UpdateSettings(ctx, req30)
	if err != nil {
		t.Fatalf("UpdateSettings 30%%: %v", err)
	}
	if !reflect.DeepEqual(planned30, saved30) {
		t.Fatalf("planned and saved QA settings differ:\n planned=%+v\n   saved=%+v", planned30, saved30)
	}

	// Raising rollout to 50% bumps qaRegimeId to 2 while preserving qaEpoch=1.
	rollout50 := 50
	saved50, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{QARolloutPct: &rollout50})
	if err != nil {
		t.Fatalf("UpdateSettings 50%%: %v", err)
	}
	if saved50.QARolloutPct != 50 || saved50.QARegimeID != 2 || saved50.QAEpoch != 1 {
		t.Fatalf("expected 50%% in regime 2 / epoch 1, got rollout=%d regime=%d epoch=%d",
			saved50.QARolloutPct, saved50.QARegimeID, saved50.QAEpoch)
	}

	// Validation rejects out-of-bounds rollout percentage and unknown flow IDs.
	badPct := 125
	if _, err := c.PlanSettings(ctx, ui.UpdateSettingsRequest{QARolloutPct: &badPct}); err == nil || !strings.Contains(err.Error(), "rolloutPct") {
		t.Fatalf("expected rolloutPct validation error, got %v", err)
	}
	badFlow := "nonexistent-flow"
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{QAFlowID: &badFlow}); err == nil || !strings.Contains(err.Error(), "flowId") {
		t.Fatalf("expected flowId validation error, got %v", err)
	}
	epoch3 := 3
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{QAEpoch: &epoch3}); err != nil {
		t.Fatalf("UpdateSettings qaEpoch=3: %v", err)
	}
	epoch1 := 1
	if _, err := c.PlanSettings(ctx, ui.UpdateSettingsRequest{QAEpoch: &epoch1}); err == nil || !strings.Contains(err.Error(), "epoch cannot decrease") {
		t.Fatalf("expected epoch downgrade rejection, got %v", err)
	}
}
