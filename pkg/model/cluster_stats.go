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

package model

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
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
	// Directions is the same window partitioned by root direction --
	// the root task a person, schedule or issue started, with every
	// follow-up task proposed down its work tree (WorkOrigin,
	// LinkProposedBy) rolled up into the root direction that produced
	// it. Most merged pull requests first.
	Directions []DirectionWork
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

// DirectionWork is one root direction's share of a window: what a
// single root task and the follow-up tasks its work tree spawned
// (WorkOrigin, LinkProposedBy) produced inside [Since, Until).
type DirectionWork struct {
	TaskID               string
	Title                string
	Repo                 *RepoRef
	Origin               WorkOrigin
	Author               Principal
	PullRequestsMerged   int
	PullRequestsMeasured int
	LinesAdded           int
	LinesDeleted         int
	TasksCompleted       int
	TasksFiled           int
	// TasksSpawned is how many distinct follow-up tasks (tasks in this
	// direction's tree other than the root task itself) were filed or
	// active inside the window.
	TasksSpawned  int
	Runs          int
	RunsSucceeded int
	AgentTime     time.Duration
}

type taskWindowWork struct {
	repo                 *RepoRef
	pullRequestsMerged   int
	pullRequestsMeasured int
	linesAdded           int
	linesDeleted         int
	tasksCompleted       int
	tasksFiled           int
	runs                 int
	runsSucceeded        int
	agentTime            time.Duration
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
	byTask := map[string]*taskWindowWork{}
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
	taskWork := func(id string, owner, name sql.NullString) *taskWindowWork {
		if tw, ok := byTask[id]; ok {
			return tw
		}
		tw := &taskWindowWork{}
		if owner.Valid && name.Valid && owner.String != "" && name.String != "" {
			r := RepoRef{Owner: owner.String, Name: name.String}
			tw.repo = &r
		}
		byTask[id] = tw
		return tw
	}
	args := []any{since.UTC(), until.UTC()}

	// Merged pull requests, and tasks completed, both off the
	// observation row the merge queue and the finisher write. Two
	// queries rather than one over a CASE, because the two columns are
	// independently NULL and a single pass would have to carry both
	// conditions anyway.
	counts := []struct {
		column  string
		total   *int
		add     func(*RepoWork)
		addTask func(*taskWindowWork, int)
	}{
		{
			"`o`.`pr_merged_at`",
			&out.PullRequestsMerged,
			func(w *RepoWork) { w.PullRequestsMerged++ },
			func(tw *taskWindowWork, n int) { tw.pullRequestsMerged += n },
		},
		{
			"`o`.`completed_at`",
			&out.TasksCompleted,
			func(w *RepoWork) { w.TasksCompleted++ },
			func(tw *taskWindowWork, n int) { tw.tasksCompleted += n },
		},
	}
	for _, c := range counts {
		query := "SELECT `t`.`id`,`t`.`target_owner`,`t`.`target_name`,COUNT(*) FROM `task_observation` `o` " +
			"JOIN `task` `t` ON `t`.`id` = `o`.`task_id` " +
			"WHERE " + c.column + " >= ? AND " + c.column + " < ? " +
			"GROUP BY `t`.`id`,`t`.`target_owner`,`t`.`target_name`"
		if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
			var id string
			var owner, name sql.NullString
			var n int
			if err := rows.Scan(&id, &owner, &name, &n); err != nil {
				return err
			}
			*c.total += n
			c.addTask(taskWork(id, owner, name), n)
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
		"SELECT `t`.`id`,`t`.`target_owner`,`t`.`target_name`,COUNT(`o`.`pr_additions`),"+
			"COALESCE(SUM(`o`.`pr_additions`),0),COALESCE(SUM(`o`.`pr_deletions`),0) "+
			"FROM `task_observation` `o` JOIN `task` `t` ON `t`.`id` = `o`.`task_id` "+
			"WHERE `o`.`pr_merged_at` >= ? AND `o`.`pr_merged_at` < ? "+
			"GROUP BY `t`.`id`,`t`.`target_owner`,`t`.`target_name`",
		args,
		func(rows *sql.Rows) error {
			var id string
			var owner, name sql.NullString
			var measured, added, deleted int
			if err := rows.Scan(&id, &owner, &name, &measured, &added, &deleted); err != nil {
				return err
			}
			out.PullRequestsMeasured += measured
			out.LinesAdded += added
			out.LinesDeleted += deleted
			tw := taskWork(id, owner, name)
			tw.pullRequestsMeasured += measured
			tw.linesAdded += added
			tw.linesDeleted += deleted
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
		"SELECT `id`,`target_owner`,`target_name`,COUNT(*) FROM `task` "+
			"WHERE `created_at` >= ? AND `created_at` < ? GROUP BY `id`,`target_owner`,`target_name`",
		args,
		func(rows *sql.Rows) error {
			var id string
			var owner, name sql.NullString
			var n int
			if err := rows.Scan(&id, &owner, &name, &n); err != nil {
				return err
			}
			out.TasksFiled += n
			taskWork(id, owner, name).tasksFiled += n
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
		"SELECT `t`.`id`,`t`.`target_owner`,`t`.`target_name`,`r`.`outcome`,"+
			"`r`.`started_at`,`r`.`finished_at` FROM `task_run` `r` "+
			"JOIN `task` `t` ON `t`.`id` = `r`.`task_id` "+
			"WHERE `r`.`started_at` >= ? AND `r`.`started_at` < ?",
		args,
		func(rows *sql.Rows) error {
			var id string
			var owner, name, outcome sql.NullString
			var started time.Time
			var finished sql.NullTime
			if err := rows.Scan(&id, &owner, &name, &outcome, &started, &finished); err != nil {
				return err
			}
			out.Runs++
			tw := taskWork(id, owner, name)
			tw.runs++
			if SucceededOutcome(outcome.String) {
				out.RunsSucceeded++
				tw.runsSucceeded++
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
				tw.agentTime += d
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

	directions, err := s.rollupDirections(ctx, byTask)
	if err != nil {
		return ClusterStats{}, fmt.Errorf("rolling up directions: %w", err)
	}
	out.Directions = directions
	return out, nil
}

// rollupDirections groups the window's per-task work by the root task
// that started each work tree (WorkOrigin, LinkProposedBy), loading the
// root task's title, repository, origin and author once per tree.
func (s *Store) rollupDirections(ctx context.Context, byTask map[string]*taskWindowWork) ([]DirectionWork, error) {
	if len(byTask) == 0 {
		return []DirectionWork{}, nil
	}
	taskIDs := make([]string, 0, len(byTask))
	for taskID := range byTask {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)

	rootCache, err := s.resolveDirectionRootTaskIDs(ctx, taskIDs)
	if err != nil {
		return nil, err
	}

	byDir := map[string]*DirectionWork{}
	for _, taskID := range taskIDs {
		tw := byTask[taskID]
		rootID := rootCache[taskID]
		if rootID == "" {
			rootID = taskID
		}
		d, ok := byDir[rootID]
		if !ok {
			d = &DirectionWork{TaskID: rootID}
			byDir[rootID] = d
		}
		if d.Repo == nil && tw.repo != nil {
			r := *tw.repo
			d.Repo = &r
		}
		d.PullRequestsMerged += tw.pullRequestsMerged
		d.PullRequestsMeasured += tw.pullRequestsMeasured
		d.LinesAdded += tw.linesAdded
		d.LinesDeleted += tw.linesDeleted
		d.TasksCompleted += tw.tasksCompleted
		d.TasksFiled += tw.tasksFiled
		if taskID != rootID {
			d.TasksSpawned++
		}
		d.Runs += tw.runs
		d.RunsSucceeded += tw.runsSucceeded
		d.AgentTime += tw.agentTime
	}

	rootIDs := make([]string, 0, len(byDir))
	for rootID := range byDir {
		rootIDs = append(rootIDs, rootID)
	}
	sort.Strings(rootIDs)
	metaByID, err := s.loadDirectionRootMeta(ctx, rootIDs)
	if err != nil {
		return nil, err
	}

	out := make([]DirectionWork, 0, len(byDir))
	for _, d := range byDir {
		if meta, ok := metaByID[d.TaskID]; ok {
			d.Title = meta.title
			if meta.repo != nil {
				r := *meta.repo
				d.Repo = &r
			}
			d.Origin = WorkOrigin(meta.originRef)
			d.Author = Principal{Kind: PrincipalKind(meta.actorKind), ID: meta.actorID}
		}
		if d.Origin == "" && d.TasksSpawned > 0 {
			d.Origin = TaskOrigin(d.TaskID)
		}
		out = append(out, *d)
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.PullRequestsMerged != b.PullRequestsMerged {
			return a.PullRequestsMerged > b.PullRequestsMerged
		}
		if a.TasksCompleted != b.TasksCompleted {
			return a.TasksCompleted > b.TasksCompleted
		}
		if a.TasksSpawned != b.TasksSpawned {
			return a.TasksSpawned > b.TasksSpawned
		}
		if a.TasksFiled != b.TasksFiled {
			return a.TasksFiled > b.TasksFiled
		}
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		return a.TaskID < b.TaskID
	})
	return out, nil
}

const clusterStatsSQLBatchSize = 200

type directionLinkNode struct {
	origin string
	parent string
	exists bool
}

type directionRootMeta struct {
	title     string
	repo      *RepoRef
	originRef string
	actorKind string
	actorID   string
}

func sqlInPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func (s *Store) resolveDirectionRootTaskIDs(ctx context.Context, taskIDs []string) (map[string]string, error) {
	nodes := map[string]directionLinkNode{}
	frontier := append([]string(nil), taskIDs...)
	for len(frontier) > 0 {
		deduped := make([]string, 0, len(frontier))
		queued := make(map[string]bool, len(frontier))
		for _, id := range frontier {
			if id == "" || queued[id] {
				continue
			}
			if _, ok := nodes[id]; ok {
				continue
			}
			queued[id] = true
			deduped = append(deduped, id)
		}
		frontier = nil
		for start := 0; start < len(deduped); start += clusterStatsSQLBatchSize {
			end := min(start+clusterStatsSQLBatchSize, len(deduped))
			chunk := deduped[start:end]
			args := make([]any, 0, len(chunk)+1)
			args = append(args, string(LinkProposedBy))
			for _, id := range chunk {
				nodes[id] = directionLinkNode{exists: false}
				args = append(args, id)
			}
			query := "SELECT `t`.`id`, `t`.`origin_ref`, " +
				"COALESCE((SELECT MIN(`target`) FROM `task_link` WHERE `task_id` = `t`.`id` AND `kind` = ?), '') " +
				"FROM `task` `t` WHERE `t`.`id` IN (" + sqlInPlaceholders(len(chunk)) + ")"
			if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
				var id, origin, parent string
				if err := rows.Scan(&id, &origin, &parent); err != nil {
					return err
				}
				nodes[id] = directionLinkNode{origin: origin, parent: parent, exists: true}
				next := parent
				if next == "" || next == id {
					next = WorkOrigin(origin).Task()
				}
				if next != "" && next != id {
					if _, loaded := nodes[next]; !loaded {
						frontier = append(frontier, next)
					}
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	rootCache := make(map[string]string, len(taskIDs))
	for _, id := range taskIDs {
		resolveDirectionRootFromNodes(id, nodes, rootCache)
	}
	return rootCache, nil
}

func resolveDirectionRootFromNodes(id string, nodes map[string]directionLinkNode, cache map[string]string) string {
	if root, ok := cache[id]; ok {
		return root
	}
	seen := map[string]bool{}
	var path []string
	cur := id
	for {
		if root, ok := cache[cur]; ok {
			cur = root
			break
		}
		if seen[cur] {
			break
		}
		seen[cur] = true
		path = append(path, cur)
		node, ok := nodes[cur]
		if !ok || !node.exists {
			break
		}
		o := WorkOrigin(node.origin)
		switch {
		case node.parent != "" && node.parent != cur:
			cur = node.parent
		case o.Task() != "" && o.Task() != cur:
			cur = o.Task()
		default:
			for _, p := range path {
				cache[p] = cur
			}
			return cur
		}
	}
	for _, p := range path {
		cache[p] = cur
	}
	return cur
}

func (s *Store) loadDirectionRootMeta(ctx context.Context, rootIDs []string) (map[string]directionRootMeta, error) {
	out := make(map[string]directionRootMeta, len(rootIDs))
	for start := 0; start < len(rootIDs); start += clusterStatsSQLBatchSize {
		end := min(start+clusterStatsSQLBatchSize, len(rootIDs))
		chunk := rootIDs[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		query := "SELECT `id`,`title`,`target_owner`,`target_name`,`origin_ref`,`origin_actor_kind`,`origin_actor_id` " +
			"FROM `task` WHERE `id` IN (" + sqlInPlaceholders(len(chunk)) + ")"
		if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
			var id, title, originRef, actorKind, actorID string
			var owner, name sql.NullString
			if err := rows.Scan(&id, &title, &owner, &name, &originRef, &actorKind, &actorID); err != nil {
				return err
			}
			meta := directionRootMeta{
				title:     title,
				originRef: originRef,
				actorKind: actorKind,
				actorID:   actorID,
			}
			if owner.Valid && name.Valid && owner.String != "" && name.String != "" {
				r := RepoRef{Owner: owner.String, Name: name.String}
				meta.repo = &r
			}
			out[id] = meta
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}
