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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The period is the key, the file name and a word an agent types back
// into a tool call, so the three have to be one string -- these are the
// tests that pin what that string is and what it means.

func TestReleaseNotePeriodIsTheISOWeek(t *testing.T) {
	for _, c := range []struct {
		when time.Time
		want string
	}{
		{time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), "2026-W38"},
		// A Monday is the first day of its own week, and the Sunday
		// before it is the last day of the week before -- the boundary
		// the weekly job fires across.
		{time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), "2026-W39"},
		{time.Date(2026, 9, 20, 23, 59, 0, 0, time.UTC), "2026-W38"},
		// ISO's own year, not the calendar one: 31 December 2025 is a
		// Wednesday in the week that runs into January.
		{time.Date(2025, 12, 31, 9, 0, 0, 0, time.UTC), "2026-W01"},
	} {
		if got := model.ReleaseNotePeriod(c.when, time.UTC); got != c.want {
			t.Errorf("ReleaseNotePeriod(%s) = %q, want %q", c.when.Format(time.RFC3339), got, c.want)
		}
	}
}

// Which week it is depends on where the deployment thinks it is: an
// instant a few hours either side of midnight is a different day, and
// sometimes a different week, on two different clocks.
func TestReleaseNotePeriodIsReadOnTheDeploymentsOwnClock(t *testing.T) {
	loc, err := time.LoadLocation("Pacific/Auckland")
	if err != nil {
		t.Skipf("no zone database here: %v", err)
	}
	// Sunday evening in UTC, already Monday in Auckland.
	when := time.Date(2026, 9, 20, 20, 0, 0, 0, time.UTC)
	if got := model.ReleaseNotePeriod(when, time.UTC); got != "2026-W38" {
		t.Errorf("in UTC = %q, want 2026-W38", got)
	}
	if got := model.ReleaseNotePeriod(when, loc); got != "2026-W39" {
		t.Errorf("in Auckland = %q, want 2026-W39", got)
	}
}

// The job fires on a Monday morning and is writing about the week that
// ended a few hours earlier, and a person opening the pane midweek means
// the same week -- the one they are in is not over.
func TestLastCompleteReleaseNotePeriodIsTheWeekBefore(t *testing.T) {
	monday := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)
	if got := model.LastCompleteReleaseNotePeriod(monday, time.UTC); got != "2026-W38" {
		t.Errorf("on the Monday = %q, want the week that just ended, 2026-W38", got)
	}
	thursday := time.Date(2026, 9, 24, 17, 0, 0, 0, time.UTC)
	if got := model.LastCompleteReleaseNotePeriod(thursday, time.UTC); got != "2026-W38" {
		t.Errorf("on the Thursday = %q, want 2026-W38", got)
	}
}

// The window the numbers are counted over comes from the period itself,
// which is what makes the notes and the figures beside them
// demonstrably about the same seven days.
func TestReleaseNotePeriodRangeIsThatWeeksMondayToMonday(t *testing.T) {
	since, until, err := model.ReleaseNotePeriodRange("2026-W38", time.UTC)
	if err != nil {
		t.Fatalf("ReleaseNotePeriodRange: %v", err)
	}
	if want := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC); !since.Equal(want) {
		t.Errorf("since = %s, want %s", since, want)
	}
	if want := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC); !until.Equal(want) {
		t.Errorf("until = %s, want %s", until, want)
	}
	// Every instant in the week reads back as the week, which is the
	// property the two directions have to agree on.
	for _, at := range []time.Time{since, since.Add(72 * time.Hour), until.Add(-time.Second)} {
		if got := model.ReleaseNotePeriod(at, time.UTC); got != "2026-W38" {
			t.Errorf("%s is in %q, want 2026-W38", at, got)
		}
	}
	// A year whose week 1 starts in the previous December, which is the
	// case a naive "first Monday of January" would get wrong.
	since, _, err = model.ReleaseNotePeriodRange("2026-W01", time.UTC)
	if err != nil {
		t.Fatalf("ReleaseNotePeriodRange(2026-W01): %v", err)
	}
	if want := time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC); !since.Equal(want) {
		t.Errorf("2026-W01 starts %s, want %s", since, want)
	}
}

func TestValidateReleaseNotePeriodRefusesEverythingElse(t *testing.T) {
	if err := model.ValidateReleaseNotePeriod("2026-W38"); err != nil {
		t.Errorf("a week grain writes itself was refused: %v", err)
	}
	for _, bad := range []string{"", "2026-W0", "2026-W54", "26-W38", "2026-38", "2026-Wxx", "2026-W 8"} {
		if err := model.ValidateReleaseNotePeriod(bad); err == nil {
			t.Errorf("%q was accepted as a week; it is a key and a file name, so a second "+
				"spelling is a week that exists and cannot be read", bad)
		}
	}
}

// What the store owes a week: it round-trips whole, a rewrite replaces
// rather than accumulates, the week it was first written stays put
// across that rewrite, and an emptied week leaves no row to be exported
// or listed afterwards.
func TestReleaseNoteRoundTripsAndRewrites(t *testing.T) {
	store, ctx := open(t)
	first := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)

	if err := store.PutReleaseNote(ctx, model.ReleaseNote{
		Period: "2026-W38", Title: "Twelve landed", Text: "A busy week.",
		UpdatedAt: first, UpdatedBy: model.Principal{Kind: model.PrincipalAgent, ID: "412"},
	}); err != nil {
		t.Fatalf("PutReleaseNote: %v", err)
	}

	note, err := store.GetReleaseNote(ctx, "2026-W38")
	if err != nil || note == nil {
		t.Fatalf("GetReleaseNote = %v, %v, want the week just written", note, err)
	}
	if note.Text != "A busy week." || note.Title != "Twelve landed" {
		t.Errorf("read back %q/%q, want what was written", note.Title, note.Text)
	}
	if note.UpdatedBy.Kind != model.PrincipalAgent {
		t.Errorf("author = %+v, want the agent that wrote it", note.UpdatedBy)
	}
	if !note.CreatedAt.Equal(first) {
		t.Errorf("createdAt = %s, want the write's own time %s", note.CreatedAt, first)
	}

	// A correction is the same week again, and it must not look like a
	// week first reported on the day it was corrected.
	corrected := first.Add(30 * time.Hour)
	if err := store.PutReleaseNote(ctx, model.ReleaseNote{
		Period: "2026-W38", Title: "Twelve landed", Text: "A busy week, and one rollback.",
		UpdatedAt: corrected, UpdatedBy: model.Principal{Kind: model.PrincipalHuman, ID: "sam"},
	}); err != nil {
		t.Fatalf("rewriting: %v", err)
	}
	note, err = store.GetReleaseNote(ctx, "2026-W38")
	if err != nil || note == nil {
		t.Fatalf("GetReleaseNote after the rewrite = %v, %v", note, err)
	}
	if note.Text != "A busy week, and one rollback." {
		t.Errorf("text = %q, want the correction", note.Text)
	}
	if !note.CreatedAt.Equal(first) {
		t.Errorf("createdAt = %s after a rewrite, want the first write's %s", note.CreatedAt, first)
	}
	if !note.UpdatedAt.Equal(corrected) {
		t.Errorf("updatedAt = %s, want the correction's %s", note.UpdatedAt, corrected)
	}

	// Listing is newest first, without the text.
	if err := store.PutReleaseNote(ctx, model.ReleaseNote{
		Period: "2026-W37", Title: "Quiet", Text: "Nothing much.", UpdatedAt: first,
	}); err != nil {
		t.Fatalf("putting an earlier week: %v", err)
	}
	list, err := store.ListReleaseNotes(ctx)
	if err != nil {
		t.Fatalf("ListReleaseNotes: %v", err)
	}
	if len(list) != 2 || list[0].Period != "2026-W38" || list[1].Period != "2026-W37" {
		t.Fatalf("listing = %+v, want both weeks newest first", list)
	}
	if list[0].Text != "" {
		t.Error("the listing carries a week's text; a year of weeks is a megabyte nobody asked for")
	}
	if list[0].Length != len("A busy week, and one rollback.") {
		t.Errorf("length = %d, want the size of the note it did not read", list[0].Length)
	}

	// Emptying withdraws the week outright rather than publishing a
	// blank one, which is how something published by mistake comes back
	// out of the state repository.
	if err := store.PutReleaseNote(ctx, model.ReleaseNote{Period: "2026-W37"}); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}
	gone, err := store.GetReleaseNote(ctx, "2026-W37")
	if err != nil {
		t.Fatalf("GetReleaseNote after withdrawal: %v", err)
	}
	if gone != nil {
		t.Errorf("the withdrawn week is still there as %+v", gone)
	}
}

func TestReleaseNoteRefusesAWeekItCannotBeReadBackUnder(t *testing.T) {
	store, ctx := open(t)
	if err := store.PutReleaseNote(ctx, model.ReleaseNote{
		Period: "last week", Text: "something",
	}); err == nil {
		t.Error("a note was filed under a period nothing else spells that way")
	}
}
