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
// One `git log` per call, bounded by the window asked for and by a commit
// count, over a repository that is a few tens of megabytes of text. It is
// read on demand like every other measurement here rather than sampled,
// which is why there is no retention question to answer: the retention is
// git's, and it is the whole history.
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
	"strconv"
	"strings"
	"time"
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

// DefaultSettingsChanges is how many commits SettingsChanges reads when
// the caller asks for no particular number. Two hundred is more settings
// changes than a deployment makes in a quarter, and a `git log` that
// returns in milliseconds.
const DefaultSettingsChanges = 200

// SettingsChanges reads the settings changes in this repository's history
// since a moment, newest first and at most limit of them.
//
// A zero since means the whole history; a limit of zero or less means
// DefaultSettingsChanges. A repository with no commits yet -- seeded this
// minute, or never seeded -- has no history to read and comes back empty
// rather than as an error, which is the same answer every other optional
// reading here gives for a precondition that is simply missing.
func (r *Repo) SettingsChanges(ctx context.Context, since time.Time, limit int) ([]SettingsChange, error) {
	if limit <= 0 {
		limit = DefaultSettingsChanges
	}
	args := []string{
		"log",
		"--max-count=" + strconv.Itoa(limit),
		// No rename detection and no context: the only thing wanted out of
		// the diff is which keyed lines moved, and -U0 is what makes the
		// output a list of those lines rather than a copy of the file
		// around each one.
		"--unified=0",
		"--no-renames",
		"--format=" + settingsLogFormat,
	}
	if !since.IsZero() {
		args = append(args, "--since="+since.UTC().Format(time.RFC3339))
	}
	args = append(args, "--")
	args = append(args, SettingsPaths()...)
	out, err := r.git(ctx, args...)
	if err != nil {
		// A repository with no HEAD -- nothing committed yet -- is not a
		// failure to report. Anything else is.
		if empty, emptyErr := r.isEmpty(ctx); emptyErr == nil && empty {
			return nil, nil
		}
		return nil, fmt.Errorf("staterepo: reading settings history: %w", err)
	}
	return parseSettingsLog(out), nil
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

// parseSettingsLog reads what that format produced: one record per
// commit, each a header line followed by the -U0 diff of the paths the
// pathspec kept.
//
// A record it cannot make sense of is skipped rather than guessed at. A
// commit whose diff yields no readable field still counts as a change,
// with its paths and its subject -- "something in settings/schedule.yml
// moved at 04:42" is the fact a tuning agent needs even when the shape of
// the file defeats the line reader.
func parseSettingsLog(out string) []SettingsChange {
	var changes []SettingsChange
	for _, record := range strings.Split(out, settingsLogRecord) {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		header, body, _ := strings.Cut(record, "\n")
		parts := strings.Split(header, settingsLogField)
		if len(parts) != 3 {
			continue
		}
		at, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue
		}
		change := SettingsChange{Commit: parts[0], At: at, Subject: parts[2]}
		change.Paths, change.Fields, change.Truncated = parseSettingsDiff(body)
		changes = append(changes, change)
	}
	return changes
}

// parseSettingsDiff reads one commit's -U0 diff into the files it touched
// and the keyed lines that moved inside them.
//
// Removed and added lines are gathered per file and then paired by key,
// which is what turns two diff lines into one "max_ci_jobs went from 10
// to 7" -- and what makes a key that appears only on one side (a column
// added, a whole row written or deleted) a field that changed with no
// values to show.
func parseSettingsDiff(body string) (paths []string, fields []SettingsField, truncated bool) {
	type fileChanges struct {
		removed map[string]string
		added   map[string]string
		order   []string
	}
	files := map[string]*fileChanges{}
	current := ""
	note := func(key, value string, removed bool) {
		f := files[current]
		if f == nil {
			return
		}
		if _, seen := f.removed[key]; !seen {
			if _, seen := f.added[key]; !seen {
				f.order = append(f.order, key)
			}
		}
		if removed {
			f.removed[key] = value
			return
		}
		f.added[key] = value
	}
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			current = strings.TrimPrefix(line, "+++ b/")
			if files[current] == nil {
				files[current] = &fileChanges{removed: map[string]string{}, added: map[string]string{}}
				paths = append(paths, current)
			}
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// A deletion's "+++ /dev/null", and the "--- a/..." half of
			// every header: the path is taken from the +++ side alone, so
			// a deleted settings file is reported under the path the diff
			// still names above it.
		case strings.HasPrefix(line, "-"):
			if key, value, ok := settingsLine(line[1:]); ok {
				note(key, value, true)
			}
		case strings.HasPrefix(line, "+"):
			if key, value, ok := settingsLine(line[1:]); ok {
				note(key, value, false)
			}
		}
	}
	for _, path := range paths {
		f := files[path]
		for _, key := range f.order {
			if len(fields) >= MaxChangedFields {
				return paths, fields, true
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
	return paths, fields, false
}

// settingsLine reads one line of a dump or a declaration as a key and a
// value: `  "max_ci_jobs": 7,` from a JSON table, `  max_ci_jobs: 7` from
// a YAML declaration. Anything else -- a brace, a list item, a wrapped
// string -- is not a keyed line and is not one this reads.
func settingsLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	if line == "" {
		return "", "", false
	}
	quoted := strings.HasPrefix(line, `"`)
	if quoted {
		line = line[1:]
		key, rest, found := strings.Cut(line, `"`)
		if !found || !strings.HasPrefix(strings.TrimSpace(rest), ":") {
			return "", "", false
		}
		return key, strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":")), ","), true
	}
	key, rest, found := strings.Cut(line, ":")
	if !found || key == "" || strings.ContainsAny(key, " \t\"{}[]") {
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
