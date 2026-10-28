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

// The set of repos no sandbox may reach through the proxy at all, held
// so that it can be replaced under a proxy that is already serving.
//
// It used to be a plain slice, read once when the daemon started and
// baked into the authorizer for the life of the process. That is right
// for the ordinary case -- the one repository in it is grain's own state
// repository, which does not change under a running daemon -- but it is
// not the only case: the Settings pane's State tab and `grain state
// adopt` both point an installation at a different repository, and the
// pane does it while the daemon runs. A deployment that adopted a state
// repository carrying secrets.enc in its history went on serving that
// repository to every sandbox until somebody restarted the daemon, with
// the refusal correct from the next start and absent before it.
//
// So the set is a holder rather than a value: cmd/grain's startGitProxy
// fills it at startup, and whatever changes which repository grain's
// state lives in replaces its contents (cmd/grain's stateManager), which
// the next request through the proxy reads.

import (
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ForbiddenSet holds the repos no sandbox may reach, for reading by the
// proxy's request goroutines while another goroutine replaces it.
//
// A nil *ForbiddenSet reads as empty rather than panicking: a proxy
// wired up without one -- a test stub, or an Authorizer built by hand --
// forbids nothing, which is the same answer the nil slice used to give.
type ForbiddenSet struct {
	repos atomic.Pointer[[]model.RepoRef]
}

// NewForbiddenSet is a set holding repos, which may be none: an empty
// set that something later fills is the ordinary way to start, since the
// daemon has to hand the proxy the holder before it knows what belongs
// in it.
func NewForbiddenSet(repos ...model.RepoRef) *ForbiddenSet {
	s := &ForbiddenSet{}
	s.Set(repos)
	return s
}

// Set replaces the whole set, atomically: what is forbidden is re-read
// as a whole (cmd/grain's forbiddenRepos), so adopting a repository that
// has never held the secrets file has to be able to *unforbid* just as
// readily as adopting one that has forbids.
//
// The slice is copied, so a caller may keep and reuse its own.
func (s *ForbiddenSet) Set(repos []model.RepoRef) {
	held := append([]model.RepoRef(nil), repos...)
	s.repos.Store(&held)
}

// Repos is what the set holds right now. The result is the set's own
// backing array and must not be written to.
func (s *ForbiddenSet) Repos() []model.RepoRef {
	if s == nil {
		return nil
	}
	held := s.repos.Load()
	if held == nil {
		return nil
	}
	return *held
}

// forbids reports whether owner/repo is in the set as it stands, matched
// the way every other repo comparison here is: case-insensitively, with
// or without .git.
func (s *ForbiddenSet) forbids(owner, repo string) bool {
	for _, r := range s.Repos() {
		if canonicalMatches(r, owner, repo) {
			return true
		}
	}
	return false
}
