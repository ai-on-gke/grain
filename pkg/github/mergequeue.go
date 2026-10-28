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
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// IsMergeQueueRequired reports whether err is GitHub refusing a direct
// merge because a rule on the base branch -- a ruleset's "Require merge
// queue", or classic protection's -- sends every change through GitHub's
// own merge queue: "Repository rule violations found: Changes must be
// made through the merge queue". That refusal is a rule like any other
// to ClassifyMergeRefusal, and one no setting on the grain side can
// change; what it asks for is a different call (EnqueuePullRequest),
// which is what a caller that can make one should do instead of giving
// up.
func IsMergeQueueRequired(err error) bool {
	if !IsMergeRuleStatus(err) {
		return false
	}
	var e *Error
	errors.As(err, &e)
	return strings.Contains(strings.ToLower(refusalText(e.Body)), "merge queue")
}

// MergeQueueEntry is where a pull request stands in its base branch's
// GitHub merge queue once EnqueuePullRequest has put it there (or found
// it there already).
type MergeQueueEntry struct {
	// State is GitHub's MergeQueueEntryState: QUEUED, AWAITING_CHECKS,
	// MERGEABLE, UNMERGEABLE or LOCKED. Empty where GitHub did not say.
	State string
	// Position is the entry's place in the queue, as GitHub numbers it.
	Position int
	// AlreadyQueued is a pull request that was in the queue before this
	// call, which therefore enqueued nothing.
	AlreadyQueued bool
}

// MergeQueuer is the optional half of Client that hands a pull request
// to GitHub's merge queue and reads where it stands there. Optional for
// MergeRulesReader's reason: only the merge queue calls it, and only
// after GitHub has said a direct merge is not allowed
// (IsMergeQueueRequired), so a client without it simply reports that
// refusal as the rule it is.
type MergeQueuer interface {
	EnqueuePullRequest(owner, repo string, number int, headSHA string) (MergeQueueEntry, error)
	MergeQueueEntryOf(owner, repo string, number int) (*MergeQueueEntry, error)
}

var _ MergeQueuer = (*RESTClient)(nil)

const pullRequestQueueQuery = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      id
      headRefOid
      merged
      isInMergeQueue
      mergeQueueEntry { state position }
    }
  }
}`

const enqueuePullRequestMutation = `mutation($id: ID!, $sha: GitObjectID) {
  enqueuePullRequest(input: {pullRequestId: $id, expectedHeadOid: $sha}) {
    mergeQueueEntry { state position }
  }
}`

type mergeQueueEntryJSON struct {
	State    string `json:"state"`
	Position int    `json:"position"`
}

func (e *mergeQueueEntryJSON) entry() MergeQueueEntry {
	if e == nil {
		return MergeQueueEntry{}
	}
	return MergeQueueEntry{State: e.State, Position: e.Position}
}

type pullRequestQueueState struct {
	ID              string               `json:"id"`
	HeadRefOid      string               `json:"headRefOid"`
	Merged          bool                 `json:"merged"`
	IsInMergeQueue  bool                 `json:"isInMergeQueue"`
	MergeQueueEntry *mergeQueueEntryJSON `json:"mergeQueueEntry"`
}

func (c *RESTClient) pullRequestQueueState(owner, repo string, number int) (pullRequestQueueState, error) {
	var read struct {
		Repository *struct {
			PullRequest *pullRequestQueueState `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := c.graphql(owner, repo, pullRequestQueueQuery, map[string]any{
		"owner": owner, "repo": repo, "number": number,
	}, &read); err != nil {
		return pullRequestQueueState{}, err
	}
	if read.Repository == nil || read.Repository.PullRequest == nil {
		return pullRequestQueueState{}, &Error{Status: http.StatusNotFound, Body: []byte(`{"message":"Not Found"}`)}
	}
	return *read.Repository.PullRequest, nil
}

// MergeQueueEntryOf is where pull request number stands in its base
// branch's GitHub merge queue, or nil where it is not in it -- which is
// both a pull request nobody queued and one the queue has since removed
// (its checks failed in the merge group, it conflicted with what landed
// ahead of it, somebody took it out), and also one that has merged: the
// caller reads that from the pull request itself.
func (c *RESTClient) MergeQueueEntryOf(owner, repo string, number int) (*MergeQueueEntry, error) {
	pr, err := c.pullRequestQueueState(owner, repo, number)
	if err != nil {
		return nil, err
	}
	if !pr.IsInMergeQueue {
		return nil, nil
	}
	entry := pr.MergeQueueEntry.entry()
	entry.AlreadyQueued = true
	return &entry, nil
}

// EnqueuePullRequest adds pull request number to its base branch's
// GitHub merge queue -- the "Merge when ready" button -- which is the only
// way a change lands on a branch whose rules require the queue. GitHub
// then builds and checks the merge group itself and merges the pull
// request when it passes; the caller learns that the way it learns of
// any merge, from the pull request reading merged.
//
// headSHA, when non-empty, pins the entry the way MergePullRequest pins
// a merge (GitHub's expectedHeadOid): a head that has moved since the
// caller's verdict is answered with a 409, as a moved head is there,
// rather than queued untested.
//
// Idempotent: a pull request already in the queue -- or already merged
// by it -- is left where it is and reported with AlreadyQueued, so a
// caller that lost track of having queued it (a restart) does not knock
// it to the back of the line.
func (c *RESTClient) EnqueuePullRequest(owner, repo string, number int, headSHA string) (MergeQueueEntry, error) {
	if err := c.gitOnly(owner, repo); err != nil {
		return MergeQueueEntry{}, err
	}
	pr, err := c.pullRequestQueueState(owner, repo, number)
	if err != nil {
		return MergeQueueEntry{}, err
	}
	if pr.IsInMergeQueue || pr.Merged {
		entry := pr.MergeQueueEntry.entry()
		entry.AlreadyQueued = true
		return entry, nil
	}
	if headSHA != "" && pr.HeadRefOid != "" && pr.HeadRefOid != headSHA {
		return MergeQueueEntry{}, &Error{Status: http.StatusConflict, Body: []byte(fmt.Sprintf(
			`{"message":"Head branch was modified. Review and try the merge again. (head is %s, not %s)"}`,
			pr.HeadRefOid, headSHA))}
	}
	var sha any
	if headSHA != "" {
		sha = headSHA
	}
	var queued struct {
		EnqueuePullRequest *struct {
			MergeQueueEntry *mergeQueueEntryJSON `json:"mergeQueueEntry"`
		} `json:"enqueuePullRequest"`
	}
	if err := c.graphql(owner, repo, enqueuePullRequestMutation, map[string]any{
		"id": pr.ID, "sha": sha,
	}, &queued); err != nil {
		return MergeQueueEntry{}, err
	}
	if queued.EnqueuePullRequest == nil {
		return MergeQueueEntry{}, nil
	}
	return queued.EnqueuePullRequest.MergeQueueEntry.entry(), nil
}
