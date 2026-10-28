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

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// What the two tools behind the "release-notes" capability owe the run
// that holds them: numbers it can quote without counting, a write it
// cannot get subtly wrong, and a refusal it can act on in the same turn.

type fakeReleaseNotes struct {
	stats   ClusterStats
	err     error
	period  string
	title   string
	text    string
	written bool
}

func (f *fakeReleaseNotes) ClusterStats(context.Context, string) (ClusterStats, error) {
	if f.err != nil {
		return ClusterStats{}, f.err
	}
	return f.stats, nil
}

func (f *fakeReleaseNotes) WriteReleaseNotes(_ context.Context, period, title, text string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.period, f.title, f.text, f.written = period, title, text, true
	if period == "" {
		// What the daemon does with an unnamed week.
		period = "2026-W38"
	}
	return period, nil
}

func releaseTool(t *testing.T, store ReleaseNotesStore, name string) Tool {
	t.Helper()
	for _, tool := range NewReleaseNotesTools(store) {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no %s tool", name)
	return Tool{}
}

func TestReadClusterStatsReportsTheWeekAndItsRepos(t *testing.T) {
	store := &fakeReleaseNotes{stats: ClusterStats{
		Period: "2026-W38", Since: "2026-09-14", Until: "2026-09-21",
		PullRequestsMerged: 12, TasksCompleted: 18, TasksFiled: 20,
		Runs: 31, RunsSucceeded: 27, AgentHours: 41.25,
		Repos: []RepoStats{
			{Repo: "bwsalmon/grain", PullRequestsMerged: 9, TasksCompleted: 12, TasksFiled: 14, Runs: 22},
			{Repo: "bwsalmon/granule", PullRequestsMerged: 3, TasksCompleted: 6, TasksFiled: 6, Runs: 9},
		},
	}}
	res := releaseTool(t, store, "read_cluster_stats").Handler(context.Background(), nil)
	if res.IsError {
		t.Fatalf("read_cluster_stats reported an error: %s", res.Text)
	}
	for _, want := range []string{
		"2026-W38 (2026-09-14 to 2026-09-21)",
		"Pull requests merged: 12",
		"Agent runs: 31 (27 succeeded)",
		"Agent time: 41.2 hours",
		"- bwsalmon/grain: 9 merged",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("answer does not say %q:\n%s", want, res.Text)
		}
	}
}

// The lines-changed figure, in each of the three shapes a window can
// be in: fully measured, partly measured, and not measured at all.
//
// The middle one is the one worth a test. Grain records a merged pull
// request's size as it observes the merge and has only done so since
// September 2026, so a window reaching back further sums over some of
// its merges and not others -- and an answer that reported the sum
// without saying how much of the week it covered would hand a run a
// figure that is wrong in the direction nobody checks.
func TestReadClusterStatsSaysHowMuchOfTheWeekItsLinesCover(t *testing.T) {
	week := func(merged, measured, added, deleted int) ClusterStats {
		return ClusterStats{
			Period: "2026-W38", Since: "2026-09-14", Until: "2026-09-21",
			PullRequestsMerged: merged, PullRequestsMeasured: measured,
			LinesAdded: added, LinesDeleted: deleted,
			Repos: []RepoStats{{
				Repo: "bwsalmon/grain", PullRequestsMerged: merged,
				PullRequestsMeasured: measured, LinesAdded: added, LinesDeleted: deleted,
			}},
		}
	}
	for _, c := range []struct {
		name          string
		stats         ClusterStats
		want, wantNot []string
	}{
		{
			name:  "every merge measured",
			stats: week(12, 12, 4210, 1875),
			want:  []string{"Lines changed: +4210 -1875 across all 12 merges", "(+4210 -1875)"},
			// Nothing about partiality, since there is none: a caveat
			// on a complete figure is one a run will repeat in the
			// notes, and it would not be true.
			wantNot: []string{"partial", "of the week's"},
		},
		{
			name:  "some merges predate the recording",
			stats: week(12, 9, 4210, 1875),
			want: []string{
				"+4210 -1875, over 9 of the week's 12 merges",
				"no diff size on file for the other 3",
				"partial figure",
				"(+4210 -1875 over 9 of them)",
			},
		},
		{
			name:    "nothing measured at all",
			stats:   week(12, 0, 0, 0),
			want:    []string{"Lines changed: not recorded for any of this week's 12 merges"},
			wantNot: []string{"+0 -0"},
		},
		{
			name:    "nothing merged at all",
			stats:   week(0, 0, 0, 0),
			wantNot: []string{"Lines changed"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := releaseTool(t, &fakeReleaseNotes{stats: c.stats},
				"read_cluster_stats").Handler(context.Background(), nil)
			if res.IsError {
				t.Fatalf("read_cluster_stats reported an error: %s", res.Text)
			}
			for _, want := range c.want {
				if !strings.Contains(res.Text, want) {
					t.Errorf("answer does not say %q:\n%s", want, res.Text)
				}
			}
			for _, unwanted := range c.wantNot {
				if strings.Contains(res.Text, unwanted) {
					t.Errorf("answer says %q and should not:\n%s", unwanted, res.Text)
				}
			}
		})
	}
}

// A deployment with a quiet week gets a sentence rather than an empty
// section: "no repository saw any work" is an answer, and a blank is
// something an agent will explain to itself.
func TestReadClusterStatsSaysSoForAWeekWithNothingInIt(t *testing.T) {
	store := &fakeReleaseNotes{stats: ClusterStats{Period: "2026-W38", Since: "a", Until: "b"}}
	res := releaseTool(t, store, "read_cluster_stats").Handler(context.Background(), nil)
	if !strings.Contains(res.Text, "No repository saw any work") {
		t.Errorf("a quiet week reads as:\n%s", res.Text)
	}
}

func TestWriteReleaseNotesPublishesTheWeek(t *testing.T) {
	store := &fakeReleaseNotes{}
	res := releaseTool(t, store, "write_release_notes").Handler(context.Background(), map[string]any{
		"title": "Twelve landed", "text": "A busy week.",
	})
	if res.IsError {
		t.Fatalf("write_release_notes reported an error: %s", res.Text)
	}
	if !store.written || store.text != "A busy week." || store.title != "Twelve landed" {
		t.Errorf("stored %+v, want the call's own title and text", store)
	}
	if store.period != "" {
		t.Errorf("period = %q, want the call to pass the week on unresolved: which week "+
			"\"this one\" is belongs to the deployment's clock, not to this process", store.period)
	}
	// The answer names the week that was actually written, since the
	// call did not name one.
	if !strings.Contains(res.Text, "2026-W38") {
		t.Errorf("answer does not name the week it wrote:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "release-notes/2026-W38.md") {
		t.Errorf("answer does not say where the week ends up:\n%s", res.Text)
	}
}

func TestWriteReleaseNotesRefusesWhatCannotBeRead(t *testing.T) {
	long := strings.Repeat("x", MaxReleaseNoteLength+1)
	for _, c := range []struct {
		name string
		args map[string]any
		says string
	}{
		{"too long", map[string]any{"title": "t", "text": long}, "keep them under"},
		{"no title", map[string]any{"text": "something happened"}, "need a \"title\""},
		{"title too long", map[string]any{
			"title": strings.Repeat("t", MaxReleaseNoteTitleLength+1), "text": "x"},
			"keep it under"},
	} {
		store := &fakeReleaseNotes{}
		res := releaseTool(t, store, "write_release_notes").Handler(context.Background(), c.args)
		if !res.IsError || !strings.Contains(res.Text, c.says) {
			t.Errorf("%s: answered %q (error=%v), want a refusal saying %q",
				c.name, res.Text, res.IsError, c.says)
		}
		if store.written {
			t.Errorf("%s: it was published anyway", c.name)
		}
	}
}

// An empty text withdraws the week, and the answer says so rather than
// reporting a publication of nothing.
func TestWriteReleaseNotesWithdrawsAWeekWrittenEmpty(t *testing.T) {
	store := &fakeReleaseNotes{}
	res := releaseTool(t, store, "write_release_notes").Handler(context.Background(), map[string]any{
		"period": "2026-W37", "text": "",
	})
	if res.IsError {
		t.Fatalf("withdrawing reported an error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Withdrew") || !strings.Contains(res.Text, "2026-W37") {
		t.Errorf("answer = %q, want it to say the week was withdrawn", res.Text)
	}
}

// A run dispatched with no route back to the daemon holds the tools and
// can do nothing with them, which it is told plainly: both halves of
// this capability are the daemon's.
func TestReleaseNotesToolsRefuseWithNoDaemon(t *testing.T) {
	for _, name := range []string{"read_cluster_stats", "write_release_notes"} {
		res := releaseTool(t, nil, name).Handler(context.Background(), map[string]any{
			"title": "t", "text": "x",
		})
		if !res.IsError || !strings.Contains(res.Text, "no route back to the grain daemon") {
			t.Errorf("%s with no store answered %q (error=%v)", name, res.Text, res.IsError)
		}
	}
}

func TestReleaseNotesToolsPassTheDaemonsRefusalBack(t *testing.T) {
	store := &fakeReleaseNotes{err: errors.New("2026-W99 is not a week grain files release notes under")}
	res := releaseTool(t, store, "write_release_notes").Handler(context.Background(), map[string]any{
		"period": "2026-W99", "title": "t", "text": "x",
	})
	if !res.IsError || !strings.Contains(res.Text, "not a week") {
		t.Errorf("answered %q (error=%v), want the daemon's own sentence", res.Text, res.IsError)
	}
}
