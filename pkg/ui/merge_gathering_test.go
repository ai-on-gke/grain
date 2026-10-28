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

// The gather history beside the gather window (grain/task-440): the
// Settings pane is where somebody decides how long the merge queue may
// hold a ready change, so it is where the record of what those holds
// did has to be -- and until this it was the one setting in that pane
// with no evidence behind it anywhere.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSettingsReportWhatTheGatherHoldsDid(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
	record := func(outcome string, waited time.Duration) {
		t.Helper()
		if err := store.RecordMergeGather(ctx, model.MergeGatherRecord{
			Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 7,
			Waiting: "c3d4", Window: 5 * time.Minute, Outcome: outcome,
			ArmedAt: at, EndedAt: at.Add(waited),
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(model.MergeGatherBatched, 30*time.Second)
	record(model.MergeGatherBatched, 90*time.Second)
	record(model.MergeGatherReleased, 20*time.Second)
	record(model.MergeGatherExpired, 5*time.Minute)
	record(model.MergeGatherExpired, 5*time.Minute)

	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := settings.MergeGathering
	if got == nil {
		t.Fatal("the pane says nothing about what the queue's holds did")
	}
	if got.Holds != 5 || got.Batched != 2 || got.Released != 1 || got.Expired != 2 {
		t.Fatalf("counted %+v, want five holds: two batched, one released, two expired", got)
	}
	// A minute each to pay off against five minutes each spent for
	// nothing: the comparison the window is shortened on.
	if got.BatchedWaitSeconds != 60 || got.ExpiredWaitSeconds != 300 {
		t.Fatalf("means of %v and %v seconds, want 60 paying off and 300 expiring",
			got.BatchedWaitSeconds, got.ExpiredWaitSeconds)
	}
	if got.ExpiredTotalSeconds != 600 || got.WindowSeconds != 300 {
		t.Fatalf("the expiries cost %v seconds under a window of %v, want 600 and 300",
			got.ExpiredTotalSeconds, got.WindowSeconds)
	}
	if got.FirstAt == nil || got.LastAt == nil {
		t.Fatalf("the counters are not bounded in time: %+v", got)
	}
}

// Nothing at all before the first hold has ended, which is every
// deployment with the wait turned off: zeroes drawn as a rate would
// read as "every wait was wasted".
func TestSettingsSayNothingAboutHoldsThatNeverHappened(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MergeGathering != nil {
		t.Fatalf("a deployment that has never held a change reports %+v", settings.MergeGathering)
	}
}
