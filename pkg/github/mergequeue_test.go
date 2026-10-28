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
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestIsMergeQueueRequiredReadsGitHubsRefusal(t *testing.T) {
	refusal := &Error{Status: 405, Body: []byte(
		`{"message":"Repository rule violations found\n\nChanges must be made through the merge queue"}`)}
	if !IsMergeQueueRequired(refusal) {
		t.Fatal("google/xmc's refusal was not read as the merge queue rule")
	}
	if ClassifyMergeRefusal(refusal) != RefusalRule {
		t.Fatal("the refusal stopped being a rule for a client that cannot enqueue")
	}
	for _, err := range []error{
		&Error{Status: 405, Body: []byte(`{"message":"Review required"}`)},
		&Error{Status: 502, Body: []byte(`{"message":"merge queue"}`)},
		errors.New("merge queue"),
		nil,
	} {
		if IsMergeQueueRequired(err) {
			t.Errorf("IsMergeQueueRequired(%v) = true", err)
		}
	}
}

func TestEnqueuePullRequestSendsTheMutationPinnedToTheHead(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(`{"data":{"repository":{"pullRequest":{"id":"PR_kw1","headRefOid":"abc",` +
			`"merged":false,"isInMergeQueue":false,"mergeQueueEntry":null}}}}`)},
		{Status: 200, Body: []byte(`{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"state":"QUEUED","position":2}}}}`)},
	}}
	entry, err := NewClient(transport, StaticToken{strPtr("t")}).EnqueuePullRequest("o", "r", 5, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != "QUEUED" || entry.Position != 2 || entry.AlreadyQueued {
		t.Fatalf("entry = %+v", entry)
	}
	var sent struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(transport.Calls[1].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.Query, "enqueuePullRequest") || !strings.Contains(sent.Query, "expectedHeadOid") ||
		sent.Variables["id"] != "PR_kw1" || sent.Variables["sha"] != "abc" {
		t.Fatalf("sent %+v", sent)
	}
}

func TestEnqueuePullRequestLeavesAQueuedPullRequestWhereItIs(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"data":{"repository":{"pullRequest":` +
		`{"id":"PR_kw1","headRefOid":"abc","merged":false,"isInMergeQueue":true,"mergeQueueEntry":{"state":"AWAITING_CHECKS","position":1}}}}}`)})
	entry, err := NewClient(transport, nil).EnqueuePullRequest("o", "r", 5, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.AlreadyQueued || entry.State != "AWAITING_CHECKS" || len(transport.Calls) != 1 {
		t.Fatalf("entry = %+v after %d calls", entry, len(transport.Calls))
	}
}

func TestEnqueuePullRequestRefusesAHeadThatMoved(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"data":{"repository":{"pullRequest":` +
		`{"id":"PR_kw1","headRefOid":"def","merged":false,"isInMergeQueue":false,"mergeQueueEntry":null}}}}`)})
	_, err := NewClient(transport, nil).EnqueuePullRequest("o", "r", 5, "abc")
	var e *Error
	if !errors.As(err, &e) || e.Status != 409 || len(transport.Calls) != 1 {
		t.Fatalf("err = %v after %d calls, want a 409 and no mutation", err, len(transport.Calls))
	}
}

func TestMergeQueueEntryOfIsNilOutsideTheQueue(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"data":{"repository":{"pullRequest":` +
		`{"id":"PR_kw1","headRefOid":"abc","merged":false,"isInMergeQueue":false,"mergeQueueEntry":null}}}}`)})
	entry, err := NewClient(transport, nil).MergeQueueEntryOf("o", "r", 5)
	if err != nil || entry != nil {
		t.Fatalf("entry = %+v, err = %v; want nil, nil", entry, err)
	}
}
