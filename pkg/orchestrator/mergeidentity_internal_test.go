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
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

type identifiedClient struct {
	github.Client
	identity string
}

func (c identifiedClient) MergeIdentity(owner, repo string) string { return c.identity }

func TestMergedAsDetailNamesTheForgesMergerAndTheMergeCredential(t *testing.T) {
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 7}
	merged := github.PullRequestDetail{Merged: true, MergedBy: "grain-merge[bot]"}
	for _, tc := range []struct {
		name   string
		client github.Client
		detail github.PullRequestDetail
		want   string
	}{
		{"through an App", identifiedClient{identity: "merge credential grain-merge (grain-merge[bot])"}, merged,
			"acme/widgets#7 merged by grain-merge[bot] through merge credential grain-merge (grain-merge[bot])"},
		{"through the push credential", identifiedClient{}, github.PullRequestDetail{Merged: true, MergedBy: "octocat"},
			"acme/widgets#7 merged by octocat through the repository's push credential"},
		{"a client that cannot say", nil, merged, "acme/widgets#7 merged by grain-merge[bot]"},
		{"not merged yet", identifiedClient{}, github.PullRequestDetail{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergedAsDetail(tc.client, ref, tc.detail); got != tc.want {
				t.Errorf("mergedAsDetail = %q, want %q", got, tc.want)
			}
		})
	}
}
