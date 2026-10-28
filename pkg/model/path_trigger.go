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
	"path"
	"sort"
	"strings"
)

// A path trigger (grain/task-1712) is a repository saying "run this
// template whenever a change to these paths merges, or is proposed":
// `.grain/triggers/<name>.yml`, parsed by pkg/repodecl and fired by
// pkg/orchestrator's pathtrigger.go.
//
// The rule that makes it safe to let a repository write one is where it
// is read from: always the declarations grain synced from the base
// branch, never a pull request's head. A change cannot add, edit or
// delete the trigger that judges it, because the copy that judges it is
// the one already merged.

// TriggerDeclarationDir holds one file per path trigger.
const TriggerDeclarationDir = DeclarationDir + "/triggers"

// TriggerEvent is what fires a path trigger.
type TriggerEvent string

const (
	// TriggerOnMerge fires once per base-branch move that touched the
	// trigger's paths.
	TriggerOnMerge TriggerEvent = "merge"
	// TriggerOnPullRequest fires when a pull request touching the paths
	// opens, and again whenever its head moves.
	TriggerOnPullRequest TriggerEvent = "pull_request"
)

// TriggerPer is how one firing's changed paths are split into tasks.
type TriggerPer string

const (
	// TriggerPerChange files one task for the whole change.
	TriggerPerChange TriggerPer = "change"
	// TriggerPerFolder files one task per top-level file or folder
	// directly under the matched path's literal prefix.
	TriggerPerFolder TriggerPer = "folder"
)

// TriggerHuman is how much of a human a gating trigger's release needs.
type TriggerHuman string

const (
	// TriggerHumanRequired never releases on its own: a human's Submit
	// is the only way the change merges.
	TriggerHumanRequired TriggerHuman = "required"
	// TriggerHumanOnFindings releases when every triggered task finished
	// without findings and pushed nothing -- the security hold's rule.
	TriggerHumanOnFindings TriggerHuman = "on-findings"
	// TriggerHumanNo releases once every triggered task has finished,
	// whatever it said.
	TriggerHumanNo TriggerHuman = "no"
)

// strictness orders the three, strictest last.
func (h TriggerHuman) strictness() int {
	switch h {
	case TriggerHumanNo:
		return 0
	case TriggerHumanRequired:
		return 2
	default:
		return 1
	}
}

// StricterHuman is the stricter of two settings: several gating triggers
// matching one pull request are released under the strictest of them.
func StricterHuman(a, b TriggerHuman) TriggerHuman {
	if a == "" {
		return b
	}
	if b == "" || a.strictness() >= b.strictness() {
		return a
	}
	return b
}

// PathTrigger is one parsed `.grain/triggers/<name>.yml`.
type PathTrigger struct {
	// Name is the file's stem, and the trigger's identity within its
	// repository.
	Name string `json:"name"`
	// Path is the file it was read from.
	Path string       `json:"path"`
	On   TriggerEvent `json:"on"`
	// Branches are the base branches it watches, globs allowed. Empty
	// is the repository's default branch.
	Branches []string `json:"branches,omitempty"`
	Paths    []string `json:"paths"`
	Ignore   []string `json:"ignore,omitempty"`
	// Template is the template id it files from: a stem of this
	// repository's own templates resolved to its declared id, or a
	// deployment template's id as written.
	Template    string       `json:"template"`
	Per         TriggerPer   `json:"per"`
	Gate        bool         `json:"gate,omitempty"`
	Human       TriggerHuman `json:"human,omitempty"`
	AutoApprove bool         `json:"auto_approve,omitempty"`
	// Sensitive marks a gating trigger as guarding sensitive paths
	// (sensitive_path.go): its template defaults to grain's security
	// review, and under `human: required` only Approvers may release it.
	Sensitive bool `json:"sensitive,omitempty"`
	// Approvers is who may release a sensitive `human: required` hold,
	// as approver tokens (a user, or "team:<name>"). Empty is the admins
	// of the held task's team.
	Approvers []string `json:"approvers,omitempty"`
}

// WatchesBranch reports whether a change landing on (or proposed
// against) branch is one this trigger looks at.
func (t PathTrigger) WatchesBranch(branch, defaultBranch string) bool {
	if len(t.Branches) == 0 {
		return branch != "" && branch == defaultBranch
	}
	for _, pattern := range t.Branches {
		if ok, _ := path.Match(pattern, branch); ok {
			return true
		}
	}
	return false
}

// Match is the files of changed this trigger fires on: those matching
// one of its paths and none of its ignores, sorted. Empty means it does
// not fire -- which is also what a change touching only ignored paths
// gets.
func (t PathTrigger) Match(changed []string) []string {
	var out []string
	for _, file := range changed {
		if t.patternFor(file) == "" || matchesAny(t.Ignore, file) {
			continue
		}
		out = append(out, file)
	}
	sort.Strings(out)
	return out
}

// Groups splits matched files into the tasks one firing files, keyed by
// group: one group named "" for per: change, and one per top-level file
// or folder under the matching path's literal prefix for per: folder.
func (t PathTrigger) Groups(matched []string) map[string][]string {
	out := map[string][]string{}
	for _, file := range matched {
		key := ""
		if t.Per == TriggerPerFolder {
			key = FolderGroup(t.patternFor(file), file)
		}
		out[key] = append(out[key], file)
	}
	return out
}

// patternFor is the first of t's paths file matches, or "".
func (t PathTrigger) patternFor(file string) string {
	for _, pattern := range t.Paths {
		if MatchPath(pattern, file) {
			return pattern
		}
	}
	return ""
}

func matchesAny(patterns []string, file string) bool {
	for _, pattern := range patterns {
		if MatchPath(pattern, file) {
			return true
		}
	}
	return false
}

// MatchPath reports whether file, a slash-separated path relative to the
// repository root, is matched by pattern.
//
// A pattern with no glob characters is a directory prefix or an exact
// file: `docs/design` matches `docs/design/foo/README.md` and
// `docs/design` itself. Otherwise it is matched segment by segment with
// path.Match, and a segment of `**` matches any number of segments,
// none included -- so `docs/design/**` matches everything under that
// directory, and `**/plan.yml` a plan.yml at any depth.
func MatchPath(pattern, file string) bool {
	pattern = strings.Trim(pattern, "/")
	file = strings.Trim(file, "/")
	if pattern == "" {
		return false
	}
	if !hasGlob(pattern) {
		return file == pattern || strings.HasPrefix(file, pattern+"/")
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pattern, file []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for i := 0; i <= len(file); i++ {
				if matchSegments(pattern[1:], file[i:]) {
					return true
				}
			}
			return false
		}
		if len(file) == 0 {
			return false
		}
		if ok, _ := path.Match(pattern[0], file[0]); !ok {
			return false
		}
		pattern, file = pattern[1:], file[1:]
	}
	return len(file) == 0
}

func hasGlob(s string) bool { return strings.ContainsAny(s, "*?[\\") }

// ValidatePathPattern says what is wrong with a paths or ignore entry.
func ValidatePathPattern(pattern string) error {
	trimmed := strings.Trim(strings.TrimSpace(pattern), "/")
	if trimmed == "" {
		return fmt.Errorf("an empty path matches nothing")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "**" {
			continue
		}
		if strings.Contains(segment, "**") {
			return fmt.Errorf("%q: `**` is a whole path segment of its own (`a/**/b`), not part of one", pattern)
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("%q: %w", pattern, err)
		}
	}
	return nil
}

// ValidateBranchPattern says what is wrong with a branches entry.
func ValidateBranchPattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("an empty branch matches nothing")
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("%q: %w", pattern, err)
	}
	return nil
}

// FolderGroup is the per: folder group file belongs to under pattern:
// the pattern's literal prefix (every segment before the first glob)
// plus file's next segment. `docs/design/**` groups
// `docs/design/foo/README.md` as `docs/design/foo` and
// `docs/design/index.md` as itself.
func FolderGroup(pattern, file string) string {
	file = strings.Trim(file, "/")
	var prefix []string
	for _, segment := range strings.Split(strings.Trim(pattern, "/"), "/") {
		if hasGlob(segment) {
			break
		}
		prefix = append(prefix, segment)
	}
	parts := strings.Split(file, "/")
	if len(parts) <= len(prefix) {
		return file
	}
	return strings.Join(parts[:len(prefix)+1], "/")
}

// PathTriggerTag is the idempotency tag every task one trigger files for
// one key carries: (repo, branch, trigger, group) for a merge,
// (pull request, trigger, group) for a pull request. At most one task
// with a given tag is ever runnable (orchestrator's coalescing).
func PathTriggerTag(scope, trigger, group string) string {
	tag := "path-trigger:" + scope + ":" + trigger
	if group != "" {
		tag += ":" + group
	}
	return tag
}

// PathTriggerMarkerTag is on every task a path trigger files, whatever
// its key -- what keeps a triggered task's own pull request from being
// judged by pull-request triggers in turn.
const PathTriggerMarkerTag = "path-trigger"

// PathTriggerFromTag records the first base commit a merge-triggered
// task covers, so a superseding task can keep the earliest one.
func PathTriggerFromTag(sha string) string { return "path-trigger-from:" + sha }

// PathTriggerFrom reads PathTriggerFromTag back off tags.
func PathTriggerFrom(tags []string) string {
	for _, tag := range tags {
		if sha, ok := strings.CutPrefix(tag, "path-trigger-from:"); ok {
			return sha
		}
	}
	return ""
}
