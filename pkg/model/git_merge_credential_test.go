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

package model_test

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A connection's merge credential is found by the same match as the
// connection itself, survives the store's round trip, and leaves the
// push credential where it was.
func TestGitMergeCredentialOnFollowsTheMatchingConnection(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if err := st.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "person", Scope: "acme/widgets", MergeCredential: " grain-merge "},
			{Credential: "person"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ repo, push, merge string }{
		{"widgets", "person", "grain-merge"},
		{"gears", "person", ""},
	} {
		push, err := st.GitConnectionOn(ctx, "", "acme", tc.repo)
		if err != nil || push != tc.push {
			t.Fatalf("GitConnectionOn(%s) = %q, %v", tc.repo, push, err)
		}
		merge, err := st.GitMergeCredentialOn(ctx, "", "acme", tc.repo)
		if err != nil || merge != tc.merge {
			t.Fatalf("GitMergeCredentialOn(%s) = %q, %v, want %q", tc.repo, merge, err, tc.merge)
		}
	}
}

func TestAMergeCredentialCannotBeAnonymous(t *testing.T) {
	if _, err := model.NormalizeTeamGitConnection(model.TeamGitConnection{Credential: "person", MergeCredential: "anonymous"}); err == nil {
		t.Fatal("an anonymous merge credential was accepted")
	}
}
