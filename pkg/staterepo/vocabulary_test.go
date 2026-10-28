package staterepo_test

// The value half of `grain state check`: a column holding a word this
// build does not have (vocabulary.go).
//
// These are all dashboards, because a dashboard is the thing here that
// is assembled by hand in a pull request rather than written by the
// daemon -- and `range` in particular, which is the field that produced
// the file: a panel written `"1d"` when the day-wide range is `"24h"`
// draws a window nobody asked for and looks exactly like a panel that
// asked for it.

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// panelDump is dumpOf with a dashboard hand-written into it: a `board`
// row of kind "panels" and whatever panels a test wants under it. The
// rows are written rather than stored through PutBoard on purpose --
// the API validates, and the whole point of these warnings is the rows
// that never went through it.
func panelDump(t *testing.T, panels []map[string]any) string {
	t.Helper()
	dir := dumpOf(t)
	writeTable(t, dir, "board", []map[string]any{{
		"id": "b1", "name": "A dashboard", "folder": "", "kind": "panels",
		"description": "", "query": "", "order_key": 1,
	}})
	for i := range panels {
		panels[i]["board_id"] = "b1"
		// board_panel is one of the settings tables, so a row that leaves
		// a column out draws a warning of its own (check.go's
		// settingsResets) and would drown the ones these tests are about.
		panels[i]["id"] = i + 1
		if _, ok := panels[i]["order_key"]; !ok {
			panels[i]["order_key"] = float64(i + 1)
		}
	}
	writeTable(t, dir, "board_panel", panels)
	return dir
}

// warningsOf runs the check and insists the dump still loads: every
// warning here is about a row that imports perfectly well and then
// draws something other than what it says.
func warningsOf(t *testing.T, dir string) string {
	t.Helper()
	_, db := openDB(t)
	report, err := staterepo.Check(context.Background(), db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	return strings.Join(report.Warnings, "\n")
}

// The one the file exists for.
func TestCheckWarnsAboutAPanelRangeThisBuildDoesNotHave(t *testing.T) {
	t.Parallel()
	dir := panelDump(t, []map[string]any{
		{"kind": "gantt", "title": "A day", "query": "", "range": "1d", "width": "half"},
	})

	joined := warningsOf(t, dir)
	if !strings.Contains(joined, `"1d"`) {
		t.Errorf("no warning naming the range that was not understood: %q", joined)
	}
	// And what it should have said, so the warning is a fix rather than
	// a puzzle.
	if !strings.Contains(joined, `"24h"`) {
		t.Errorf("warning does not offer the vocabulary: %q", joined)
	}
	if !strings.Contains(joined, "board_panel.json") {
		t.Errorf("warning does not name the file: %q", joined)
	}
}

// Every word this build does have, on the kind that has it, warns about
// nothing -- including "all", which is a timeline range and not a
// metrics one.
func TestCheckIsSilentAboutRangesThisBuildHas(t *testing.T) {
	t.Parallel()
	var panels []map[string]any
	for _, r := range model.PanelRanges {
		panels = append(panels, map[string]any{
			"kind": "gantt", "title": "", "query": "", "range": r, "width": "half",
		})
	}
	for _, r := range model.MetricsPanelRanges {
		panels = append(panels, map[string]any{
			"kind": "metrics", "title": "", "query": "", "range": r, "width": "half",
		})
	}
	// And the two kinds that draw no span, carrying no range, which is
	// the only thing they should carry.
	panels = append(panels,
		map[string]any{"kind": "list", "title": "", "query": "", "range": "", "width": "third"},
		map[string]any{"kind": "states", "title": "", "query": "", "range": "", "width": "full"},
	)
	dir := panelDump(t, panels)

	if joined := warningsOf(t, dir); joined != "" {
		t.Errorf("a dashboard using only words this build has warned: %q", joined)
	}
}

// "all" on a throughput panel is the case that wants its own sentence:
// not a typo, a range this build has and this kind cannot be asked for.
// A reviewer told "not a valid range" would go looking for a misspelling
// that is not there.
func TestCheckSaysAllIsARangeAReportCannotBeMeasuredOver(t *testing.T) {
	t.Parallel()
	dir := panelDump(t, []map[string]any{
		{"kind": "metrics", "title": "", "query": "", "range": "all", "width": "half"},
	})

	joined := warningsOf(t, dir)
	if !strings.Contains(joined, `"all"`) {
		t.Errorf("no warning about a metrics panel ranged to everything: %q", joined)
	}
	if !strings.Contains(joined, "window that ends now") {
		t.Errorf("warning does not say why the report cannot take it: %q", joined)
	}
}

// A range on a list or a state count is not read at all, which is the
// other way somebody's meaning goes missing in silence.
func TestCheckWarnsAboutARangeOnAPanelThatDrawsNoSpan(t *testing.T) {
	t.Parallel()
	dir := panelDump(t, []map[string]any{
		{"kind": "list", "title": "", "query": "", "range": "7d", "width": "half"},
	})

	joined := warningsOf(t, dir)
	if !strings.Contains(joined, "not read at all") {
		t.Errorf("no warning about a range on a kind that has none: %q", joined)
	}
	if !strings.Contains(joined, `"7d"`) {
		t.Errorf("warning does not name the value that was dropped: %q", joined)
	}
}

// Somebody who believes the range is spelled "1d" spells it that way on
// every panel of the dashboard. One line about the word, not one per
// row, or the warning that matters is buried under its own copies.
func TestCheckWarnsOnceForARangeRepeatedOnEveryPanel(t *testing.T) {
	t.Parallel()
	dir := panelDump(t, []map[string]any{
		{"kind": "gantt", "title": "One", "query": "", "range": "1d", "width": "half"},
		{"kind": "gantt", "title": "Two", "query": "", "range": "1d", "width": "half"},
		{"kind": "gantt", "title": "Three", "query": "", "range": "1d", "width": "half"},
	})

	joined := warningsOf(t, dir)
	if n := strings.Count(joined, `"1d"`); n != 1 {
		t.Errorf("warned %d times about one misspelt range, want once: %q", n, joined)
	}
}

// The other two vocabularies on the same row. Both are refused outright
// by the write API (model.Board.Validate), which is exactly why the
// dump is where they can still arrive.
func TestCheckWarnsAboutAPanelKindAndWidthThisBuildDoesNotHave(t *testing.T) {
	t.Parallel()
	dir := panelDump(t, []map[string]any{
		{"kind": "timeline", "title": "", "query": "", "range": "", "width": "half"},
		{"kind": "list", "title": "", "query": "", "range": "", "width": "quarter"},
	})

	joined := warningsOf(t, dir)
	if !strings.Contains(joined, `"timeline"`) {
		t.Errorf("no warning about a panel kind this build does not have: %q", joined)
	}
	if !strings.Contains(joined, `"quarter"`) {
		t.Errorf("no warning about a width this build does not have: %q", joined)
	}
}

// A panel whose kind is already unrecognised draws as a list, and which
// vocabulary its range was meant to be measured against is not
// something this can know. It says the kind and stops rather than
// inventing the missing fact.
func TestCheckDoesNotGuessTheRangeOfAPanelWhoseKindItDoesNotKnow(t *testing.T) {
	t.Parallel()
	dir := panelDump(t, []map[string]any{
		{"kind": "timeline", "title": "", "query": "", "range": "1d", "width": "half"},
	})

	joined := warningsOf(t, dir)
	if !strings.Contains(joined, `"timeline"`) {
		t.Errorf("no warning about the kind: %q", joined)
	}
	if strings.Contains(joined, `"1d"`) {
		t.Errorf("warned about the range of a panel whose kind it does not know: %q", joined)
	}
}

// The board itself, and the sections of a report board: the same silent
// fallback on the rows either side of a panel.
func TestCheckWarnsAboutABoardKindThisBuildDoesNotHave(t *testing.T) {
	t.Parallel()
	dir := dumpOf(t)
	writeTable(t, dir, "board", []map[string]any{{
		"id": "b1", "name": "A dashboard", "folder": "", "kind": "dashboard",
		"description": "", "query": "", "order_key": 1,
	}})
	writeTable(t, dir, "board_section", []map[string]any{{
		"id": 1, "board_id": "b1", "kind": "throughtput", "title": "", "order_key": 1,
	}})

	joined := warningsOf(t, dir)
	if !strings.Contains(joined, `"dashboard"`) {
		t.Errorf("no warning about a board kind this build does not have: %q", joined)
	}
	if !strings.Contains(joined, `"throughtput"`) {
		t.Errorf("no warning about a report section this build does not have: %q", joined)
	}
}

// A dump grain wrote itself, with a real dashboard in it, still warns
// about nothing -- the guard every warning in check.go needs, since a
// warning on every deployment's export would be a warning nobody reads.
func TestCheckIsSilentAboutADashboardGrainStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	board := model.Board{
		ID: "b1", Name: "A dashboard", Kind: model.BoardPanels,
		Panels: []model.BoardPanel{
			{Kind: model.PanelGantt, Range: "24h", Width: model.PanelHalf, OrderKey: 1},
			{Kind: model.PanelMetrics, Range: "30d", Width: model.PanelHalf, OrderKey: 2},
			{Kind: model.PanelList, Width: model.PanelFull, OrderKey: 3},
		},
	}
	if err := board.Validate(); err != nil {
		t.Fatalf("the board this test stores is not valid: %v", err)
	}
	if err := store.PutBoard(ctx, board); err != nil {
		t.Fatalf("storing the board: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatalf("stamping: %v", err)
	}

	if joined := warningsOf(t, dir); joined != "" {
		t.Errorf("grain's own export of a dashboard warned: %q", joined)
	}
}
