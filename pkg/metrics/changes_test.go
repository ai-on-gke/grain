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

package metrics_test

// Changes: when this deployment's own parameters moved, as a series to read
// the rest of the report against (grain/task-495).

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
)

// change is one settings change d into the window.
func change(d time.Duration, commit, subject string, fields ...metrics.SettingsField) metrics.SettingsChange {
	return metrics.SettingsChange{
		At:      since.Add(d),
		Commit:  commit,
		Subject: subject,
		Paths:   []string{"tables/grain_config.json"},
		Fields:  fields,
	}
}

func dial(name, from, to string) metrics.SettingsField {
	return metrics.SettingsField{Path: "tables/grain_config.json", Name: name, From: from, To: to}
}

// The evening this section exists for: a parameter dropped halfway across
// the window, and a series that says which half it was dropped in.
func TestChangesLineUpWithTheWindowsOwnSlices(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:          win,
		Buckets:         2,
		SettingsHistory: true,
		SettingsChanges: []metrics.SettingsChange{
			change(6*24*time.Hour, "beef", "max_ci_jobs 10 -> 7", dial("max_ci_jobs", "10", "7")),
			change(30*time.Minute, "cafe", "two dials at once",
				dial("max_workers", "5", "7"), dial("sandbox_cpus", "4", "2")),
			// Before the window: history, not an event in it.
			{At: since.Add(-time.Hour), Commit: "f00d", Subject: "last week"},
		},
	})
	c := rep.Changes
	if !c.Available {
		t.Fatal("Available is false with a history supplied")
	}
	if c.N != 2 || c.Fields != 3 {
		t.Errorf("N = %d, Fields = %d; want the window's two changes and the three dials in them", c.N, c.Fields)
	}
	if len(c.Buckets) != 2 || c.Buckets[0].Changes != 1 || c.Buckets[1].Changes != 1 {
		t.Errorf("Buckets = %+v, want one change in each half", c.Buckets)
	}
	if c.Buckets[0].Fields != 2 || c.Buckets[1].Fields != 1 {
		t.Errorf("Buckets = %+v, want the two-dial commit's fields in the first half", c.Buckets)
	}
	// Newest first, so a reader of a bounded list gets the changes nearest
	// the numbers it is looking at.
	if len(c.Events) != 2 || c.Events[0].Commit != "beef" {
		t.Errorf("Events = %+v, want the newest change first", c.Events)
	}
	// And how far back the reader actually got, which is what says whether
	// an empty window means "nothing changed" or "nothing is recorded".
	if c.Reaches == nil || !c.Reaches.Equal(since.Add(-time.Hour)) {
		t.Errorf("Reaches = %v, want the oldest change read, window or no window", c.Reaches)
	}
}

// A deployment with no state repository to read has no history, which is a
// different answer from a deployment nobody has changed anything on.
func TestChangesSaysWhenThereIsNoHistoryToRead(t *testing.T) {
	rep := metrics.Compute(metrics.Input{Window: win, Buckets: 2})
	if rep.Changes.Available {
		t.Error("Available is true with no settings history supplied")
	}
	if rep.Changes.N != 0 || rep.Changes.Reaches != nil {
		t.Errorf("Changes = %+v, want nothing measured", rep.Changes)
	}

	unchanged := metrics.Compute(metrics.Input{Window: win, Buckets: 2, SettingsHistory: true})
	if !unchanged.Changes.Available || unchanged.Changes.N != 0 {
		t.Errorf("Changes = %+v, want a history that is available and empty", unchanged.Changes)
	}
}

// The detail is bounded and the count is not: a window full of settings
// churn still reports how much churn there was.
func TestChangesBoundTheDetailAndNotTheCount(t *testing.T) {
	var events []metrics.SettingsChange
	for i := 0; i < metrics.MaxChangeEvents+5; i++ {
		events = append(events, change(time.Duration(i)*time.Hour, "c"+string(rune('a'+i%26)), "a change",
			dial("max_ci_jobs", "10", "7")))
	}
	rep := metrics.Compute(metrics.Input{Window: win, SettingsHistory: true, SettingsChanges: events})
	c := rep.Changes
	if c.N != len(events) {
		t.Errorf("N = %d, want every change in the window: %d", c.N, len(events))
	}
	if len(c.Events) != metrics.MaxChangeEvents || c.Dropped != 5 {
		t.Errorf("carried %d events with %d dropped, want %d and 5", len(c.Events), c.Dropped, metrics.MaxChangeEvents)
	}
}
