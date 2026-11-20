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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Every test in this package raises its standing conditions with
// strictLevelTriggered on, so a reconciler raising a family through
// ensurePipelineNotification that model.LevelTriggeredFamily does not list
// fails its own test, and a bulk clear's expectedBack cannot fall behind.
func init() { strictLevelTriggered = true }

func TestEnsurePipelineNotificationRefusesAnUnlistedFamily(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	err := ensurePipelineNotification(ctx, store, model.Notification{
		Subject: "pipeline.not-a-listed-family:x", Title: "t", Severity: model.NotificationWarning}, now)
	if err == nil || !strings.Contains(err.Error(), "levelTriggeredFamilies") {
		t.Fatalf("an unlisted family = %v, want a refusal naming levelTriggeredFamilies", err)
	}
	for _, f := range []string{FamilyTaskStalled, FamilyPortStale, model.JudgementModelUnresolvedSubject,
		model.SubjectStewardRegression, model.FamilyRepairFailing, model.FamilyFlakeBudget, model.FamilyReleaseEscalated} {
		if !model.LevelTriggeredFamily(model.NotificationFamily(f)) {
			t.Errorf("%s is raised through ensurePipelineNotification but not listed as level-triggered", f)
		}
	}
}
