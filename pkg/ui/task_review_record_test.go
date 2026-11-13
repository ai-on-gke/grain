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

// A review's record on the wire: the head it read and the verdict it
// reached, which a report over many reviews reads off /api/tasks rather
// than out of each review's closing note.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestTaskAPICarriesAReviewsHeadAndVerdict(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	review := create(t, c, ctx)
	tagged := create(t, c, ctx)
	plain := create(t, c, ctx)

	const head = "0123456789abcdef0123456789abcdef01234567"
	const left = "89abcdef0123456789abcdef0123456789abcdef"
	at := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	if err := store.UpdateTask(ctx, review.ID, func(task *model.Task) error {
		model.SetReviewedHead(task, head)
		task.Links = append(task.Links, model.Link{Kind: model.LinkReviewerHead, Target: left})
		model.SetReviewVerdict(task, model.ReviewVerdictRecord{
			Verdict: model.VerdictFindings, FindingCount: 2,
			Categories: []string{model.FindingTests, model.FindingSecurity},
			Head:       head, RunID: "run-1", At: at,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A gating trigger's review filed before the link was written still
	// has its head, from the tag it was filed with.
	if err := store.UpdateTask(ctx, tagged.ID, func(task *model.Task) error {
		task.Tags = append(task.Tags, model.PathTriggerHeadTagPrefix+left)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := c.GetTask(ctx, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ReviewedHead != head || detail.ReviewerHead != left {
		t.Fatalf("reviewedHead = %q, reviewerHead = %q; want %s and %s", detail.ReviewedHead, detail.ReviewerHead, head, left)
	}
	v := detail.ReviewVerdict
	if v == nil || v.Verdict != model.VerdictFindings || v.FindingCount != 2 ||
		!slices.Equal(v.Categories, []string{"tests", "security"}) || v.Head != head || !v.At.Equal(at) {
		t.Fatalf("reviewVerdict = %+v, want findings, 2, [tests security] at %s", v, head)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"reviewedHead":"` + head, `"reviewVerdict":{"verdict":"findings","findingCount":2,"categories":["tests","security"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("GET /api/tasks/%s = %s, want it to contain %s", review.ID, raw, want)
		}
	}

	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, task := range list {
		switch task.ID {
		case review.ID:
			seen++
			if task.ReviewedHead != head {
				t.Errorf("listed review's reviewedHead = %q, want %s", task.ReviewedHead, head)
			}
		case tagged.ID:
			seen++
			if task.ReviewedHead != left {
				t.Errorf("listed tagged review's reviewedHead = %q, want %s from its path-trigger-head tag", task.ReviewedHead, left)
			}
		case plain.ID:
			seen++
			if task.ReviewedHead != "" || task.ReviewerHead != "" || task.ReviewVerdict != nil {
				t.Errorf("listed plain task = %+v, want no review record on it", task)
			}
		}
	}
	if seen != 3 {
		t.Fatalf("listed %d of the 3 tasks", seen)
	}
}
