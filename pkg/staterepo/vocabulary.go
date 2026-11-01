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

package staterepo

// vocabulary.go is the half of `grain state check` that reads the
// *values* in a dump rather than its shape.
//
// check.go's other warnings are all about names: a file naming a table
// this build does not have, a row carrying a column it does not have, a
// settings column a file leaves out. Every one of them is a place where
// Import silently passes over something, and the warning exists because
// "loads fine and does nothing" is what both a dump from a newer build
// and a typo look like from here.
//
// A closed vocabulary is the same failure one level in. `board_panel`
// carries three columns whose value is a word this build either has or
// does not -- kind, width and range -- and every one of them falls back
// rather than failing: an unknown kind draws as a task list, an unknown
// width sits at half, and an unknown range draws at whatever the
// browser last chose (ui/src/panels.js's normalizePanel and
// panelGanttView). The board API refuses an unknown range when panels
// are created or replaced, but rows written by hand into this
// repository deliberately remain loadable for forward compatibility.
// That tolerance still needs a warning before an imported typo reaches
// the browser.
//
// The one that produced this file is `range`. A hand run seeded a
// dashboard with a second timeline panel "ranged to a single day" and
// measured it as identical to the seven-day one, and concluded that a
// panel's range does not decide what it draws
// (docs/hand-runs/2026-09-07-panel-dashboard.md, section 5). It does.
// The seed said `"1d"`, the day-wide range is `"24h"`, and the panel
// fell back to the week the browser was already showing. A measurement
// was wrong for a whole section because a word nobody recognised looked
// exactly like a word that had been understood.
//
// So the check says it here, on the pull request the row arrives in,
// which is the earliest place a human is looking -- and says it as a
// warning rather than a failure, for the reason every other warning in
// check.go is one: a dump written by a newer build legitimately holds
// words this one has dropped, this same code runs inside a new image
// before pkg/upgrade cuts over to it, and refusing would block the
// upgrade that adds the word on the ground that the word is about to be
// added. The reviewer decides, which is what a warning is for.
//
// It is not the only place. The panel says it too, once it is drawing
// (ui/src/panels.js's panelRangeNote), because the reader looking at a
// window nobody asked for is the person the hand run's measurer
// actually was. Before the fact from here, after the fact from there --
// settingsResets' own split.

import (
	"fmt"
	"slices"

	"github.com/ai-on-gke/grain/pkg/model"
)

// vocabularyWarnings reads one table file's rows for words this build
// does not have.
//
// Only the board tables and grain_config, and that is not an accident
// of where somebody started: these are the rows a person or an agent
// writes by hand into the dump, in prose, from a prompt (a dashboard;
// a Settings change settings_locked sent to a config pull request) --
// every other table here is
// written by the daemon out of values it already had. A typo needs
// somewhere for a typo to come from.
//
// Reported once per column and per distinct word, naming the first row
// that carries it, rather than once per row. check.go's unknown-column
// loop makes the same choice for the same reason: a dashboard whose
// author believed the range was spelled "1d" spelled it that way on
// every panel, and six copies of one warning would bury the five other
// lines a reviewer needs to read.
func vocabularyWarnings(path, table string, rows []map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	// say records one warning, unless this column has already reported
	// this word.
	say := func(column, word, format string, args ...any) {
		key := column + "\x00" + word
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, fmt.Sprintf(format, args...))
	}
	for i, row := range rows {
		switch table {
		case "board":
			if word, ok := wordIn(row, "kind"); ok && !model.BoardKind(word).Valid() {
				say("kind", word, "%s row %d has kind %q, which is not %s; the UI draws a board whose "+
					"kind it does not know as the task list, so this board would load and show "+
					"something other than what it names",
					path, i, word, model.BoardKindsPhrase())
			}
		case "board_panel":
			panelWarnings(path, i, row, say)
		case "grain_config":
			grainConfigWarnings(path, i, row, say)
		case "board_section":
			if word, ok := wordIn(row, "kind"); ok && !model.ReportSection(word).Valid() {
				say("kind", word, "%s row %d names section %q, which is not %s; a section this build "+
					"does not have is not printed at all, so this row would add nothing to the report "+
					"board it names",
					path, i, word, model.ReportSectionsPhrase())
			}
		}
	}
	return out
}

// grainConfigWarnings is the deployment's own row, which a config pull
// request edits by hand too (settings_locked sends every Settings change
// there). Two of its columns are closed vocabularies that fall back
// rather than fail, and one falls back the wrong way for a typo: an
// upgrade_check_mode this build does not know reads as "auto"
// (model.NormalizeUpgradeCheckMode), so "approve " spelt wrong turns
// unattended upgrades on. Empty is each column's own default, not a
// word.
func grainConfigWarnings(path string, i int, row map[string]any, say func(column, word, format string, args ...any)) {
	if word, ok := wordIn(row, "upgrade_check_mode"); ok && word != "" && !model.ValidUpgradeCheckMode(word) {
		say("upgrade_check_mode", word, "%s row %d has upgrade_check_mode %q, which is not one of %q; "+
			"grain reads a mode it does not know as %q, so this would upgrade the deployment unattended",
			path, i, word, model.UpgradeCheckModes(), model.NormalizeUpgradeCheckMode(word))
	}
	if word, ok := wordIn(row, "network_access"); ok && word != "" && !model.ValidNetworkAccess(model.NetworkAccess(word)) {
		say("network_access", word, "%s row %d has network_access %q, which is not %s",
			path, i, word, model.NetworkAccessNames())
	}
}

// panelWarnings is the dashboard row: three closed vocabularies on one
// line of JSON, and a fourth rule that is about the pair of them --
// `range` means nothing on a kind that draws no span.
func panelWarnings(path string, i int, row map[string]any, say func(column, word, format string, args ...any)) {
	kind, kindGiven := wordIn(row, "kind")
	if kindGiven && !model.PanelKind(kind).Valid() {
		say("kind", kind, "%s row %d has kind %q, which is not %s; a panel whose kind this build does "+
			"not know is drawn as a task list rather than left out, so the dashboard would render "+
			"with a panel nobody asked for in its place",
			path, i, kind, model.PanelKindsPhrase())
	}
	if width, ok := wordIn(row, "width"); ok && width != "" && !model.PanelWidth(width).Valid() {
		say("width", width, "%s row %d has width %q, which is not %s; a width this build does not know "+
			"falls back to %q",
			path, i, width, model.PanelWidthsPhrase(), string(model.PanelHalf))
	}
	// The range, which is why this file exists. Three different things
	// can be wrong with one, and they want three different sentences: a
	// word that is no range at all, a range this build has that this
	// *kind* cannot be asked for, and a range on a kind that draws no
	// span. Saying "not a valid range" for all three would send the
	// author of the second two looking for a typo that is not there.
	//
	// Nothing is said about the range of a panel whose kind is already
	// unrecognised: such a panel draws as a list, the kind warning above
	// has said so, and guessing which vocabulary its range was meant to
	// be measured against would be inventing the fact that is missing.
	rng, rangeGiven := wordIn(row, "range")
	if !rangeGiven || rng == "" || (kindGiven && !model.PanelKind(kind).Valid()) {
		return
	}
	panelKind := model.PanelKind(kind)
	allowed := model.PanelRangesFor(panelKind)
	switch {
	case len(allowed) == 0:
		say("range", rng, "%s row %d is a %q panel, which draws no span, and carries range %q; that "+
			"value is not read at all -- only %q and %q panels have a range, so this row is either "+
			"the wrong kind or the range belongs on another one",
			path, i, kind, rng, string(model.PanelGantt), string(model.PanelMetrics))
	case slices.Contains(allowed, rng):
	case slices.Contains(model.PanelRanges, rng):
		say("range", rng, "%s row %d is a %q panel with range %q, which must be %s; %q is a range this "+
			"build has, but not one a report can be measured over -- a report is computed over a "+
			"window that ends now -- so this panel falls back to the span its query names, or to the "+
			"default week",
			path, i, kind, rng, model.PanelRangesPhrase(panelKind), rng)
	default:
		say("range", rng, "%s row %d has range %q, which is not %s; an imported row remains loadable, so this panel "+
			"draws over the default span instead and looks exactly like a panel that asked for it",
			path, i, rng, model.PanelRangesPhrase(panelKind))
	}
}

// wordIn reads a column that holds one word of a closed vocabulary.
//
// The second result is whether the column is there to be read at all: a
// row that leaves it out is check.go's business rather than this file's
// -- either a column added since the dump was written, which is normal,
// or one the schema requires, which the import below refuses on its own.
//
// A value that is not a string is reported as one anyway, in Go's own
// rendering of it. A `range` written as the number 7 rather than the
// string "7d" is precisely the sort of hand-editing slip this file is
// for, and answering "not a word, never mind" to it would be silence
// again.
func wordIn(row map[string]any, column string) (string, bool) {
	v, ok := row[column]
	if !ok || v == nil {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	return fmt.Sprint(v), true
}
