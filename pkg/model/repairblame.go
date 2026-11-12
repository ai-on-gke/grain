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
	"regexp"
	"slices"
	"strings"
)

// Stage1RepairTagPrefix is the fixed part of the tag every Stage 1 repair
// carries (orchestrator.Stage1RepairTag): "stage1-repair:<owner/name>".
const Stage1RepairTagPrefix = "stage1-repair:"

// RepairKind names which of grain's repairs a task is -- the work filed
// because a branch or a release went red rather than because somebody
// asked for a change -- or "" for a task that is none of them. It is
// what decides whether a merged task's commits are read for the merge
// it blames (LinkBlames).
func RepairKind(task Task) string {
	switch {
	case FixesBaseBranch(task):
		return "base-branch"
	case slices.ContainsFunc(task.Tags, func(t string) bool { return strings.HasPrefix(t, Stage1RepairTagPrefix) }):
		return "stage1"
	case strings.Contains(task.Title, "[rc-blocker "):
		return "rc-blocker"
	}
	return ""
}

var (
	// blamesTrailer is `Blames: <task>` on a repair's commit: the task
	// whose merge it repairs, as "4820", "#4820", "task 4820" or a link
	// to its page.
	blamesTrailer = regexp.MustCompile(`(?im)^Blames:[ \t]*(?:task[ \t]+)?(\S+)[ \t]*$`)
	// fixesCommitTrailer is `Fixes-commit: <sha>`: the commit on the base
	// branch the repair fixes, which grain maps to the merge that brought
	// it.
	fixesCommitTrailer = regexp.MustCompile(`(?im)^Fixes-commit:[ \t]*([0-9a-f]{7,40})[ \t]*$`)
)

// BlameTrailers is what a repair's commit messages say it blames: the
// task IDs its Blames: trailers name and the commits its Fixes-commit:
// trailers name, each once and in the order first found.
func BlameTrailers(messages []string) (tasks, commits []string) {
	for _, msg := range messages {
		for _, m := range blamesTrailer.FindAllStringSubmatch(msg, -1) {
			if id := NormalizeTaskID(m[1]); id != "" && !slices.Contains(tasks, id) {
				tasks = append(tasks, id)
			}
		}
		for _, m := range fixesCommitTrailer.FindAllStringSubmatch(msg, -1) {
			if sha := strings.ToLower(m[1]); !slices.Contains(commits, sha) {
				commits = append(commits, sha)
			}
		}
	}
	return tasks, commits
}

// TaskLinkTargets is the targets of task's links of kind, in order.
func TaskLinkTargets(task Task, kind LinkKind) []string {
	var out []string
	for _, l := range task.Links {
		if l.Kind == kind {
			out = append(out, l.Target)
		}
	}
	return out
}

// RecordBlame links repairID to culpritID as the merge it repaired:
// LinkBlames on the repair and LinkBlamedBy on the culprit, in one write.
// It reports whether anything was added -- false for a pair already
// linked -- and refuses a task blaming itself or a culprit that does not
// exist, since a link to nothing is a failure counted against nobody.
func (s *Store) RecordBlame(ctx context.Context, repairID, culpritID string) (bool, error) {
	if repairID == "" || culpritID == "" {
		return false, fmt.Errorf("recording a blame: both tasks are needed")
	}
	if repairID == culpritID {
		return false, fmt.Errorf("recording a blame: task %s cannot blame itself", repairID)
	}
	added := false
	err := s.write(ctx, "record blame "+repairID+" -> "+culpritID, func(tx *sql.Tx) error {
		added = false
		repair, err := getTask(ctx, tx, repairID)
		if err != nil {
			return err
		}
		if repair == nil {
			return fmt.Errorf("recording a blame: no task %s", repairID)
		}
		culprit, err := getTask(ctx, tx, culpritID)
		if err != nil {
			return err
		}
		if culprit == nil {
			return fmt.Errorf("recording a blame: task %s blames %s, which does not exist", repairID, culpritID)
		}
		if !slices.Contains(TaskLinkTargets(*repair, LinkBlames), culpritID) {
			repair.Links = append(repair.Links, Link{Kind: LinkBlames, Target: culpritID})
			if err := putTask(ctx, tx, *repair); err != nil {
				return err
			}
			added = true
		}
		if !slices.Contains(TaskLinkTargets(*culprit, LinkBlamedBy), repairID) {
			culprit.Links = append(culprit.Links, Link{Kind: LinkBlamedBy, Target: repairID})
			if err := putTask(ctx, tx, *culprit); err != nil {
				return err
			}
			added = true
		}
		return nil
	})
	return added, err
}
