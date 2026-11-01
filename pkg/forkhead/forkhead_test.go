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

package forkhead_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/forkhead"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// finder answers one head label with one pull request.
type finder struct {
	head   string
	detail github.PullRequestDetail
}

func (f finder) FindOpenPullRequestForHead(_, _, head string) (*github.PullRequest, error) {
	if head != f.head {
		return nil, nil
	}
	return &github.PullRequest{Number: f.detail.Number}, nil
}

func (f finder) GetPullRequest(string, string, int) (github.PullRequestDetail, error) {
	return f.detail, nil
}

var grain = model.RepoRef{Owner: "ai-on-gke", Name: "grain"}

func TestResolveAForkHeadIsBasedOnWhatItMergesInto(t *testing.T) {
	t.Parallel()
	f := finder{head: "andrzejfudro:chore/bump-agy-1.4.9", detail: github.PullRequestDetail{
		Number: 139, HeadRef: "chore/bump-agy-1.4.9", BaseRef: "main", HeadRepo: "andrzejfudro/grain",
		AuthorLogin: "andrzejfudro", MaintainerCanModify: true,
	}}
	r, err := forkhead.Resolve(f, grain, model.ForkHead{Owner: "andrzejfudro", Branch: "chore/bump-agy-1.4.9"})
	if err != nil {
		t.Fatal(err)
	}
	if r.InRepo || r.Base != "main" || r.Link().Target != "ai-on-gke/grain#139" {
		t.Fatalf("Resolve = %+v", r)
	}
	note := r.Note()
	for _, want := range []string{"refs/pull/139/head", "never to a fork", "@andrzejfudro", "andrzejfudro/grain"} {
		if !strings.Contains(note, want) {
			t.Errorf("Note does not say %q:\n%s", want, note)
		}
	}
}

func TestResolveAHeadInTheRepoIsBasedOnIt(t *testing.T) {
	t.Parallel()
	f := finder{head: "ai-on-gke:feature/x", detail: github.PullRequestDetail{Number: 7, HeadRef: "feature/x", BaseRef: "main"}}
	r, err := forkhead.Resolve(f, grain, model.ForkHead{Owner: "ai-on-gke", Branch: "feature/x"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.InRepo || r.Base != "feature/x" {
		t.Fatalf("Resolve = %+v", r)
	}
}

func TestResolveNamesALabelNoPullRequestHeads(t *testing.T) {
	t.Parallel()
	_, err := forkhead.Resolve(finder{}, grain, model.ForkHead{Owner: "andrzejfudro", Branch: "gone"})
	if !errors.Is(err, forkhead.ErrNoPullRequest) || !strings.Contains(err.Error(), `"andrzejfudro:gone"`) {
		t.Fatalf("err = %v", err)
	}
	// The refusal is shown as it stands, so it says "no open pull
	// request" once rather than ending on the sentinel's own words.
	if strings.HasSuffix(err.Error(), forkhead.ErrNoPullRequest.Error()) {
		t.Fatalf("err = %q, want it not to end on %q", err, forkhead.ErrNoPullRequest)
	}
}
