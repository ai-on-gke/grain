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

package orchestrator

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// recordRepairBlame writes down which merge a repair that has just merged
// repaired (model.LinkBlames / model.LinkBlamedBy), when the repair's own
// evidence names it.
//
// It exists for the change failure rate the weekly code-quality report
// computes: the share of merged changes a later repair blamed. A revert
// says which change it undoes (`grain git revert` links LinkReverts), but
// a repair that fixes forward -- a base-branch repair, a Stage 1 repair,
// an [rc-blocker <tag>] task -- recorded only the red range, so a report
// reading it had to count every repair whose range held more than one
// merge as unattributed rather than guess.
//
// The evidence, in the order it is believed:
//
//   - The repair's own commits. A `Blames: <task>` trailer names the
//     culprit task outright; a `Fixes-commit: <sha>` trailer names the
//     commit on the base branch, and the culprit is the task whose pull
//     request merged it. This is how an agent that bisected the red range
//     reports what it found, and it is the only evidence a Stage 1, a
//     GitHub default-branch (model.GitHubBaseRepairTag) or an rc-blocker
//     repair has. Where any trailer is present, it is the
//     whole answer: a commit it names that no task merged (a push
//     straight to the branch) blames nobody rather than falling back to
//     a guess the repair has just contradicted.
//   - For a base-branch repair with no trailer, the branch's red range
//     (model.BaseBranchCI.LastGreenCommit .. RedStreakSince) when it is a
//     single commit -- the first red tip's first parent is the last green
//     one -- and that commit is a task's merge.
//
// Anything else -- a range holding more than one merge, a range nobody
// recorded, a red commit no task merged -- leaves no link, and the repair
// stays unattributed. Nothing here reaches back over repairs that merged
// before it existed.
func recordRepairBlame(ctx context.Context, store *model.Store, client github.Client, task model.Task,
	ref model.PullRequestRef, base string) error {

	kind := model.RepairKind(task)
	if kind == "" || store == nil || client == nil {
		return nil
	}
	culprits, evidence, err := repairCulprits(ctx, store, client, task, kind, ref, base)
	if err != nil {
		return fmt.Errorf("orchestrator: reading what %s repair %s blames: %w", kind, task.ID, err)
	}
	for _, culprit := range culprits {
		added, err := store.RecordBlame(ctx, task.ID, culprit)
		if err != nil {
			return fmt.Errorf("orchestrator: recording that %s blames %s: %w", task.ID, culprit, err)
		}
		if added {
			log.Printf("orchestrator: %s repair %s blames %s (%s)", kind, task.ID, culprit, evidence)
		}
	}
	return nil
}

// maxBlameCommitLookups is how many of a repair's Fixes-commit: trailers
// are mapped to the merge that brought them: one forge read each.
const maxBlameCommitLookups = 10

// repairCulprits is the tasks a merged repair blames and the evidence in
// a phrase, for the log; see recordRepairBlame.
func repairCulprits(ctx context.Context, store *model.Store, client github.Client, task model.Task, kind string,
	ref model.PullRequestRef, base string) ([]string, string, error) {

	commits, err := client.ListPullRequestCommits(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return nil, "", fmt.Errorf("listing %s's commits: %w", ref, err)
	}
	messages := make([]string, 0, len(commits))
	for _, c := range commits {
		messages = append(messages, c.Message)
	}
	tasks, shas := model.BlameTrailers(messages)
	if len(tasks)+len(shas) > 0 {
		var culprits []string
		for _, id := range tasks {
			if id == task.ID || slices.Contains(culprits, id) {
				continue
			}
			found, err := store.GetTask(ctx, id)
			if err != nil {
				return nil, "", err
			}
			if found == nil {
				log.Printf("orchestrator: %s names task %s in a Blames: trailer, and there is no such task", task.ID, id)
				continue
			}
			// The trailer is agent-written, so it may only name a merge into
			// the repository the repair itself merged into -- the same reach
			// a Fixes-commit: trailer has -- rather than write a blamed-by
			// link on any task in the deployment.
			if found.Target == nil || !found.Target.Same(ref.Repo) {
				log.Printf("orchestrator: %s names task %s in a Blames: trailer, which is not a task of %s", task.ID, id, ref.Repo)
				continue
			}
			culprits = append(culprits, id)
		}
		// Each commit costs a forge read inside the sync cycle, and the
		// trailers are agent-written, so only the first few are looked up.
		if len(shas) > maxBlameCommitLookups {
			log.Printf("orchestrator: %s names %d commits in Fixes-commit: trailers; reading only the first %d",
				task.ID, len(shas), maxBlameCommitLookups)
			shas = shas[:maxBlameCommitLookups]
		}
		for _, sha := range shas {
			id, err := taskMergedAs(ctx, store, client, ref.Repo, sha)
			if err != nil {
				return nil, "", err
			}
			if id == "" || id == task.ID || slices.Contains(culprits, id) {
				continue
			}
			culprits = append(culprits, id)
		}
		return culprits, "named by its commit trailers", nil
	}
	if kind != "base-branch" || base == "" {
		return nil, "", nil
	}
	row, err := store.GetBaseBranchCI(ctx, ref.Repo, base)
	if err != nil || row == nil {
		return nil, "", err
	}
	single, err := singleCommitRedRange(client, ref.Repo, *row)
	if err != nil || !single {
		return nil, "", err
	}
	id, err := taskMergedAs(ctx, store, client, ref.Repo, row.RedStreakSince)
	if err != nil || id == "" || id == task.ID {
		return nil, "", err
	}
	return []string{id}, fmt.Sprintf("the only commit in %s's red range %s..%s", base,
		shortSHA(row.LastGreenCommit), shortSHA(row.RedStreakSince)), nil
}

// singleCommitRedRange reports whether row's red range is one commit: the
// first red tip's first parent is the last green tip. A merge commit
// counts as one -- its own branch's commits arrived together and are one
// task's change.
func singleCommitRedRange(client github.Client, repo model.RepoRef, row model.BaseBranchCI) (bool, error) {
	if row.LastGreenCommit == "" || row.RedStreakSince == "" || row.LastGreenCommit == row.RedStreakSince {
		return false, nil
	}
	commits, err := client.CompareCommits(repo.Owner, repo.Name, row.LastGreenCommit, row.RedStreakSince)
	if err != nil {
		return false, fmt.Errorf("comparing %s..%s on %s: %w",
			shortSHA(row.LastGreenCommit), shortSHA(row.RedStreakSince), repo, err)
	}
	for _, c := range commits {
		if strings.EqualFold(c.SHA, row.RedStreakSince) {
			return len(c.Parents) > 0 && strings.EqualFold(c.Parents[0], row.LastGreenCommit), nil
		}
	}
	return false, nil
}

// taskMergedAs is the task whose pull request merged sha into repo, or ""
// for a commit no task's pull request brought -- a push straight to the
// branch, or a pull request grain did not file.
func taskMergedAs(ctx context.Context, store *model.Store, client github.Client, repo model.RepoRef, sha string) (string, error) {
	pr, err := client.MergedPullRequestForCommit(repo.Owner, repo.Name, sha)
	if err != nil {
		return "", fmt.Errorf("finding the pull request that merged %s on %s: %w", shortSHA(sha), repo, err)
	}
	if pr == nil || pr.Number <= 0 {
		return "", nil
	}
	return store.TaskForPullRequest(ctx, model.PullRequestRef{Repo: repo, Number: pr.Number}.String())
}
