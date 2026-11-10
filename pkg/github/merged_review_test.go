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

package github

import (
	"reflect"
	"testing"
)

// An approval counts only from a reviewer other than the author, as their
// latest word, and on the head commit that merged.
func TestMergedPullRequestReviewCountsApprovalsOfTheMergedHead(t *testing.T) {
	sha := "1111111111111111111111111111111111111111"
	transport := NewFakeTransport(
		ApiResponse{Status: 200, Body: []byte(`[
			{"number": 3, "merged_at": "2026-11-10T00:00:00Z", "merge_commit_sha": "other", "base": {"ref": "main"}},
			{"number": 4, "merged_at": "2026-11-10T00:00:00Z", "merge_commit_sha": "` + sha + `", "base": {"ref": "main"}}]`)},
		ApiResponse{Status: 200, Body: []byte(`{"html_url": "https://forge.example/pr/4", "merged": true,
			"merge_commit_sha": "` + sha + `", "user": {"login": "writer"}, "merged_by": {"login": "writer"},
			"head": {"sha": "head2"}}`)},
		ApiResponse{Status: 200, Body: []byte(`[
			{"state": "APPROVED", "commit_id": "head2", "user": {"login": "writer"}},
			{"state": "APPROVED", "commit_id": "head1", "user": {"login": "stale"}},
			{"state": "APPROVED", "commit_id": "head2", "user": {"login": "changed-mind"}},
			{"state": "CHANGES_REQUESTED", "commit_id": "head2", "user": {"login": "changed-mind"}},
			{"state": "APPROVED", "commit_id": "head2", "user": {"login": "reviewer"}},
			{"state": "COMMENTED", "commit_id": "head2", "user": {"login": "reviewer"}}]`)},
	)
	c := NewClient(transport, StaticToken{})
	got, err := c.MergedPullRequestReview("acme", "config", "main", sha)
	if err != nil {
		t.Fatal(err)
	}
	want := &MergedReview{Number: 4, URL: "https://forge.example/pr/4", Author: "writer", MergedBy: "writer", Approvers: []string{"reviewer"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergedPullRequestReview = %+v, want %+v", got, want)
	}

	// A commit no merged pull request into main names is no review.
	transport = NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`[
		{"number": 5, "merged_at": "2026-11-10T00:00:00Z", "merge_commit_sha": "` + sha + `", "base": {"ref": "release"}}]`)})
	if got, err := NewClient(transport, StaticToken{}).MergedPullRequestReview("acme", "config", "main", sha); err != nil || got != nil {
		t.Fatalf("MergedPullRequestReview of a merge into another branch = %+v, %v; want nil, nil", got, err)
	}
}
