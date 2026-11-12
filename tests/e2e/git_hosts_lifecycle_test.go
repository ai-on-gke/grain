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

package e2e

// git_hosts_lifecycle_test.go runs pkg/testing/hostsrig's scenarios (docs/git-hosts.md
// section 8) against gitproxytest's in-process command-backed forge fake:
//
//   - a second GitHub-kind host (GitHub Enterprise style) on its own
//     token, reached at a base URL of its own;
//   - a plain git server, kind "git", with no forge API at all;
//   - a command-backed forge, gitproxytest's made-up forgecli source on
//     depot.code.forge.test, which behaves the way a forge reached only
//     through its own gh-style tool does: every request, git included, is
//     one run of `forgecli api`, and a lapsed login answers with the
//     login page rather than a refusal.
//
// Editions import pkg/testing/hostsrig directly to run the same scenarios
// against a real command-backed source and scratch repository.

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/testing/hostsrig"
)

func TestTaskLifecycleOnEveryKindOfGitHost(t *testing.T) {
	hostsrig.Lifecycle(t, hostsrig.Config{})
}

func TestACommandBackedForgesLapsedLoginSaysToRenewIt(t *testing.T) {
	hostsrig.LapsedLogin(t, hostsrig.Config{})
}

func TestAHostsConnectionRemovedMidTaskRefusesNamingTheHost(t *testing.T) {
	hostsrig.ConnectionRemovedMidTask(t, hostsrig.Config{})
}
