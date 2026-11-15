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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestGrantAlreadyStoredMatchesBareSandboxRepo: a request_access for the
// bare github#sandbox-repo reference, or the retired github-sandbox name,
// is already held by a repository whose CI capabilities row migration 124
// rewrote to github#sandbox-repo={} -- and the reverse.
func TestGrantAlreadyStoredMatchesBareSandboxRepo(t *testing.T) {
	stored := []string{model.GitHubSandboxRepoGrant}
	for _, name := range []string{
		model.GitHubSandboxRepoRef,
		"https://grain.dev/capabilities/GitHub#sandbox-repo",
	} {
		if !grantAlreadyStored(stored, name, nil) {
			t.Errorf("grantAlreadyStored(%v, %q) = false, want true", stored, name)
		}
	}
	// The retired bare github-sandbox name is no longer an alias.
	if grantAlreadyStored(stored, "github-sandbox", nil) {
		t.Error("grantAlreadyStored matched retired bare github-sandbox")
	}
	if !grantAlreadyStored([]string{model.GitHubSandboxRepoRef}, model.GitHubSandboxRepoGrant, nil) {
		t.Error("a bare stored row does not hold the recorded grant")
	}
	if grantAlreadyStored(stored, model.ImageSigningRef, nil) {
		t.Error("github#sandbox-repo={} holds release#image-signing")
	}
}
