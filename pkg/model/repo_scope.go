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
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// ConfigRepo is the stable, controller-local repository every deployment
// exposes through its authenticated git proxy. It is deliberately outside
// Config.TargetRepos: that list controls external repositories, while this
// repository is intrinsic to the deployment and cannot be removed.
func ConfigRepo() RepoRef { return RepoRef{Owner: "grain", Name: "config"} }

// IsConfigRepo reports whether repo is the deployment's configuration
// repository, grain/config, matched as RepoRef.Same matches. It is the
// only one there is: grain/config-<team> repositories went with teams,
// and a name of that shape is an ordinary repository nothing serves.
func IsConfigRepo(repo RepoRef) bool { return repo.Same(ConfigRepo()) }

// RepoAllowed reports whether target is a repository this deployment may
// dispatch work into, judged against Config.TargetRepos: always for the
// controller-local ConfigRepo, always when allowed
// is empty (that field's own "unrestricted" zero value, v1's target_repos
// "leave empty for a single-repo deployment"), otherwise only when target
// is one of allowed.
//
// Each entry is compared as a repo rather than as text (RepoRef.Same),
// so an allowlist naming `bwsalmon/grain` admits a task that says
// `BWSalmon/Grain` -- GitHub resolves the two to one repository, and
// parking a task that names an allowed repo in another case would be
// grain refusing to work in a repo it was told to work in. An entry that
// does not parse names no repo grain could act on and matches nothing,
// which is what it did as text too.
//
// It lives here rather than in pkg/ui, where it started, because two
// packages now ask this question of the same list: ui.CreateTask, which
// parks a task naming a repo outside it, and
// orchestrator.proposedTarget, which decides whether a propose_task call
// may name a repository other than the one its own run worked in. Two
// copies of a boundary rule are two rules.
func RepoAllowed(allowed []string, target RepoRef) bool {
	if IsConfigRepo(target) || len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		trimmed := strings.TrimSpace(a)
		if trimmed == "*" || trimmed == "*/*" {
			return true
		}
		if matched, err := path.Match(strings.ToLower(trimmed), strings.ToLower(target.String())); err == nil && matched {
			return true
		}
		if parsed, err := ParseRepo(trimmed); err == nil {
			if parsed.Same(target) {
				return true
			}
			if strings.EqualFold(parsed.Owner, target.Owner) && (parsed.Name == "*" || parsed.Name == "") {
				return true
			}
		}
	}
	return false
}

// The four things grain says when RepoAllowed above answers no, kept
// together and here rather than in whichever package happens to be
// speaking.
//
// The rule has one home (RepoAllowed), and now so does the vocabulary,
// for the same reason: the moments are answered by different packages --
// pkg/ui refuses a write and parks a filing, orchestrator parks a task
// the list stopped admitting while it sat in the queue -- and a wording
// that drifted apart between them would leave one operator told to widen
// targetRepos under Settings and another told something subtly different
// about the same list. Each one names the list as it currently stands
// (QuotedRepoList), because somebody who has hit this needs to know
// which of "widen the list" and "pick another repo" is in front of them.
//
// They are four wordings rather than one because they are four
// situations, and the difference between them is the whole value: which
// of the operator's own acts is missing, and what has already happened
// to the work.

// QuotedRepoList renders Config.TargetRepos for the messages below. An
// entry is quoted as written rather than parsed and re-spelled: a list
// holding an entry that does not parse is exactly the case where
// somebody needs to see what is actually stored (RepoAllowed matches
// nothing against such an entry), and a typo shown back is the fastest
// way to find it.
//
// An empty list cannot reach here -- RepoAllowed admits every repo when
// there is nothing to check against -- so there is no "unrestricted"
// wording to write.
func QuotedRepoList(repos []string) string {
	quoted := make([]string, 0, len(repos))
	for _, repo := range repos {
		quoted = append(quoted, "`"+repo+"`")
	}
	return strings.Join(quoted, ", ")
}

// OffAllowlistRefusal is what every path that *writes* a repo grain
// would dispatch work into is told when that repo is off the list --
// ui.(*Client).refuseOffAllowlist's message, which is where the
// reasoning for refusing rather than parking lives. Nothing is written
// and the thing being edited is left exactly as it was.
//
// consequence completes "`repo` is not on this deployment's target repo
// list, so ..." -- what that caller was about to do and now will not, in
// the terms of the thing being written.
func OffAllowlistRefusal(target RepoRef, allowed []string, consequence string) string {
	return fmt.Sprintf(
		"%s is not on this deployment's target repo list, so %s: nothing here can clone, push "+
			"to, or open a pull request against a repo the list does not name. The list currently "+
			"holds %s -- an operator widens targetRepos under Settings.",
		target, consequence, QuotedRepoList(allowed))
}

// OffAllowlistParkComment is what a task filed against an off-list repo
// is parked on -- ui.(*Client).parkOffAllowlist's comment, which has why
// a filing parks where every other write refuses.
//
// It asks for two acts in an order, because that is what lifts this
// park: widen the list under Settings, then reply here.
func OffAllowlistParkComment(target RepoRef) string {
	return fmt.Sprintf(
		"`%s` is not on this deployment's target repo list, so nothing here "+
			"can clone, push to, or open a pull request against it. An operator "+
			"widens targetRepos under Settings, then replies here to let this "+
			"task run.", target)
}

// OffAllowlistAgainComment is what a reply that skipped the first of
// those two acts gets -- the comment ui.(*Client).AddComment re-parks
// with. The operator has now done something and it did not work, so this says
// which of the two is still missing rather than repeating the first
// comment.
func OffAllowlistAgainComment(target RepoRef, allowed []string) string {
	return fmt.Sprintf(
		"`%s` is still not on this deployment's target repo list, so this task "+
			"stays parked: replying does not widen the list, and nothing here can "+
			"clone or push to a repo the list does not name. The list currently "+
			"holds %s. An operator adds the repo under Settings, and a reply after "+
			"that lets this task run.", target, QuotedRepoList(allowed))
}

// OffAllowlistAtDispatchComment is the fourth moment, and the one where
// nobody did anything to the task at all: it was filed against a repo
// the list named, sat in the queue, and the list was narrowed underneath
// it (ui.UpdateSettings). orchestrator.reconcileTargetRepos parks it on
// this rather than letting the tick that follows clone and push into a
// repo the deployment has just said it does not work in.
//
// So it says what happened to the list rather than what is wrong with
// the task, and it offers the second way out that the other three do not
// need to: the target itself can be moved, since this task named a repo
// that was allowed when somebody filed it.
func OffAllowlistAtDispatchComment(target RepoRef, allowed []string) string {
	return fmt.Sprintf(
		"`%s` is no longer on this deployment's target repo list, so this task is parked "+
			"rather than dispatched: nothing here can clone, push to, or open a pull request "+
			"against a repo the list does not name. It was on the list when this task was "+
			"filed, and came off it since. The list currently holds %s. An operator puts the "+
			"repo back under Settings, or retargets this task at a repo the list names, and a "+
			"reply after that lets it run.", target, QuotedRepoList(allowed))
}

// StrandedWork is one repository a proposed Config.TargetRepos would
// stop naming, and the work already stored against it that the narrowing
// would strand there -- what Store.StrandedByTargetRepos answers with,
// and what an operator is told before the save that does it
// (ui.Settings.TargetReposStranded).
//
// "Stranded" is exact rather than dramatic: none of this is deleted,
// disabled or closed. A task still in play is parked on its next
// dispatch (orchestrator.reconcileTargetRepos, and
// OffAllowlistAtDispatchComment is what it is parked on), and a schedule
// stops firing for as long as the repo is off the list
// (Store.suppressOffTargetRepos). Putting the repo back under Settings,
// and a reply on each parked task, is what lifts all of it -- which is
// why this is worth showing at the moment of the save, when whoever is
// making it can still decide not to.
//
// The schedules are still worth naming, and are still not disabled, for
// the same reason they were when every one of their firings parked
// instead (grain/task-411, grain/task-430): a schedule disabled by a
// settings save is not re-enabled by putting the repo back, so the repo
// would return with the nightly work silently stopped -- the one failure
// worse than the noise, because nothing would be left saying it had
// happened. Suppressing each firing is exactly what avoids that, since
// it changes nothing an operator has to undo: the first cadence after
// the repo goes back on the list fires by itself. Naming the schedules
// here says which chores stop meanwhile, and pausing one for good is
// still the operator's own act to make.
type StrandedWork struct {
	Repo RepoRef
	// Tasks is the id of every task targeting Repo that is still in play
	// -- Store.UnsettledTasksInRepo, the same question
	// ui.(*Client).RemoveTargetRepo has always asked of a repo somebody
	// is taking off the list.
	Tasks []string
	// Schedules is the id of every enabled schedule whose firings target
	// Repo. Disabled ones are left out because they fire nothing: a
	// schedule that is paused strands nothing, whatever its target says,
	// and listing it would pad the count this exists to make honest.
	//
	// Read off Schedule.Target, which is where a schedule's firings go
	// (Store.fireTaskSchedule) and which ui.(*Client).UpdateSchedule
	// keeps in step with the binding of any template it fires from.
	Schedules []string
	// QualificationPlan is true when Repo has a plan configured of its
	// own (Store.GetQualificationPlan): a standing declaration, like a
	// schedule, whose run fires tasks into Repo whenever a candidate
	// there is qualified -- each of which would park.
	QualificationPlan bool
}

// Empty reports whether this repository would actually strand anything.
// A repository coming off the list with nothing stored against it is not
// news, and is left out of what an operator is shown rather than
// reported as a nil-everywhere row.
func (w StrandedWork) Empty() bool {
	return len(w.Tasks) == 0 && len(w.Schedules) == 0 && !w.QualificationPlan
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// StrandedByTargetRepos reports what saving proposed as this
// deployment's Config.TargetRepos would strand: every repository the
// stored list admits today that proposed does not, with the work already
// stored against it (StrandedWork).
//
// It is the question ui.PlanSettings and ui.UpdateSettings both ask, so
// that narrowing the list is something an operator is told about while
// they are doing it rather than something they discover as tasks going
// quiet. The dispatch side already keeps that narrowing *safe*
// (orchestrator.reconcileTargetRepos, grain/task-404); this is the other
// half, and it is deliberately a report rather than a refusal -- see
// ui.(*Client).RemoveTargetRepo for why the one refusal that used to be
// here became this instead.
//
// Which repositories are even candidates is WorkRepos' own answer, and
// for its reasons: a restricted deployment's candidates are exactly the
// entries of the list being narrowed, and an unrestricted one's are the
// repositories it has actually worked in -- which is the set that a
// first save of a list, the "adding one repo restricts to that one repo"
// case, takes work away from. A repository the stored list already did
// not admit is not a candidate either way, because this save is not what
// stranded it.
//
// Empty proposed (an unrestricted deployment, that field's own zero
// value) admits every repository, so nothing is stranded and nothing is
// read.
func (s *Store) StrandedByTargetRepos(ctx context.Context, proposed []string) ([]StrandedWork, error) {
	if len(proposed) == 0 {
		return nil, nil
	}
	candidates, err := s.WorkRepos(ctx, 0)
	if err != nil {
		return nil, err
	}
	var losing []RepoRef
	for _, repo := range candidates {
		if !RepoAllowed(proposed, repo) {
			losing = append(losing, repo)
		}
	}
	if len(losing) == 0 {
		return nil, nil
	}
	// Read once for all of them rather than once per repository: the
	// schedule table is small enough to list whole (ListSchedules' own
	// reasoning) and a candidate list is as long as the target repo list.
	schedules, err := s.ListSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("looking for schedules firing into a repo coming off the target repo list: %w", err)
	}
	var out []StrandedWork
	for _, repo := range losing {
		work := StrandedWork{Repo: repo}
		if work.Tasks, err = s.UnsettledTasksInRepo(ctx, repo); err != nil {
			return nil, err
		}
		for _, sched := range schedules {
			if sched.Enabled && sched.Target.Same(repo) {
				work.Schedules = append(work.Schedules, sched.ID)
			}
		}
		plan, err := s.GetQualificationPlan(ctx, repo)
		if err != nil {
			return nil, err
		}
		work.QualificationPlan = plan != nil && plan.Configured && len(plan.Items) > 0
		if work.Empty() {
			continue
		}
		out = append(out, work)
	}
	return out, nil
}

// WorksInRepo reports whether repo is one this deployment actually works
// in -- the question a propose_task call naming a repository of its own
// has to pass before grain files the proposal against it
// (orchestrator.proposedTarget).
//
// A deployment that restricts its repos (Config.TargetRepos) has already
// answered it: that list is exactly the set ui.CreateTask will let a
// task target, and a task filed against anything else is parked before
// it is ever dispatched (ui.(*Client).parkOffAllowlist). Filing a
// proposal outside the list would file work nothing can run, so the list
// is the whole answer here rather than one source among several.
//
// An unrestricted deployment -- the "leave empty for a single-repo
// deployment" default, and the shape most deployments are in -- has no
// list to read, so the answer is where it has already worked: a
// repository some task targets, one some task declares as an extra write
// target (WriteTarget), or one carrying a RepoConfig of its own. That is
// the same union the repo dropdowns are built from in the frontend
// (ui/src/state.js's knownRepos), for the same reason -- it is the
// evidence a store holds about which repositories a deployment's work
// belongs in.
//
// Read targets (Task.Reads) are deliberately not evidence. A repository
// grain was given to read is not one it was given to land work in, and
// the schema keeps the two apart on purpose (schema.go's task_read). A
// proposal naming one is still filed, against the proposing task's own
// repo with a note saying what was asked for, which is the answer for
// every repository this cannot vouch for.
func (s *Store) WorksInRepo(ctx context.Context, repo RepoRef) (bool, error) {
	if IsConfigRepo(repo) {
		return true, nil
	}
	// lower() on both sides rather than an exact match, for RepoRef.Same's
	// reason: two spellings of one repository are one repository to
	// GitHub, so a run that wrote `BWSalmon/Grain` named a repo this
	// deployment works in.
	owner, name := strings.ToLower(repo.Owner), strings.ToLower(repo.Name)
	const q = "SELECT 1 FROM `task` WHERE lower(`target_owner`) = ? AND lower(`target_name`) = ? " +
		"UNION ALL " +
		"SELECT 1 FROM `task_write` WHERE lower(`owner`) = ? AND lower(`name`) = ? " +
		"UNION ALL " +
		"SELECT 1 FROM `repo_config` WHERE lower(`owner`) = ? AND lower(`name`) = ? " +
		"LIMIT 1"
	var one int
	err := s.db.QueryRowContext(ctx, q, owner, name, owner, name, owner, name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("looking for work in %s: %w", repo, err)
	}
	return true, nil
}

// WorksInRepoForTeam reports whether work may be filed in repo on behalf of teamID.
// If teamID specifies TargetRepos, only those repos (and the config repo)
// are allowed. Otherwise, it falls back to WorksInRepo.
func (s *Store) WorksInRepoForTeam(ctx context.Context, teamID string, repo RepoRef) (bool, error) {
	if IsConfigRepo(repo) {
		return true, nil
	}
	targets, err := s.TargetRepos(ctx)
	if err == nil && len(targets) > 0 {
		return RepoAllowed(targets, repo), nil
	}
	return s.WorksInRepo(ctx, repo)
}

// WorkRepos is WorksInRepo read forwards: instead of judging one
// repository a run already named, it lists the repositories a run could
// name -- most recently worked in first, capped at limit (limit <= 0 for
// all of them).
//
// It exists because the gate above is silent. A propose_task call may
// name any repository this deployment works in, and nothing tells a run
// which those are: a run that does not know a repository is nameable
// files the work against its own repo instead, and nobody finds out
// until a human retargets the proposal by hand (proposedTarget's own doc
// comment has the case that motivated it -- seven Go changes filed
// against a state repository). pkg/cli/mcpserver.go's -propose-repo
// carries this list into propose_task's own description, which is where
// a run reads the argument.
//
// Both of its sources are WorksInRepo's, deliberately and exactly: a
// list that advertised a repository the gate would then refuse would be
// worse than no list at all. An allowlisted deployment (Config.TargetRepos)
// gets that list, in the order it was written, since that is the whole
// of what the gate consults there. An unrestricted one gets the
// repositories it has already worked in -- a task's target, a task's
// extra write target (WriteTarget), or a repository carrying a
// RepoConfig -- and not the repositories it may merely read (Task.Reads),
// which the gate does not count either.
//
// Most recent first is what makes a cap worth having rather than
// arbitrary. A deployment with forty repositories cannot spend a tool
// description on all of them, and the useful subset of that forty is
// where work is actually going on now: recency is read off the task rows
// that are the evidence in the first place, and a repository known only
// by a RepoConfig of its own (no task, no date) sorts last. A caller
// that passes a cap is asking for the head of that list, and a list that
// stops short is still every repository the caller had room to name.
func (s *Store) WorkRepos(ctx context.Context, limit int) ([]RepoRef, error) {
	// The three sources unioned into (repo, when it was worked in), then
	// folded to one row per repository. Grouped by lower() for
	// RepoRef.Same's reason -- two spellings of one repository are one
	// repository to GitHub, and offering both would spend half a cap on
	// a distinction nobody made on purpose. The bare `owner`/`name`
	// beside MAX() are SQLite's documented "row of the max": whichever
	// spelling the most recent task used, which is the one a run should
	// be reading.
	//
	// A NULL date -- a repo_config with no task behind it, or a task row
	// old enough to have been written without one -- sorts last under
	// DESC, since SQLite orders NULL below every value.
	const q = "SELECT `owner`, `name`, MAX(`at`) AS `recent` FROM (" +
		"SELECT `target_owner` AS `owner`, `target_name` AS `name`, `created_at` AS `at` FROM `task` " +
		"WHERE `target_owner` IS NOT NULL AND `target_owner` != '' " +
		"AND `target_name` IS NOT NULL AND `target_name` != '' " +
		"UNION ALL " +
		"SELECT `w`.`owner`, `w`.`name`, `t`.`created_at` FROM `task_write` `w` " +
		"LEFT JOIN `task` `t` ON `t`.`id` = `w`.`task_id` " +
		"UNION ALL " +
		"SELECT `owner`, `name`, NULL FROM `repo_config`" +
		") GROUP BY lower(`owner`), lower(`name`) " +
		"ORDER BY `recent` DESC, lower(`owner`), lower(`name`) " +
		"LIMIT ?"
	// SQLite reads a negative LIMIT as no limit at all, which is what a
	// caller asking for every repository means.
	bound := limit
	if bound <= 0 {
		bound = -1
	}
	rows, err := s.db.QueryContext(ctx, q, bound)
	if err != nil {
		return nil, fmt.Errorf("listing the repos this deployment works in: %w", err)
	}
	defer rows.Close()
	var repos []RepoRef
	for rows.Next() {
		var owner, name string
		var recent sql.NullString
		if err := rows.Scan(&owner, &name, &recent); err != nil {
			return nil, fmt.Errorf("listing the repos this deployment works in: %w", err)
		}
		if owner == "" || name == "" {
			continue
		}
		repos = append(repos, RepoRef{Owner: owner, Name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the repos this deployment works in: %w", err)
	}
	return repos, nil
}

// WorkReposForTeam lists repositories that work can be filed against for teamID.
// If the team specifies TargetRepos, those are returned (up to limit).
// Otherwise, it falls back to WorkRepos.
func (s *Store) WorkReposForTeam(ctx context.Context, teamID string, limit int) ([]RepoRef, error) {
	targets, err := s.TargetRepos(ctx)
	if err == nil && len(targets) > 0 {
		var repos []RepoRef
		for _, entry := range targets {
			parsed, err := ParseRepo(entry)
			if err != nil {
				continue
			}
			if slices.ContainsFunc(repos, parsed.Same) {
				continue
			}
			repos = append(repos, parsed)
			if limit > 0 && len(repos) == limit {
				break
			}
		}
		return repos, nil
	}
	return s.WorkRepos(ctx, limit)
}
