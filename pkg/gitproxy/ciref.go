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

package gitproxy

// Which refs a CI job holding repo-write may update.
//
// A release workflow (.grain/workflows/release-train.yml,
// design/design/release-from-trunk.design.md §6) pushes three kinds of ref: a
// release or release-candidate tag, refs/tags/v*, created once; the
// `latest` tag, moved on every promotion; and refs/heads/candidate, the
// staging pointer, force-moved onto each cut. All three are allowed, the
// two moving refs force-pushes included. What no CI job may do is
// move trunk: an update (including a fast-forward) or a deletion of a
// protected branch (CIProtectedBranches) is refused outright. Every
// other ref is the job's declaration to push, as it was before these
// rules.
//
// The question is answered here rather than by a workflow's own
// allowlist because a workflow file is a file any branch can rewrite:
// the rules have to hold whatever the job's YAML says.

import (
	"fmt"
	"sort"
	"strings"
)

// CIProtectedBranches are the branches a CI job may neither update nor
// delete.
var CIProtectedBranches = map[string]bool{
	"refs/heads/main": true,
}

// ciRefVia is what the audit log names as having allowed a CI job's
// update (RefGrant.Via).
const ciRefVia = "repo-write"

// CIRefUpdates returns why a CI job with write access to owner/repo may
// not make updates, or "" when it may make all of them.
func CIRefUpdates(check *pushCheck, owner, repo string, updates []RefUpdate) string {
	var problems []string
	var grants []RefGrant
	for _, u := range updates {
		kind, problem := ciRefUpdate(check, u)
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		grants = append(grants, RefGrant{Ref: u.Ref, Kind: kind, Via: ciRefVia})
	}
	if len(problems) > 0 {
		return fmt.Sprintf("refusing to update %s/%s: %s. A CI job's repo-write grant may create and move tags "+
			"(refs/tags/v*, a force-moved refs/tags/latest) and other branches (a force-moved refs/heads/candidate), "+
			"but may not update or delete %s", owner, repo, strings.Join(problems, "; "), protectedList())
	}
	check.grant(grants...)
	return ""
}

// ciRefUpdate is one update's kind when CIRefUpdates allows it, or why
// not.
func ciRefUpdate(_ *pushCheck, u RefUpdate) (kind, problem string) {
	kind = updateKind(u)
	if !CIProtectedBranches[u.Ref] {
		if kind == "update" && strings.HasPrefix(u.Ref, "refs/tags/") {
			// Moving a tag is always a force-push; say so in the audit line.
			kind = "force"
		}
		return kind, ""
	}
	switch kind {
	case "create":
		return kind, ""
	case "delete":
		return "", fmt.Sprintf("%s may not be deleted from CI", u.Ref)
	default:
		return "", fmt.Sprintf("%s may not be updated from CI", u.Ref)
	}
}

func protectedList() string {
	refs := make([]string, 0, len(CIProtectedBranches))
	for ref := range CIProtectedBranches {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return strings.Join(refs, ", ")
}
