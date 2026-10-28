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

// The refs a repository-declared template's runs may push beyond their
// own task branch (grain/task-2536).
//
// Every run may push its own task branch and nothing else
// (gitproxy.ModelAuthorizer.AuthorizeRefUpdates). A template written in
// a repository's own `.grain/templates/<name>.yml` may widen that, for
// the tasks filed from it and only in that same repository, by listing
// the refs those runs push:
//
//	push:
//	  - refs/heads/candidate
//	  - ref: refs/tags/latest
//	    force: true
//
// That is the whole of the policy. There is no team setting and no admin
// grant behind it: the declaration is the allowance, read from the
// repository's default branch by the declaration sync (so a run cannot
// widen its own set by editing `.grain` on its task branch), and the
// filing team's own git credential is the authority, as it is for every
// push the proxy forwards.

import (
	"fmt"
	"path"
	"strings"
)

// RefPush is one entry of a declared template's `push:` list.
type RefPush struct {
	// Pattern is a full ref name, refs/heads/<branch> or
	// refs/tags/<tag>, where `*` and `?` match as path.Match matches
	// them: within one path segment, so refs/tags/v* is every tag
	// starting with v and refs/heads/release/* is one level of branches
	// under release/.
	Pattern string `json:"ref"`
	// Force allows an update the proxy cannot show is a fast-forward:
	// a branch moved to a commit that does not descend from where it
	// was, or a tag that already exists moved at all.
	Force bool `json:"force,omitempty"`
	// Delete allows deleting a matching ref.
	Delete bool `json:"delete,omitempty"`
}

// RefPushPrefixes is the two namespaces a declaration may name refs in.
// Nothing else -- refs/pull/*, refs/notes/*, refs/meta/* -- is a ref a
// release job pushes.
var RefPushPrefixes = []string{"refs/heads/", "refs/tags/"}

// ValidateRefPushPattern says why pattern is not one a declaration may
// write, or nil. The rule is "explicit": a full ref name in one of
// RefPushPrefixes, globbing only with `*` and `?`, and naming at least
// one literal character after the prefix, so `refs/heads/*` --
// every branch, the default one included -- has to be spelt out as the
// branches it means.
func ValidateRefPushPattern(pattern string) error {
	if pattern != strings.TrimSpace(pattern) || pattern == "" {
		return fmt.Errorf("%q is not a ref name", pattern)
	}
	var rest string
	prefixed := false
	for _, p := range RefPushPrefixes {
		if strings.HasPrefix(pattern, p) {
			rest, prefixed = strings.TrimPrefix(pattern, p), true
			break
		}
	}
	if !prefixed {
		return fmt.Errorf("%q: a declared push names a full ref under %s", pattern, strings.Join(RefPushPrefixes, " or "))
	}
	if strings.ContainsAny(pattern, "[]\\ ~^:") || strings.Contains(pattern, "..") || strings.Contains(pattern, "//") ||
		strings.HasSuffix(pattern, "/") || strings.HasSuffix(pattern, ".lock") {
		return fmt.Errorf("%q is not a ref name grain accepts in a push declaration (only `*` and `?` glob)", pattern)
	}
	if strings.Trim(rest, "*?/") == "" {
		return fmt.Errorf("%q would match every ref under %s -- a declaration names the refs it pushes, "+
			"so spell out the branches or tags it means", pattern, strings.TrimSuffix(pattern, rest))
	}
	if _, err := path.Match(pattern, pattern); err != nil {
		return fmt.Errorf("%q is not a valid pattern: %w", pattern, err)
	}
	return nil
}

// Matches reports whether ref is one of the refs r names.
func (r RefPush) Matches(ref string) bool {
	ok, err := path.Match(r.Pattern, ref)
	return err == nil && ok
}

// String is how one entry reads in a refusal or an audit line.
func (r RefPush) String() string {
	var flags []string
	if r.Force {
		flags = append(flags, "force")
	}
	if r.Delete {
		flags = append(flags, "delete")
	}
	if len(flags) == 0 {
		return r.Pattern
	}
	return r.Pattern + " (" + strings.Join(flags, ", ") + ")"
}

// RefPushSet is a declared template's whole `push:` list.
type RefPushSet []RefPush

// Allows is what the set says about one ref: whether any entry names it
// at all, and whether any entry naming it allows a force-move or a
// deletion. Entries union: two that both match a ref give it the
// widest of what they say, which is what somebody who wrote both meant.
func (s RefPushSet) Allows(ref string) (matched, force, del bool) {
	for _, r := range s {
		if !r.Matches(ref) {
			continue
		}
		matched = true
		force = force || r.Force
		del = del || r.Delete
	}
	return matched, force, del
}

// DeclaredPush is what the git proxy reads off a live run before letting
// it update a ref other than its own branch: the template the run's task
// was filed from, the file that declared it, and the push set that file
// says -- as of the declaring repository's default branch, which is the
// only place a declaration is ever read from.
type DeclaredPush struct {
	TaskID     string
	TemplateID string
	// Declaration is where the template was declared: the repository its
	// push set applies to, and the file a refusal points at.
	Declaration RepoDeclaration
	Push        RefPushSet
}
