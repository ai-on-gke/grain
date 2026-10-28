package staterepo

// resets.go says what a merged settings change took away.
//
// `grain state check` already warns on the pull request when a settings
// file leaves out a column (check.go's settingsResets), and it says
// everything it can: the column is missing, and applying the file lands
// it on the schema's default. What it cannot say is the half with the
// value in it. The check imports into a throwaway database, so it knows
// the default the column would land on and not the value it would land
// on instead of -- "this would become 'America/Los_Angeles'" rather than
// "you are about to lose 'Europe/London'".
//
// The deployment doing the reset knows both sides. It has the running
// database on one hand and the merged file on the other, and for the few
// milliseconds before ImportTables replaces the row it can read the value
// that is about to go. So it does, and says so: to the journal, which is
// this package's own record and cannot fail, and to whatever else the
// deployment can reach with it (Config.AnnounceResets) -- in cmd/grain,
// the pull request the merge came in on, which is where the person who
// caused the reset is. The journal keeps the fact; that carries it to
// somebody.
//
// A notice and not a repair, and not a refusal. Leaving a column the file
// does not mention alone was considered and rejected where the rest of
// this decision is written down (SettingsTables' own doc comment): it
// would make a restore and a merge read the same file differently, and
// make a settings file a patch rather than the settings. Refusing the
// import outright would be worse still -- the working tree would sit at a
// commit the database has not taken up (ErrNotApplied), so a merge whose
// missing column was perfectly deliberate would stop every later settings
// change from arriving until somebody went to the host. The replacement
// is what this repository says it is; what was missing until now is
// anybody being told what it cost.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Reset is one value a merged settings file did not mention and the
// import therefore replaced with the schema's default.
//
// Exported because the journal is not the only place this is said: it is
// what Config.AnnounceResets hands to whatever else this deployment can
// reach with it, and every field of it is part of the sentence.
//
// Both values are rendered as SQL literals, in the notation the schema
// itself spells a default with ('Europe/London', 0, NULL), so that From
// and To can be read side by side and so that To is the same characters a
// reader finds in the CREATE TABLE.
//
// The tags are for the one place this is written down rather than said:
// the marker a notice nobody could deliver is kept in
// (recordUndeliveredReset), which an operator on the host may well end
// up reading with their own eyes.
type Reset struct {
	// Path is the file in the dump that did not mention the column,
	// relative to the repository root -- the file to add it back to, and
	// the table it holds, which is why the table is not a field of its
	// own.
	Path string `json:"path"`
	// Column is the column whose value went.
	Column string `json:"column"`
	// Row names the row it lived in by its primary key ("id = 1"), since
	// a table like repo_config or template has many and only one of them
	// lost anything.
	Row string `json:"row"`
	// From is what the running database held, and To the default it
	// landed on.
	From string `json:"from"`
	To   string `json:"to"`
}

func (r Reset) String() string {
	return fmt.Sprintf("%s (%s): %s was %s and is now %s", r.Path, r.Row, r.Column, r.From, r.To)
}

// resetsFromApplying reports what importing dir's settings tables into db
// would overwrite: one reset per column a file leaves out whose value in
// db is not already the default that column would land on.
//
// Read before ImportTables and not after, for the obvious reason:
// afterwards the value is gone and nothing can say what it was.
//
// Only where something is actually lost. A column the file omits whose
// database value already *is* the default is not reported, because
// nothing changes and a line about it would be noise on every deployment
// that has never set the setting -- which is most of them, for most
// settings. That is the difference between this and the check: the check
// has to warn blind, because from a throwaway database "the deployment is
// already running the default" and "the deployment is running something
// else" look identical. Here they do not.
//
// Nor is a row the file adds reported. It has no database side to lose,
// and its columns land on defaults the way any newly inserted row's do.
func resetsFromApplying(ctx context.Context, db *sql.DB, dir string) ([]Reset, error) {
	present, err := tableNames(ctx, db)
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	for _, t := range present {
		has[t] = true
	}
	var out []Reset
	for _, table := range SettingsTables {
		// A table this build does not have is one ImportTables skips, so
		// there is nothing for it to overwrite either.
		if !has[table] {
			continue
		}
		rows, present, err := dumpRowsOf(dir, table)
		if err != nil {
			return nil, err
		}
		if !present {
			// A table the dump has no file for is imported as empty
			// (importTable): every row of it goes, which is a deletion
			// rather than a reset and is visible in the diff as one.
			continue
		}
		cols, err := columns(ctx, db, table)
		if err != nil {
			return nil, err
		}
		// A withheld column is kept, not reset (withheld.go).
		resets, err := tableResets(ctx, db, table, withoutWithheld(table, cols), rows)
		if err != nil {
			return nil, err
		}
		out = append(out, resets...)
	}
	return out, nil
}

// tableResets is resetsFromApplying for one table: which of this file's rows
// leave a column out, and what the database holds for it.
//
// The row in the file has to be matched to the row in the database, and
// the primary key is what does it -- the same key the export orders by
// (orderBy), so a file row and a database row that share it are the same
// row in every sense this package has. A table with no primary key at all
// is passed over rather than guessed at: there is no way to say which
// database row a file row was, so there is no way to say what it held.
// None of today's settings tables are in that position, and one that
// arrives there loses a notice rather than gaining a wrong one.
func tableResets(ctx context.Context, db querier, table string, cols []column, rows []map[string]any) ([]Reset, error) {
	var key []column
	for _, c := range cols {
		if c.pk > 0 {
			key = append(key, c)
		}
	}
	if len(key) == 0 {
		return nil, nil
	}
	sort.Slice(key, func(i, j int) bool { return key[i].pk < key[j].pk })
	var out []Reset
	for _, row := range rows {
		var missing []column
		for _, c := range cols {
			if _, ok := row[c.name]; !ok {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			continue
		}
		where, args, desc, ok := rowKey(key, row)
		if !ok {
			// A row that does not carry its own key cannot be matched to
			// anything, and is a row importTable is about to refuse or
			// insert as a new one. Either way there is nothing here to
			// report.
			continue
		}
		resets, err := rowResets(ctx, db, table, missing, where, args, desc)
		if err != nil {
			return nil, err
		}
		out = append(out, resets...)
	}
	return out, nil
}

// rowKey builds the WHERE that finds a file row's counterpart in the
// database, the values to bind to it, and the same key written out for a
// human ("id = 1", "owner = 'grain', name = 'grain'") -- which is what a
// journal line names the row by, since `id` IS 1 is a query and not a
// sentence. Reports false when the file row does not carry every key
// column, or carries one this build cannot decode.
//
// IS NOT DISTINCT FROM rather than = in the query, so that a key column
// holding NULL matches itself. Not SQLite's shorter IS, which Postgres
// only accepts before NULL, TRUE or FALSE -- `"id" IS $2` is a syntax
// error there.
func rowKey(key []column, row map[string]any) (where string, args []any, desc string, ok bool) {
	terms := make([]string, 0, len(key))
	described := make([]string, 0, len(key))
	args = make([]any, 0, len(key))
	for _, c := range key {
		raw, present := row[c.name]
		if !present {
			return "", nil, "", false
		}
		v, err := decodeValue(raw, c)
		if err != nil {
			return "", nil, "", false
		}
		terms = append(terms, quote(c.name)+" IS NOT DISTINCT FROM ?")
		described = append(described, c.name+" = "+literal(v))
		args = append(args, v)
	}
	return strings.Join(terms, " AND "), args, strings.Join(described, ", "), true
}

// rowResets asks the database, in one query, what it holds for each
// column the file left out and whether that is already the default.
//
// The comparison is done by the database rather than in Go, because the
// database is what will apply the default and is therefore the only thing
// whose opinion of "already the default" is the one that matters -- the
// same affinity rules, the same collation, the same reading of the
// literal. It is also why the default is interpolated into the SQL rather
// than bound as a parameter: a DEFAULT is a piece of the schema's own
// text, and a bound '0' is a string where the schema means a number.
// Nothing from the dump, or from anything a pull request can write,
// reaches the statement -- the text comes from PRAGMA table_info on this
// build's own tables, and the key values are bound.
//
// Written in SQL both stores accept. SQLite's own spelling of this --
// quote("col") and ("col" IS <default>) -- is a syntax error on Postgres
// (`syntax error at or near "”"` for a text default), which turned every
// settings import there into "could not work out what this would
// replace". So the held value is read raw and rendered by literal, and
// the comparison is IS NOT DISTINCT FROM in a CASE, which means the same
// thing in both. A default that calls nextval() is not evaluated here:
// reading what a row would lose must not consume a sequence, and such a
// column is a generated key rather than a setting.
func rowResets(ctx context.Context, db querier, table string, missing []column, where string, args []any, row string) ([]Reset, error) {
	sel := make([]string, 0, 2*len(missing))
	for _, c := range missing {
		dflt := defaultLiteral(c)
		cmp := "0"
		if !strings.Contains(strings.ToLower(dflt), "nextval(") {
			cmp = "CASE WHEN " + quote(c.name) + " IS NOT DISTINCT FROM (" + dflt + ") THEN 1 ELSE 0 END"
		}
		sel = append(sel, quote(c.name), cmp)
	}
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + quote(table) + " WHERE " + where
	held := make([]any, len(missing))
	atDefault := make([]int64, len(missing))
	dest := make([]any, 0, 2*len(missing))
	for i := range missing {
		dest = append(dest, &held[i], &atDefault[i])
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading what applying %s would overwrite: %w", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		// No such row in the database: the file is adding it, and an added
		// row loses nothing.
		return nil, rows.Err()
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, fmt.Errorf("staterepo: reading what applying %s would overwrite: %w", table, err)
	}
	var out []Reset
	for i, c := range missing {
		if atDefault[i] != 0 {
			continue
		}
		out = append(out, Reset{
			Path:   filepath.FromSlash(DumpPath(table)),
			Column: c.name,
			Row:    row,
			// literal renders the value as the literal that would produce
			// it, which is the notation defaultLiteral already reports a
			// default in, so the two sides of a reset read alike.
			From: abbreviate(literal(held[i])),
			To:   abbreviate(defaultLiteral(c)),
		})
	}
	return out, rows.Err()
}

// literal renders a bound key value the way SQLite's own quote() renders
// a stored one, so a key printed here and a value printed by rowResets
// are in one notation.
func literal(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case string:
		return "'" + strings.ReplaceAll(t, "'", "''") + "'"
	case []byte:
		return "<blob>"
	default:
		return fmt.Sprint(t)
	}
}

// resetValueLimit is how much of a value a journal line carries. A
// settings column can hold a whole prompt extension, and a line that
// carried the whole of one would bury the news in it.
const resetValueLimit = 120

// abbreviate makes a value fit on one line of a journal: newlines and
// tabs become spaces, because a multi-line value would arrive as several
// lines nothing ties together, and anything past resetValueLimit is cut
// with a note of how long the whole was -- so a reader can tell a value
// that was trimmed for length from one that ends there.
func abbreviate(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) <= resetValueLimit {
		return s
	}
	runes := []rune(s)
	return string(runes[:resetValueLimit]) + "... (" + strconv.Itoa(len(runes)) + " characters)"
}

// applySettings is the one way this package makes a merged settings
// change live: work out what the arriving commit changed against the
// commit this host last agreed with the repository at (merge.go), read
// what importing that is about to overwrite, import it -- keeping every
// row and column the commit did not change at the database's value --
// and say what it took. Any failure to work out the first two refuses the
// import and keeps the database as it is.
//
// Two things are read beforehand and said afterwards, and they are two
// halves of one question. What the arriving file leaves out is here
// (resetsForNotice): a column no row of the dump mentions, which lands on
// the schema's default. What it carries is replaced.go's
// (replacedForNotice): every settings row whose stored value differs from
// the one that arrived, which is how a database that has moved on since
// the last export finds out what the import undid.
//
// Both of the calls that import into a database with something to lose go
// through it -- Apply's tick and the branch of Load that takes up a
// commit that arrived while grain was stopped -- because they are the
// same event seen from either side of a restart, and an operator who
// restarts into a merge is owed the same sentence as one whose deployment
// took it up on a tick.
//
// The restore does not, and deliberately: Load builds a database out of
// the dump when there is no marker or nothing in the store, and there a
// column the dump does not carry lands on its default because the default
// is the right answer and no value is being replaced. Saying "reset" of a
// database that held nothing a moment ago would be false.
//
// head is the commit being taken up, carried no further than the notice:
// nothing about the import turns on it, and the surfaces that are not
// the journal need it to say which change did this -- and, for the one
// that posts to the pull request the merge arrived on, to find that pull
// request at all.
func applySettings(ctx context.Context, r *Repo, db *sql.DB, head, marker string) error {
	dir := r.Dir()
	// What the arriving commit changed, against the commit this host last
	// agreed with the repository at (merge.go). Everything below that is
	// read before the import is read against the merge, so that a row the
	// merge keeps is not reported as replaced.
	base, err := r.mergeBase(ctx, marker, head)
	if err != nil {
		return refuseApply(err)
	}
	baseDir, err := r.checkoutTree(ctx, base)
	if err != nil {
		return refuseApply(err)
	}
	defer os.RemoveAll(baseDir)
	tables := append(append([]string{}, SettingsTables...), memoryTables...)
	plan, err := planMerge(ctx, db, base, baseDir, dir, tables)
	if err != nil {
		return refuseApply(err)
	}
	// Asked before the import and reported after it, so that a failed
	// import -- importInto is one transaction, so a failure changes
	// nothing -- does not leave the journal claiming a loss that was
	// rolled back.
	//
	// A failure to work out any of these refuses the import. They used to
	// be notices only, applied around ("could not work out what ... would
	// replace; applying them anyway"), and on Postgres the reads failed on
	// every import -- so an import that rewound seventy rows went ahead
	// with nothing able to say so beforehand. A deployment that cannot say
	// what an import would do keeps its database until it can.
	resets, err := resetsForNotice(ctx, db, dir)
	if err != nil {
		return refuseApply(fmt.Errorf("staterepo: could not work out what the settings that just arrived would replace: %w", err))
	}
	replaced, err := replacedForNotice(ctx, db, dir)
	if err != nil {
		return refuseApply(fmt.Errorf("staterepo: could not work out which of this deployment's settings rows the settings that just arrived would replace: %w", err))
	}
	edits, err := systemScheduleEditsForNotice(ctx, db, dir)
	if err != nil {
		return refuseApply(fmt.Errorf("staterepo: could not work out whether the settings that just arrived edit one of grain's own schedules: %w", err))
	}
	resets = plan.keptResets(resets)
	replaced = plan.keptReplaced(replaced)

	// If the import would change at least one row, write a snapshot of the
	// settings tables (SettingsTables + memoryTables) before importing.
	if len(replaced) > 0 || len(resets) > 0 {
		snapshotDir, err := takeSettingsSnapshot(ctx, r.DataDir(), db, base, head, replaced)
		if err != nil {
			return refuseApply(fmt.Errorf("staterepo: taking settings snapshot before applying %s: %w", head, err))
		}
		log.Printf("staterepo: wrote settings snapshot to %s before applying %s", snapshotDir, head)
	}

	present, err := tableNames(ctx, db)
	if err != nil {
		return err
	}
	has := map[string]bool{}
	for _, t := range present {
		has[t] = true
	}
	var wanted []string
	for _, t := range tables {
		if has[t] {
			wanted = append(wanted, t)
		}
	}
	if err := importInto(ctx, db, dir, wanted, true, plan); err != nil {
		return err
	}
	plan.report()
	reportReplaced(replaced)
	reportResets(resets)
	announceResets(ctx, r, head, resets)
	// Last, and after the two above it, because it is the narrowest of
	// the three: the ones above say what the import replaced, and this
	// one says which piece of that the deployment is about to put back
	// (systemschedules.go).
	reportSystemScheduleEdits(edits)
	announceSystemScheduleEdits(ctx, r, head, edits)
	return nil
}

// refuseApply logs a refused import loudly and hands the error back for
// the caller to wrap in ErrNotApplied.
func refuseApply(err error) error {
	log.Printf("staterepo: refusing to apply the settings that just arrived, keeping this deployment's "+
		"database as it is: %v", err)
	return fmt.Errorf("staterepo: refusing to apply: %w", err)
}

// announceResets offers the same fact to whatever else this deployment
// can say it with (Config.AnnounceResets), once the journal has it, and
// keeps what did not get out.
//
// Journal first and always, and this second: the line above is written
// by this process, to a file that is already open, and cannot fail. What
// is here needs a credential, a network and a pull request that may not
// exist, and a deployment that has none of them must still end up with
// the fact recorded somewhere.
//
// Nothing at all when nothing was lost, which is the ordinary tick. A
// surface that heard from every apply would have to decide for itself
// what "no resets" means, and every one of them would decide it the same
// way.
//
// A callback that could not deliver leaves a marker behind
// (recordUndeliveredReset), and that is the whole of the third surface:
// the deployment with no remote, no credential or no pull request has
// nowhere to be told except the host it runs on, and a journal is a
// place a fact is kept rather than a place anybody is told. What the
// marker costs is one dot and one dismissable notice on a pane; what its
// absence costs is a value nobody knew had gone.
func announceResets(ctx context.Context, r *Repo, head string, resets []Reset) {
	if len(resets) == 0 || r == nil || r.cfg.AnnounceResets == nil {
		return
	}
	err := r.cfg.AnnounceResets(ctx, head, resets)
	if err == nil {
		return
	}
	// Deliberately not logged, whether or not it wraps
	// ErrNowhereToAnnounce: the callback has already written the line
	// that says why in the words it chose, and this one would be the
	// same sentence a second time. A shutdown is not worth a marker
	// either -- the context that cancelled the announcement is the one
	// cancelling this write, and the merge is taken up again on the next
	// start.
	if ctx.Err() != nil {
		return
	}
	if err := r.recordUndeliveredReset(ctx, head, resets, err); err != nil {
		// The one thing here worth a line of its own: the fact reached
		// neither the surface it was meant for nor the marker that stands
		// in for it, so the journal above is now the only copy and
		// nothing on screen will ever point at it.
		log.Printf("staterepo: could not keep the notice above about what %s replaced for this "+
			"deployment's State pane (%v); the journal line above is the only record of it", head, err)
	}
}

// ErrNowhereToAnnounce is what a Config.AnnounceResets callback wraps to
// say the notice did not get out because this deployment has nowhere to
// say it: a state repository that is local only or is not on a host this
// build has an API for, or a commit pushed straight at the branch rather
// than merged from a pull request.
//
// It is recorded exactly as any other undelivered notice is -- a reader
// who cannot be told on a pull request is precisely the reader the
// marker exists for -- and it is kept apart from a plain error for one
// reason: it is not a failure, and a deployment running the way it was
// set up to run must not be told that something went wrong with it.
// Undelivered.Failed carries the difference through to the pane and to
// `grain state status`, which say "there was nowhere to post this" of
// one and "grain could not post this" of the other.
//
// Wrapped, never returned bare: the sentence a caller writes around it
// is what a reader is actually shown, and "there is nowhere to say it"
// on its own tells nobody which of the reasons above they are looking
// at.
var ErrNowhereToAnnounce = errors.New("there is nowhere to say it")

// undeliveredResetsFile holds the notices this host could not deliver.
//
// Inside the git directory, beside grain-workflow-refused and for the
// same reason: it is a fact about this host, its remote and its
// credential rather than about the repository -- another clone of the
// same state, with a credential that may comment, has nothing
// undelivered -- so it must not be committed and must not travel.
const undeliveredResetsFile = "grain-undelivered-resets"

// undeliveredNoticeLimit is how many notices the marker keeps, and
// undeliveredValueLimit how many values one of them carries.
//
// Both exist because this file is written by a deployment nobody is
// reading and cleared by a person: a settings file branched before an
// upgrade is missing every column that upgrade added, in every row of
// every settings table, so one merge can lose several hundred values --
// and the deployment this is for is the one where nothing ever comes
// along to clear the last one. So it is bounded on both axes, the way
// the pull-request comment is (cmd/grain's resetCommentValues), and what
// is past the bound is counted rather than dropped silently: the pane
// says how many more there were and where the whole of it is written
// down.
//
// Smaller than that comment's bound because the two are read
// differently. A comment is scrolled past once; this is a panel on a
// settings pane that stays until somebody dismisses it, and a panel of
// two hundred database values is one nobody reads at all.
const (
	undeliveredNoticeLimit = 20
	undeliveredValueLimit  = 25
)

// Undelivered is one settings-reset notice that reached nobody: what a
// merge replaced, and why the deployment could not say so where the
// person who merged it would see it.
type Undelivered struct {
	// At is when the merge was taken up, on this host's clock.
	At time.Time `json:"at"`
	// Commit is the merge that did it -- the whole SHA, abbreviated by
	// whatever renders it.
	Commit string `json:"commit"`
	// Reason is the sentence the surface that could not deliver this
	// wrote about why (Config.AnnounceResets' error), shown as it stands.
	Reason string `json:"reason"`
	// Failed distinguishes "grain tried and could not" from "there was
	// nowhere to try" (ErrNowhereToAnnounce). Both are here; only one of
	// them is something being wrong.
	Failed bool `json:"failed,omitempty"`
	// Values is what the merge replaced, cut to undeliveredValueLimit,
	// with MoreValues counting the rest -- which are in the journal, in
	// the line reportResets wrote in the same moment.
	Values     []Reset `json:"values"`
	MoreValues int     `json:"moreValues,omitempty"`
}

// UndeliveredResets is the marker as a reader gets it: the notices kept,
// newest last, and how many older ones fell off the end.
type UndeliveredResets struct {
	Notices []Undelivered `json:"notices"`
	// Dropped counts notices this host discarded to stay inside
	// undeliveredNoticeLimit. Reported rather than swallowed for the
	// reason the cap itself is bounded: it is the number of times grain
	// had something to tell somebody and ran out of room, which is worth
	// one clause on the pane pointing at the journal.
	Dropped int `json:"dropped,omitempty"`
}

// UndeliveredResets reports the notices this host could not deliver, and
// whether there are any -- WorkflowRefusedAt's shape, and for the same
// job: a marker beside the working tree read back out for the State pane
// and for `grain state status`.
//
// An unreadable or unparsable marker reports nothing rather than an
// error. What it would cost to report loudly is a pane that says
// something is wrong with a deployment because a file grain wrote about
// a notice cannot be read; what it costs to pass over is one notice,
// whose values are in the journal either way.
func (r *Repo) UndeliveredResets(ctx context.Context) (UndeliveredResets, bool) {
	out := r.readUndeliveredResets(ctx)
	if len(out.Notices) == 0 {
		return UndeliveredResets{}, false
	}
	return out, true
}

func (r *Repo) readUndeliveredResets(ctx context.Context) UndeliveredResets {
	path, err := r.gitDirFile(ctx, undeliveredResetsFile)
	if err != nil {
		return UndeliveredResets{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return UndeliveredResets{}
	}
	var out UndeliveredResets
	if err := json.Unmarshal(data, &out); err != nil {
		return UndeliveredResets{}
	}
	return out
}

// ClearUndeliveredResets forgets every notice kept here, which is what a
// person dismissing them asks for -- on the State pane, or with `grain
// state resets clear` on the host, since the deployment this marker
// exists for is the one with no browser open on it.
//
// It deletes rather than marking them read, and there is nothing to
// un-dismiss: this is an event and not a condition, so unlike
// Settings.LocalOnlyAcknowledged there is no state left to change one's
// mind about -- and the values themselves are in this deployment's
// journal, which a dismissal does not touch. A later merge that resets
// something and cannot say so puts a fresh notice up.
//
// Unlike clearWorkflowRefused this reports its failure, because a person
// is waiting on the answer: a dismissal that silently did not happen is
// a notice that comes back on the next poll with nothing to say why.
func (r *Repo) ClearUndeliveredResets(ctx context.Context) error {
	path, err := r.gitDirFile(ctx, undeliveredResetsFile)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("staterepo: clearing %s: %w", path, err)
	}
	return nil
}

// recordUndeliveredReset appends one notice to the marker.
//
// Appends, and does not replace: a second merge that resets something
// before anybody has opened the pane would otherwise throw away the
// first notice, which is the one failure this whole surface exists to
// prevent. The cap above is what keeps that bounded, and the count of
// what it dropped is kept beside the notices rather than lost with them.
//
// Whole-file rewrite under a temporary name and a rename, so a reader
// that arrives mid-write sees one version of the file or the other and
// never half of a JSON document. Nothing else writes it: the announce
// happens on the applying goroutine, underneath the same lock a sync and
// an adopt take.
func (r *Repo) recordUndeliveredReset(ctx context.Context, head string, resets []Reset, cause error) error {
	path, err := r.gitDirFile(ctx, undeliveredResetsFile)
	if err != nil {
		return err
	}
	notice := Undelivered{
		At:     r.now().UTC(),
		Commit: head,
		Reason: cause.Error(),
		Failed: !errors.Is(cause, ErrNowhereToAnnounce),
		Values: resets,
	}
	if len(notice.Values) > undeliveredValueLimit {
		notice.MoreValues = len(notice.Values) - undeliveredValueLimit
		notice.Values = notice.Values[:undeliveredValueLimit]
	}
	kept := r.readUndeliveredResets(ctx)
	kept.Notices = append(kept.Notices, notice)
	if over := len(kept.Notices) - undeliveredNoticeLimit; over > 0 {
		kept.Notices = kept.Notices[over:]
		kept.Dropped += over
	}
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}

// resetsForNotice is resetsFromApplying with a cancelled context
// treated as nothing to say. The import below is about to fail on the
// same context, and a line about the notice being unavailable would be
// noise on top of a shutdown.
func resetsForNotice(ctx context.Context, db *sql.DB, dir string) ([]Reset, error) {
	resets, err := resetsFromApplying(ctx, db, dir)
	if err != nil && ctx.Err() != nil {
		return nil, nil
	}
	return resets, err
}

// reportResets writes the journal half of the notice: one line naming
// every value the merge replaced, with what it was and what it is now.
// announceResets, above, is what offers the same values to a surface
// somebody reads without logging into this host.
//
// One line rather than one per value, because these arrive together --
// a file branched before an upgrade is missing every column the upgrade
// added -- and a journal is read by the line.
func reportResets(resets []Reset) {
	if len(resets) == 0 {
		return
	}
	lines := make([]string, len(resets))
	for i, r := range resets {
		lines[i] = r.String()
	}
	log.Printf("staterepo: the settings that just arrived did not mention %d %s this deployment was "+
		"running on, and a settings file replaces whole rows, so %s back to the schema's default: %s. "+
		"If that was not meant, add the column back to the file with the value you want and merge "+
		"that; the next export writes whatever the database holds back out either way.",
		len(resets),
		plural(len(resets), "value", "values"),
		plural(len(resets), "it has gone", "they have gone"),
		strings.Join(lines, "; "))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
