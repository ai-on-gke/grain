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

import "testing"

// A repair tag names its repo in RepoRef.String's written form, so a
// host-qualified repo -- and one whose host carries a port, a colon the
// tag's own separator must not be confused with -- reads back as the
// RepoRef that wrote it.
func TestParseBaseRepairBranchTagRoundTrip(t *testing.T) {
	for _, repo := range []RepoRef{
		{Owner: "acme", Name: "app"},
		{Host: "depot.code.corp.goog", Owner: "experimental", Name: "grain"},
		{Host: "git.example.com:8443", Owner: "acme", Name: "app"},
	} {
		tag := BaseRepairBranchTag(repo, "release-1.2")
		got, branch, ok := ParseBaseRepairBranchTag(tag)
		if !ok || !got.Same(repo) || got != repo || branch != "release-1.2" {
			t.Errorf("ParseBaseRepairBranchTag(%q) = %+v, %q, %v; want %+v, release-1.2, true", tag, got, branch, ok, repo)
		}
	}
	for _, tag := range []string{
		"base-ci-repair:acme/app",
		"base-ci-repair:acme/app:",
		"base-ci-repair:acme:main",
		"base-ci-repair:a/b/c/d:main",
		"base-ci:acme/app:main:abc",
	} {
		if r, b, ok := ParseBaseRepairBranchTag(tag); ok {
			t.Errorf("ParseBaseRepairBranchTag(%q) = %+v, %q, true; want refused", tag, r, b)
		}
	}
}

// A persistent sandbox may be defined on a repo off the default git host.
func TestValidatePersistentSandboxHostQualifiedRepo(t *testing.T) {
	for _, repo := range []string{"acme/app", "depot.code.corp.goog/experimental/grain"} {
		sb := PersistentSandbox{Name: "a", Repo: repo, Command: "x"}
		if err := ValidatePersistentSandbox(sb); err != nil {
			t.Errorf("repo %q refused: %v", repo, err)
		}
	}
	for _, repo := range []string{"nope", "a/b/c", "a/b/c/d", "acme/ap p"} {
		sb := PersistentSandbox{Name: "a", Repo: repo, Command: "x"}
		if err := ValidatePersistentSandbox(sb); err == nil {
			t.Errorf("repo %q accepted", repo)
		}
	}
}
