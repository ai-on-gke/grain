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

package model_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A task schedule's previous firing holds it until that task closes, but
// a workflow schedule's review holds it only until workflowReviewHold, and
// the starved comment (and Inbox body) must say which.
func TestStarvedScheduleCommentSaysHowLongTheHoldLasts(t *testing.T) {
	sched := model.Schedule{ID: "live-secrets", Title: "Review live-secrets"}
	base := model.StarvedScheduleFiring{TaskID: "7", HeldFor: time.Hour, Occurrences: 3, From: 5, To: 9}

	task := model.StarvedScheduleComment(sched, base)
	if !strings.Contains(task, "held until this task closes") || strings.Contains(task, "review") {
		t.Errorf("task-firing comment = %q, want it held until the task closes", task)
	}

	review := base
	review.Review = true
	review.ReviewUntil = time.Date(2026, 11, 15, 5, 53, 0, 0, time.UTC)
	got := model.StarvedScheduleComment(sched, review)
	const want = "held until this review closes or until 2026-11-15T05:53:00Z, when the schedule fires over it and leaves it open"
	if !strings.Contains(got, want) || strings.Contains(got, "held until this task closes") ||
		strings.Contains(got, "stops for as long as it waits") {
		t.Errorf("review comment = %q, want it to say %q", got, want)
	}

	// A review whose filing time cannot be measured has no deadline to give.
	review.ReviewUntil = time.Time{}
	got = model.StarvedScheduleComment(sched, review)
	if !strings.Contains(got, "held until this review closes.") || strings.Contains(got, "0001-01-01") {
		t.Errorf("unmeasurable review comment = %q, want it held until the review closes with no date", got)
	}
}
