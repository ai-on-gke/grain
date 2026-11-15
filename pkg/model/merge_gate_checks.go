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
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// HeadChecks is what grain knows of the checks GitHub ran at one pull
// request's head: the CI gate's input for a repository GitHub Actions
// gates (ci_mode: github), where grain runs no CI of its own and so
// Observation.CI is never written.
//
// A nil *HeadChecks means nobody looked; a non-nil one with no checks in
// it means grain looked and has no record of any, which the CI gate says
// in so many words rather than claiming checks are running.
type HeadChecks struct {
	// Commit is the head these checks ran at, "" when grain could not
	// tell.
	Commit string
	// Passed counts the checks that passed (success, neutral, skipped).
	Passed int
	// Failing names the checks that failed, were cancelled or need an
	// action, by JobKey.
	Failing []string
	// Pending names the checks that have not concluded.
	Pending []string
}

// Total is how many checks h counts.
func (h *HeadChecks) Total() int {
	if h == nil {
		return 0
	}
	return h.Passed + len(h.Failing) + len(h.Pending)
}

// HeadChecksFrom summarizes GitHub check runs at a pull request's head.
// jobs may span several commits of the branch: those at head are kept
// when head is given (none of them, if nothing is recorded at it yet);
// otherwise those at the commit the most recently recorded job ran at,
// which is the newest head grain has seen checks for. Of a check run more than once at that commit (a
// re-run), the latest attempt counts: the highest run attempt, then the
// newest check run. Never nil.
func HeadChecksFrom(jobs []GitHubCIJob, head string) *HeadChecks {
	commit := head
	if commit != "" && !slices.ContainsFunc(jobs, func(j GitHubCIJob) bool { return strings.EqualFold(j.Commit, head) }) {
		// Nothing recorded at this head yet: what ran at an older one is
		// not its verdict.
		return &HeadChecks{Commit: head}
	}
	if commit == "" {
		var newest *GitHubCIJob
		for i := range jobs {
			j := &jobs[i]
			if j.Commit == "" {
				continue
			}
			if newest == nil || gitHubJobAt(*j).After(gitHubJobAt(*newest)) {
				newest = j
			}
		}
		if newest != nil {
			commit = newest.Commit
		}
	}
	out := &HeadChecks{Commit: commit}
	latest := map[string]GitHubCIJob{}
	var keys []string
	for _, j := range jobs {
		if commit != "" && !strings.EqualFold(j.Commit, commit) {
			continue
		}
		if commit == "" && j.Commit != "" {
			continue
		}
		key := cmp.Or(j.JobKey(), fmt.Sprint(j.CheckRunID))
		prev, seen := latest[key]
		if !seen {
			keys = append(keys, key)
		}
		if !seen || j.RunAttempt > prev.RunAttempt ||
			(j.RunAttempt == prev.RunAttempt && j.CheckRunID > prev.CheckRunID) {
			latest[key] = j
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		switch strings.ToLower(latest[key].Conclusion) {
		case "success", "neutral", "skipped":
			out.Passed++
		case "":
			out.Pending = append(out.Pending, key)
		default:
			out.Failing = append(out.Failing, key)
		}
	}
	return out
}

// gitHubJobAt is when j was last heard of: its completion, or failing
// that when it was last recorded.
func gitHubJobAt(j GitHubCIJob) time.Time {
	if j.CompletedAt != nil {
		return *j.CompletedAt
	}
	if j.UpdatedAt.After(j.RecordedAt) {
		return j.UpdatedAt
	}
	return j.RecordedAt
}
