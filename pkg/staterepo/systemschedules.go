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

// systemschedules.go says when a merged settings change edited one of
// grain's own schedules -- because that edit does not survive the tick.
//
// A schedule whose id begins `system:` is a job grain ships rather than
// one this deployment made (model.SystemTemplate): what it fires is the
// template compiled into the running build, and the row's title, body,
// reads, writes, grants, custom fields, target and base are a cache of
// that template for the schedules list. Nothing about them reaches a
// run. So orchestrator.reconcileSystemSchedules writes this build's
// template back over them on the first cycle that finds them saying
// anything else, which is what keeps the pane from advertising a prompt
// no agent will ever be given.
//
// From the outside that repair looks like a merge being thrown away. A
// `schedule` row is a settings row (SettingsTables), so a hand-edited
// dump imports over those columns like any other merge, the next cycle
// undoes it, and the export after that writes the revert back here: the
// pull request lands and its change vanishes a tick later. Until now the
// only account of it was a line in the daemon's journal, on a host the
// person who merged it may never log in to.
//
// This is the notice for it, and it deliberately fires on the way *in*
// rather than where the repair happens. The resync is in
// pkg/orchestrator, which has no GitHub credential and no idea which
// import a row arrived in; the import is here, where the commit is
// known and Config.AnnounceSystemScheduleEdits reaches the pull request
// the merge came in on -- resets.go's road exactly (Config.AnnounceResets
// and pkg/cli/stateresets.go), for the same reason resets.go took it:
// the person who needs telling is the one who merged it, and they are
// not reading this host's journal. The orchestrator stays the repair and
// this stays the notice.
//
// What it compares is the arriving row against the row this database
// holds, not against the template itself. The stored row *is* this
// build's template -- that is what the resync guarantees -- so the two
// questions have the same answer in the steady state, and comparing
// against the database asks the narrower one that is actually worth a
// comment: not "does this row differ from the template" (which is also
// true, harmlessly, on every deployment for the cycle between an upgrade
// and its first resync) but "is this import changing content grain owns".
// It also keeps this file out of the business of resolving a system
// template, which needs the deployment's own state repository
// (model.SystemTemplate) and would report a whole schedule as edited on
// every ordinary merge if it resolved it wrongly.
//
// A notice and not a refusal, for resets.go's reason and replaced.go's:
// the repository is what says what a setting is, and a host that refused
// an import would sit at a commit it has not taken up and stop every
// later settings change from arriving until somebody went to the host.
//
// Two neighbouring cases are deliberately not here. A dump that deletes
// the row altogether is the seeder's case rather than the resync's --
// grain writes a fresh row on the next cycle, with the deployment's
// cadence gone rather than restored, and that is a different sentence
// from this one; it is reported as a removal by the line replaced.go
// writes. And a change to the columns a deployment *is* allowed to make
// -- how often the job runs, whether firings may overlap, whether it is
// enabled at all (ui.refuseSystemScheduleContentEdit) -- is an ordinary
// settings change that lands and stays landed, so nothing here says a
// word about it.

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SystemScheduleEdit is one of grain's own schedules that a merged
// settings change edited the content of, and that the next reconcile
// cycle will therefore put back.
//
// Exported for Config.AnnounceSystemScheduleEdits' sake: cmd/grain has
// the credential and posts this on the pull request the merge came in
// on, and every field of it is part of that sentence.
//
// Columns and not values. A reset is worth quoting because the value it
// took is gone from everywhere but the journal (Reset's own doc
// comment); an edit that is about to be undone is written down in full
// in the diff of the very pull request this is posted on, so naming the
// columns says which part of the change is the part that will not stick
// and leaves the text where the reader is already looking.
type SystemScheduleEdit struct {
	// Path is the file that carried the row, relative to the repository
	// root -- always the schedule table's, since that is the row this is
	// about even when what changed was one of the child tables.
	Path string `json:"path"`
	// ID is the schedule's own id, which is also the id of the system
	// template it fires ("system:memory-agent").
	ID string `json:"id"`
	// Columns names what arrived changed, in the vocabulary the
	// orchestrator's own resync line uses: "title", "body", "reads",
	// "grants" and the rest, rather than the table columns they are
	// stored in.
	Columns []string `json:"columns"`
}

func (e SystemScheduleEdit) String() string {
	return fmt.Sprintf("%s (id = '%s'): %s", e.Path, e.ID, strings.Join(e.Columns, ", "))
}

// systemScheduleContent is the `schedule` table's own content columns,
// each with the name the orchestrator calls it by
// (orchestrator.systemContentDrift). The pairs are in that function's
// order so that a line here and a line in the daemon's journal name the
// same drift the same way round.
//
// target_owner and target_name are one thing to a reader -- the repo the
// firing is bound to -- so they carry one name between them, and
// changedColumns says it once.
//
// Everything not on this list is the deployment's own: the recurrence
// columns, overlap, enabled, the firing times and created_at. Those
// import and stay imported, which is the whole point of the split.
var systemScheduleContent = []struct{ column, name string }{
	{"template_id", "template"},
	{"title", "title"},
	{"body", "body"},
	{"auto_merge", "autoMerge"},
	{"target_owner", "repo"},
	{"target_name", "repo"},
	{"base", "base"},
}

// systemScheduleContentTables is the rest of a schedule's content, which
// lives a row at a time in tables of its own -- and is resynced just as
// the columns above are, so an import that widens one of grain's own
// jobs by adding a grant to the dump is undone in exactly the same way.
var systemScheduleContentTables = []struct{ table, name string }{
	{"schedule_read", "reads"},
	{"schedule_write", "writes"},
	{"schedule_grant", "grants"},
	{"schedule_custom_field", "customFields"},
}

// systemScheduleColumn is the column those child tables name their
// schedule by.
const systemScheduleColumn = "schedule_id"

// systemScheduleEditsFromApplying reports which of grain's own schedules
// importing dir would change the content of.
//
// Read before ImportTables, for resetsFromApplying's reason: afterwards
// the database holds the file's version and there is nothing left to
// compare.
//
// A system schedule the database does not hold is not reported -- there
// is nothing for the import to change, and a row arriving for a template
// this build does not ship is not something the resync touches at all.
func systemScheduleEditsFromApplying(ctx context.Context, db querier, dir string) ([]SystemScheduleEdit, error) {
	present, err := tableNames(ctx, db)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, t := range present {
		has[t] = true
	}
	if !has["schedule"] {
		return nil, nil
	}
	cols, err := columns(ctx, db, "schedule")
	if err != nil {
		return nil, err
	}
	byName := map[string]column{}
	for _, c := range cols {
		byName[c.name] = c
	}
	stored, err := storedRows(ctx, db, "schedule", cols)
	if err != nil {
		return nil, err
	}
	arriving, err := arrivingRows(dir, "schedule", byName)
	if err != nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	for _, row := range arriving {
		if id, ok := textOf(row["id"]); ok {
			byID[id] = row
		}
	}
	child, err := childContentEdits(ctx, db, dir, has)
	if err != nil {
		return nil, err
	}
	path := filepath.FromSlash(DumpPath("schedule"))
	var out []SystemScheduleEdit
	for _, row := range stored {
		id, ok := textOf(row["id"])
		if !ok || !model.IsSystemTemplateID(id) {
			continue
		}
		file, ok := byID[id]
		if !ok {
			// The file does not carry this row: a deletion rather than an
			// edit, which is the seeder's case and this file's doc
			// comment says why it is not reported here.
			continue
		}
		changed := append(changedColumns(row, file), child[id]...)
		if len(changed) == 0 {
			continue
		}
		out = append(out, SystemScheduleEdit{Path: path, ID: id, Columns: changed})
	}
	return out, nil
}

// changedColumns names the content of one system schedule row that the
// arriving row says something else about.
//
// Only columns the file carries. One it leaves out is resets.go's to
// report -- that lands on the schema's default, which is a loss with a
// value on the other side of it rather than a change somebody wrote --
// and reporting the same column in two sentences about one import would
// be two accounts of one event.
func changedColumns(stored, arriving map[string]any) []string {
	var out []string
	for _, c := range systemScheduleContent {
		was, held := stored[c.column]
		now, carried := arriving[c.column]
		if !held || !carried {
			continue
		}
		if canonical(was) == canonical(now) {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == c.name {
			// target_owner and target_name, both moved: one repo, said
			// once.
			continue
		}
		out = append(out, c.name)
	}
	return out
}

// childContentEdits is changedColumns for the content that lives a row
// at a time: which of grain's own schedules the arriving dump gives a
// different set of reads, writes, grants or custom field values to.
//
// Compared as sets rather than row by row, because that is what the
// column means -- a schedule's grants are the rows that name it, in no
// particular order -- so an added row, a removed one and an edited one
// all come out as the one thing worth saying, which is that the grants
// arrived different.
func childContentEdits(ctx context.Context, db querier, dir string, has map[string]bool) (map[string][]string, error) {
	out := map[string][]string{}
	for _, t := range systemScheduleContentTables {
		if !has[t.table] {
			// A table this build does not have is one ImportTables skips.
			continue
		}
		cols, err := columns(ctx, db, t.table)
		if err != nil {
			return nil, err
		}
		byName := map[string]column{}
		for _, c := range cols {
			byName[c.name] = c
		}
		stored, err := storedRows(ctx, db, t.table, cols)
		if err != nil {
			return nil, err
		}
		arriving, err := arrivingRows(dir, t.table, byName)
		if err != nil {
			return nil, err
		}
		compared := comparableColumns(cols, arriving)
		held := systemRowsBySchedule(stored, cols, compared)
		coming := systemRowsBySchedule(arriving, cols, compared)
		for id, was := range held {
			if !equalRowSets(was, coming[id]) {
				out[id] = append(out[id], t.name)
			}
		}
		for id, now := range coming {
			if _, seen := held[id]; !seen && len(now) > 0 {
				out[id] = append(out[id], t.name)
			}
		}
	}
	return out, nil
}

// comparableColumns is the columns of a child table both sides can be
// compared on: this build's, less any the arriving file does not carry
// on every row of that table.
//
// A dump written before a column existed carries none of it -- the
// import lands that column on its default, which resets.go reports with
// the value it took -- and comparing on a column one side simply does
// not have would call every row of the table changed on the strength of
// a column nobody edited.
func comparableColumns(cols []column, arriving []map[string]any) []column {
	var out []column
	for _, c := range cols {
		carried := true
		for _, row := range arriving {
			if _, ok := row[c.name]; !ok {
				carried = false
				break
			}
		}
		if carried {
			out = append(out, c)
		}
	}
	return out
}

// systemRowsBySchedule groups a child table's rows by the system
// schedule they belong to, each row rendered as the text of the columns
// being compared so that two sides can be held up against each other.
// Rows belonging to an ordinary schedule are dropped here: nothing
// resyncs those, so an edit to one is an ordinary settings change.
func systemRowsBySchedule(rows []map[string]any, cols, compared []column) map[string][]string {
	out := map[string][]string{}
	for _, row := range rows {
		id, ok := textOf(row[systemScheduleColumn])
		if !ok || !model.IsSystemTemplateID(id) {
			continue
		}
		parts := make([]string, 0, len(compared))
		for _, c := range compared {
			parts = append(parts, canonical(row[c.name]))
		}
		out[id] = append(out[id], strings.Join(parts, "\x00"))
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out
}

func equalRowSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// arrivingRows reads one settings table's file as the values ImportTables
// would bind for it, or nothing at all for a table the dump has no file
// for -- which imports as empty, and is a removal rather than an edit.
func arrivingRows(dir, table string, byName map[string]column) ([]map[string]any, error) {
	rows, present, err := dumpRowsOf(dir, table)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		decoded, ok := decodeRow(dir, byName, row)
		if !ok {
			// A row the import is about to fail on. An import that fails
			// reports nothing at all (applySettings' ordering), so the
			// only thing a half-read row could do here is be wrong.
			continue
		}
		out = append(out, decoded)
	}
	return out, nil
}

// textOf is a stored or decoded value read as the string it is, and
// false for anything else -- a NULL id, a blob, a number where this
// schema has TEXT. None of those name a system template, which is the
// only question asked of it.
func textOf(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	default:
		return "", false
	}
}

// systemScheduleEditsForNotice is systemScheduleEditsFromApplying with a
// cancelled context treated as nothing to say, for resetsForNotice's
// reason: the import below is about to fail on the same context, and a
// line about the notice being unavailable would be noise on top of a
// shutdown.
func systemScheduleEditsForNotice(ctx context.Context, db querier, dir string) ([]SystemScheduleEdit, error) {
	edits, err := systemScheduleEditsFromApplying(ctx, db, dir)
	if err != nil && ctx.Err() != nil {
		return nil, nil
	}
	return edits, err
}

// reportSystemScheduleEdits writes the journal half of the notice: which
// of grain's own schedules the merge edited, which of their content it
// changed, and what becomes of that.
//
// One line rather than one per schedule, for reportResets' reason: these
// arrive together, as one import of one commit, and a journal is read by
// the line.
//
// It says what will happen rather than what has happened, because at
// this point nothing has: the row now holds what arrived, and the resync
// that undoes it is the orchestrator's next cycle. The line the daemon
// writes when that runs is the other end of the same story.
func reportSystemScheduleEdits(edits []SystemScheduleEdit) {
	if len(edits) == 0 {
		return
	}
	lines := make([]string, len(edits))
	for i, e := range edits {
		lines[i] = e.String()
	}
	how := "one of its own schedules"
	if len(edits) != 1 {
		how = fmt.Sprintf("%d of its own schedules", len(edits))
	}
	log.Printf("staterepo: the settings that just arrived changed content grain owns on %s: "+
		"%s. Those columns are a cache of the template this build ships and are not what fires, "+
		"so the next reconcile cycle writes the template back over them and the export after that "+
		"writes the revert out to the state repository -- this change is undone rather than kept. "+
		"If one of these jobs should say something different, that belongs in a pull request "+
		"against grain's own source, where the template is code, rather than against the state "+
		"repository.",
		how, strings.Join(lines, "; "))
}

// announceSystemScheduleEdits offers the same fact to whatever else this
// deployment can say it with (Config.AnnounceSystemScheduleEdits), once
// the journal has it.
//
// Journal first and always, for announceResets' reason: the line above
// is written by this process to a file that is already open and cannot
// fail, and what is here needs a credential, a network and a pull
// request that may not exist.
//
// Unlike a reset, a callback that could not deliver leaves no marker on
// the host. The two facts differ in exactly the way that surface exists
// for: a reset's value is gone from everywhere but the journal, so a
// deployment that could not say so has lost something nobody will ever
// see again -- while what is edited here is written out in full in the
// pull request that carried it and in the state repository's own
// history, and is undone by a reconciler that says so again in this
// journal on the cycle it runs. There is nothing here for a person to
// recover from a pane.
func announceSystemScheduleEdits(ctx context.Context, r *Repo, head string, edits []SystemScheduleEdit) {
	if len(edits) == 0 || r == nil || r.cfg.AnnounceSystemScheduleEdits == nil {
		return
	}
	if err := r.cfg.AnnounceSystemScheduleEdits(ctx, head, edits); err != nil {
		if ctx.Err() != nil {
			// A shutdown cancelled the announcement rather than anything
			// being wrong with it, and the merge is taken up again on the
			// next start.
			return
		}
		// One line, and only here: the callback has already written
		// whatever it had to say about why in its own words, and this is
		// the sentence that ties its failure to the notice above it.
		log.Printf("staterepo: the notice above about the schedule content %s changed did not reach "+
			"the pull request it came in on (%v); the journal line above is the only record of it",
			head, err)
	}
}
