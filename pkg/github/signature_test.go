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
	"strings"
	"testing"
)

type signedTokenSource struct {
	token  string
	author string
}

func (s signedTokenSource) TokenFor(owner, repo string) *string {
	return &s.token
}

func (s signedTokenSource) AuthorSignature(owner, repo string) string {
	return s.author
}

type captureTransport struct {
	lastMethod string
	lastPath   string
	lastBody   []byte
	status     int
	respBody   string
}

func (c *captureTransport) Request(method, path string, headers map[string]string, body []byte) (ApiResponse, error) {
	c.lastMethod = method
	c.lastPath = path
	c.lastBody = append([]byte(nil), body...)
	status := c.status
	if status == 0 {
		status = 201
	}
	resp := c.respBody
	if resp == "" {
		resp = `{"id": 101, "number": 42, "html_url": "https://github.com/acme/widgets/pull/42"}`
	}
	return ApiResponse{Status: status, Body: []byte(resp)}, nil
}

func TestSignPullRequestBodyAndIdempotency(t *testing.T) {
	raw := "Fix off-by-one in header parser\n\n---\nAutomated change for grain task 42."
	signed := SignPullRequestBody(raw, "Ada Lovelace")
	want := "Fix off-by-one in header parser\n\n---\nSigned by Ada Lovelace (via Grain)\nAutomated change for grain task 42."
	if signed != want {
		t.Fatalf("SignPullRequestBody =\n%q\nwant:\n%q", signed, want)
	}
	// Idempotent on second call.
	if again := SignPullRequestBody(signed, "Ada Lovelace"); again != want {
		t.Fatalf("SignPullRequestBody not idempotent:\n%q", again)
	}
	// PreservePullRequestSignature carries the signature onto a rebuilt description.
	rebuilt := "Fix off-by-one in header parser\n\nAlso on this branch:\n- Add test\n\n---\nAutomated change for grain task 42."
	preserved := PreservePullRequestSignature(rebuilt, signed)
	wantPreserved := "Fix off-by-one in header parser\n\nAlso on this branch:\n- Add test\n\n---\nSigned by Ada Lovelace (via Grain)\nAutomated change for grain task 42."
	if preserved != wantPreserved {
		t.Fatalf("PreservePullRequestSignature =\n%q\nwant:\n%q", preserved, wantPreserved)
	}
}

func TestSignCommentBodyAndIdempotency(t *testing.T) {
	raw := "Addressed in the latest commit."
	signed := SignCommentBody(raw, "Ada Lovelace")
	want := "Addressed in the latest commit.\n\n---\n*Signed by Ada Lovelace (via Grain)*"
	if signed != want {
		t.Fatalf("SignCommentBody =\n%q\nwant:\n%q", signed, want)
	}
	if again := SignCommentBody(signed, "Ada Lovelace"); again != want {
		t.Fatalf("SignCommentBody not idempotent:\n%q", again)
	}
}

func TestRESTClientSignsPullRequestsAndCommentsWithDelegatedAuthor(t *testing.T) {
	tr := &captureTransport{}
	client := NewClient(tr, signedTokenSource{token: "gho_test", author: "Ada Lovelace"})

	if _, err := client.CreatePullRequest("acme", "widgets", "grain/task-7", "main", "Fix bug", "Summary\n\n---\nAutomated change for grain task 7."); err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	var prPayload map[string]string
	_ = json.Unmarshal(tr.lastBody, &prPayload)
	if !strings.Contains(prPayload["body"], "Signed by Ada Lovelace (via Grain)\nAutomated change for grain task 7.") {
		t.Fatalf("CreatePullRequest body missing signature:\n%s", prPayload["body"])
	}

	if _, err := client.CreateComment("acme", "widgets", 42, "Question for reviewer"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	var commentPayload map[string]string
	_ = json.Unmarshal(tr.lastBody, &commentPayload)
	if !strings.HasSuffix(commentPayload["body"], "*Signed by Ada Lovelace (via Grain)*") {
		t.Fatalf("CreateComment body missing signature:\n%s", commentPayload["body"])
	}

	if _, err := client.ReplyToReviewComment("acme", "widgets", 42, 9, "Fixed on line 12."); err != nil {
		t.Fatalf("ReplyToReviewComment: %v", err)
	}
	_ = json.Unmarshal(tr.lastBody, &commentPayload)
	if !strings.HasSuffix(commentPayload["body"], "*Signed by Ada Lovelace (via Grain)*") {
		t.Fatalf("ReplyToReviewComment body missing signature:\n%s", commentPayload["body"])
	}

	tr.status = 200
	if _, err := client.SubmitReview("acme", "widgets", 42, ReviewEventComment, "Review summary", nil); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	var reviewPayload map[string]any
	_ = json.Unmarshal(tr.lastBody, &reviewPayload)
	if body, _ := reviewPayload["body"].(string); !strings.HasSuffix(body, "*Signed by Ada Lovelace (via Grain)*") {
		t.Fatalf("SubmitReview body missing signature:\n%s", body)
	}
}
