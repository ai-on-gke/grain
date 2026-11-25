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
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openFeedbackStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return store
}

func TestValidateGrainFeedbackSlugAndURL(t *testing.T) {
	t.Parallel()
	for _, valid := range []string{"missing-pr-draft-flag", "cli-exit-2", "a"} {
		if err := model.ValidateGrainFeedbackSlug(valid); err != nil {
			t.Errorf("ValidateGrainFeedbackSlug(%q): unexpected error %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "-leading", "trailing-", "double--dash", "UpperCase", "has_underscore", "has space"} {
		if err := model.ValidateGrainFeedbackSlug(invalid); err == nil {
			t.Errorf("ValidateGrainFeedbackSlug(%q): expected error, got nil", invalid)
		}
	}
	if got, err := model.ValidateGrainFeedbackURL(" https://github.com/ai-on-gke/grain/issues/42 "); err != nil || got != "https://github.com/ai-on-gke/grain/issues/42" {
		t.Errorf("ValidateGrainFeedbackURL valid = %q, %v", got, err)
	}
	for _, badURL := range []string{"javascript:alert(1)", "ftp://example.com/x", "not-a-url"} {
		if _, err := model.ValidateGrainFeedbackURL(badURL); err == nil {
			t.Errorf("ValidateGrainFeedbackURL(%q): expected error, got nil", badURL)
		}
	}
}

func TestGrainFeedbackRecordUpsertPreservesHumanTriage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openFeedbackStore(t)

	t0 := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	agent1 := model.Principal{Kind: model.PrincipalAgent, ID: "101", Source: model.SourceSandbox}
	agent2 := model.Principal{Kind: model.PrincipalAgent, ID: "202", Source: model.SourceSandbox}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "vluk", Source: model.SourceAccessCode}

	first, created, err := store.RecordGrainFeedback(ctx, model.GrainFeedbackRecord{
		Slug:    "pr-open-draft-flag",
		Title:   "grain pr open lacks --draft flag",
		Surface: model.GrainFeedbackSurfaceCLI,
		Body:    "Tasks lose turns trying `--draft` on `grain pr open`.",
		Repos:   []string{"acme/widgets"},
		TaskIDs: []string{"#101", "99"},
		Now:     t0,
		By:      agent1,
	})
	if err != nil {
		t.Fatalf("first RecordGrainFeedback: %v", err)
	}
	if !created || first.OccurrenceCount != 1 || first.Status != model.GrainFeedbackProposed {
		t.Fatalf("first = %+v, created=%v; want created=true count=1 status=proposed", first, created)
	}
	if !slices.Equal(first.TaskIDs, []string{"101", "99"}) {
		t.Errorf("TaskIDs = %v, want [101 99]", first.TaskIDs)
	}

	// Human marks it filed with an upstream URL and refines the title.
	filedStatus := model.GrainFeedbackFiled
	upstream := "https://github.com/ai-on-gke/grain/issues/2000"
	curatedTitle := "Support --draft on grain pr open"
	triaged, err := store.UpdateGrainFeedback(ctx, "pr-open-draft-flag", model.GrainFeedbackPatch{
		Title:       &curatedTitle,
		Status:      &filedStatus,
		UpstreamURL: &upstream,
		Now:         t0.Add(time.Hour),
		By:          human,
	})
	if err != nil {
		t.Fatalf("UpdateGrainFeedback: %v", err)
	}
	if triaged.Status != model.GrainFeedbackFiled || triaged.UpstreamURL != upstream || triaged.Title != curatedTitle {
		t.Fatalf("triaged = %+v", triaged)
	}

	// Second reflection in another repository hits the same slug: counts and
	// evidence grow, while human-set status, URL, and curated title/body stay intact.
	second, created, err := store.RecordGrainFeedback(ctx, model.GrainFeedbackRecord{
		Slug:    "pr-open-draft-flag",
		Title:   "Agent overwrite attempt",
		Surface: model.GrainFeedbackSurfaceTool,
		Body:    "Agent body overwrite attempt",
		Repos:   []string{"acme/gadgets", "acme/widgets"},
		TaskIDs: []string{"202", "101"},
		Now:     t0.Add(2 * time.Hour),
		By:      agent2,
	})
	if err != nil {
		t.Fatalf("second RecordGrainFeedback: %v", err)
	}
	if created {
		t.Error("second RecordGrainFeedback reported created=true on existing slug")
	}
	if second.OccurrenceCount != 2 {
		t.Errorf("OccurrenceCount = %d, want 2", second.OccurrenceCount)
	}
	if second.Status != model.GrainFeedbackFiled || second.UpstreamURL != upstream {
		t.Errorf("agent sighting overwrote human triage: status=%q url=%q", second.Status, second.UpstreamURL)
	}
	if second.Title != curatedTitle || second.Body != first.Body {
		t.Errorf("agent sighting overwrote curated prose on triaged item: title=%q body=%q", second.Title, second.Body)
	}
	if !slices.Equal(second.Repos, []string{"acme/gadgets", "acme/widgets"}) {
		t.Errorf("Repos = %v, want [acme/gadgets acme/widgets]", second.Repos)
	}
	if !slices.Equal(second.TaskIDs, []string{"101", "202", "99"}) {
		t.Errorf("TaskIDs = %v, want [101 202 99]", second.TaskIDs)
	}

	// Add an open entry so ListGrainFeedback exercises open-first ordering.
	_, _, err = store.RecordGrainFeedback(ctx, model.GrainFeedbackRecord{
		Slug:    "ci-dispatch-missing",
		Title:   "No workflow_dispatch support in grain ci",
		Surface: model.GrainFeedbackSurfaceCLI,
		Body:    "Runs cannot trigger workflow_dispatch suites.",
		Repos:   []string{"acme/widgets"},
		TaskIDs: []string{"303"},
		Now:     t0.Add(30 * time.Minute),
		By:      agent1,
	})
	if err != nil {
		t.Fatalf("third RecordGrainFeedback: %v", err)
	}

	list, err := store.ListGrainFeedback(ctx)
	if err != nil {
		t.Fatalf("ListGrainFeedback: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	// Open (`proposed`) sorts ahead of closed (`filed`) even with lower count.
	if list[0].Slug != "ci-dispatch-missing" || list[1].Slug != "pr-open-draft-flag" {
		t.Errorf("list order = [%s, %s], want open first", list[0].Slug, list[1].Slug)
	}
	if list[0].Body != "" || list[0].Length == 0 {
		t.Errorf("ListGrainFeedback should omit body while reporting Length: body=%q len=%d", list[0].Body, list[0].Length)
	}

	if err := store.DeleteGrainFeedback(ctx, "ci-dispatch-missing"); err != nil {
		t.Fatalf("DeleteGrainFeedback: %v", err)
	}
	if err := store.DeleteGrainFeedback(ctx, "ci-dispatch-missing"); !errors.Is(err, model.ErrGrainFeedbackNotFound) {
		t.Errorf("second DeleteGrainFeedback = %v, want ErrGrainFeedbackNotFound", err)
	}
}
