package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// What batching the merge queue has actually cost and saved
// (grain/task-300), which until this nothing recorded at all: a
// MergeBatch row is deleted the moment its batch ends, so the only
// surviving trace of a batch was a line in the daemon log.
//
// The question it exists to answer is the one grain/task-294 left open --
// DefaultMergeBatchSize is three because three was a guess, and "raise it
// if batches keep passing, lower it if they keep bisecting" needs a count
// of batches that passed and batches that bisected. One row per finished
// batch *run* (not per lineage): a batch of three that fails and halves
// writes one row for the three and another for the two, which is what
// makes the suites spent add up, since each of those rows is one suite.
//
// Bounded rather than kept forever, like the CI run history beside it:
// this is a sample to read a rate off, not an audit log, and the rate
// stops being about today's deployment long before MergeBatchHistoryKept
// rows have gone by.

// MergeBatchHistoryKept is how many finished batches keep their record.
// Two hundred is months of history on a deployment merging a few dozen
// changes a day, and a few kilobytes: every column here is a number or a
// short string, and no member list is kept -- who was in a batch is on
// the members' own tasks, where noteBatchMerged writes it.
const MergeBatchHistoryKept = 200

// The four ways a batch ends, which are the four branches of
// orchestrator.advanceMergeBatch's verdict. They are grain's own words
// rather than pkg/ci conclusions, because what a reader wants to count is
// what the *queue* did about the run, not what the run said: a failure
// that halved and a failure that named a culprit are one ci conclusion
// and two very different things to have spent a suite on.
const (
	// MergeBatchMerged: the suite passed and the members landed on it.
	MergeBatchMerged = "merged"
	// MergeBatchSplit: the suite failed with more than one member, so the
	// batch halved from the front and a smaller one took its place. The
	// row for that smaller batch is the next one on this branch.
	MergeBatchSplit = "split"
	// MergeBatchCulprit: the suite failed with a single member left, which
	// is the bisect's last step -- that member broke the tree and was sent
	// back to its own branch.
	MergeBatchCulprit = "culprit"
	// MergeBatchAbandoned: no answer about the tree at all -- an errored
	// or cancelled run, a run the daemon lost, a base branch that moved
	// underneath it, a batch older than maxMergeBatchAge. The members go
	// back to the ordinary queue and the suite is spent for nothing, which
	// is exactly why it is counted.
	MergeBatchAbandoned = "abandoned"
)

// Why an abandoned batch produced no answer (MergeBatchRecord.Reason).
// The detail beside it says the same thing in words, but words cannot be
// counted: a history in which "abandoned: 9" might be nine restarts or
// nine base branches moving by hand is one nobody can act on once the
// daemon log that told them apart has rotated away (grain/task-975).
//
// One word per arm of orchestrator.abandonMergeBatch's callers, grouped
// by what a reader would do about it rather than by where in the code it
// happened.
const (
	// AbandonInterrupted: the daemon running the batch stopped under it
	// -- its run was interrupted or is no longer known, or the batch was
	// part-way through merging when the daemon went.
	AbandonInterrupted = "interrupted"
	// AbandonCancelled: the batch's run ended cancelled.
	AbandonCancelled = "cancelled"
	// AbandonTimedOut: grain CI had not reported within maxMergeBatchAge
	// and the queue gave up on it.
	AbandonTimedOut = "timed_out"
	// AbandonBaseMoved: the base branch moved while the batch ran, so its
	// verdict is about a tree a merge would no longer produce.
	AbandonBaseMoved = "base_moved"
	// AbandonInheritedRed: every job the batch failed is failing on the
	// base branch too, so there was nothing to bisect.
	AbandonInheritedRed = "inherited_red"
	// AbandonNoAnswer: the run ended errored, or any other way that says
	// nothing about the tree.
	AbandonNoAnswer = "no_answer"
	// AbandonMergeStopped: the batch passed and a member could not be
	// merged on its verdict (refused, or the base's tip could not be read
	// after a merge), so the rest went back to the queue.
	AbandonMergeStopped = "merge_stopped"
	// AbandonNotBuilt: the halved batch after a failure could not be
	// built or started.
	AbandonNotBuilt = "not_built"
	// AbandonDisabled: batching was turned off while the batch ran.
	AbandonDisabled = "disabled"
	// AbandonUnrecorded is what a row written before Reason existed
	// counts under, so the counts still add up to Abandoned.
	AbandonUnrecorded = "unrecorded"
)

// MergeBatchRecord is one finished batch, flattened to what a person
// asking "is our batch size right?" has to count.
//
// It deliberately holds no members: the record of which changes were
// tested together lives on each member's own task (orchestrator's
// noteBatchMerged and sendBatchCulpritBack), and a history that copied it
// would grow with the batch size and go stale the moment a task was
// deleted. What is here is the shape of the bet and how it came out.
type MergeBatchRecord struct {
	// ID is the row's own autoincrement, which is also the order the
	// batches finished in -- the rows are appended as batches end.
	ID     int64
	Repo   RepoRef
	Branch string
	// Commit is the throwaway ref's tip that grain CI judged, and RunID
	// that run, so a row in a summary can still be traced to the suite
	// behind it after the MergeBatch row itself has gone.
	Commit string
	RunID  string
	// Size is how many members the batch actually tested -- the bet's
	// size, which is at most Config.MergeBatchSize and is usually smaller,
	// since a batch takes what the queue happens to hold.
	Size int
	// Queued is how many changes were in this repository's merge queue
	// when the batch formed. It is the number that says whether the size
	// *limit* was the binding constraint: a deployment whose batches are
	// all smaller than the limit is not one whose limit is too low, however
	// well its batches do, and raising it would change nothing.
	//
	// An upper bound rather than an exact count of what a bigger batch
	// could have taken: it counts every entry in the queue for the
	// repository, including ones on another base branch and ones no batch
	// would have been allowed to take (orchestrator.batchable). Counted
	// that way because it is free -- the queue is already in memory when a
	// batch forms -- where an exact answer costs a GitHub read per extra
	// entry, every cycle, to answer a question asked once a week.
	Queued int
	// Splits is how many halvings deep this batch was: 0 for one formed
	// fresh from the queue, 1 for the half of a failed batch, and so on.
	// A history whose rows are mostly Splits > 0 is one where batches are
	// routinely failing and bisecting, which is the signal to lower the
	// size.
	Splits int
	// Outcome is one of the four above, and Merged how many members
	// actually landed on this batch's verdict -- not always Size even on a
	// pass, since a member whose branch moved while the batch ran is
	// refused by GitHub's pinned merge and drops back to the queue.
	Outcome string
	Merged  int
	// Reason is why an abandoned batch produced no answer, one of the
	// Abandon* words above, and empty for every other outcome -- and for
	// an abandoned row written before it was recorded.
	Reason string
	// Detail is the verdict in words, as it was on the batch row: the
	// failing jobs, or why the batch was abandoned.
	Detail string
	// PostMerge is whether grain tests this repository's base branch
	// after a merge at all (orchestrator.baseBranchCIWanted). It is what
	// the comparison below hangs on, and it is recorded per batch rather
	// than assumed because it is a setting an operator changes: a
	// repository with the post-merge run turned off spends nothing per
	// merge, so a batch there saves no suites whatever it does -- it buys
	// the combination being tested before it lands, and that alone.
	PostMerge             bool
	StartedAt, FinishedAt time.Time
}

// Suites is what this batch cost in full CI suites, counting the one that
// is easy to forget.
//
// A batch run is one suite against the throwaway ref. The subtlety is the
// *post-merge* suite: grain tests a base branch after its tip moves
// whatever moved it (orchestrator.startPostMergeCI, BaseBranchCI), and a
// batch that lands three changes moves it once where the one-at-a-time
// queue moves it three times. That run therefore belongs on both sides of
// the comparison rather than cancelling out, so it is counted here for a
// batch that merged anything and in SerialSuites for every change that
// would have merged alone.
//
// Both sides are counted in tip moves, which is an over-count of suites
// on a busy branch: see SerialSuites.
func (r MergeBatchRecord) Suites() int {
	if r.Merged > 0 && r.PostMerge {
		return 2
	}
	return 1
}

// SerialSuites is the most the one-at-a-time queue would have spent to
// land the same changes: one post-merge suite each, since each would have
// been merged on its own cycle against its own new tip.
//
// The most, not the count, and the difference is the whole of what
// grain/task-501 found. Only one post-merge run goes at a time per
// branch, so merges arriving faster than a suite finishes are judged
// together by the next run rather than one apiece (BaseBranchCI.Covers):
// a change that would have merged alone into a branch already coalescing
// c merges to the run costs 1/c of a suite, not one. Measured on
// bwsalmon/grain, c is about 2.7 -- enough that a batch of three is a
// quarter of a suite in the red rather than one in the black. Counting
// tip moves is still what this can do honestly from a batch's own row,
// which knows nothing about how busy the branch was, so Saved is an
// upper bound and is reported as one; what would make it a count is a
// history of Covers, which does not exist yet.
//
// Zero for a batch that merged nothing, which is the honest answer and
// the reason a split or an abandoned batch shows up as pure cost: the
// changes it did not land still cost their own suites later, on whichever
// row (or ordinary merge) finally lands them.
func (r MergeBatchRecord) SerialSuites() int {
	if !r.PostMerge {
		return 0
	}
	return r.Merged
}

// MergeBatchStats is a set of counters over a deployment's batch history
// -- the answer to "are our batches paying off?", in the smallest set of
// numbers that can say either yes or no.
//
// Counting rather than statistics, deliberately: no percentiles, no
// rates, no window. A caller that wants "batches per day" has FirstAt and
// LastAt, and a caller that wants a distribution has the rows.
type MergeBatchStats struct {
	// Batches is how many finished batch runs the history holds, and
	// Changes how many changes landed on one of their verdicts.
	Batches int
	Changes int
	// Passed, Split, Culprits and Abandoned partition Batches by outcome.
	Passed    int
	Split     int
	Culprits  int
	Abandoned int
	// AbandonedBy partitions Abandoned by MergeBatchRecord.Reason, with
	// rows that carry none counted under AbandonUnrecorded. Nil when
	// nothing was abandoned.
	AbandonedBy map[string]int
	// FirstTry is batches that were formed fresh from the queue (Splits
	// == 0) and passed: the case the whole bet is made on. Fresh is how
	// many were formed fresh at all, so FirstTry/Fresh is the pass rate a
	// reader is actually after -- a halved batch's pass is not a first
	// try, it is the bisect working.
	FirstTry int
	Fresh    int
	// Biggest and TotalSize describe the bets themselves: TotalSize/Batches
	// is the mean size, and AtLimit counts the batches that took as many
	// members as the size limit allowed *and* had more queued behind them,
	// which is the only evidence that raising the limit would have bought
	// anything.
	Biggest   int
	TotalSize int
	AtLimit   int
	// Suites and SerialSuites are the two sides of the trade, summed over
	// the history: what batching spent, and the most the one-at-a-time
	// queue would have spent to land the same changes. Saved is the
	// difference, and is negative for a deployment whose batches keep
	// failing. An upper bound rather than a count, because the serial
	// side counts tip moves and a busy branch's post-merge runs already
	// share themselves out between merges: see SerialSuites.
	Suites       int
	SerialSuites int
	Saved        int
	// FirstAt and LastAt bound the history, so a reader knows whether
	// these counters are about a week or about an hour.
	FirstAt *time.Time
	LastAt  *time.Time
}

// SummarizeMergeBatches counts a history. limit is the size the queue is
// configured for now (Config.MergeBatchSizeOr), which is what AtLimit is
// measured against -- a batch is only evidence for a bigger limit if it
// was as big as the limit allows and there was more behind it.
func SummarizeMergeBatches(records []MergeBatchRecord, limit int) MergeBatchStats {
	var s MergeBatchStats
	for _, r := range records {
		s.Batches++
		s.Changes += r.Merged
		s.TotalSize += r.Size
		s.Suites += r.Suites()
		s.SerialSuites += r.SerialSuites()
		if r.Size > s.Biggest {
			s.Biggest = r.Size
		}
		if limit > 1 && r.Size >= limit && r.Queued > r.Size {
			s.AtLimit++
		}
		if r.Splits == 0 {
			s.Fresh++
		}
		switch r.Outcome {
		case MergeBatchMerged:
			s.Passed++
			if r.Splits == 0 {
				s.FirstTry++
			}
		case MergeBatchSplit:
			s.Split++
		case MergeBatchCulprit:
			s.Culprits++
		case MergeBatchAbandoned:
			s.Abandoned++
			reason := r.Reason
			if reason == "" {
				reason = AbandonUnrecorded
			}
			if s.AbandonedBy == nil {
				s.AbandonedBy = map[string]int{}
			}
			s.AbandonedBy[reason]++
		}
		at := r.FinishedAt
		if s.FirstAt == nil || at.Before(*s.FirstAt) {
			first := at
			s.FirstAt = &first
		}
		if s.LastAt == nil || at.After(*s.LastAt) {
			last := at
			s.LastAt = &last
		}
	}
	s.Saved = s.SerialSuites - s.Suites
	return s
}

// RecordMergeBatch appends one finished batch to the history and trims it
// back to MergeBatchHistoryKept rows, both in the same write: a history
// that is bounded by a separate sweep is one that is unbounded whenever
// the sweep is not running.
//
// Never called for a batch still in flight -- a row here is a batch that
// has ended, which is what makes the counters countable.
func (s *Store) RecordMergeBatch(ctx context.Context, r MergeBatchRecord) error {
	return s.write(ctx, "record finished merge batch for "+r.Repo.String()+" "+r.Branch,
		func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `merge_batch_history` (`owner`,`name`,`branch`,`commit`,`run_id`,"+
					"`size`,`queued`,`splits`,`merged`,`outcome`,`detail`,`post_merge`,"+
					"`started_at`,`finished_at`,`reason`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
				r.Repo.Owner, r.Repo.Name, r.Branch, r.Commit, r.RunID, r.Size, r.Queued,
				r.Splits, r.Merged, r.Outcome, r.Detail, r.PostMerge,
				r.StartedAt.UTC(), r.FinishedAt.UTC(), r.Reason); err != nil {
				return err
			}
			// Newest kept by id, which is both the insertion order and the
			// order they finished in, so the bound never drops a row newer
			// than one it keeps.
			_, err := tx.ExecContext(ctx,
				"DELETE FROM `merge_batch_history` WHERE `id` NOT IN "+
					"(SELECT `id` FROM `merge_batch_history` ORDER BY `id` DESC LIMIT ?)",
				MergeBatchHistoryKept)
			return err
		})
}

// MergeBatchRecords is the whole kept history, newest first -- at most
// MergeBatchHistoryKept rows, so it is read entire rather than paged.
func (s *Store) MergeBatchRecords(ctx context.Context) ([]MergeBatchRecord, error) {
	var out []MergeBatchRecord
	err := each(ctx, s.db,
		"SELECT `id`,`owner`,`name`,`branch`,`commit`,`run_id`,`size`,`queued`,`splits`,"+
			"`merged`,`outcome`,`detail`,`post_merge`,`started_at`,`finished_at`,`reason` "+
			"FROM `merge_batch_history` ORDER BY `id` DESC", nil,
		func(rows *sql.Rows) error {
			var r MergeBatchRecord
			var started, finished time.Time
			if err := rows.Scan(&r.ID, &r.Repo.Owner, &r.Repo.Name, &r.Branch, &r.Commit,
				&r.RunID, &r.Size, &r.Queued, &r.Splits, &r.Merged, &r.Outcome, &r.Detail,
				&r.PostMerge, &started, &finished, &r.Reason); err != nil {
				return err
			}
			r.StartedAt, r.FinishedAt = started.UTC(), finished.UTC()
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the merge batch history: %w", err)
	}
	return out, nil
}
