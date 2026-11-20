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

package sqlite_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// BenchmarkObserveTaskGatesCachedPRRead guards the store-layer read-only
// observation replay cost on a PR task with 10 linked review tasks and cached
// shadow/history JSON views.
func BenchmarkObserveTaskGatesCachedPRRead(b *testing.B) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(b.TempDir()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		b.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	task := model.Task{
		ID:        "2",
		Intent:    model.IntentImplement,
		Title:     "Bench store gate observation",
		Target:    &repo,
		Base:      "main",
		Links:     []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#9000"}, {Kind: model.LinkTargetedReviewTrigger, Target: "api"}},
		CreatedAt: &base,
	}
	for i := 3; i <= 12; i++ {
		rid := strconv.Itoa(i)
		if err := store.PutTask(ctx, model.Task{
			ID:        rid,
			Intent:    model.IntentReview,
			Title:     fmt.Sprintf("Review %d", i),
			Target:    &repo,
			CreatedAt: &base,
		}); err != nil {
			b.Fatal(err)
		}
		if i%3 == 0 {
			task.Links = append(task.Links, model.Link{Kind: model.LinkSecurityReviewTask, Target: rid})
			model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: rid, HeadSHA: "head1"})
		} else {
			task.Links = append(task.Links, model.Link{Kind: model.LinkReviewTask, Target: rid})
			model.AddReviewFindings(&task, model.ReviewFindingsRecord{Gate: "trigger:api", ReviewTask: rid, HeadSHA: "head1"})
		}
	}
	if err := store.PutTask(ctx, task); err != nil {
		b.Fatal(err)
	}
	now := base.Add(time.Minute)
	_, _, fence, err := store.GateChecksInput(ctx, task.ID)
	if err != nil {
		b.Fatal(err)
	}
	known, err := store.RecordedGateChecks(ctx, repo, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		b.Fatal(err)
	}
	checks := []gates.ChecksFacts{{
		TaskID:                task.ID,
		Subject:               model.CheckSubjects(task)[0],
		Head:                  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Mode:                  "github",
		PolicyVersion:         gates.Digest("policy"),
		PolicyComplete:        true,
		ObservedAt:            now,
		ValidUntil:            now.Add(time.Minute),
		RecordedChecksVersion: known.Version,
		Sources: []gates.CheckSource{{
			Kind:     "github",
			Head:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Version:  gates.Digest("checks"),
			Complete: true,
			All:      true,
			Attempts: []gates.CheckAttempt{{
				Identity:   "github-actions/go",
				Names:      []string{"go"},
				Head:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Version:    gates.Digest("go"),
				Order:      1,
				State:      "completed",
				Conclusion: "success",
			}},
		}},
	}}
	// Prime shadow and decoded-view caches.
	if _, _, err := store.ObserveTaskGatesWithChecks(ctx, task.ID, now, fence, checks); err != nil {
		b.Fatal(err)
	}
	if _, _, err := store.ObserveTaskGatesWithChecks(ctx, task.ID, now.Add(time.Second), fence, checks); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v, d, err := store.ObserveTaskGatesWithChecks(ctx, task.ID, now.Add(2*time.Second), fence, checks)
		if err != nil || v == nil || d == nil {
			b.Fatalf("ObserveTaskGatesWithChecks: err=%v v=%v d=%v", err, v, d)
		}
	}
}
