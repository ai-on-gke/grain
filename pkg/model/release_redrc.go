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
	"strconv"
	"time"
)

// What a red release candidate leaves behind
// (design/playbook/releases.playbook.md §3, §4, §5 and §12;
// implementation gap 8). The reconciler (orchestrator's releaseredrc.go)
// raises the notifications and keeps the hold; the pipeline steward
// triages, files the blockers and links them with RCBlockerMarker.

// ReleaseMaxRCAttempts is scripts/release.conf's RELEASE_MAX_RC_ATTEMPTS:
// how many candidates one version may use before a red one escalates
// (FamilyReleaseEscalated). It is a threshold, not a stop: the train keeps
// cutting rc.(N+1) from main past it (playbook §5).
const ReleaseMaxRCAttempts = 3

// ReleaseHoldCheckName is the check run the reconciler posts on a red
// candidate's commit as soon as qualification concludes failure: in
// progress while pipeline.rc-failed awaits triage (up to
// ReleaseHoldTriageGrace, unless the pipeline steward finishes triaging it
// with no blocker) and while any blocker linked to it is open, and
// completed once triage needs no blocker, the triage window elapses, or
// every blocker has landed. The failed candidate's commit is the floor the
// train cuts rc.(N+1) after, so it is where the train
// (scripts/release-from-trunk.sh) reads the hold.
const ReleaseHoldCheckName = "grain/rc-hold"

// ReleaseHoldTriageGrace is how long grain/rc-hold holds rc.(N+1) while
// pipeline.rc-failed:<rc_tag> awaits triage, before any task titled
// RCBlockerMarker(rc_tag) exists: long enough to cover the release train's
// 20-minute cadence while the pipeline steward or a person files blockers,
// and bounded so an untriaged candidate does not wedge the train forever.
// Overridable with GRAIN_RC_HOLD_GRACE.
const ReleaseHoldTriageGrace = time.Hour

// RCBlockerMarker is what a task's title carries to hold the candidate
// after rcTag until it lands: `[rc-blocker vX.Y.Z-rc.N]`, naming the red
// candidate. The steward files fix tasks and reverts with it.
func RCBlockerMarker(rcTag string) string {
	return "[rc-blocker " + rcTag + "]"
}

// FlakeBudgetRetries and FlakeBudgetWindow are playbook §3's ">= 2 RC
// flake retries in 24 hours": one qualification target retried this
// often inside the window raises FamilyFlakeBudget.
const (
	FlakeBudgetRetries = 2
	FlakeBudgetWindow  = 24 * time.Hour
)

// ReleaseInfraStalledAfter is playbook §4's bounded patience in time: an
// item blocked on infrastructure this long raises
// FamilyReleaseInfraStalled even before it has used its re-runs.
const ReleaseInfraStalledAfter = 2 * time.Hour

var releaseTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// ParseReleaseTag parses name as a promoted release, `vX.Y.Z`, into the
// RCTag ordering (RC 0, so it sorts before every candidate of its own
// version).
func ParseReleaseTag(name string) (RCTag, bool) {
	m := releaseTagPattern.FindStringSubmatch(name)
	if m == nil {
		return RCTag{}, false
	}
	var n [3]int
	for i := range n {
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			return RCTag{}, false
		}
		n[i] = v
	}
	return RCTag{Major: n[0], Minor: n[1], Patch: n[2]}, true
}

// ReleaseFloor is the newest promoted release older than rc's version:
// the commit `<floor>..<rc_sha>` starts from, and so every commit the
// candidate would ship that the last release did not. ok is false for a
// repository with no release before it.
func ReleaseFloor(tags map[string]string, rc RCTag) (name, sha string, ok bool) {
	limit := RCTag{Major: rc.Major, Minor: rc.Minor, Patch: rc.Patch}
	var best RCTag
	for tag, commit := range tags {
		v, isRelease := ParseReleaseTag(tag)
		if !isRelease || commit == "" || !v.Less(limit) {
			continue
		}
		if !ok || best.Less(v) {
			best, name, sha, ok = v, tag, commit, true
		}
	}
	return name, sha, ok
}

// PreviousCandidate is the newest earlier candidate of rc's own version
// in tags -- rc.(N-1), or whichever came before -- whose commit starts
// the range of what changed since the last red one.
func PreviousCandidate(tags map[string]string, rc RCTag) (name, sha string, ok bool) {
	var best RCTag
	for tag, commit := range tags {
		c, isRC := ParseRCTag(tag)
		if !isRC || commit == "" || c.Version() != rc.Version() || !c.Less(rc) {
			continue
		}
		if !ok || best.Less(c) {
			best, name, sha, ok = c, tag, commit, true
		}
	}
	return name, sha, ok
}

// OpenTasksTitled is every task not yet closed whose title contains
// marker, oldest first: the blockers RCBlockerMarker links to a hold.
func (s *Store) OpenTasksTitled(ctx context.Context, marker string) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `t`.`id` FROM `task` AS `t` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `t`.`id` "+
			"WHERE instr(`t`.`title`, ?) > 0 AND `st`.`state` != ? "+
			"ORDER BY `t`.`created_at`, `t`.`id`",
		[]any{marker, string(StateClosed)},
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing the open tasks titled %s: %w", marker, err)
	}
	return out, nil
}

// HasTasksTitled reports whether any task, open or closed, carries marker in
// its title: once at least one RCBlockerMarker task has been filed for a red
// candidate, closing every open one means its blockers have landed rather
// than that triage is still pending.
func (s *Store) HasTasksTitled(ctx context.Context, marker string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task` WHERE instr(`title`, ?) > 0",
		marker,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("counting the tasks titled %s: %w", marker, err)
	}
	return n > 0, nil
}

// unreleasedDropPattern is the trailer scripts/release-from-trunk.sh puts
// on a `-discarded` tag when it drops a candidate without judging it.
var unreleasedDropPattern = regexp.MustCompile(`(?m)^Discard-Reason: (daily-cap|held-over)\r?$`)

// UnreleasedDrop reports whether a `-discarded` tag's message says the
// train dropped the candidate for the daily cap (`Discard-Reason:
// daily-cap`) or as held over from an earlier day (`Discard-Reason:
// held-over`), playbook §7, rather than for failing qualification. Such
// a candidate is not red: it raises no pipeline.rc-failed, holds no next
// cut, and counts toward no escalation.
func UnreleasedDrop(tagMessage string) bool {
	return unreleasedDropPattern.MatchString(tagMessage)
}
