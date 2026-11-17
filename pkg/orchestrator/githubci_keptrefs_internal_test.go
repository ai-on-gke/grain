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

package orchestrator

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// keptRefsClient answers DefaultBranch alone.
type keptRefsClient struct {
	github.Client
	branch string
	err    error
}

func (c keptRefsClient) DefaultBranch(string, string) (string, error) { return c.branch, c.err }

// The refs a prune keeps for eight weeks are read from the forge, not
// only from what this process happened to cache, so a restart cannot
// leave a default branch other than main or master out -- and when one
// cannot be read the prune is told the list is incomplete, rather than
// forgetting that branch's verdicts by the short window.
func TestGitHubCIKeptRefsReadsEveryActionsRepositorysDefaultBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	refs, complete := gitHubCIKeptRefs(ctx, Deps{Store: store, Client: keptRefsClient{err: errors.New("forge down")}}, now)
	if complete || !slices.Equal(refs, []string{"main", "master"}) {
		t.Fatalf("an unreadable default branch = %v, complete %v; want main, master and incomplete", refs, complete)
	}
	if _, complete := gitHubCIKeptRefs(ctx, Deps{Store: store}, now); complete {
		t.Fatal("no forge client still read as a complete list")
	}

	refs, complete = gitHubCIKeptRefs(ctx, Deps{Store: store, Client: keptRefsClient{branch: "trunk"}}, now)
	if !complete || !slices.Contains(refs, "trunk") {
		t.Fatalf("refs = %v, complete %v; want trunk among them, complete", refs, complete)
	}
}
