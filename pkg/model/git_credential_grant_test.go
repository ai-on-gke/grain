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
	"sort"
	"testing"
)

// TestGitCredentialMigrationFidelity holds git#git-repo {credential,
// repo: "*"}, the grant schema migration 103 rewrote each
// github-credential:<name> to, to the credential the git proxy used
// before: for every repository a request names (the task's target, one
// it only reads, none at all) and for tasks holding one, two or no
// credential grants, beside grants that are not credentials.
func TestGitCredentialMigrationFidelity(t *testing.T) {
	sets := [][]string{
		{"github-credential:release-bot"},
		{"github-credential:release-bot", "self-debug"},
		{"github-credential:docs-bot", "github-credential:release-bot"},
		{"github-credential:zed", "github-credential:alpha", "read-issues"},
		{"self-debug"},
		nil,
	}
	repos := [][2]string{{"acme", "widgets"}, {"acme", "tools"}, {"other", "thing"}, {"", ""}}
	for _, names := range sets {
		var before, after []Grant
		for _, n := range names {
			before = append(before, Grant{Capability: n, Via: GrantByLabel})
			after = append(after, Grant{Capability: CanonicalLegacyGrant(n), Via: GrantByLabel})
		}
		// The store reads a task's grants sorted by capability (grantsOf).
		sort.Slice(before, func(i, j int) bool { return before[i].Capability < before[j].Capability })
		sort.Slice(after, func(i, j int) bool { return after[i].Capability < after[j].Capability })
		for _, r := range repos {
			oldName, oldOK := gitCredentialOverride(before, r[0], r[1])
			newName, newOK := gitCredentialOverride(after, r[0], r[1])
			if oldName != newName || oldOK != newOK {
				t.Errorf("%v on %s/%s: before %q,%v; after %q,%v", names, r[0], r[1], oldName, oldOK, newName, newOK)
			}
		}
	}
}

func TestGitCredentialGrantScopesToItsRepo(t *testing.T) {
	grants := []Grant{
		{Capability: GitCredentialServerGrant("everywhere", GitCredentialAnyRepo)},
		{Capability: GitCredentialServerGrant("widgets-bot", "acme/widgets")},
		// The external git server's own clone grant names no credential.
		{Capability: GitRepoRef + `={"mode":"read","repo":"acme/tools"}`},
	}
	for _, c := range []struct{ owner, repo, want string }{
		{"acme", "widgets", "widgets-bot"},
		{"Acme", "Widgets", "widgets-bot"},
		{"acme", "tools", "everywhere"},
		{"", "", "everywhere"},
	} {
		if got, ok := gitCredentialOverride(grants, c.owner, c.repo); !ok || got != c.want {
			t.Errorf("%s/%s: %q,%v; want %q", c.owner, c.repo, got, ok, c.want)
		}
	}
	if _, ok := gitCredentialOverride(grants[1:], "acme", "tools"); ok {
		t.Error("a grant for acme/widgets answered for acme/tools")
	}
	if name, repo, ok := GitCredentialGrantTarget("github-credential:release-bot"); !ok || name != "release-bot" || repo != GitCredentialAnyRepo {
		t.Errorf("legacy target = %q, %q, %v", name, repo, ok)
	}
	if got, want := CanonicalLegacyGrant("github-credential:release-bot"),
		`https://grain.dev/capabilities/git#git-repo={"credential":"release-bot","repo":"*"}`; got != want {
		t.Errorf("alias = %s, want %s", got, want)
	}
}
