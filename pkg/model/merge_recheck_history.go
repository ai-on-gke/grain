package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// What the merge queue's overlap rule said about every merge
// (grain/task-985, docs/merge-queue-behind.md "The overlap re-check").
//
// The rule decides whether a change that is green on its own head has to
// be tested again on top of the branch it is merging into before it
// lands: only when what it changes and what has landed since the tree it
// was tested on overlap -- the same file, the same Go package, or a
// global build input. It is rolled out log-only first
// (RepoConfig.MergeRecheck, MergeRecheckObserve): grain decides as it
// always has, and each merge leaves one of these rows saying what the
// rule would have done. Those rows are the evidence an operator switches
// a repository to MergeRecheckEnforce on, and the trigger rate in hot
// packages is the number they asked for before doing so.
//
// Off SettingsTables for merge_batch_history's reason: a decision that
// has been made is a measurement, not a setting. Bounded like the batch
// history beside it, but with a larger bound because there is one row
// per merge rather than one per batch: a deployment landing three
// hundred changes a week keeps about a fortnight.

// MergeRecheckHistoryKept is how many merges keep their record.
const MergeRecheckHistoryKept = 1000

// MergeRecheckMode is one repository's choice of how the overlap rule is
// applied (RepoConfig.MergeRecheck).
type MergeRecheckMode string

const (
	// MergeRecheckOff turns the overlap rule off: a change green on its own
	// head merges as it stands and nothing is recorded.
	MergeRecheckOff MergeRecheckMode = "off"
	// MergeRecheckObserve behaves as MergeRecheckOff and records, for every
	// merge, what the rule would have said.
	MergeRecheckObserve MergeRecheckMode = "observe"
	// MergeRecheckEnforce applies the rule: a change whose diff overlaps
	// what has landed since its tested tree is tested again on top of the
	// branch (a single re-check on the merge queue's own ref) before it
	// lands, and one that does not overlap merges on its own green. It is
	// the default once merge batching is removed (grain/task-1023): a
	// repository with no setting of its own reads as this.
	MergeRecheckEnforce MergeRecheckMode = "enforce"
)

// MergeRecheckModes is every mode, in the order a person would list them.
var MergeRecheckModes = []MergeRecheckMode{MergeRecheckOff, MergeRecheckObserve, MergeRecheckEnforce}

// ParseMergeRecheckMode reads a mode as a person or a settings file
// spells it. "" is the default (enforce), returned as "" so a stored row
// keeps saying "nobody chose" rather than having the default written
// into it.
func ParseMergeRecheckMode(s string) (MergeRecheckMode, error) {
	m := MergeRecheckMode(strings.ToLower(strings.TrimSpace(s)))
	if m == "" {
		return "", nil
	}
	for _, known := range MergeRecheckModes {
		if m == known {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown merge re-check mode %q (want off, observe or enforce)", s)
}

// MergeRecheckModeOf is the mode a repository's configuration says, with
// the default filled in: nil configuration and an empty or unreadable
// setting are both MergeRecheckEnforce.
func MergeRecheckModeOf(c *RepoConfig) MergeRecheckMode {
	if c == nil {
		return MergeRecheckEnforce
	}
	m, err := ParseMergeRecheckMode(string(c.MergeRecheck))
	if err != nil || m == "" {
		return MergeRecheckEnforce
	}
	return m
}

// The clauses of the rule, as they are recorded in
// MergeRecheckRecord.Clauses. More than one can fire for the same merge,
// and all that did are recorded, so the count for one clause is not
// hidden behind another that fired first.
const (
	// MergeRecheckClauseSameFile: a file the change touches was also
	// touched by what landed since its tested tree.
	MergeRecheckClauseSameFile = "same-file"
	// MergeRecheckClauseSamePackage: some Go package directory has a
	// changed .go file on both sides.
	MergeRecheckClauseSamePackage = "same-package"
	// MergeRecheckClauseGlobalInput: what landed touched an input every
	// build reads -- go.mod, a lockfile, the CI workflows, a Makefile.
	MergeRecheckClauseGlobalInput = "global-input"
	// MergeRecheckClauseUnknown: the two sides could not be read in full
	// (an unreadable comparison, GitHub's 300-file cap), so the rule
	// cannot say they do not overlap and errs towards re-checking.
	MergeRecheckClauseUnknown = "unknown"
)

// The ways a branch's own post-merge CI judged the tree a merge produced
// (MergeRecheckRecord.BaseVerdict).
const (
	MergeRecheckBaseGreen = "green"
	MergeRecheckBaseRed   = "red"
)

// MergeRecheckRecord is one merge and what the overlap rule said about
// it.
type MergeRecheckRecord struct {
	ID     int64
	Repo   RepoRef
	Branch string
	Number int
	TaskID string
	// Mode is the repository's mode when the merge happened, so a history
	// spanning a switch from observe to enforce can be read either side
	// of it.
	Mode MergeRecheckMode
	// Head is the commit that merged; Fork is where it last met the
	// branch -- the base the tree grain CI passed actually contains --
	// and Tip is the branch's tip the merge landed on. Fork == Tip is the
	// "nothing landed since" case, which never triggers.
	Head, Fork, Tip string
	// Triggered is the rule's answer: whether this merge would be (in
	// enforce, was) re-checked on top of Tip. Clauses is which of the
	// clauses above fired, comma-separated, empty when none did; Detail
	// names what fired them -- the first overlapping file, package or
	// input -- for a person checking a row by hand.
	Triggered bool
	Clauses   string
	Detail    string
	// Rechecked is whether the change was in fact tested on top of the
	// branch before it merged -- a merge batch, whether formed because
	// changes queued together or, in enforce mode, because this rule
	// asked for one. Caught is whether that re-check failed and named
	// this change: green on its own head, red on top of the branch.
	Rechecked bool
	Caught    bool
	// BaseVerdict is what the branch's own post-merge CI next made of a
	// tree containing this merge: MergeRecheckBaseGreen,
	// MergeRecheckBaseRed, or "" while nothing has judged it. A merge the
	// rule skipped whose base then went red is the rule's miss, or a
	// flake, or somebody else's change in the same run -- an upper bound
	// to read the rows behind, not a verdict on its own.
	BaseVerdict string
	// DecidedAt is when the rule was asked: the moment of the merge, or
	// of the batch failure that caught it.
	DecidedAt time.Time
}

// HasClause reports whether clause is one of those that fired.
func (r MergeRecheckRecord) HasClause(clause string) bool {
	for _, c := range strings.Split(r.Clauses, ",") {
		if c == clause {
			return true
		}
	}
	return false
}

// SamePackage is the Go package directory the same-package clause first
// fired on, read back out of Detail ("same-package pkg/ui"), or "" where
// it did not fire.
func (r MergeRecheckRecord) SamePackage() string {
	for _, part := range strings.Split(r.Detail, "; ") {
		if rest, ok := strings.CutPrefix(part, MergeRecheckClauseSamePackage+" "); ok {
			return rest
		}
	}
	return ""
}

// MergeRecheckStats is the history counted the way the rollout is read.
type MergeRecheckStats struct {
	// Merges is how many records are counted; Triggered and Skipped
	// partition it by the rule's answer.
	Merges    int
	Triggered int
	Skipped   int
	// BaseCurrent is how many of the skipped had nothing landed since
	// their tested tree at all (Fork == Tip).
	BaseCurrent int
	// Clauses counts each clause over the triggered merges. A merge on
	// which two fired counts once under each.
	Clauses map[string]int
	// Rechecked is how many were in fact re-checked; TriggeredAndCaught
	// is how many of the triggered ones had a re-check catch them, and
	// SkippedAndCaught the same for the skipped ones -- a catch the rule
	// would have let through. Both are zero in observe mode unless a
	// batch happened to test them.
	Rechecked          int
	TriggeredAndCaught int
	SkippedAndCaught   int
	// SkippedBaseRed is how many skipped merges were followed by the
	// branch's own CI going red on a tree containing them.
	SkippedBaseRed int
	// FirstAt and LastAt bound the history.
	FirstAt *time.Time
	LastAt  *time.Time
}

// TriggerRate is Triggered over Merges, zero for an empty history.
func (s MergeRecheckStats) TriggerRate() float64 {
	if s.Merges == 0 {
		return 0
	}
	return float64(s.Triggered) / float64(s.Merges)
}

// SummarizeMergeRechecks counts a history of records.
func SummarizeMergeRechecks(records []MergeRecheckRecord) MergeRecheckStats {
	s := MergeRecheckStats{Clauses: map[string]int{}}
	for _, r := range records {
		s.Merges++
		if r.Triggered {
			s.Triggered++
			for _, c := range strings.Split(r.Clauses, ",") {
				if c != "" {
					s.Clauses[c]++
				}
			}
		} else {
			s.Skipped++
			if r.Fork != "" && r.Fork == r.Tip {
				s.BaseCurrent++
			}
		}
		if r.Rechecked {
			s.Rechecked++
		}
		if r.Caught {
			if r.Triggered {
				s.TriggeredAndCaught++
			} else {
				s.SkippedAndCaught++
			}
		}
		if !r.Triggered && r.BaseVerdict == MergeRecheckBaseRed {
			s.SkippedBaseRed++
		}
		at := r.DecidedAt
		if s.FirstAt == nil || at.Before(*s.FirstAt) {
			first := at
			s.FirstAt = &first
		}
		if s.LastAt == nil || at.After(*s.LastAt) {
			last := at
			s.LastAt = &last
		}
	}
	return s
}

// RecordMergeRecheck appends one record and trims the history back to
// MergeRecheckHistoryKept rows in the same write, for RecordMergeBatch's
// reason.
func (s *Store) RecordMergeRecheck(ctx context.Context, r MergeRecheckRecord) error {
	return s.write(ctx, "record merge re-check decision for "+r.Repo.String(),
		func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `merge_recheck_history` (`owner`,`name`,`branch`,`number`,`task_id`,"+
					"`mode`,`head`,`fork`,`tip`,`triggered`,`clauses`,`detail`,`rechecked`,`caught`,"+
					"`base_verdict`,`decided_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
				r.Repo.Owner, r.Repo.Name, r.Branch, r.Number, r.TaskID,
				string(r.Mode), r.Head, r.Fork, r.Tip, r.Triggered, r.Clauses, r.Detail,
				r.Rechecked, r.Caught, r.BaseVerdict, r.DecidedAt.UTC()); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				"DELETE FROM `merge_recheck_history` WHERE `id` NOT IN "+
					"(SELECT `id` FROM `merge_recheck_history` ORDER BY `id` DESC LIMIT ?)",
				MergeRecheckHistoryKept)
			return err
		})
}

// JudgeMergeRechecks records the branch's own post-merge verdict on every
// merge into it that nothing has judged yet and that happened no later
// than judgedFrom -- the moment the run that judged the branch started,
// whose tree contains every one of them.
func (s *Store) JudgeMergeRechecks(ctx context.Context, repo RepoRef, branch string,
	judgedFrom time.Time, verdict string) error {

	return s.write(ctx, "record the base verdict on merges into "+repo.String()+" "+branch,
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				"UPDATE `merge_recheck_history` SET `base_verdict` = ? "+
					"WHERE `owner` = ? AND `name` = ? AND `branch` = ? AND `base_verdict` = '' "+
					"AND `decided_at` <= ?",
				verdict, repo.Owner, repo.Name, branch, judgedFrom.UTC())
			return err
		})
}

// MergeRecheckRecords is the whole kept history, newest first.
func (s *Store) MergeRecheckRecords(ctx context.Context) ([]MergeRecheckRecord, error) {
	var out []MergeRecheckRecord
	err := each(ctx, s.db,
		"SELECT `id`,`owner`,`name`,`branch`,`number`,`task_id`,`mode`,`head`,`fork`,`tip`,"+
			"`triggered`,`clauses`,`detail`,`rechecked`,`caught`,`base_verdict`,`decided_at` "+
			"FROM `merge_recheck_history` ORDER BY `id` DESC", nil,
		func(rows *sql.Rows) error {
			var r MergeRecheckRecord
			var mode string
			var decided time.Time
			if err := rows.Scan(&r.ID, &r.Repo.Owner, &r.Repo.Name, &r.Branch, &r.Number, &r.TaskID,
				&mode, &r.Head, &r.Fork, &r.Tip, &r.Triggered, &r.Clauses, &r.Detail,
				&r.Rechecked, &r.Caught, &r.BaseVerdict, &decided); err != nil {
				return err
			}
			r.Mode = MergeRecheckMode(mode)
			r.DecidedAt = decided.UTC()
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the merge re-check history: %w", err)
	}
	return out, nil
}
