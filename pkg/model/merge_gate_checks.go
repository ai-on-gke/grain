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
	// Unsettled marks a summary of finished checks only
	// (RecordedHeadChecks) that cannot yet stand for the head's verdict:
	// every check grain has a record of passed, but others may still be
	// running, or the head may have moved past the commit they ran at.
	// The CI gate reads such a summary as running, never as passed.
	Unsettled bool
	// UnsettledWhy says which, for the gate's detail.
	UnsettledWhy string
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

// RecordedChecksQuiet is how long the checks at a commit must have gone
// without another one finishing before RecordedHeadChecks takes the ones
// it has a record of as the whole run: longer than any one job here
// takes, so a job still running has had its chance to report.
const RecordedChecksQuiet = time.Hour

// RecordedHeadChecks is HeadChecksFrom over what grain recorded of a
// branch's checks (GitHubCIJob), which holds a check only once it has
// finished: a check still running is in no row, so "every recorded check
// passed" is not yet "CI passed". Such a summary is marked Unsettled
// unless the checks at its commit are complete -- every check the
// branch's previous commit ran has finished at it too -- or have gone
// RecordedChecksQuiet without another one finishing. One at a commit
// other than head (the head grain last classified, when it knows one)
// is unsettled as well: the head has moved on, or grain has not seen it
// move. head is ignored once checks are recorded at a commit that first
// showed up after it did: that classification is stale, and the newer
// commit is the head. A failing check stands either way.
func RecordedHeadChecks(jobs []GitHubCIJob, head string, now time.Time) *HeadChecks {
	if head != "" && newerCommitThan(jobs, head) {
		// The classification is stale: checks have since been recorded
		// at a commit that first showed up after head's did, so that
		// commit is the newer head.
		head = ""
	}
	movedOn := head != "" && !slices.ContainsFunc(jobs, func(j GitHubCIJob) bool { return strings.EqualFold(j.Commit, head) })
	if movedOn {
		head = ""
	}
	out := HeadChecksFrom(jobs, head)
	if len(out.Failing) > 0 || len(out.Pending) > 0 || out.Passed == 0 {
		return out
	}
	if movedOn {
		out.Unsettled = true
		out.UnsettledWhy = "the pull request's head has moved past it"
		return out
	}
	at := map[string]bool{}
	var latest time.Time
	prev, prevAt := "", time.Time{}
	for _, j := range jobs {
		if strings.EqualFold(j.Commit, out.Commit) {
			at[cmp.Or(j.JobKey(), fmt.Sprint(j.CheckRunID))] = true
			if t := gitHubJobAt(j); t.After(latest) {
				latest = t
			}
		} else if j.Commit != "" && gitHubJobAt(j).After(prevAt) {
			prev, prevAt = j.Commit, gitHubJobAt(j)
		}
	}
	if !latest.IsZero() && now.Sub(latest) >= RecordedChecksQuiet {
		return out
	}
	if prev != "" {
		complete := true
		for _, j := range jobs {
			if strings.EqualFold(j.Commit, prev) && !at[cmp.Or(j.JobKey(), fmt.Sprint(j.CheckRunID))] {
				complete = false
				break
			}
		}
		if complete {
			return out
		}
	}
	out.Unsettled = true
	out.UnsettledWhy = "others may still be running"
	return out
}

// newerCommitThan reports whether jobs hold checks at a commit other than
// head that grain first heard of after it first heard of head's -- a push
// after head. False when nothing is recorded at head.
func newerCommitThan(jobs []GitHubCIJob, head string) bool {
	first := map[string]time.Time{}
	for _, j := range jobs {
		if j.Commit == "" {
			continue
		}
		c := strings.ToLower(j.Commit)
		if t, ok := first[c]; !ok || gitHubJobSeen(j).Before(t) {
			first[c] = gitHubJobSeen(j)
		}
	}
	h, ok := first[strings.ToLower(head)]
	if !ok {
		return false
	}
	for c, t := range first {
		if c != strings.ToLower(head) && t.After(h) {
			return true
		}
	}
	return false
}

// gitHubJobSeen is when j's run first showed: its start, or failing that
// the time gitHubJobAt gives.
func gitHubJobSeen(j GitHubCIJob) time.Time {
	if j.StartedAt != nil {
		return *j.StartedAt
	}
	return gitHubJobAt(j)
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
