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

// The batch history beside the batch size (grain/task-300): the Settings
// pane is where somebody decides how deep to speculate, so it is where
// the record of how that bet has gone has to be.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSettingsReportWhatTheMergeBatchesHaveCost(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.Config{MergeBatchSize: 3}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 19, 4, 0, 0, 0, time.UTC)
	record := func(outcome string, size, merged, queued int) {
		t.Helper()
		if err := store.RecordMergeBatch(ctx, model.MergeBatchRecord{
			Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Branch: "main",
			Size: size, Queued: queued, Outcome: outcome, Merged: merged, PostMerge: true,
			StartedAt: at, FinishedAt: at.Add(20 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(model.MergeBatchMerged, 3, 3, 5)
	record(model.MergeBatchMerged, 2, 2, 2)

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MergeBatchSize != 3 {
		t.Fatalf("the pane reports a batch size of %d, want 3", settings.MergeBatchSize)
	}
	got := settings.MergeBatching
	if got == nil {
		t.Fatal("the pane says nothing about what those batches cost")
	}
	if got.Batches != 2 || got.Changes != 5 || got.FirstTry != 2 || got.Fresh != 2 {
		t.Fatalf("counted %+v, want two batches landing five changes first time", got)
	}
	// Four suites -- two batch runs and the post-merge run each batch's
	// merges trigger -- against the five a one-at-a-time queue would have
	// spent, and only one batch was ever held down by the limit.
	if got.Suites != 4 || got.SerialSuites != 5 || got.Saved != 1 {
		t.Fatalf("spent %d suite(s) against %d serial (saved %d), want 4, 5 and 1",
			got.Suites, got.SerialSuites, got.Saved)
	}
	if got.AtLimit != 1 || got.Biggest != 3 {
		t.Fatalf("%d batch(es) reached the limit, biggest %d, want 1 and 3", got.AtLimit, got.Biggest)
	}
	if got.FirstAt == nil || got.LastAt == nil {
		t.Fatalf("the counters are not bounded in time: %+v", got)
	}
}

// A deployment that has never formed a batch says nothing rather than
// reporting a row of zeroes, which a pane would draw as a batching
// machinery that has failed every time it ran.
func TestSettingsSayNothingAboutBatchesThatNeverHappened(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.Config{MergeBatchSize: 3}); err != nil {
		t.Fatal(err)
	}
	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MergeBatching != nil {
		t.Fatalf("a deployment with no batch history reports %+v", settings.MergeBatching)
	}
}

// Clearing the Admin pane's merge batch size sends 0, which is what a
// stored zero means: grain's own default. It used to be refused as a
// size below 1, so once somebody had chosen a batch size the pane had no
// way back to the default (docs/test-runs/2026-10-11-deployment-operations.md).
func TestSettingsTakeAZeroMergeBatchSizeAsTheDefault(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	seven := 7
	first := firstSettings()
	first.MergeBatchSize = &seven
	chosen, err := c.UpdateSettings(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.MergeBatchSize != 7 || chosen.MergeBatchSizeChosen != 7 {
		t.Fatalf("after choosing 7 the pane reports %d (chosen %d), want 7 (chosen 7)",
			chosen.MergeBatchSize, chosen.MergeBatchSizeChosen)
	}
	zero := 0
	settings, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{MergeBatchSize: &zero})
	if err != nil {
		t.Fatalf("clearing the merge batch size: %v", err)
	}
	if settings.MergeBatchSize != model.DefaultMergeBatchSize {
		t.Fatalf("the pane reports a batch size of %d after clearing it, want the default %d",
			settings.MergeBatchSize, model.DefaultMergeBatchSize)
	}
	// The box reads what was chosen, and nothing was: blank, not the
	// default as though somebody had typed it (2026-10-12 walkthrough).
	if settings.MergeBatchSizeChosen != 0 {
		t.Fatalf("the pane's box would show a chosen batch size of %d after clearing it, want 0 (blank)",
			settings.MergeBatchSizeChosen)
	}
	stored, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MergeBatchSize != 0 {
		t.Fatalf("stored a batch size of %d, want 0 (grain's default)", stored.MergeBatchSize)
	}
	negative := -1
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{MergeBatchSize: &negative}); err == nil {
		t.Fatal("a negative merge batch size was accepted")
	}
}
