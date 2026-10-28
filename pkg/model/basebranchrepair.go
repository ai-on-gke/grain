package model

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// A task that fixes a red base branch (grain/task-3451).
//
// A repair of a red base branch is a task carrying the branch's repair
// tag: one filed by system:pipeline-steward (or, before it, by grain
// itself -- orchestrator.fileBaseBranchRepair), or a task of a person's
// own -- "De flake tests", "fix main's go vet break" -- marked as fixing
// it.
//
// The mark is not a column. It is the very tag grain's own repairs carry,
// BaseRepairBranchTag(repo, branch) for the task's own base branch, so that
// every check that already asks "is this the branch's repair?" -- the
// exemption from waiting, single flight, the open repair work waits on --
// answers the same way for a task a person marked as for one grain filed.
// What still tells the two apart is one more tag a person's mark adds
// beside it, MarkedBaseRepairTag (GrainBaseRepair is the branch tag
// without it): that is what decides whether a green branch closes the
// task (grain's) or only says so on it (a person's).
//
// The mark follows the task's base: a task whose repo or base is edited
// is re-tagged for the new one (SetFixesBaseBranch).

// baseRepairBranchTagPrefix is BaseRepairBranchTag's fixed part.
const baseRepairBranchTagPrefix = "base-ci-repair:"

// MarkedBaseRepairTag is carried, beside the branch repair tag, by a task
// a person (or an agent's propose_task) marked as fixing its base branch,
// and by none grain filed itself.
const MarkedBaseRepairTag = "fixes-base-branch"

// baseRepairWaitTagPrefix is BaseRepairWaitTag's fixed part.
const baseRepairWaitTagPrefix = "base-ci-wait:"

// BaseRepairBranchTag is the branch-scoped tag every repair of repo's
// branch carries, grain's own and a person's alike.
func BaseRepairBranchTag(repo RepoRef, branch string) string {
	return fmt.Sprintf("%s%s:%s", baseRepairBranchTagPrefix, repo, branch)
}

// GrainBaseRepairCommitTag is the commit-scoped idempotency tag on a
// repair grain filed itself: one per commit that went red.
func GrainBaseRepairCommitTag(repo RepoRef, branch, commit string) string {
	return fmt.Sprintf("base-ci:%s:%s:%s", repo, branch, commit)
}

// BaseRepairWaitTag was carried by a task grain linked to wait on repair
// repairID because its base branch was red. grain no longer links work
// that way (system:pipeline-steward coordinates a red branch); the tag is
// still recognised on tasks that carry it from before.
func BaseRepairWaitTag(repairID string) string { return baseRepairWaitTagPrefix + repairID }

// TaskBaseBranch is the repo and base branch a task's work lands on --
// its target, else its first extra writeable repo -- with "main" for a
// base left unset. The zero RepoRef and "" for a task with neither.
func TaskBaseBranch(task Task) (RepoRef, string) {
	if task.Target != nil && task.Target.Owner != "" {
		base := task.Base
		if base == "" {
			base = "main"
		}
		return *task.Target, base
	}
	for _, w := range task.Writes {
		if w.Repo.Owner != "" {
			base := w.Base
			if base == "" {
				base = "main"
			}
			return w.Repo, base
		}
	}
	return RepoRef{}, ""
}

// FixesBaseBranch reports whether task is marked as a repair of a base
// branch, by grain or by a person.
func FixesBaseBranch(task Task) bool {
	return slices.ContainsFunc(task.Tags, func(t string) bool {
		return strings.HasPrefix(t, baseRepairBranchTagPrefix)
	})
}

// GrainBaseRepair reports whether task is a repair grain filed itself:
// it carries a branch repair tag and not the mark a person's adds.
func GrainBaseRepair(task Task) bool {
	return FixesBaseBranch(task) && !slices.Contains(task.Tags, MarkedBaseRepairTag)
}

// SetFixesBaseBranch marks task as fixing its own base branch, or clears
// the mark, reporting whether it could: a task with no repo has no base
// branch to fix. Marking replaces any mark for another branch, which is
// how an edited base carries the mark with it, and a repair grain filed
// stays grain's own.
func SetFixesBaseBranch(task *Task, on bool) bool {
	grains := GrainBaseRepair(*task)
	tags := slices.DeleteFunc(slices.Clone(task.Tags), func(t string) bool {
		return strings.HasPrefix(t, baseRepairBranchTagPrefix) || t == MarkedBaseRepairTag
	})
	if on {
		repo, branch := TaskBaseBranch(*task)
		if repo.Owner == "" || branch == "" {
			return false
		}
		tags = append(tags, BaseRepairBranchTag(repo, branch))
		if !grains {
			tags = append(tags, MarkedBaseRepairTag)
		}
	}
	task.Tags = tags
	return true
}

// BaseRepairBases is every task carrying a branch repair tag
// (BaseRepairBranchTag), by task id, with that tag -- one read for the
// question dispatch asks of every candidate the sandbox budget refuses:
// is this a base-branch repair, and of which branch (grain/task-488).
// Closed tasks are included; a caller asks about tasks it already knows
// are ready or running.
func (s *Store) BaseRepairBases(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := each(ctx, s.db,
		"SELECT `task_id`, `tag` FROM `task_tag` WHERE `tag` LIKE ?",
		[]any{baseRepairBranchTagPrefix + "%"},
		func(rows *sql.Rows) error {
			var id, tag string
			if err := rows.Scan(&id, &tag); err != nil {
				return err
			}
			out[id] = tag
			return nil
		})
	return out, err
}

// ParseBaseRepairBranchTag is BaseRepairBranchTag read back: the repo
// and branch a repair tag names, and false for any other tag.
func ParseBaseRepairBranchTag(tag string) (RepoRef, string, bool) {
	rest, ok := strings.CutPrefix(tag, baseRepairBranchTagPrefix)
	if !ok {
		return RepoRef{}, "", false
	}
	repo, branch, ok := strings.Cut(rest, ":")
	if !ok || branch == "" {
		return RepoRef{}, "", false
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return RepoRef{}, "", false
	}
	return RepoRef{Owner: owner, Name: name}, branch, true
}
