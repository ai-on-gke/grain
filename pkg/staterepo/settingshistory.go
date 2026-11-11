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

// When this deployment's settings last changed, read back off the one
// record that already has it: this repository's own git history.
//
// # Why there is nothing else to read it from
//
// grain stores settings as rows, and a row remembers what it is, not when
// it became that. `grain_config.max_ci_jobs` is 7; nothing anywhere says
// it was 10 until an operator dropped it at nine o'clock on a Sunday
// evening because the host was at 99% CPU. So every question of the form
// "we changed X, did Y improve" -- which is the whole of what a tuning
// agent does -- had no left-hand side at all, and the only way anybody
// ever answered one was to remember having made the change.
//
// The history is already here. Sync exports the database as text and
// commits whatever moved (bind.go), so a settings change is a commit
// touching a settings file, with a timestamp on it, and has been since
// this package existed. What was missing was anything that read it as a
// series of events rather than as a repository somebody might browse.
// This is that reader, and it adds no storage, no counter and no write:
// it is `git log` over an allowlist of paths, parsed.
//
// # What counts as a settings change, and what it says about one
//
// The paths are SettingsTables' own dump files plus the directories that
// hold a declaration -- settings/ (templates, suites, schedules) and
// workflows/ (the CI a repo runs). That is the same allowlist Apply
// imports into a running daemon, which is the right definition of "a
// parameter": exactly the things somebody changes in a pull request
// against this repository and that take effect without a restart. A
// commit that only moves grain's own record of what it did -- tasks,
// runs, CI history -- is not a settings change and is not reported.
//
// Each change names the fields it touched and, for the ones that are
// numbers or booleans, what they went from and to. That asymmetry is
// deliberate and is a redaction rule, not a shortcut: a dial an operator
// turns is a number or a flag (max_ci_jobs, max_workers,
// sandbox_cpus, auto_merge_by_default), and those are exactly the values a
// tuning agent has to see. Everything else in a settings row is prose, a
// path, a credential name or a capability list, and this report is served
// to agents and to anyone with the UI open -- so a field whose value is
// not a dial is named as having changed and its value is not quoted.
//
// # What it costs, and what it cannot say
//
// A few pages of `git log` per call, bounded by the window asked for and
// by a count of the changes kept, over a repository that is a few tens of
// megabytes of text. It is read on demand like every other measurement
// here rather than sampled, which is why there is no retention question to
// answer: the retention is git's, and it is the whole history.
//
// # Bookkeeping is not a settings change
//
// Some of what lives in a settings file is not anybody's choice: a
// schedule's next-run-at and last-run-at are rewritten by the daemon every
// time the schedule fires (declschema's Kept keys), and grain_config's
// agent_backends is re-exported by model discovery every half hour or so.
// On a busy deployment those commits outnumber real changes by a hundred
// to one, and a reader that counted raw commits saw back about a day
// (task 4405). So those fields are dropped, a commit left with nothing
// else is not a change at all, and the limit counts the changes kept --
// the reader pages back through history until it has that many or the
// window or the history runs out.
//
// Two things it cannot say. A setting changed and changed back inside one
// export interval is one commit or none -- the export is a snapshot every
// thirty seconds, not a journal of writes -- and a setting changed on a
// deployment whose state repository was reset (resets.go) has whatever
// history the new repository has. Both are stated rather than papered
// over: Reaches says how far back the reader actually got.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/declschema"
)

// SettingsChange is one commit that changed something a setting lives in.
type SettingsChange struct {
	// At is the commit's own author date, which for a change merged in a
	// pull request against this repository is when it was written rather
	// than when this deployment first saw it. That is the honest moment
	// for a human's change; for grain's own export commits the two are the
	// same instant.
	At time.Time
	// Commit is the full hash, so a reader can go and look, and Subject is
	// the commit's summary line -- which for an export commit already says
	// what moved (exportCommitMessage) and for a merged pull request is
	// whatever the person called it.
	Commit  string
	Subject string
	// Paths are the settings files the commit touched, repo-relative, in
	// the order git listed them.
	Paths []string
	// Fields are the individual settings that moved, as far as the diff
	// could be read: at most MaxChangedFields of them, Truncated saying
	// when there were more.
	Fields    []SettingsField
	Truncated bool
}

// SettingsField is one setting a commit moved.
type SettingsField struct {
	// Path is the file it lives in and Name the key inside it, both
	// spelled as the file spells them ("max_ci_jobs", not "CI jobs").
	Path string
	Name string
	// From and To are the values, and are set only for a field whose old
	// and new values are both numbers, booleans or null -- see this file's
	// own header on why a string value is named and not quoted. Both empty
	// means "this changed, and what it changed to is not reported".
	From string
	To   string
}

// MaxChangedFields is how many fields one change reports before saying
// there were more. A settings commit an operator made moves one or two; a
// commit that rewrites a whole row -- which is what applying a
// declaration over a table does -- moves every column it has, and none of
// those columns is the thing anybody is looking for. Twelve is generous
// for the first case and mercifully short for the second.
const MaxChangedFields = 12

// DefaultSettingsChanges is how many settings changes SettingsChanges
// collects when the caller asks for no particular number. It counts the
// changes kept, not the commits read: two hundred is more settings changes
// than a deployment makes in a quarter.
const DefaultSettingsChanges = 200

// settingsLogPage is how many commits one `git log` call reads, and
// maxSettingsCommitsScanned how many the reader reads in all before it
// stops and says how far it got. A daemon that commits a schedule's
// next-run-at every few minutes writes a few thousand commits a week to
// settings paths; the cap is there so that a whole-history read of a
// years-old repository still answers in seconds.
const (
	settingsLogPage           = 500
	maxSettingsCommitsScanned = 50000
)

// SettingsHistory is what SettingsHistory read: the changes, and how far
// back the reading actually got.
type SettingsHistory struct {
	// Changes is newest first.
	Changes []SettingsChange
	// Reaches is the oldest moment the reader can vouch for: nothing that
	// changed after it is missing from Changes. It is the oldest change
	// kept when the limit stopped the reader, the window's start (or the
	// repository's first commit, if that is later) when history ran out,
	// and the oldest commit read when the scan cap did. Zero for a
	// repository with no commits.
	Reaches time.Time
}

// SettingsChanges reads the settings changes in this repository's history
// since a moment, newest first and at most limit of them. It is
// SettingsHistory without the reach.
func (r *Repo) SettingsChanges(ctx context.Context, since time.Time, limit int) ([]SettingsChange, error) {
	h, err := r.SettingsHistory(ctx, since, limit)
	return h.Changes, err
}

// SettingsHistory reads the settings changes in this repository's history
// since a moment, newest first and at most limit of them, and says how far
// back it got.
//
// A zero since means the whole history; a limit of zero or less means
// DefaultSettingsChanges. A repository with no commits yet -- seeded this
// minute, or never seeded -- has no history to read and comes back empty
// rather than as an error, which is the same answer every other optional
// reading here gives for a precondition that is simply missing.
//
// The limit is applied to the changes kept, after bookkeeping is dropped
// (isBookkeepingKey): the reader pages back through `git log` until it has
// limit of them, or the window or the history runs out.
func (r *Repo) SettingsHistory(ctx context.Context, since time.Time, limit int) (SettingsHistory, error) {
	if limit <= 0 {
		limit = DefaultSettingsChanges
	}
	headOut, err := r.git(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		// A repository with no HEAD -- nothing committed yet -- is not a
		// failure to report.
		return SettingsHistory{}, nil
	}
	// Every page is read from the same head, so a commit the export loop
	// makes while this is paging cannot shift --skip by one and read a
	// commit twice.
	head := strings.TrimSpace(headOut)
	var out SettingsHistory
	var oldestSeen time.Time
	for scanned := 0; ; {
		args := []string{
			"log",
			"--max-count=" + strconv.Itoa(settingsLogPage),
			"--skip=" + strconv.Itoa(scanned),
			// No rename detection and no context: the only thing wanted out
			// of the diff is which keyed lines moved, and -U0 is what makes
			// the output a list of those lines rather than a copy of the
			// file around each one.
			"--unified=0",
			"--no-renames",
			"--format=" + settingsLogFormat,
		}
		if !since.IsZero() {
			args = append(args, "--since="+since.UTC().Format(time.RFC3339))
		}
		args = append(args, head, "--")
		args = append(args, SettingsPaths()...)
		text, err := r.git(ctx, args...)
		if err != nil {
			return SettingsHistory{}, fmt.Errorf("staterepo: reading settings history: %w", err)
		}
		records := parseSettingsRecords(text)
		for _, rec := range records {
			if !rec.change.At.IsZero() && (oldestSeen.IsZero() || rec.change.At.Before(oldestSeen)) {
				oldestSeen = rec.change.At
			}
			change, ok := r.settingsChangeOf(ctx, rec)
			if !ok {
				continue
			}
			out.Changes = append(out.Changes, change)
			if len(out.Changes) >= limit {
				out.Reaches = change.At
				return out, nil
			}
		}
		scanned += len(records)
		if len(records) < settingsLogPage {
			break
		}
		if scanned >= maxSettingsCommitsScanned {
			out.Reaches = oldestSeen
			return out, nil
		}
	}
	// History ran out before the limit did: everything since the window's
	// start was read, or everything the repository has if it is younger.
	out.Reaches = r.firstCommitAt(ctx)
	if !since.IsZero() && (out.Reaches.IsZero() || since.After(out.Reaches)) {
		out.Reaches = since
	}
	if out.Reaches.IsZero() {
		out.Reaches = oldestSeen
	}
	return out, nil
}

// firstCommitAt is when this repository's oldest root commit was made, or
// zero if git cannot say.
func (r *Repo) firstCommitAt(ctx context.Context) time.Time {
	text, err := r.git(ctx, "log", "--max-parents=0", "--format=%cI", "HEAD")
	if err != nil {
		return time.Time{}
	}
	var oldest time.Time
	for _, line := range strings.Split(text, "\n") {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(line))
		if err != nil {
			continue
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	return oldest
}

// SettingsPaths is the allowlist SettingsChanges reads, repo-relative: the
// dump file of every table Apply imports, and the two directories a
// declaration lives in. Exported because it is also the answer to "which
// files in here are settings", which a reader of this repository may
// reasonably want without running a log.
//
// Deduplicated, because several tables share one file: a template and its
// fields, grants and read scope are all one settings/templates.yml
// (DumpPath), and naming a path six times in a pathspec only makes the
// command harder to read in an error message.
func SettingsPaths() []string {
	paths := make([]string, 0, len(SettingsTables)+2)
	seen := map[string]bool{}
	add := func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}
	for _, table := range SettingsTables {
		add(DumpPath(table))
	}
	add(SettingsDir)
	add(WorkflowsDir)
	return paths
}

// The record and field separators the log format uses: a NUL between
// commits and a unit separator between a commit's own fields, neither of
// which can appear in a hash, a date or a commit subject, and so neither
// of which can be confused for content the way a newline could.
//
// They are written as git's own %x escapes in the format string rather
// than as the bytes themselves: an argv entry cannot carry a NUL at all,
// so the separator has to be something git emits rather than something
// passed to it.
const (
	settingsLogRecord = "\x00"
	settingsLogField  = "\x1f"
	settingsLogFormat = "%x00%H%x1f%aI%x1f%s"
)

// settingsRecord is one commit as `git log` printed it, before anything is
// decided about whether it is a settings change.
type settingsRecord struct {
	// ok is false for a record the reader could not make sense of; it is
	// still a record, so that paging counts it as a commit read.
	ok     bool
	change SettingsChange
	files  []settingsFileDiff
}

// settingsFileDiff is one file's lines in a -U0 diff, each with the line
// number it has on its own side.
type settingsFileDiff struct {
	path  string
	lines []settingsDiffLine
}

type settingsDiffLine struct {
	removed bool
	line    int
	text    string
}

// parseSettingsRecords reads what settingsLogFormat produced: one record
// per commit, each a header line followed by the -U0 diff of the paths the
// pathspec kept.
func parseSettingsRecords(out string) []settingsRecord {
	var records []settingsRecord
	for _, record := range strings.Split(out, settingsLogRecord) {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		header, body, _ := strings.Cut(record, "\n")
		parts := strings.Split(header, settingsLogField)
		if len(parts) != 3 {
			records = append(records, settingsRecord{})
			continue
		}
		at, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			records = append(records, settingsRecord{})
			continue
		}
		records = append(records, settingsRecord{
			ok:     true,
			change: SettingsChange{Commit: parts[0], At: at, Subject: parts[2]},
			files:  splitSettingsDiff(body),
		})
	}
	return records
}

// settingsChangeOf decides what one commit changed, and whether it is a
// settings change at all.
//
// A commit whose diff yields no readable field still counts as a change,
// with its paths and its subject -- "something in settings/schedule.yml
// moved at 04:42" is the fact a tuning agent needs even when the shape of
// the file defeats the line reader. A commit whose every moved field is
// bookkeeping (isBookkeepingKey) is not one, and ok is false.
//
// A YAML file whose diff moved anything but bookkeeping is read at the
// commit and at its parent, so that a line inside a block scalar -- a
// schedule's `body: |` prose -- is reported as the key that holds it
// rather than read as a key of its own. A -U0 diff of a body edit carries
// only the prose lines, and nothing in them says they are prose.
func (r *Repo) settingsChangeOf(ctx context.Context, rec settingsRecord) (SettingsChange, bool) {
	if !rec.ok {
		return SettingsChange{}, false
	}
	blocks := func(f settingsFileDiff) (old, new map[int]string) {
		if !needsBlockScan(f) {
			return nil, nil
		}
		return blockScalarLines(r.blob(ctx, rec.change.Commit+"^:"+f.path)),
			blockScalarLines(r.blob(ctx, rec.change.Commit+":"+f.path))
	}
	change := rec.change
	var bookkeepingOnly bool
	change.Paths, change.Fields, change.Truncated, bookkeepingOnly = settingsFields(rec.files, blocks)
	if bookkeepingOnly {
		return SettingsChange{}, false
	}
	return change, true
}

// blob is one file's content at a revision, or empty if it has none there
// -- a file the commit created has nothing at its parent, and a root
// commit has no parent at all.
func (r *Repo) blob(ctx context.Context, spec string) string {
	out, err := r.git(ctx, "cat-file", "blob", spec)
	if err != nil {
		return ""
	}
	return out
}

// splitSettingsDiff reads one commit's -U0 diff into its files and their
// changed lines, numbered as hunk headers number them.
func splitSettingsDiff(body string) []settingsFileDiff {
	var files []settingsFileDiff
	var current *settingsFileDiff
	inHeader := false
	oldPath := ""
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHeader, current, oldPath = true, nil, ""
		case inHeader && strings.HasPrefix(line, "--- "):
			oldPath = strings.TrimPrefix(strings.TrimPrefix(line, "--- "), "a/")
		case inHeader && strings.HasPrefix(line, "+++ "):
			// The path is the +++ side's, or for a deletion -- whose +++
			// side is /dev/null -- the --- side's.
			path := strings.TrimPrefix(line, "+++ ")
			if path == "/dev/null" {
				path = oldPath
			} else {
				path = strings.TrimPrefix(path, "b/")
			}
			if path == "" || path == "/dev/null" {
				continue
			}
			files = append(files, settingsFileDiff{path: path})
			current = &files[len(files)-1]
		case strings.HasPrefix(line, "@@"):
			inHeader = false
			oldLine, newLine = hunkStarts(line)
		case inHeader || current == nil:
		case strings.HasPrefix(line, "-"):
			current.lines = append(current.lines, settingsDiffLine{removed: true, line: oldLine, text: line[1:]})
			oldLine++
		case strings.HasPrefix(line, "+"):
			current.lines = append(current.lines, settingsDiffLine{line: newLine, text: line[1:]})
			newLine++
		}
	}
	return files
}

// hunkStarts reads `@@ -a,b +c,d @@` into a and c.
func hunkStarts(header string) (oldStart, newStart int) {
	for _, field := range strings.Fields(header) {
		if len(field) < 2 || (field[0] != '-' && field[0] != '+') {
			continue
		}
		num, _, _ := strings.Cut(field[1:], ",")
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		if field[0] == '-' {
			oldStart = n
		} else {
			newStart = n
		}
	}
	return oldStart, newStart
}

// settingsFields turns one commit's files into the fields that moved.
//
// Removed and added lines are gathered per file and then paired by key,
// which is what turns two diff lines into one "max_ci_jobs went from 10
// to 7" -- and what makes a key that appears only on one side (a column
// added, a whole row written or deleted) a field that changed with no
// values to show.
//
// blocks, when not nil, names the lines of a file that sit inside a block
// scalar on each side, and the key that holds them; such a line is read as
// part of that key's value. bookkeepingOnly is true when the commit moved
// fields and every one of them was bookkeeping.
func settingsFields(files []settingsFileDiff, blocks func(settingsFileDiff) (old, new map[int]string)) (paths []string, fields []SettingsField, truncated, bookkeepingOnly bool) {
	type fileChanges struct {
		removed map[string]string
		added   map[string]string
		order   []string
	}
	byPath := map[string]*fileChanges{}
	dropped := false
	for _, file := range files {
		f := byPath[file.path]
		if f == nil {
			f = &fileChanges{removed: map[string]string{}, added: map[string]string{}}
			byPath[file.path] = f
			paths = append(paths, file.path)
		}
		var oldBlocks, newBlocks map[int]string
		if blocks != nil {
			oldBlocks, newBlocks = blocks(file)
		}
		for _, l := range file.lines {
			side, sideBlocks := f.added, newBlocks
			if l.removed {
				side, sideBlocks = f.removed, oldBlocks
			}
			key, inBlock := sideBlocks[l.line]
			value := ""
			if inBlock {
				// Marked so that no line of prose can read as a dial, and
				// joined so that the two sides of an edited body differ.
				value = side[key] + "\n|" + l.text
			} else {
				var ok bool
				key, value, ok = settingsLine(l.text)
				if !ok {
					continue
				}
			}
			if isBookkeepingKey(key) {
				dropped = true
				continue
			}
			if _, seen := f.removed[key]; !seen {
				if _, seen := f.added[key]; !seen {
					f.order = append(f.order, key)
				}
			}
			side[key] = value
		}
	}
	for _, path := range paths {
		f := byPath[path]
		for _, key := range f.order {
			if len(fields) >= MaxChangedFields {
				return paths, fields, true, false
			}
			from, to := f.removed[key], f.added[key]
			if from == to {
				// The same value on both sides of a diff is a line that
				// moved rather than a setting that changed -- a row
				// reordered, a field inserted above it -- and reporting it
				// as a change would be a false positive in the one place
				// a tuning agent is meant to trust.
				continue
			}
			field := SettingsField{Path: path, Name: key}
			if isDial(from) && isDial(to) {
				field.From, field.To = from, to
			}
			fields = append(fields, field)
		}
	}
	return paths, fields, false, dropped && len(fields) == 0
}

// needsBlockScan is whether a file's diff is worth reading the file for:
// it is YAML, and something in it moved besides bookkeeping. The daemon's
// own schedule bookkeeping is the bulk of this history and costs no
// reads.
func needsBlockScan(f settingsFileDiff) bool {
	if !strings.HasSuffix(f.path, ".yml") && !strings.HasSuffix(f.path, ".yaml") {
		return false
	}
	for _, l := range f.lines {
		key, _, ok := settingsLine(l.text)
		if !ok || !isBookkeepingKey(key) {
			return true
		}
	}
	return false
}

// blockScalarHeader is a YAML key whose value is a block scalar:
// `body: |`, `  - prompt: >-`, with an optional comment after it.
var blockScalarHeader = regexp.MustCompile(`^( *)(- +)?([A-Za-z_][A-Za-z0-9_.-]*) *: *[|>][-+0-9]* *(#.*)?$`)

// blockScalarLines maps each line (1-based) of a YAML document that is a
// block scalar's continuation to the key that holds it. A block scalar
// runs on while lines are blank or indented deeper than its key.
func blockScalarLines(content string) map[int]string {
	if content == "" {
		return nil
	}
	out := map[int]string{}
	inBlock, indent, key := false, 0, ""
	for i, line := range strings.Split(content, "\n") {
		if inBlock {
			if strings.TrimSpace(line) == "" || len(line)-len(strings.TrimLeft(line, " ")) > indent {
				out[i+1] = key
				continue
			}
			inBlock = false
		}
		if m := blockScalarHeader.FindStringSubmatch(line); m != nil {
			inBlock, indent, key = true, len(m[1])+len(m[2]), m[3]
		}
	}
	return out
}

// bookkeepingKeys is every key in a settings file that grain writes rather
// than an operator chooses: declschema's Kept keys and the columns behind
// them (a schedule's next-run-at and last-run-at, which the daemon
// rewrites every time it fires), and grain_config's agent_backends, which
// model discovery re-exports every half hour or so. A change to one of
// them is runtime state moving, not a parameter, and on a busy deployment
// it is nearly every commit.
//
// grain_config.console_sessions is deliberately not here: only schema
// migrations write it, and otherwise it is an operator's choice.
var bookkeepingKeys = func() map[string]bool {
	keys := map[string]bool{"agent_backends": true}
	for _, schema := range declschema.All() {
		for _, field := range schema.Fields {
			kept := false
			for _, key := range field.Keys {
				if key.Why == declschema.Kept {
					keys[key.Name], kept = true, true
				}
			}
			if kept {
				for _, column := range field.Columns {
					keys[column] = true
				}
			}
		}
	}
	return keys
}()

// isBookkeepingKey is whether key is runtime state rather than a setting
// (bookkeepingKeys).
func isBookkeepingKey(key string) bool { return bookkeepingKeys[key] }

// settingsKey is the shape of a key this reads: an identifier. Anything
// else before a colon -- "(`api", "have)", "`state=current`" -- is prose
// that happens to contain one.
var settingsKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// settingsLine reads one line of a dump or a declaration as a key and a
// value: `  "max_ci_jobs": 7,` from a JSON table, `  max_ci_jobs: 7` from
// a YAML declaration. Anything else -- a brace, a list item, a wrapped
// string, a key that is not identifier-shaped -- is not a keyed line and
// is not one this reads.
func settingsLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	if line == "" {
		return "", "", false
	}
	var rest string
	if strings.HasPrefix(line, `"`) {
		var found bool
		key, rest, found = strings.Cut(line[1:], `"`)
		rest = strings.TrimSpace(rest)
		if !found || !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		rest = strings.TrimPrefix(rest, ":")
	} else {
		var found bool
		key, rest, found = strings.Cut(line, ":")
		if !found {
			return "", "", false
		}
	}
	if !settingsKey.MatchString(key) {
		return "", "", false
	}
	return key, strings.TrimSuffix(strings.TrimSpace(rest), ","), true
}

// isDial reports whether a value is one this may quote: a number, a
// boolean, or the absence of one. SQLite stores a boolean as 0 or 1, so
// the numeric case covers most of them, and a YAML declaration writes
// true and false.
//
// An empty string is a dial for this purpose so that a field appearing or
// disappearing on one side of a diff can still show the side it has --
// `"" -> 7` reads as "it was not set and is now 7", which is true and is
// what an operator wants.
func isDial(value string) bool {
	switch value {
	case "", "true", "false", "null":
		return true
	}
	_, err := strconv.ParseFloat(value, 64)
	return err == nil
}
