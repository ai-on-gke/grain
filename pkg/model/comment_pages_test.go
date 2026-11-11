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

// CommentsBefore and LatestHandover: reading the end of a long thread,
// and its standing summary, without reading the whole of it
// (design/design/durable-tasks.design.md#DT-15).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCommentsBeforePagesBackwardsFromTheNewest(t *testing.T) {
	t.Parallel()
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := st.PutTask(ctx, model.Task{ID: "long", Intent: model.IntentImplement, Title: "long"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTask(ctx, model.Task{ID: "other", Intent: model.IntentImplement, Title: "other"}); err != nil {
		t.Fatal(err)
	}
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := st.AddComment(ctx, model.Comment{TaskID: "long", Author: human, Body: fmt.Sprint(i), CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if _, err := st.AddComment(ctx, model.Comment{TaskID: "other", Author: human, Body: "elsewhere", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	page, more, err := st.CommentsBefore(ctx, "long", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(page) != 2 || page[0].ID != ids[3] || page[1].ID != ids[4] {
		t.Fatalf("newest page = %+v (more %v), want comments %d and %d oldest first, with more", page, more, ids[3], ids[4])
	}
	page, more, err = st.CommentsBefore(ctx, "long", page[0].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(page) != 2 || page[0].ID != ids[1] || page[1].ID != ids[2] {
		t.Fatalf("second page = %+v (more %v), want comments %d and %d, with more", page, more, ids[1], ids[2])
	}
	page, more, err = st.CommentsBefore(ctx, "long", page[0].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(page) != 1 || page[0].ID != ids[0] || page[0].Body != "0" {
		t.Fatalf("last page = %+v (more %v), want comment %d alone, with no more", page, more, ids[0])
	}
	if _, _, err := st.CommentsBefore(ctx, "long", 0, 0); err == nil {
		t.Error("a page of 0 comments was answered; it should be refused")
	}
}

func TestLatestHandoverIsTheNewestHandoverComment(t *testing.T) {
	t.Parallel()
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := st.PutTask(ctx, model.Task{ID: "hub", Intent: model.IntentImplement, Title: "hub"}); err != nil {
		t.Fatal(err)
	}
	if h, err := st.LatestHandover(ctx, "hub"); err != nil || h != nil {
		t.Fatalf("handover of a task with none = %+v (%v), want nil", h, err)
	}
	grain := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
	for _, c := range []model.Comment{
		{Body: "first summary", Handover: true},
		{Body: "second summary", Handover: true},
		{Body: "an ordinary comment"},
	} {
		c.TaskID, c.Author, c.CreatedAt = "hub", grain, now
		if _, err := st.AddComment(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	h, err := st.LatestHandover(ctx, "hub")
	if err != nil {
		t.Fatal(err)
	}
	if h == nil || h.Body != "second summary" || !h.Handover {
		t.Fatalf("latest handover = %+v, want the second summary", h)
	}
}

func TestRunsBeforePagesBackwardsAndCountsOlderAttempts(t *testing.T) {
	t.Parallel()
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := st.PutTask(ctx, model.Task{ID: "runs", Intent: model.IntentImplement, Title: "runs"}); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 5; n++ {
		runID := fmt.Sprintf("runs-%d", n)
		start := now.Add(time.Duration(n) * time.Minute)
		if err := st.StartRun(ctx, model.Run{ID: runID, TaskID: "runs", Sandbox: "sb", Attempt: n, StartedAt: start}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishRun(ctx, runID, start.Add(10*time.Second), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
	}

	page, older, err := st.RunsBefore(ctx, "runs", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if older != 3 || len(page) != 2 || page[0].Attempt != 4 || page[1].Attempt != 5 {
		t.Fatalf("newest runs = %+v (older %d), want attempts 4 and 5 with 3 older", page, older)
	}
	page, older, err = st.RunsBefore(ctx, "runs", page[0].Attempt, 2)
	if err != nil {
		t.Fatal(err)
	}
	if older != 1 || len(page) != 2 || page[0].Attempt != 2 || page[1].Attempt != 3 {
		t.Fatalf("second page = %+v (older %d), want attempts 2 and 3 with 1 older", page, older)
	}
	page, older, err = st.RunsBefore(ctx, "runs", page[0].Attempt, 2)
	if err != nil {
		t.Fatal(err)
	}
	if older != 0 || len(page) != 1 || page[0].Attempt != 1 {
		t.Fatalf("oldest page = %+v (older %d), want attempt 1 with 0 older", page, older)
	}
}

func TestFailureStreakWalksPastABatchBoundary(t *testing.T) {
	t.Parallel()
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := st.PutTask(ctx, model.Task{ID: "streak", Intent: model.IntentImplement, Title: "streak"}); err != nil {
		t.Fatal(err)
	}
	// Run 1 succeeded, then 40 consecutive failed runs (past streakBatchSize=32).
	for n := 1; n <= 41; n++ {
		runID := fmt.Sprintf("streak-%d", n)
		start := now.Add(time.Duration(n) * time.Minute)
		if err := st.StartRun(ctx, model.Run{ID: runID, TaskID: "streak", Sandbox: "sb", Attempt: n, StartedAt: start}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		outcome := "failed"
		if n == 1 {
			outcome = "succeeded"
		}
		if err := st.FinishRun(ctx, runID, start.Add(5*time.Second), outcome, fmt.Sprintf("detail-%d", n)); err != nil {
			t.Fatal(err)
		}
	}
	streak, err := st.FailureStreak(ctx, "streak")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 40 || streak.LastFailureDetail != "detail-41" {
		t.Fatalf("FailureStreak = %+v, want Count=40 and LastFailureDetail=detail-41", streak)
	}
}
