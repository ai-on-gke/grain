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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSubmitReviewDowngradesToCommentOnOwnPullRequest(t *testing.T) {
	t.Parallel()
	transport := NewFakeTransport(
		ApiResponse{Status: 422, Body: []byte(`{"message":"Review Can not request changes on your own pull request"}`)},
		ApiResponse{Status: 200, Body: []byte(`{"id":77}`)},
	)
	client := NewClient(transport, nil)
	id, err := client.SubmitReview("acme", "widgets", 42, ReviewEventRequestChanges, "Needs work", []NewReviewComment{
		{Path: "main.go", Line: 10, Body: "check nil"},
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if id != 77 {
		t.Errorf("id = %d, want 77", id)
	}
	if len(transport.Calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(transport.Calls))
	}
	var first, second map[string]any
	_ = json.Unmarshal(transport.Calls[0].Body, &first)
	_ = json.Unmarshal(transport.Calls[1].Body, &second)
	if first["event"] != "REQUEST_CHANGES" {
		t.Errorf("first event = %v, want REQUEST_CHANGES", first["event"])
	}
	if second["event"] != "COMMENT" {
		t.Errorf("second event = %v, want COMMENT", second["event"])
	}
}

func TestCollaboratorPermissionAndReactionsAndChecks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	transport := NewFakeTransport(
		ApiResponse{Status: 200, Body: []byte(`{"permission":"write"}`)},
		ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)},
		ApiResponse{Status: 201, Body: []byte(`{"id":1}`)},
		ApiResponse{Status: 201, Body: []byte(`{"id":2}`)},
		ApiResponse{Status: 201, Body: []byte(`{"id":901}`)},
		ApiResponse{Status: 200, Body: []byte(`{"id":901}`)},
	)
	client := NewClient(transport, nil)

	perm, err := client.CollaboratorPermission("acme", "widgets", "alice")
	if err != nil || perm != "write" || !IsTrustedCollaboratorPermission(perm) {
		t.Fatalf("CollaboratorPermission(alice) = (%q, %v)", perm, err)
	}
	perm2, err := client.CollaboratorPermission("acme", "widgets", "stranger")
	if err != nil || perm2 != "none" || IsTrustedCollaboratorPermission(perm2) {
		t.Fatalf("CollaboratorPermission(stranger) = (%q, %v)", perm2, err)
	}

	if err := client.CreateIssueCommentReaction("acme", "widgets", 11, "eyes"); err != nil {
		t.Fatalf("CreateIssueCommentReaction: %v", err)
	}
	if err := client.CreateReviewCommentReaction("acme", "widgets", 22, "rocket"); err != nil {
		t.Fatalf("CreateReviewCommentReaction: %v", err)
	}

	runID, err := client.CreateCheckRun("acme", "widgets", CheckRunWrite{
		Name:      "grain/task",
		HeadSHA:   "0123456789abcdef0123456789abcdef01234567",
		Status:    "in_progress",
		StartedAt: &now,
		Output:    &CheckRunOutput{Title: "Working", Summary: "Running agent"},
	})
	if err != nil || runID != 901 {
		t.Fatalf("CreateCheckRun = (%d, %v), want (901, nil)", runID, err)
	}
	if err := client.UpdateCheckRun("acme", "widgets", runID, CheckRunWrite{
		Status:      "completed",
		Conclusion:  "success",
		CompletedAt: &now,
	}); err != nil {
		t.Fatalf("UpdateCheckRun: %v", err)
	}
}

func TestRealTransportInvalidatePrefixDropsMatchingEntriesOnly(t *testing.T) {
	t.Parallel()
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rt := &RealTransport{
		Host:        srv.Listener.Addr().String(),
		UseTLS:      false,
		Conditional: true,
	}
	_, _ = rt.Request("GET", "/repos/acme/widgets/pulls/1", nil, nil)
	_, _ = rt.Request("GET", "/repos/other/repo/pulls/1", nil, nil)

	rt.InvalidatePrefix("/repos/acme/widgets")

	// Clear ETag check expectation: after invalidation, acme/widgets sends no If-None-Match.
	var sawIfNoneMatch map[string]string = map[string]string{}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch[r.URL.Path] = r.Header.Get("If-None-Match")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	_, _ = rt.Request("GET", "/repos/acme/widgets/pulls/1", nil, nil)
	_, _ = rt.Request("GET", "/repos/other/repo/pulls/1", nil, nil)

	if got := sawIfNoneMatch["/repos/acme/widgets/pulls/1"]; got != "" {
		t.Errorf("invalidated path still sent If-None-Match = %q", got)
	}
	if got := sawIfNoneMatch["/repos/other/repo/pulls/1"]; got != `"v1"` {
		t.Errorf("unrelated path If-None-Match = %q, want \"v1\"", got)
	}
}
