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

import (
	"sync/atomic"
	"testing"
)

// Scenarios 1 and 4 of grain/task-4249, at the proxy's own layer: a
// connection removed from the deployment stops resolving for its
// repositories at once, and one re-added under the same name with a new
// token serves the new token -- no restart, and no cached credential
// outliving the file it was read from.
func TestARemovedConnectionStopsResolvingAndAReAddedOneServesItsNewToken(t *testing.T) {
	creds, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const name = "gc-lifecycle"
	var connected atomic.Bool
	creds.SetConnections(func(owner, repo string) (string, bool) {
		if connected.Load() && owner == "acme" {
			return name, true
		}
		return "", false
	})
	token := func() (string, bool) {
		t.Helper()
		got, ok := creds.Select("acme", "widgets")
		if !ok || got.Token == nil {
			return "", false
		}
		return *got.Token, true
	}

	if err := creds.SetPAT(name, "ghp_first"); err != nil {
		t.Fatal(err)
	}
	connected.Store(true)
	if got, ok := token(); !ok || got != "ghp_first" {
		t.Fatalf("connected repository resolves %q, %v; want ghp_first", got, ok)
	}

	// Removal, as DeleteTeamGitConnection does it: the connection list
	// first, then the credential.
	connected.Store(false)
	if err := creds.Remove(name); err != nil {
		t.Fatal(err)
	}
	if got, ok := token(); ok && got == "ghp_first" {
		t.Fatal("a removed connection's token still resolves for its repository")
	}
	if _, ok := creds.Get(name); ok {
		t.Fatal("a removed credential is still readable by name")
	}

	if err := creds.SetPAT(name, "ghp_second"); err != nil {
		t.Fatal(err)
	}
	connected.Store(true)
	if got, ok := token(); !ok || got != "ghp_second" {
		t.Fatalf("re-added connection resolves %q, %v; want the new token ghp_second", got, ok)
	}
}
