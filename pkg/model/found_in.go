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
	"fmt"
	"regexp"
	"strings"
)

// LinkFoundIn records the release a bug-fix task is filed against: its
// target is a release tag, `vX.Y.Z` or `vX.Y.Z-rc.N` (`found-in:
// v2.5.0-rc.2`). It is what the weekly code-quality report counts escaped
// defects per release by, instead of guessing a version out of a title.
// Provenance only: it holds nothing back.
//
// Grain records it itself on what it files from a release-related source
// (DerivedFoundIn): an `[rc-blocker <rc>]` task (RCBlockerMarker), the
// Stage 1 repair of an rc (orchestrator's fileOrUpdateStage1Repair) and a
// port or backport onto `release/vX.Y.Z`. A person or an agent filing a
// bug against a release can state it too (propose_task's found_in, the
// new-task form's "Found in").
const LinkFoundIn LinkKind = "found-in"

var (
	foundInPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-rc\.\d+)?$`)
	// rcBlockerTitle is RCBlockerMarker read back out of a title.
	rcBlockerTitle = regexp.MustCompile(`\[rc-blocker (v\d+\.\d+\.\d+-rc\.\d+)\]`)
	// releaseBranchPattern is a patch-release branch, `release/vX.Y.Z`.
	releaseBranchPattern = regexp.MustCompile(`^release/(v\d+\.\d+\.\d+)$`)
)

// NormalizeFoundIn is version as LinkFoundIn records it: trimmed, with
// the leading `v` a bare `2.5.0` left off, and "" for nothing stated. It
// is refused unless it is a release tag, `vX.Y.Z` or `vX.Y.Z-rc.N`.
func NormalizeFoundIn(version string) (string, error) {
	v := strings.TrimSpace(version)
	if v == "" {
		return "", nil
	}
	if v[0] >= '0' && v[0] <= '9' {
		v = "v" + v
	}
	if !foundInPattern.MatchString(v) {
		return "", fmt.Errorf("found-in %q is not a release version: give a tag such as v2.5.0 or v2.5.0-rc.2", version)
	}
	return v, nil
}

// FoundIn is the release t is filed against (LinkFoundIn), or "".
func FoundIn(t Task) string {
	for _, l := range t.Links {
		if l.Kind == LinkFoundIn {
			return l.Target
		}
	}
	return ""
}

// SetFoundIn records version as the release t is filed against,
// replacing any it named before; "" clears it.
func SetFoundIn(t *Task, version string) {
	links := t.Links[:0:0]
	for _, l := range t.Links {
		if l.Kind != LinkFoundIn {
			links = append(links, l)
		}
	}
	if version != "" {
		links = append(links, Link{Kind: LinkFoundIn, Target: version})
	}
	t.Links = links
}

// ReleaseBranchVersion is the release a patch-release branch,
// `release/vX.Y.Z`, carries fixes for.
func ReleaseBranchVersion(branch string) (string, bool) {
	m := releaseBranchPattern.FindStringSubmatch(strings.TrimSpace(branch))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// DerivedFoundIn is the release a task's own fields say it is filed
// against, and "" when they name none: the candidate an `[rc-blocker
// <rc>]` title holds (RCBlockerMarker), or else the version of the
// `release/vX.Y.Z` branch it lands on -- its base, or a branch it writes
// in its own repository -- which is what a backport proposal names.
func DerivedFoundIn(t Task) string {
	if m := rcBlockerTitle.FindStringSubmatch(t.Title); m != nil {
		return m[1]
	}
	if v, ok := ReleaseBranchVersion(t.Base); ok {
		return v
	}
	for _, w := range t.Writes {
		if t.Target != nil && !strings.EqualFold(w.Repo.String(), t.Target.String()) {
			continue
		}
		if v, ok := ReleaseBranchVersion(w.Base); ok {
			return v
		}
	}
	return ""
}

// ApplyDerivedFoundIn records DerivedFoundIn on a task about to be filed
// that does not already say which release it is against. What a person
// or an agent stated wins.
func ApplyDerivedFoundIn(t *Task) {
	if FoundIn(*t) != "" {
		return
	}
	if v := DerivedFoundIn(*t); v != "" {
		SetFoundIn(t, v)
	}
}
