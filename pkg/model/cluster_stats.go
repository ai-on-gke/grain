package model

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// ClusterStats is how much work this deployment did over one window,
// counted from its own record: the numbers the weekly release notes
// lead with (ReleaseNote, release_note.go), and the only part of them
// nobody should have to take an agent's word for.
//
// It exists because the alternative is an agent counting. The run that
// writes the notes holds the self-debug task tools and could page
// through every task the week touched and tally them by hand, and the
// tally would be wrong sooner or later in a way nobody reading prose
// would ever catch -- a page of tasks missed at a limit, a fix task
// double-counted as work. These are one set of SQL aggregates over the
// same rows, computed once, handed to the run as the figures to write
// around (mcp's read_cluster_stats).
//
// Every number here is grain's own record of what it did, which is
// narrower than "what happened in these repositories" and deliberately
// so: a pull request a person opened and merged by hand is not in it,
// because grain never saw it. What the window is, is the half-open
// range [Since, Until) -- a week, when it is a week's notes being
// written (ReleaseNotePeriodRange).
type ClusterStats struct {
	Since time.Time
	Until time.Time

	// PullRequestsMerged is the headline: tasks whose own tracked pull
	// request merged inside the window (Observation.PrMergedAt), which
	// is grain's record of work that actually landed rather than work
	// that was attempted.
	PullRequestsMerged int
	// LinesAdded and LinesDeleted are how much code those merges
	// changed, summed from what GitHub said each pull request's diff was
	// at the moment grain observed it merge (Observation.PrAdditions).
	// The one hero number that is about the size of the week's work
	// rather than the count of it.
	//
	// PullRequestsMeasured is how many of PullRequestsMerged those two
	// totals are actually over, and it is what keeps them honest. Grain
	// only started recording a diff's size in September 2026, so a
	// window containing merges older than that has rows carrying no size
	// at all -- which is "not recorded", not "changed nothing". Those
	// merges contribute nothing to the sums, so a total quoted without
	// this number beside it would be silently too small. Where
	// PullRequestsMeasured is less than PullRequestsMerged the figure is
	// partial and every reader of it is told so (mcp.renderClusterStats,
	// ui's Release notes pane).
	LinesAdded           int
	LinesDeleted         int
	PullRequestsMeasured int
	// TasksCompleted is tasks that finished inside the window
	// (Observation.CompletedAt) -- wider than the line above, since a
	// task can complete with an answer, a comment or a review rather
	// than with a branch.
	TasksCompleted int
	// TasksFiled is tasks created inside the window, whoever filed
	// them: it is the other end of the pipe, and the pair says whether
	// the backlog grew or shrank.
	TasksFiled int
	// Runs is agent runs started inside the window, and RunsSucceeded
	// how many of them ended 'succeeded'. Reported together because a
	// count of runs on its own says nothing: the interesting number is
	// how much of the week's dispatching went somewhere.
	Runs          int
	RunsSucceeded int
	// AgentTime is how long those runs spent running, summed -- the
	// wall-clock hours this deployment's agents actually worked, which
	// is the one figure here that is about effort rather than output.
	// Runs still in flight at the end of the window contribute what
	// they had done by then.
	AgentTime time.Duration
	// Repos is the same counts per repository, most merged pull
	// requests first, so the notes can say which of the cluster's
	// repositories the week actually went into. A task with no target
	// repository at all -- a question, a memory agent run, a report --
	// is counted in the totals above and in no repository, which is
	// what keeps the per-repo rows adding up to something a reader can
	// check.
	Repos []RepoWork
}

// RepoWork is one repository's share of a window.
//
// LinesAdded/LinesDeleted/PullRequestsMeasured mean per repository
// exactly what ClusterStats' own three mean for the whole window,
// partiality included: a repository whose week contains a merge from
// before grain recorded diff sizes has a measured count short of its
// merged count, and its lines are as partial as the total is.
type RepoWork struct {
	Repo                 RepoRef
	PullRequestsMerged   int
	PullRequestsMeasured int
	LinesAdded           int
	LinesDeleted         int
	TasksCompleted       int
	TasksFiled           int
	Runs                 int
}

// ClusterStats counts one window. since is inclusive and until
// exclusive, so consecutive windows neither double-count a moment nor
// drop one.
func (s *Store) ClusterStats(ctx context.Context, since, until time.Time) (ClusterStats, error) {
	if !until.After(since) {
		return ClusterStats{}, fmt.Errorf("a stats window ends after it starts, got %s to %s",
			since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	}
	out := ClusterStats{Since: since.UTC(), Until: until.UTC()}
	byRepo := map[RepoRef]*RepoWork{}
	// A repository is added to the map by whichever count reaches it
	// first: a repo that only had a task filed this week is as much
	// part of the week as one that merged five.
	work := func(owner, name sql.NullString) *RepoWork {
		if !owner.Valid || !name.Valid || owner.String == "" || name.String == "" {
			return nil
		}
		repo := RepoRef{Owner: owner.String, Name: name.String}
		if w, ok := byRepo[repo]; ok {
			return w
		}
		w := &RepoWork{Repo: repo}
		byRepo[repo] = w
		return w
	}
	args := []any{since.UTC(), until.UTC()}

	// Merged pull requests, and tasks completed, both off the
	// observation row the merge queue and the finisher write. Two
	// queries rather than one over a CASE, because the two columns are
	// independently NULL and a single pass would have to carry both
	// conditions anyway.
	counts := []struct {
		column string
		total  *int
		add    func(*RepoWork)
	}{
		{"`o`.`pr_merged_at`", &out.PullRequestsMerged, func(w *RepoWork) { w.PullRequestsMerged++ }},
		{"`o`.`completed_at`", &out.TasksCompleted, func(w *RepoWork) { w.TasksCompleted++ }},
	}
	for _, c := range counts {
		query := "SELECT `t`.`target_owner`,`t`.`target_name`,COUNT(*) FROM `task_observation` `o` " +
			"JOIN `task` `t` ON `t`.`id` = `o`.`task_id` " +
			"WHERE " + c.column + " >= ? AND " + c.column + " < ? " +
			"GROUP BY `t`.`target_owner`,`t`.`target_name`"
		if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
			var owner, name sql.NullString
			var n int
			if err := rows.Scan(&owner, &name, &n); err != nil {
				return err
			}
			*c.total += n
			if w := work(owner, name); w != nil {
				for i := 0; i < n; i++ {
					c.add(w)
				}
			}
			return nil
		}); err != nil {
			return ClusterStats{}, fmt.Errorf("counting this deployment's work: %w", err)
		}
	}

	// How much those merges changed, over the same rows the first count
	// above walked. A third query rather than three more columns on that
	// one: it is the only aggregate here that has to distinguish a NULL
	// from a zero, and COUNT of a nullable column is what does it --
	// SQLite counts the rows where the column is not NULL, which is
	// exactly "merges grain has a size on file for" (LinesAdded's own
	// doc comment on why that number is reported beside the sums rather
	// than folded into them).
	if err := each(ctx, s.db,
		"SELECT `t`.`target_owner`,`t`.`target_name`,COUNT(`o`.`pr_additions`),"+
			"COALESCE(SUM(`o`.`pr_additions`),0),COALESCE(SUM(`o`.`pr_deletions`),0) "+
			"FROM `task_observation` `o` JOIN `task` `t` ON `t`.`id` = `o`.`task_id` "+
			"WHERE `o`.`pr_merged_at` >= ? AND `o`.`pr_merged_at` < ? "+
			"GROUP BY `t`.`target_owner`,`t`.`target_name`",
		args,
		func(rows *sql.Rows) error {
			var owner, name sql.NullString
			var measured, added, deleted int
			if err := rows.Scan(&owner, &name, &measured, &added, &deleted); err != nil {
				return err
			}
			out.PullRequestsMeasured += measured
			out.LinesAdded += added
			out.LinesDeleted += deleted
			if w := work(owner, name); w != nil {
				w.PullRequestsMeasured += measured
				w.LinesAdded += added
				w.LinesDeleted += deleted
			}
			return nil
		}); err != nil {
		return ClusterStats{}, fmt.Errorf("counting the lines those merges changed: %w", err)
	}

	if err := each(ctx, s.db,
		"SELECT `target_owner`,`target_name`,COUNT(*) FROM `task` "+
			"WHERE `created_at` >= ? AND `created_at` < ? GROUP BY `target_owner`,`target_name`",
		args,
		func(rows *sql.Rows) error {
			var owner, name sql.NullString
			var n int
			if err := rows.Scan(&owner, &name, &n); err != nil {
				return err
			}
			out.TasksFiled += n
			if w := work(owner, name); w != nil {
				w.TasksFiled += n
			}
			return nil
		}); err != nil {
		return ClusterStats{}, fmt.Errorf("counting the tasks filed: %w", err)
	}

	// Runs are keyed by their task's repository rather than by the one
	// the run itself recorded (task_run.target_owner/target_name).
	// Those columns are written when a sandbox is acquired
	// (SetRunSandbox), so a run that never got one carries neither, and
	// a report that counted the run in its total and in no repository
	// would be one whose per-repo rows quietly disagreed with the three
	// counts above them -- which are all the task's.
	if err := each(ctx, s.db,
		"SELECT `t`.`target_owner`,`t`.`target_name`,`r`.`outcome`,"+
			"`r`.`started_at`,`r`.`finished_at` FROM `task_run` `r` "+
			"JOIN `task` `t` ON `t`.`id` = `r`.`task_id` "+
			"WHERE `r`.`started_at` >= ? AND `r`.`started_at` < ?",
		args,
		func(rows *sql.Rows) error {
			var owner, name, outcome sql.NullString
			var started time.Time
			var finished sql.NullTime
			if err := rows.Scan(&owner, &name, &outcome, &started, &finished); err != nil {
				return err
			}
			out.Runs++
			if SucceededOutcome(outcome.String) {
				out.RunsSucceeded++
			}
			// A run still going at the end of the window is credited
			// with the part of it that is inside the window, which is
			// the same arithmetic a finished run gets and keeps a
			// Monday-morning report from counting the run that is
			// writing it as having taken no time at all.
			end := until.UTC()
			if finished.Valid && finished.Time.Before(end) {
				end = finished.Time.UTC()
			}
			if d := end.Sub(started.UTC()); d > 0 {
				out.AgentTime += d
			}
			if w := work(owner, name); w != nil {
				w.Runs++
			}
			return nil
		}); err != nil {
		return ClusterStats{}, fmt.Errorf("counting the runs: %w", err)
	}

	out.Repos = make([]RepoWork, 0, len(byRepo))
	for _, w := range byRepo {
		out.Repos = append(out.Repos, *w)
	}
	// Most merged first, and by name within a tie, so two reads of an
	// unchanged store produce the same order and a reader can tell
	// where the week went by looking at the top of the list.
	sort.Slice(out.Repos, func(i, j int) bool {
		a, b := out.Repos[i], out.Repos[j]
		if a.PullRequestsMerged != b.PullRequestsMerged {
			return a.PullRequestsMerged > b.PullRequestsMerged
		}
		if a.TasksCompleted != b.TasksCompleted {
			return a.TasksCompleted > b.TasksCompleted
		}
		return a.Repo.String() < b.Repo.String()
	})
	return out, nil
}
