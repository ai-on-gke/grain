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

// The API surface of this deployment's weekly account of itself: which
// week a caller that names none means, what the numbers beside the prose
// are counted over, and what a week too long to be notes is told.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestReleaseNotesArePublishedAndListed(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	note, err := c.PutReleaseNote(ctx, "2026-W38", "Twelve landed", "A busy week.",
		model.Principal{Kind: model.PrincipalAgent, ID: "412"})
	if err != nil {
		t.Fatalf("publishing a week: %v", err)
	}
	if note.Period != "2026-W38" || note.Text != "A busy week." {
		t.Fatalf("published %+v, want the week that was written", note)
	}
	if note.UpdatedByKind != string(model.PrincipalAgent) {
		t.Errorf("author kind = %q, want the agent the caller named", note.UpdatedByKind)
	}

	listed, err := c.ListReleaseNotes(ctx)
	if err != nil {
		t.Fatalf("ListReleaseNotes: %v", err)
	}
	if len(listed) != 1 || listed[0].Period != "2026-W38" {
		t.Fatalf("listing = %+v, want the one week published", listed)
	}
	if listed[0].Text != "" || listed[0].Length == 0 {
		t.Errorf("listing carries the text (%q) rather than its size", listed[0].Text)
	}

	read, err := c.GetReleaseNote(ctx, "2026-W38")
	if err != nil {
		t.Fatalf("GetReleaseNote: %v", err)
	}
	if read.Text != "A busy week." {
		t.Errorf("read back %q, want the note", read.Text)
	}
}

// A week nothing has been written about is an answer, not a 404: a
// deployment that has only just installed this build has nothing but
// weeks like that, and the pane renders the absence.
func TestGetReleaseNoteAnswersForAWeekNobodyHasWritten(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	note, err := c.GetReleaseNote(ctx, "2026-W12")
	if err != nil {
		t.Fatalf("GetReleaseNote: %v", err)
	}
	if note.Period != "2026-W12" || note.Text != "" {
		t.Errorf("unwritten week = %+v, want the week itself and no text", note)
	}
}

// Naming no week means the week that just ended, resolved once here, so
// that the job writing, the tool counting and the pane reading cannot
// end up on different weeks.
func TestReleaseNotesDefaultToTheWeekThatJustEnded(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	now := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }

	note, err := c.PutReleaseNote(ctx, "", "Last week", "What happened.", model.Principal{})
	if err != nil {
		t.Fatalf("publishing with no week named: %v", err)
	}
	if note.Period != "2026-W38" {
		t.Errorf("filed under %q, want the week that ended on the Sunday, 2026-W38", note.Period)
	}
	stats, err := c.ClusterStats(ctx, "")
	if err != nil {
		t.Fatalf("ClusterStats: %v", err)
	}
	if stats.Period != note.Period {
		t.Errorf("the numbers are for %q and the notes for %q; they have to be the same week",
			stats.Period, note.Period)
	}
	if !stats.Since.Equal(time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("counted from %s, want that week's own Monday", stats.Since)
	}
	if !stats.Until.Equal(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("counted to %s, want the Monday after it", stats.Until)
	}
}

// The lines the week's merges changed, reaching the API with the count
// of how many merges they were summed over beside them -- which is what
// lets the pane show "9 of 12 merges" rather than a total that is
// quietly short (model.ClusterStats.PullRequestsMeasured).
func TestClusterStatsCarriesTheLinesAndHowManyMergesTheyCover(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	for i, size := range []*int{intPtr(214), intPtr(37), nil} {
		id := string(rune('a' + i))
		merged := monday.Add(time.Duration(i+1) * 24 * time.Hour)
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: "task " + id, Target: &repo, CreatedAt: &monday,
			Origin: model.Origin{Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}},
			Binding: model.BindingDirective,
		}); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
		obs := model.Observation{TaskID: id, PrMergedAt: &merged, CompletedAt: &merged}
		if size != nil {
			obs.PrAdditions, obs.PrDeletions, obs.PrChangedFiles = size, intPtr(1), intPtr(2)
		}
		if err := store.Observe(ctx, obs); err != nil {
			t.Fatalf("observing %s: %v", id, err)
		}
	}

	stats, err := c.ClusterStats(ctx, "2026-W38")
	if err != nil {
		t.Fatalf("ClusterStats: %v", err)
	}
	if stats.PullRequestsMerged != 3 || stats.PullRequestsMeasured != 2 {
		t.Fatalf("merged/measured = %d/%d, want 3 and 2: the third merge carries no size",
			stats.PullRequestsMerged, stats.PullRequestsMeasured)
	}
	if stats.LinesAdded != 251 || stats.LinesDeleted != 2 {
		t.Errorf("lines = +%d -%d, want +251 -2", stats.LinesAdded, stats.LinesDeleted)
	}
	if len(stats.Repos) != 1 {
		t.Fatalf("repos = %+v, want the one the week went into", stats.Repos)
	}
	if stats.Repos[0].LinesAdded != 251 || stats.Repos[0].PullRequestsMeasured != 2 {
		t.Errorf("repo row = %+v, want the same lines and the same two merges measured", stats.Repos[0])
	}
}

func intPtr(v int) *int { return &v }

// The names those numbers travel under. The pane reads them by key
// (ui/src/components/ReleaseNotesPanel.jsx), and a rename on this side
// would not fail a single Go test while quietly leaving every chip
// blank -- so the route's own JSON is asserted here, spelled the way
// the pane spells it.
func TestClusterStatsRouteNamesTheLinesTheWayThePaneReadsThem(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(c)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	merged := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID: "a", Title: "a task", Target: &repo, CreatedAt: &merged,
		Origin: model.Origin{Attribution: model.Attribution{
			Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}},
		Binding: model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing: %v", err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: "a", PrMergedAt: &merged, CompletedAt: &merged,
		PrAdditions: intPtr(214), PrDeletions: intPtr(37), PrChangedFiles: intPtr(9),
	}); err != nil {
		t.Fatalf("observing: %v", err)
	}

	rec := do(t, srv, http.MethodGet, "/api/release-notes/stats?period=2026-W38", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := decode[map[string]any](t, rec)
	for key, want := range map[string]float64{
		"pullRequestsMerged":   1,
		"pullRequestsMeasured": 1,
		"linesAdded":           214,
		"linesDeleted":         37,
	} {
		got, ok := body[key]
		if !ok {
			t.Errorf("the answer carries no %q, which is the key the pane reads: %v", key, body)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	repos, _ := body["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("repos = %v, want the one the week went into", body["repos"])
	}
	row, _ := repos[0].(map[string]any)
	if row["linesAdded"] != float64(214) || row["pullRequestsMeasured"] != float64(1) {
		t.Errorf("repo row = %v, want the same lines under the same keys", row)
	}
	directions, _ := body["directions"].([]any)
	if len(directions) != 1 {
		t.Fatalf("directions = %v, want the one root direction", body["directions"])
	}
	dirRow, _ := directions[0].(map[string]any)
	if dirRow["taskId"] != "a" || dirRow["author"] != "alice" || dirRow["linesAdded"] != float64(214) {
		t.Errorf("direction row = %v, want taskId a, author alice and linesAdded 214", dirRow)
	}
}

func TestClusterStatsSupportsDailyWeeklyAndMonthlyRollupsAndDirections(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	now := time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Root task #10 filed on Nov 3 ("yesterday"), spawning follow-up #11 on
	// Nov 4 ("today") that merges a PR on Nov 4.
	yesterday := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	today := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	merged := today.Add(2 * time.Hour)
	if err := store.PutTask(ctx, model.Task{
		ID: "10", Title: "Improve CI caching", Target: &repo, CreatedAt: &yesterday,
		Origin: model.Origin{Attribution: model.Attribution{
			Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}}},
		Binding: model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing root 10: %v", err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: "11", Title: "Warm npm cache in step", Target: &repo, CreatedAt: &today,
		Origin: model.Origin{Attribution: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "10"}}},
		Links:   []model.Link{{Kind: model.LinkProposedBy, Target: "10"}},
		Binding: model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing child 11: %v", err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: "11", PrMergedAt: &merged, CompletedAt: &merged,
		PrAdditions: intPtr(120), PrDeletions: intPtr(30), PrChangedFiles: intPtr(3),
	}); err != nil {
		t.Fatalf("observing child 11: %v", err)
	}

	// Daily rollup for "today" (2026-11-04): child #11 was filed and merged
	// today, rolled up under root direction #10 ("Improve CI caching").
	dayStats, err := c.ClusterStats(ctx, "today")
	if err != nil {
		t.Fatalf("ClusterStats(today): %v", err)
	}
	if dayStats.Period != "2026-11-04" {
		t.Errorf("today period = %q, want 2026-11-04", dayStats.Period)
	}
	if dayStats.PullRequestsMerged != 1 || dayStats.TasksFiled != 1 {
		t.Errorf("today stats = %+v, want 1 merged and 1 filed", dayStats)
	}
	if len(dayStats.Directions) != 1 {
		t.Fatalf("today directions = %+v, want 1 direction", dayStats.Directions)
	}
	dir := dayStats.Directions[0]
	if dir.TaskID != "10" || dir.Title != "Improve CI caching" || dir.Author != "operator" || dir.TasksSpawned != 1 {
		t.Errorf("today direction = %+v, want root #10 by operator with 1 spawned follow-up", dir)
	}

	// Monthly rollup for "this-month" (2026-11): includes both yesterday's
	// root filing and today's follow-up filing + merge under direction #10.
	monthStats, err := c.ClusterStats(ctx, "this-month")
	if err != nil {
		t.Fatalf("ClusterStats(this-month): %v", err)
	}
	if monthStats.Period != "2026-11" {
		t.Errorf("this-month period = %q, want 2026-11", monthStats.Period)
	}
	if monthStats.TasksFiled != 2 || monthStats.PullRequestsMerged != 1 {
		t.Errorf("this-month stats = %+v, want 2 tasks filed and 1 merged", monthStats)
	}
	if len(monthStats.Directions) != 1 || monthStats.Directions[0].TasksFiled != 2 || monthStats.Directions[0].TasksSpawned != 1 {
		t.Errorf("this-month directions = %+v, want 1 direction with 2 filed and 1 spawned", monthStats.Directions)
	}
}

func TestReleaseNotesRefuseWhatIsNoLongerNotes(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	_, err := c.PutReleaseNote(ctx, "2026-W38", "t",
		strings.Repeat("x", model.MaxReleaseNoteLength+1), model.Principal{})
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a week longer than the bound was accepted (%v)", err)
	}
	if _, err := c.PutReleaseNote(ctx, "2026-W38", "", "something", model.Principal{}); err == nil {
		t.Error("a week with no headline was published; the pane lists weeks by their headline")
	}
	if _, err := c.PutReleaseNote(ctx, "last week", "t", "x", model.Principal{}); err == nil {
		t.Error("a week filed under a period nothing else spells that way was accepted")
	}
}
