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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- RealTransport: exercised against a real local HTTP server rather
// than mocked, since the whole point is to check that net/http is driven
// correctly (method/path/headers/body out, status/headers/body back).
// UseTLS false is a real, documented configuration -- the mocked-GitHub
// live-test seam -- so this is not testing a code path production never
// takes.

func TestRealTransportSendsTheRequestAndParsesTheResponse(t *testing.T) {
	var gotMethod, gotPath, gotAccept string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Reply", "yes")
		w.WriteHeader(200)
		w.Write([]byte("hello"))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	transport := &RealTransport{Host: host, UseTLS: false}
	resp, err := transport.Request("POST", "/repos/acme/widgets/issues",
		map[string]string{"Accept": "application/vnd.github+json"}, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != "hello" || resp.Headers["X-Reply"] != "yes" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if gotMethod != "POST" || gotPath != "/repos/acme/widgets/issues" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if string(gotBody) != "payload" {
		t.Fatalf("unexpected body: %s", gotBody)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Fatalf("unexpected accept header: %s", gotAccept)
	}
}

func issueJSONFixture(number int, isPR bool, labels ...string) map[string]any {
	if len(labels) == 0 {
		labels = []string{"grain-agent"}
	}
	labelObjs := make([]map[string]string, len(labels))
	for i, l := range labels {
		labelObjs[i] = map[string]string{"name": l}
	}
	item := map[string]any{
		"number": number, "title": "issue", "body": "do the thing",
		"html_url": "https://github.com/o/r/issues/1", "labels": labelObjs,
	}
	if isPR {
		item["pull_request"] = map[string]string{"url": "..."}
	}
	return item
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func strPtr(s string) *string { return &s }

func TestListIssuesReturnsOpenIssues(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, []any{issueJSONFixture(1, false)})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	issues, err := client.ListIssues("o", "r", "grain-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 1 {
		t.Fatalf("got %+v", issues)
	}
	if !issues[0].HasLabel("grain-agent") {
		t.Fatalf("expected grain-agent label, got %+v", issues[0].Labels)
	}
}

func TestListIssuesFiltersOutPullRequests(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{
		Status: 200, Body: mustJSON(t, []any{issueJSONFixture(1, false), issueJSONFixture(2, true)}),
	})
	client := NewClient(transport, StaticToken{strPtr("t")})
	issues, err := client.ListIssues("o", "r", "grain-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 1 {
		t.Fatalf("got %+v", issues)
	}
}

func TestListBranchesFollowsPagination(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/branches?page=2>; rel="next"`}, Body: []byte(`[{"name":"main"}]`)},
		{Status: 200, Body: []byte(`[{"name":"feature/x"}]`)},
	}}
	got, err := NewClient(transport, nil).ListBranches("o", "r")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"main", "feature/x"}) {
		t.Fatalf("branches = %v", got)
	}
	if transport.Calls[0].Path != "/repos/o/r/branches?per_page=100" || transport.Calls[1].Path != "/repos/o/r/branches?page=2" {
		t.Fatalf("paths = %q, %q", transport.Calls[0].Path, transport.Calls[1].Path)
	}
}

func TestListIssuesFollowsLinkHeaderPagination(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/issues?page=2>; rel="next"`},
			Body: mustJSON(t, []any{issueJSONFixture(1, false)})},
		{Status: 200, Body: mustJSON(t, []any{issueJSONFixture(2, false)})},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	issues, err := client.ListIssues("o", "r", "grain-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].Number != 1 || issues[1].Number != 2 {
		t.Fatalf("got %+v", issues)
	}
	if transport.Calls[1].Path != "/repos/o/r/issues?page=2" {
		t.Fatalf("unexpected second call path: %s", transport.Calls[1].Path)
	}
}

func TestNextPagePathReturnsEmptyWhenTheLinkHeaderHasNoNextRel(t *testing.T) {
	header := `<https://api.github.com/repos/o/r/issues?page=1>; rel="prev"`
	if got := nextPagePath(header); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestNextPagePathSkipsEarlierSegmentsToFindNext(t *testing.T) {
	header := `<https://api.github.com/repos/o/r/issues?page=1>; rel="prev", ` +
		`<https://api.github.com/repos/o/r/issues?page=3>; rel="next"`
	if got := nextPagePath(header); got != "/repos/o/r/issues?page=3" {
		t.Fatalf("got %q", got)
	}
}

func TestNextPagePathKeepsAnEscapedSegmentEscaped(t *testing.T) {
	header := `<https://api.github.com/repos/o/r/commits/feature%2Fx/check-runs?per_page=100&page=2>; rel="next"`
	if got := nextPagePath(header); got != "/repos/o/r/commits/feature%2Fx/check-runs?per_page=100&page=2" {
		t.Fatalf("got %q", got)
	}
}

func TestNextPagePathStripsGHEApiV3Prefix(t *testing.T) {
	header := `<https://depot.code.forge.test/api/v3/repositories/80876/pulls/1/files?per_page=100&page=2>; rel="next"`
	if got := nextPagePath(header); got != "/repositories/80876/pulls/1/files?per_page=100&page=2" {
		t.Fatalf("got %q", got)
	}
}

func TestListIssuesRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 403, Body: []byte("nope")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.ListIssues("o", "r", "grain-agent"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestGetIssueReturnsTheIssue(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, issueJSONFixture(7, false))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	issue, err := client.GetIssue("o", "r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 7 || !issue.HasLabel("grain-agent") {
		t.Fatalf("got %+v", issue)
	}
	if transport.Calls[0].Path != "/repos/o/r/issues/7" {
		t.Fatalf("got path %s", transport.Calls[0].Path)
	}
}

func TestGetIssueRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.GetIssue("o", "r", 7); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAddLabelPostsTheLabelBody(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("[]")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.AddLabel("o", "r", 1, "grain-agent-in-progress"); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "POST" {
		t.Fatalf("got method %s", call.Method)
	}
	var body struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Labels) != 1 || body.Labels[0] != "grain-agent-in-progress" {
		t.Fatalf("got %+v", body)
	}
}

func TestRemoveLabelToleratesA404TheLabelIsAlreadyGone(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.RemoveLabel("o", "r", 1, "grain-agent"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestRemoveLabelEscapesASlashInTheLabelName(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("[]")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.RemoveLabel("o", "r", 1, "area/ci"); err != nil {
		t.Fatal(err)
	}
	if got := transport.Calls[0].Path; got != "/repos/o/r/issues/1/labels/area%2Fci" {
		t.Fatalf("path = %q, want the label as one escaped segment", got)
	}
}

func TestRemoveLabelRaisesOnOtherErrors(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.RemoveLabel("o", "r", 1, "grain-agent"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCloseIssuePatchesTheIssueClosed(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.CloseIssue("o", "r", 1); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "PATCH" || call.Path != "/repos/o/r/issues/1" {
		t.Fatalf("got %+v", call)
	}
	var body map[string]string
	json.Unmarshal(call.Body, &body)
	if body["state"] != "closed" {
		t.Fatalf("got %+v", body)
	}
}

// The pulls endpoint, not the issues one: a pull request's state belongs
// to PATCH .../pulls/{n}, which is also the only endpoint that would
// refuse the call on a pull request grain has no push access to.
func TestClosePullRequestPatchesThePullRequestClosed(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.ClosePullRequest("o", "r", 7); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "PATCH" || call.Path != "/repos/o/r/pulls/7" {
		t.Fatalf("got %+v", call)
	}
	var body map[string]string
	json.Unmarshal(call.Body, &body)
	if body["state"] != "closed" {
		t.Fatalf("got %+v", body)
	}
	// Nothing else in the payload: a PATCH carrying a base or a title
	// grain did not mean to change would rewrite the pull request while
	// closing it.
	if len(body) != 1 {
		t.Fatalf("body = %+v, want only the state field", body)
	}
}

// A refusal is the caller's to hear about. It is not fatal to the close
// of the task that provoked it (ui.Client.noteOrphanedPullRequests folds
// it into the note instead), but it has to arrive as an error rather than
// as a close that quietly did nothing.
func TestClosePullRequestReportsARefusal(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 403, Body: []byte(`{"message":"Resource not accessible"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	err := client.ClosePullRequest("o", "r", 7)
	var ghErr *Error
	if !errors.As(err, &ghErr) || ghErr.Status != 403 {
		t.Fatalf("got %v, want the 403 back", err)
	}
}

func TestReopenIssuePatchesTheIssueOpen(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.ReopenIssue("o", "r", 1); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	json.Unmarshal(transport.Calls[0].Body, &body)
	if body["state"] != "open" {
		t.Fatalf("got %+v", body)
	}
}

func TestUpdateIssuePatchesOnlyTheFieldsGiven(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	title := "new title"
	if err := client.UpdateIssue("o", "r", 1, &title, nil); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "PATCH" || call.Path != "/repos/o/r/issues/1" {
		t.Fatalf("got %+v", call)
	}
	var body map[string]string
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "new title" {
		t.Fatalf("got %+v", body)
	}
	if _, ok := body["body"]; ok {
		t.Fatalf("body field should be omitted when nil, got %+v", body)
	}
}

func TestUpdateIssueRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	body := "new body"
	if err := client.UpdateIssue("o", "r", 1, nil, &body); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAnonymousClientSendsNoAuthorizationHeader(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("[]")})
	client := NewClient(transport, nil)
	if _, err := client.ListIssues("o", "r", "grain-agent"); err != nil {
		t.Fatal(err)
	}
	if _, ok := transport.Calls[0].Headers["Authorization"]; ok {
		t.Fatal("expected no Authorization header")
	}
}

func TestBranchExistsTrueOn200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	exists, err := client.BranchExists("o", "r", "grain/issue-1")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}

func TestBranchExistsFalseOn404(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	exists, err := client.BranchExists("o", "r", "grain/issue-1")
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}

func TestBranchExistsPercentEncodesTheSlashInTheBranchName(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.BranchExists("o", "r", "grain/issue-1"); err != nil {
		t.Fatal(err)
	}
	if transport.Calls[0].Path != "/repos/o/r/branches/grain%2Fissue-1" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

func TestBranchExistsRaisesOnOtherErrors(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.BranchExists("o", "r", "grain/issue-1"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestGetBranchHeadReturnsTheShaAndMessageOn200(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"commit": map[string]any{"sha": "abc123", "commit": map[string]any{"message": "Fix the thing\n\nDetails."}},
	})
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: body})
	client := NewClient(transport, StaticToken{strPtr("t")})
	head, err := client.GetBranchHead("o", "r", "grain/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.SHA != "abc123" || head.Message != "Fix the thing\n\nDetails." {
		t.Fatalf("got %+v", head)
	}
}

func TestGetBranchHeadReturnsNilOn404(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	head, err := client.GetBranchHead("o", "r", "grain/issue-1")
	if err != nil || head != nil {
		t.Fatalf("head=%+v err=%v", head, err)
	}
}

func TestGetBranchHeadRaisesOnOtherErrors(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.GetBranchHead("o", "r", "grain/issue-1"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCompareCommitsReturnsEachCommitOldestFirst(t *testing.T) {
	body := mustJSON(t, map[string]any{"commits": []any{
		map[string]any{
			"sha":     "aaa",
			"commit":  map[string]any{"message": "Add the parser\n\nWhy it was needed."},
			"parents": []any{map[string]any{"sha": "base"}},
		},
		map[string]any{
			"sha":     "bbb",
			"commit":  map[string]any{"message": "Merge main into grain/issue-1"},
			"parents": []any{map[string]any{"sha": "aaa"}, map[string]any{"sha": "main"}},
		},
	}})
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: body})
	client := NewClient(transport, StaticToken{strPtr("t")})
	commits, err := client.CompareCommits("o", "r", "main", "grain/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %+v", commits)
	}
	if commits[0].SHA != "aaa" || commits[0].Message != "Add the parser\n\nWhy it was needed." || commits[0].Merge {
		t.Fatalf("got %+v", commits[0])
	}
	// Two parents is the only thing that makes a commit a merge, and the
	// caller building a description drops exactly those.
	if !commits[1].Merge {
		t.Fatalf("expected a merge commit, got %+v", commits[1])
	}
}

// A branch name's own "/" stays a "/" in the compare path: both refs
// live in one path segment separated by "...", the way github.com's own
// compare URLs spell it.
func TestCompareCommitsKeepsSlashesInRefs(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"commits":[]}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CompareCommits("o", "r", "main", "grain/issue-1"); err != nil {
		t.Fatal(err)
	}
	if got := transport.Calls[0].Path; got != "/repos/o/r/compare/main...grain/issue-1" {
		t.Fatalf("got %q", got)
	}
}

func TestCompareCommitsRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("no such ref")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CompareCommits("o", "r", "main", "gone"); err == nil {
		t.Fatal("expected an error")
	}
}

// CompareRefs is the same request read whole: the standing, the merge
// base and each commit's own parents. The parents are what let a reader
// find the merge that brought a branch into its base once the branch has
// landed and the commits between the two refs have become none.
func TestCompareRefsReportsTheStandingAndTheMergeBase(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"status":            "behind",
		"merge_base_commit": map[string]any{"sha": "fork42"},
		"commits": []any{
			map[string]any{
				"sha":     "bbb",
				"commit":  map[string]any{"message": "Merge pull request #7"},
				"parents": []any{map[string]any{"sha": "base01"}, map[string]any{"sha": "head99"}},
			},
		},
	})
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: body})
	client := NewClient(transport, StaticToken{strPtr("t")})
	comparison, err := client.CompareRefs("o", "r", "main", "grain/task-1")
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Status != "behind" || comparison.MergeBase != "fork42" {
		t.Fatalf("comparison = %+v", comparison)
	}
	if len(comparison.Commits) != 1 ||
		!slices.Equal(comparison.Commits[0].Parents, []string{"base01", "head99"}) {
		t.Fatalf("commits = %+v", comparison.Commits)
	}
	if got := transport.Calls[0].Path; got != "/repos/o/r/compare/main...grain/task-1" {
		t.Fatalf("path = %q", got)
	}
}

func TestCompareDiffRequestsTheRawComparison(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("diff --git a/a b/a\n+new\n")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	diff, err := client.CompareDiff("o", "r", "release/v1", "grain/task-1")
	if err != nil {
		t.Fatal(err)
	}
	if diff != "diff --git a/a b/a\n+new\n" {
		t.Fatalf("diff = %q", diff)
	}
	call := transport.Calls[0]
	if call.Path != "/repos/o/r/compare/release/v1...grain/task-1" {
		t.Errorf("path = %q", call.Path)
	}
	if call.Headers["Accept"] != "application/vnd.github.diff" {
		t.Errorf("Accept = %q, want raw diff media type", call.Headers["Accept"])
	}
	if call.Headers["Authorization"] != "token t" {
		t.Errorf("Authorization = %q, want the repo credential", call.Headers["Authorization"])
	}
}

func TestCompareDiffRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("no such ref")})
	client := NewClient(transport, nil)
	if _, err := client.CompareDiff("o", "r", "main", "gone"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUpdatePullRequestBodyPatchesTheBodyAlone(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.UpdatePullRequestBody("o", "r", 7, "a real description"); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "PATCH" || call.Path != "/repos/o/r/pulls/7" {
		t.Fatalf("got %+v", call)
	}
	var body map[string]string
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["body"] != "a real description" {
		t.Fatalf("got %+v", body)
	}
	// Nothing else: a title (or a state) this call never meant to touch
	// would be sent as its zero value and overwrite what is there.
	if len(body) != 1 {
		t.Fatalf("expected the body field alone, got %+v", body)
	}
}

func TestUpdatePullRequestBodyRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.UpdatePullRequestBody("o", "r", 7, "x"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUpdatePullRequestTitlePatchesTheTitleAlone(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("{}")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.UpdatePullRequestTitle("o", "r", 7, "Fix the off-by-one"); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "PATCH" || call.Path != "/repos/o/r/pulls/7" {
		t.Fatalf("got %+v", call)
	}
	var body map[string]string
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "Fix the off-by-one" {
		t.Fatalf("got %+v", body)
	}
	// The mirror of the assertion above: a description a rename never
	// meant to touch would be sent empty and wipe out the one a reviewer
	// is reading.
	if len(body) != 1 {
		t.Fatalf("expected the title field alone, got %+v", body)
	}
}

func TestUpdatePullRequestTitleRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.UpdatePullRequestTitle("o", "r", 7, "x"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCreateBranchPostsTheRefAndSha(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: []byte(`{"ref":"refs/heads/release/3.1-rc1"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.CreateBranch("o", "r", "release/3.1-rc1", "abc123"); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/git/refs" {
		t.Fatalf("got %s %s", call.Method, call.Path)
	}
	var payload map[string]string
	if err := json.Unmarshal(call.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ref"] != "refs/heads/release/3.1-rc1" || payload["sha"] != "abc123" {
		t.Fatalf("got %+v", payload)
	}
}

func TestCreateBranchRaisesOnANon201(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 422, Body: []byte("Reference already exists")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.CreateBranch("o", "r", "release/3.1-rc1", "abc123"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUpdateBranchPatchesTheShaAndForceFlag(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"ref":"refs/heads/rc"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.UpdateBranch("o", "r", "rc", "abc123", true); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "PATCH" || call.Path != "/repos/o/r/git/refs/heads/rc" {
		t.Fatalf("got %s %s", call.Method, call.Path)
	}
	var payload map[string]any
	if err := json.Unmarshal(call.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["sha"] != "abc123" || payload["force"] != true {
		t.Fatalf("got %+v", payload)
	}
}

func TestUpdateBranchRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 422, Body: []byte("not a fast forward")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.UpdateBranch("o", "r", "rc", "abc123", false); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCreatePullRequestPostsHeadBaseAndTitle(t *testing.T) {
	body := mustJSON(t, map[string]any{"number": 42, "html_url": "https://github.com/o/r/pull/42"})
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: body})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.CreatePullRequest("o", "r", "grain/issue-1", "main", "grain: fix #1", "")
	if err != nil {
		t.Fatal(err)
	}
	if pr != (PullRequest{Number: 42, HTMLURL: "https://github.com/o/r/pull/42"}) {
		t.Fatalf("got %+v", pr)
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/pulls" {
		t.Fatalf("got %+v", call)
	}
	var sent map[string]string
	json.Unmarshal(call.Body, &sent)
	if sent["head"] != "grain/issue-1" || sent["base"] != "main" || sent["title"] != "grain: fix #1" {
		t.Fatalf("got %+v", sent)
	}
}

func TestCreatePullRequestRaisesOnANon201(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 422, Body: []byte("already exists")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CreatePullRequest("o", "r", "grain/issue-1", "main", "x", ""); err == nil {
		t.Fatal("expected an error")
	}
}

func TestFindingTheOpenPRForABranchFiltersByAQualifiedHead(t *testing.T) {
	body := mustJSON(t, []any{map[string]any{"number": 42, "html_url": "https://github.com/o/r/pull/42"}})
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: body})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.FindOpenPullRequestForBranch("o", "r", "grain/issue-1")
	if err != nil || pr == nil || pr.Number != 42 {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	call := transport.Calls[0]
	if call.Method != "GET" || call.Path != "/repos/o/r/pulls?state=open&head=o%3Agrain%2Fissue-1" {
		t.Fatalf("got %+v", call)
	}
}

func TestFindingTheOpenPRForABranchReturnsNoneWhenThereIsNone(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("[]")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.FindOpenPullRequestForBranch("o", "r", "grain/issue-1")
	if err != nil || pr != nil {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
}

func TestFindingTheOpenPRForABranchRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.FindOpenPullRequestForBranch("o", "r", "grain/issue-1"); err == nil {
		t.Fatal("expected an error")
	}
}

// The commit-to-pull-request lookup answers with the merge that
// delivered the commit, and only with that: the endpoint also lists open
// pull requests whose branch happens to contain it, and those did not
// deliver anything.
func TestTheMergedPRForACommitSkipsThePRsThatDidNotDeliverIt(t *testing.T) {
	body := mustJSON(t, []any{
		map[string]any{"number": 41, "html_url": "https://github.com/o/r/pull/41", "merged_at": nil},
		map[string]any{"number": 42, "html_url": "https://github.com/o/r/pull/42", "merged_at": "2026-09-07T01:02:03Z"},
	})
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: body})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.MergedPullRequestForCommit("o", "r", "0123456789abcdef")
	if err != nil || pr == nil || pr.Number != 42 {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	call := transport.Calls[0]
	if call.Method != "GET" || call.Path != "/repos/o/r/commits/0123456789abcdef/pulls?per_page=100" {
		t.Fatalf("got %+v", call)
	}
}

// A commit pushed straight at the branch belongs to no pull request,
// which is an answer rather than a failure.
func TestTheMergedPRForACommitReturnsNoneWhenThereIsNone(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte("[]")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.MergedPullRequestForCommit("o", "r", "0123456789abcdef")
	if err != nil || pr != nil {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
}

func TestTheMergedPRForACommitRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.MergedPullRequestForCommit("o", "r", "0123456789abcdef"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCreateIssuePostsTitleBodyAndLabels(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: mustJSON(t, issueJSONFixture(9, false))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	issue, err := client.CreateIssue("o", "r", "issue 9", "do the thing", []string{"grain-agent-needs-approval"})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 9 || !issue.HasLabel("grain-agent") {
		t.Fatalf("got %+v", issue)
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/issues" {
		t.Fatalf("got %+v", call)
	}
	var sent map[string]any
	json.Unmarshal(call.Body, &sent)
	if sent["title"] != "issue 9" || sent["body"] != "do the thing" {
		t.Fatalf("got %+v", sent)
	}
	labels, _ := sent["labels"].([]any)
	if len(labels) != 1 || labels[0] != "grain-agent-needs-approval" {
		t.Fatalf("got %+v", sent["labels"])
	}
}

func TestCreateIssueOmitsLabelsKeyWhenNoneGiven(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: mustJSON(t, issueJSONFixture(9, false))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CreateIssue("o", "r", "issue 9", "", nil); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	json.Unmarshal(transport.Calls[0].Body, &sent)
	if _, ok := sent["labels"]; ok {
		t.Fatalf("expected no labels key, got %+v", sent)
	}
}

func TestCreateIssueRaisesOnANon201(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 422, Body: []byte("nope")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CreateIssue("o", "r", "x", "", nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestMergePullRequestPutsToTheMergeEndpoint(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"merged": true}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.MergePullRequest("o", "r", 5, ""); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "PUT" || call.Path != "/repos/o/r/pulls/5/merge" {
		t.Fatalf("got %+v", call)
	}
}

// The `sha` field is what makes this a merge of one named commit rather
// than of whatever the head branch points at when GitHub gets round to
// it. orchestrator.syncEntry sends the commit it read CI for, so a push
// that lands in between is refused (409) instead of merged untested.
func TestMergePullRequestPinsTheHeadSHAWhenGivenOne(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"merged": true}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.MergePullRequest("o", "r", 5, "cafef00d"); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(transport.Calls[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["sha"] != "cafef00d" {
		t.Fatalf("body = %+v, want the head sha pinned", sent)
	}
}

// Empty means unpinned: a human clicking merge is not merging a
// particular commit's verdict, and a `sha` GitHub had no reason to check
// could only refuse a merge that was asked for.
func TestMergePullRequestOmitsTheSHAKeyWhenUnpinned(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"merged": true}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.MergePullRequest("o", "r", 5, ""); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(transport.Calls[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["sha"]; ok {
		t.Fatalf("expected no sha key, got %+v", sent)
	}
}

// GitHub's answer to a pinned merge whose branch has moved. It reaches
// the caller as an *Error carrying the status, which is what lets
// orchestrator.SyncPullRequests treat it as "look again next cycle"
// rather than as a broken deployment.
func TestMergePullRequestSurfacesAMovedHeadAsA409(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 409, Body: []byte(
		`{"message":"Head branch was modified. Review and try the merge again."}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	err := client.MergePullRequest("o", "r", 5, "cafef00d")
	if err == nil {
		t.Fatal("expected a moved head to be refused, not merged")
	}
	ghErr, ok := err.(*Error)
	if !ok || ghErr.Status != 409 {
		t.Fatalf("got %v", err)
	}
}

func TestMergePullRequestRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 405, Body: []byte("not mergeable")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	err := client.MergePullRequest("o", "r", 5, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	ghErr, ok := err.(*Error)
	if !ok || ghErr.Status != 405 {
		t.Fatalf("got %v", err)
	}
}

func TestMergePullRequestFallsBackToSquashWhenMergeCommitsDisallowed(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{
			Status: 405,
			Body:   []byte(`{"message":"Merge commits are not allowed on this repository.","documentation_url":"https://docs.github.com/enterprise-server@3.20/rest/pulls/pulls#merge-a-pull-request","status":"405"}`),
		},
		{Status: 200, Body: []byte(`{"merged": true}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.MergePullRequest("o", "r", 5, "cafef00d"); err != nil {
		t.Fatalf("expected squash fallback to succeed, got %v", err)
	}
	if len(transport.Calls) != 2 {
		t.Fatalf("made %d calls, want 2: %+v", len(transport.Calls), transport.Calls)
	}
	var first, second map[string]any
	if err := json.Unmarshal(transport.Calls[0].Body, &first); err != nil {
		t.Fatal(err)
	}
	if _, ok := first["merge_method"]; ok {
		t.Fatalf("first call should omit merge_method, got %+v", first)
	}
	if first["sha"] != "cafef00d" {
		t.Fatalf("first call body = %+v, want sha pinned", first)
	}
	if err := json.Unmarshal(transport.Calls[1].Body, &second); err != nil {
		t.Fatal(err)
	}
	if second["merge_method"] != "squash" || second["sha"] != "cafef00d" {
		t.Fatalf("second call body = %+v, want merge_method=squash and sha preserved", second)
	}
}

func TestMergePullRequestWithMessageFallsBackToSquashCarryingTitleAndMessage(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{
			Status: 405,
			Body:   []byte(`{"message":"Merge commits are not allowed on this repository.","status":"405"}`),
		},
		{Status: 200, Body: []byte(`{"merged": true}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	err := client.MergePullRequestWithMessage("o", "r", 5, "cafef00d", "fix: title", "Origin: task 150\nFiled-by: task 150")
	if err != nil {
		t.Fatalf("expected squash fallback to succeed, got %v", err)
	}
	if len(transport.Calls) != 2 {
		t.Fatalf("made %d calls, want 2: %+v", len(transport.Calls), transport.Calls)
	}
	var second map[string]any
	if err := json.Unmarshal(transport.Calls[1].Body, &second); err != nil {
		t.Fatal(err)
	}
	if second["merge_method"] != "squash" ||
		second["sha"] != "cafef00d" ||
		second["commit_title"] != "fix: title" ||
		second["commit_message"] != "Origin: task 150\nFiled-by: task 150" {
		t.Fatalf("second call body = %+v, want merge_method=squash with sha, commit_title, and commit_message preserved", second)
	}
}

func TestMergePullRequestWithMessageFallsBackToRebaseAndSurfaces405WhenAllFail(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 405, Body: []byte(`{"message":"Merge commits are not allowed on this repository."}`)},
		{Status: 405, Body: []byte(`{"message":"Squash commits are not allowed on this repository."}`)},
		{Status: 200, Body: []byte(`{"merged": true}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.MergePullRequestWithMessage("o", "r", 5, "cafef00d", "title", "msg"); err != nil {
		t.Fatalf("expected rebase fallback to succeed, got %v", err)
	}
	if len(transport.Calls) != 3 {
		t.Fatalf("made %d calls, want 3: %+v", len(transport.Calls), transport.Calls)
	}
	var third map[string]any
	if err := json.Unmarshal(transport.Calls[2].Body, &third); err != nil {
		t.Fatal(err)
	}
	if third["merge_method"] != "rebase" || third["sha"] != "cafef00d" || third["commit_title"] != "title" || third["commit_message"] != "msg" {
		t.Fatalf("third call body = %+v, want merge_method=rebase with fields preserved", third)
	}

	transportFail := &FakeTransport{Responses: []ApiResponse{
		{Status: 405, Body: []byte(`{"message":"Merge commits are not allowed on this repository."}`)},
		{Status: 405, Body: []byte(`{"message":"Squash commits are not allowed on this repository."}`)},
		{Status: 405, Body: []byte(`{"message":"Rebase commits are not allowed on this repository."}`)},
	}}
	clientFail := NewClient(transportFail, StaticToken{strPtr("t")})
	err := clientFail.MergePullRequestWithMessage("o", "r", 5, "cafef00d", "title", "msg")
	if err == nil {
		t.Fatal("expected an error when all merge methods fail")
	}
	if ClassifyMergeRefusal(err) != RefusalRule {
		t.Fatalf("ClassifyMergeRefusal(%v) = %q, want %q", err, ClassifyMergeRefusal(err), RefusalRule)
	}
}

// The merges endpoint's three answers are three status codes, which is
// the whole reason the merge queue reaches for it rather than for the
// asynchronous update-branch endpoint. Each one is load-bearing
// separately, so each gets its own case: a 204 misread as "merged" burns
// the one refresh attempt without refreshing anything, and a 409 misread
// as an ordinary error turns a genuine conflict into a cycle that errors
// forever instead of filing a fix.
func TestMergeBranchPostsToTheMergesEndpoint(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: []byte(`{"sha":"cafef00d"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	got, err := client.MergeBranch("o", "r", "grain/task-27", "main", "Merge main into grain/task-27")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Merged || got.SHA != "cafef00d" {
		t.Fatalf("got %+v, want the merge commit reported", got)
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/merges" {
		t.Fatalf("got %+v", call)
	}
	var sent map[string]any
	if err := json.Unmarshal(call.Body, &sent); err != nil {
		t.Fatal(err)
	}
	// base is what is being caught up, head is what is merged into it --
	// the opposite way round from a pull request, and the mistake worth
	// pinning.
	if sent["base"] != "grain/task-27" || sent["head"] != "main" {
		t.Fatalf("body = %+v, want the base branch merged into the head branch", sent)
	}
	if sent["commit_message"] != "Merge main into grain/task-27" {
		t.Fatalf("body = %+v, want the caller's own commit message", sent)
	}
}

// 204: the base already contained the head. Nothing was written, and the
// caller has its authoritative answer to "was this branch behind at all"
// -- not an error, and not a merge.
func TestMergeBranchReportsAnUpToDateBranchAsNotMerged(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 204})
	client := NewClient(transport, StaticToken{strPtr("t")})
	got, err := client.MergeBranch("o", "r", "grain/task-27", "main", "")
	if err != nil {
		t.Fatalf("204 is an answer, not a failure: %v", err)
	}
	if got.Merged || got.SHA != "" {
		t.Fatalf("got %+v, want nothing merged", got)
	}
}

func TestMergeBranchOmitsAnEmptyCommitMessage(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 204})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.MergeBranch("o", "r", "grain/task-27", "main", ""); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(transport.Calls[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["commit_message"]; ok {
		t.Fatalf("body = %+v, want no commit_message key when none was given", sent)
	}
}

func TestMergeBranchSurfacesAConflictAsA409(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 409, Body: []byte(`{"message":"Merge conflict"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	_, err := client.MergeBranch("o", "r", "grain/task-27", "main", "")
	if err == nil {
		t.Fatal("expected a conflicting merge to be refused")
	}
	if !IsMergeConflict(err) {
		t.Fatalf("got %v, want a conflict this caller can tell from a transient failure", err)
	}
}

// Everything else stays an ordinary error, and specifically is not a
// conflict: 404 is what a fork pull request's head branch reads as (it
// lives in another repository), and the caller's answer to that is "leave
// this one alone", not "resolve a conflict".
func TestMergeBranchRaisesOnAnyOtherStatus(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	_, err := client.MergeBranch("o", "r", "grain/task-27", "main", "")
	ghErr, ok := err.(*Error)
	if !ok || ghErr.Status != 404 {
		t.Fatalf("got %v, want a 404 *Error", err)
	}
	if IsMergeConflict(err) {
		t.Fatal("a 404 read as a merge conflict")
	}
}

func checkRunsJSON(runs ...CheckRun) map[string]any {
	items := make([]map[string]any, len(runs))
	for i, r := range runs {
		items[i] = map[string]any{"name": r.Name, "status": r.Status, "conclusion": r.Conclusion}
	}
	return map[string]any{"total_count": len(runs), "check_runs": items}
}

func TestListCheckRunsReadsTheCheckRunShape(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, checkRunsJSON(
		CheckRun{Name: "build", Status: "completed", Conclusion: strPtr("failure")},
	))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.ListCheckRuns("o", "r", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Name != "build" || *runs[0].Conclusion != "failure" {
		t.Fatalf("got %+v", runs)
	}
	if transport.Calls[0].Path != "/repos/o/r/commits/feature-x/check-runs?per_page=100" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

func TestListCheckRunsToleratesAStillRunningCheckWithNoConclusion(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, checkRunsJSON(
		CheckRun{Name: "build", Status: "in_progress", Conclusion: nil},
	))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.ListCheckRuns("o", "r", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Conclusion != nil {
		t.Fatalf("got %+v", runs)
	}
}

func TestListCheckRunsFollowsLinkHeaderPagination(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/commits/feature-x/check-runs?page=2>; rel="next"`},
			Body: mustJSON(t, checkRunsJSON(CheckRun{Name: "a", Status: "completed", Conclusion: strPtr("success")}))},
		{Status: 200, Body: mustJSON(t, checkRunsJSON(CheckRun{Name: "b", Status: "completed", Conclusion: strPtr("success")}))},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.ListCheckRuns("o", "r", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Name != "a" || runs[1].Name != "b" {
		t.Fatalf("got %+v", runs)
	}
}

// The Actions fallback for a credential that cannot reach the Checks API.
// Its response is wrapped in "workflow_runs" rather than being a bare
// array, and it is mapped onto CheckRun so a caller handles one shape.
func TestListWorkflowRunsReadsTheWorkflowRunShape(t *testing.T) {
	body := map[string]any{"total_count": 2, "workflow_runs": []any{
		map[string]any{"name": "tests", "status": "completed", "conclusion": "success"},
		map[string]any{"name": "lint", "status": "completed", "conclusion": "failure"},
	}}
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.ListWorkflowRuns("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Name != "tests" || runs[0].Status != "completed" || *runs[0].Conclusion != "success" {
		t.Errorf("first run = %+v", runs[0])
	}
	if runs[1].Conclusion == nil || *runs[1].Conclusion != "failure" {
		t.Errorf("second run = %+v, want a failure conclusion", runs[1])
	}
}

// Scoped to a commit, never a branch: runs for an older commit on the
// same branch must not stand in for the commit the PR points at now.
func TestListWorkflowRunsScopesToTheHeadSHA(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"workflow_runs":[]}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.ListWorkflowRuns("o", "r", "abc123"); err != nil {
		t.Fatal(err)
	}
	want := "/repos/o/r/actions/runs?head_sha=abc123&per_page=100"
	if transport.Calls[0].Path != want {
		t.Fatalf("got %s, want %s", transport.Calls[0].Path, want)
	}
}

func TestListWorkflowRunsToleratesAStillRunningRunWithNoConclusion(t *testing.T) {
	body := map[string]any{"workflow_runs": []any{
		map[string]any{"name": "tests", "status": "in_progress", "conclusion": nil},
	}}
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.ListWorkflowRuns("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Conclusion != nil || runs[0].Status != "in_progress" {
		t.Fatalf("got %+v", runs)
	}
}

func TestListWorkflowRunsFollowsLinkHeaderPagination(t *testing.T) {
	transport := NewFakeTransport(
		ApiResponse{Status: 200,
			Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/actions/runs?page=2>; rel="next"`},
			Body:    []byte(`{"workflow_runs":[{"name":"tests","status":"completed","conclusion":"success"}]}`)},
		ApiResponse{Status: 200,
			Body: []byte(`{"workflow_runs":[{"name":"lint","status":"completed","conclusion":"success"}]}`)},
	)
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.ListWorkflowRuns("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs across two pages, want 2", len(runs))
	}
}

// A 403 here has to reach the caller as a *github.Error so
// IsPermissionDenied can classify it -- that is what tells checkRunsFor
// the Actions read is unavailable too, rather than merely empty.
func TestListWorkflowRunsRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 403,
		Body: []byte(`{"message":"Resource not accessible by personal access token"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	_, err := client.ListWorkflowRuns("o", "r", "abc123")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsPermissionDenied(err) {
		t.Errorf("a 403 from the Actions API was not classified as a permission denial: %v", err)
	}
}

// The one read here that is not scoped to a commit: how one workflow
// file has been running lately, which is the only way to ask whether a
// check has *ever* worked. A deployment asks it about the check it
// installs on its own state repository, where "every finished run of it
// died before a job started" is a repository that cannot run Actions at
// all rather than a change that does not load.
func TestWorkflowFileRunsAsksAboutOneWorkflowByName(t *testing.T) {
	body := map[string]any{"total_count": 2, "workflow_runs": []any{
		map[string]any{"name": "grain state check", "status": "completed",
			"conclusion": "startup_failure", "html_url": "https://github.com/o/r/actions/runs/9"},
		map[string]any{"name": "grain state check", "status": "queued", "conclusion": nil},
	}}
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.WorkflowFileRuns("o", "r", "grain-state-check.yml", 20)
	if err != nil {
		t.Fatal(err)
	}
	// One page, and one page only: this is asked on a timer, so a
	// workflow with thousands of runs behind it must not become a walk of
	// all of them.
	want := "/repos/o/r/actions/workflows/grain-state-check.yml/runs?per_page=20"
	if len(transport.Calls) != 1 || transport.Calls[0].Path != want {
		t.Fatalf("asked %+v, want one read of %s", transport.Calls, want)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Conclusion == nil || *runs[0].Conclusion != StartupFailure {
		t.Errorf("first run = %+v, want a startup failure", runs[0])
	}
	if runs[0].HTMLURL != "https://github.com/o/r/actions/runs/9" {
		t.Errorf("the run's own page did not survive: %+v", runs[0])
	}
	if runs[1].Conclusion != nil {
		t.Errorf("a queued run came back with a conclusion: %+v", runs[1])
	}
}

// A workflow GitHub has never registered -- a file on a branch Actions
// does not watch, a repository with Actions switched off -- is no runs
// rather than an error. The caller's question is only ever about runs
// that exist, and neither of those is evidence of anything.
func TestWorkflowFileRunsReadsA404AsNoRuns(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	runs, err := client.WorkflowFileRuns("o", "r", "grain-state-check.yml", 20)
	if err != nil {
		t.Fatalf("a 404 for a workflow that has never run: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("got %+v, want no runs", runs)
	}
}

// Anything else does reach the caller: a credential that may not read
// Actions is a condition of its own, and swallowing it would leave a
// deployment quietly not watching its own check.
func TestWorkflowFileRunsRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 403,
		Body: []byte(`{"message":"Resource not accessible by personal access token"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.WorkflowFileRuns("o", "r", "grain-state-check.yml", 20); !IsPermissionDenied(err) {
		t.Errorf("a 403 from the Actions API was not classified as a permission denial: %v", err)
	}
}

// The same question asked of a whole repository rather than of one
// workflow file: does anything here get a job started at all. It is what
// the merge queue asks once a pull request has failed before any job of
// it ran, to tell "this repository's checks have never started" from
// "this one run did not" (orchestrator's checkstartups.go).
//
// A run that succeeded is decisive on its own and costs no second read.
// A run concluded plain `failure` is the ambiguous one -- a job that ran
// and broke, or a job GitHub created and refused to start -- so its jobs
// are read, and a job with a completed step behind it settles it.
func TestRecentRunStartsSeesARunThatGotAJobStarted(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(`{"workflow_runs":[
			{"id":9,"status":"completed","conclusion":"startup_failure",
			 "html_url":"https://github.com/o/r/actions/runs/9"},
			{"id":42,"status":"completed","conclusion":"failure",
			 "html_url":"https://github.com/o/r/actions/runs/42"},
			{"id":43,"status":"queued","conclusion":null}
		]}`)},
		{Status: 200, Body: []byte(`{"jobs":[
			{"id":7,"name":"go","status":"completed","conclusion":"failure",
			 "steps":[{"name":"Test","status":"completed","conclusion":"failure"}]}
		]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	starts, err := client.RecentRunStarts("o", "r", RunStartsRead)
	if err != nil {
		t.Fatal(err)
	}
	// One page for the runs, and one jobs read for the one run whose
	// conclusion did not answer the question by itself.
	want := []string{
		"/repos/o/r/actions/runs?per_page=20",
		"/repos/o/r/actions/runs/42/jobs?per_page=100",
	}
	var paths []string
	for _, c := range transport.Calls {
		paths = append(paths, c.Path)
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("asked %q, want %q", paths, want)
	}
	// The queued run is counted as nothing: it has said nothing yet.
	if starts.Finished != 2 || starts.Started != 1 || starts.NeverStarted != 1 {
		t.Errorf("got %+v, want two finished runs, one started, one not", starts)
	}
	if starts.RunURL != "https://github.com/o/r/actions/runs/9" {
		t.Errorf("the never-started run's own page did not survive: %+v", starts)
	}
	// And the verdict is no: one run here did get a job started, so
	// whatever is failing is failing inside a job.
	if starts.ChecksNeverStarted() {
		t.Error("a repository with a run that started a job was called dead")
	}
}

// The shape a repository out of Actions minutes actually produces: GitHub
// creates the jobs, refuses to start them, and concludes the run plain
// `failure`. Nothing but the empty `steps` array separates that from an
// ordinary red build, which is why the jobs are read at all -- and it is
// the case the verdict most needs to catch, since it is the one that
// costs an account money to fix.
func TestRecentRunStartsCountsAJobGitHubCreatedAndNeverStarted(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(`{"workflow_runs":[
			{"id":42,"status":"completed","conclusion":"failure",
			 "html_url":"https://github.com/o/r/actions/runs/42"}
		]}`)},
		{Status: 200, Body: []byte(`{"jobs":[
			{"id":7,"name":"go","status":"completed","conclusion":"failure","steps":[]}
		]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	starts, err := client.RecentRunStarts("o", "r", RunStartsRead)
	if err != nil {
		t.Fatal(err)
	}
	if starts.Finished != 1 || starts.Started != 0 || starts.NeverStarted != 1 {
		t.Errorf("got %+v, want the one run counted as never started", starts)
	}
	if !starts.ChecksNeverStarted() {
		t.Errorf("got %+v, want the verdict that nothing here starts a job", starts)
	}
}

// A repository with Actions switched off answers 404, and so does one
// this credential may not read Actions on. Both are "grain cannot say",
// which is the zero value and never the verdict: a claim that a
// repository's checks have never started, made because grain could not
// look, would send somebody to change a setting that was never the
// problem.
func TestRecentRunStartsSaysNothingWhenActionsCannotBeRead(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	starts, err := client.RecentRunStarts("o", "r", RunStartsRead)
	if err != nil {
		t.Fatalf("a 404 from the runs endpoint should be no runs, not an error: %v", err)
	}
	if starts != (RunStarts{}) || starts.ChecksNeverStarted() {
		t.Errorf("got %+v, want nothing said either way", starts)
	}
}

// Cancelled runs are finished and are evidence of nothing: nobody can
// tell from one whether a job of it ever started, and a repository whose
// only recent history is cancellations has told grain nothing to repeat
// to an operator.
func TestRecentRunStartsHoldsNoOpinionOnCancelledRuns(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: []byte(`{"workflow_runs":[
		{"id":42,"status":"completed","conclusion":"cancelled",
		 "html_url":"https://github.com/o/r/actions/runs/42"}
	]}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	starts, err := client.RecentRunStarts("o", "r", RunStartsRead)
	if err != nil {
		t.Fatal(err)
	}
	if starts.Finished != 1 || starts.Started != 0 || starts.NeverStarted != 0 {
		t.Errorf("got %+v, want a finished run counted and nothing concluded from it", starts)
	}
	if starts.ChecksNeverStarted() {
		t.Error("a repository whose runs were cancelled was called dead")
	}
}

// FailedJobLogs is three reads deep -- a commit's runs, a failed run's
// jobs, a failed job's log -- because GitHub offers no path from a commit
// to a log. Each step filters to what failed, so what a caller gets back
// is the output of the jobs that broke and nothing else.
func TestFailedJobLogsWalksFromACommitToTheFailingJobsLog(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"tests","status":"completed","conclusion":"failure"}]}`)},
		{Status: 200, Body: []byte(`{"jobs":[
			{"id":7,"name":"go","status":"completed","conclusion":"failure",
			 "html_url":"https://github.com/o/r/actions/runs/42/job/7"},
			{"id":8,"name":"terraform","status":"completed","conclusion":"success",
			 "html_url":"https://github.com/o/r/actions/runs/42/job/8"}
		]}`)},
		{Status: 200, Body: []byte("--- FAIL: TestThing (0.00s)\nFAIL\n")},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v, want the one failed job", logs)
	}
	if logs[0].Name != "go" || logs[0].URL != "https://github.com/o/r/actions/runs/42/job/7" {
		t.Errorf("got %+v", logs[0])
	}
	if !strings.Contains(logs[0].Log, "--- FAIL: TestThing") || logs[0].Truncated {
		t.Errorf("got %q (truncated=%v), want the whole short log", logs[0].Log, logs[0].Truncated)
	}
	want := []string{
		"/repos/o/r/actions/runs?head_sha=abc123&per_page=100",
		"/repos/o/r/actions/runs/42/jobs?per_page=100",
		"/repos/o/r/actions/jobs/7/logs",
	}
	for i, path := range want {
		if len(transport.Calls) <= i || transport.Calls[i].Path != path {
			t.Fatalf("call %d = %+v, want %s", i, transport.Calls, path)
		}
	}
}

// A run that passed is not asked for its jobs at all: on a repo whose
// pull requests are mostly green, this is the difference between one
// call per sync and a call per job.
func TestFailedJobLogsDoesNotOpenARunThatPassed(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"tests","status":"completed","conclusion":"success"}]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("got %+v, want nothing for a green commit", logs)
	}
	if len(transport.Calls) != 1 {
		t.Fatalf("made %d calls, want only the runs read: %+v", len(transport.Calls), transport.Calls)
	}
}

// GitHub deletes job logs after its retention window and answers 410 for
// them afterwards, and a credential without Actions read gets a 403. The
// job's name is already known either way; only its log is missing, so
// that one job is skipped rather than the whole read failing -- and the
// run it belongs to is reported in its place, since "one failing job,
// log unavailable" is a different thing to tell a reader than silence.
func TestFailedJobLogsSkipsAJobWhoseLogGitHubWillNotServe(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"tests","status":"completed","conclusion":"failure",
			 "html_url":"https://github.com/o/r/actions/runs/42"}]}`)},
		{Status: 200, Body: []byte(
			`{"jobs":[{"id":7,"name":"go","status":"completed","conclusion":"failure"}]}`)},
		{Status: 410, Body: []byte(`{"message":"Not Found"}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatalf("an expired log failed the whole read: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v, want the run itself in place of the log it has none of", logs)
	}
	got := logs[0]
	if !got.IsRunFailure() || got.Log != "" {
		t.Errorf("got %+v, want an entry standing for the run rather than a job's output", got)
	}
	if got.RunConclusion != "failure" || got.RunJobsFailed != 1 {
		t.Errorf("got conclusion %q over %d failed job(s), want the run's own verdict",
			got.RunConclusion, got.RunJobsFailed)
	}
	if got.Name != "tests" || got.URL != "https://github.com/o/r/actions/runs/42" {
		t.Errorf("got %+v, want the workflow's name and the run's own page", got)
	}
}

// A run that failed before it started a job -- an unparseable workflow
// file, Actions turned off for the repository, no minutes left on a
// private repository -- has no job to walk to and so, for the whole life
// of this function until now, produced nothing at all. Every reader then
// said the same thing about it as about a third-party CI's check: no
// Actions log here, go and reproduce it. Nothing in the tree reproduces
// it. So the run is reported as itself, conclusion included.
func TestFailedJobLogsReportsARunThatFailedBeforeAnyJobStarted(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"grain state check","status":"completed",
			 "conclusion":"startup_failure",
			 "html_url":"https://github.com/o/r/actions/runs/42"}]}`)},
		{Status: 200, Body: []byte(`{"total_count":0,"jobs":[]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v, want the failed run reported despite having no job", logs)
	}
	got := logs[0]
	if got.RunConclusion != StartupFailure || got.RunJobsFailed != 0 {
		t.Errorf("got %+v, want the startup failure and no failed job under it", got)
	}
	if got.Name != "grain state check" || got.URL != "https://github.com/o/r/actions/runs/42" {
		t.Errorf("got %+v, want the workflow's name and the run's own page", got)
	}
	if len(transport.Calls) != 2 {
		t.Errorf("made %d calls, want the runs read and the jobs read: %+v",
			len(transport.Calls), transport.Calls)
	}
}

// The third shape, and the one that cost three separate runs before it
// was told apart from the second: GitHub created the jobs and then
// declined to start them -- no Actions minutes, a spending limit reached,
// no runner for the labels asked for -- so it concluded them `failure`
// with an empty `steps` array and never wrote a log. Read as "jobs failed,
// logs unreadable" that is an instruction to go and reproduce a failure
// that has never once depended on the commit. The steps say otherwise,
// and the check run behind the job says why in one sentence.
func TestFailedJobLogsReportsAJobGitHubNeverStarted(t *testing.T) {
	reason := "The job was not started because recent account payments have failed " +
		"or your spending limit needs to be increased"
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"grain can load this","status":"completed",
			 "conclusion":"failure","html_url":"https://github.com/o/r/actions/runs/42"}]}`)},
		{Status: 200, Body: []byte(`{"jobs":[{"id":7,"name":"load","status":"completed",
			 "conclusion":"failure","steps":[],
			 "check_run_url":"https://api.github.com/repos/o/r/check-runs/99"}]}`)},
		// No log, exactly as for an expired one -- which is what made
		// these two indistinguishable.
		{Status: 410, Body: []byte(`{"message":"Not Found"}`)},
		{Status: 200, Body: mustJSON(t, map[string]any{
			"output": map[string]any{"title": reason, "summary": nil},
		})},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v, want the run reported in place of a job that never ran", logs)
	}
	got := logs[0]
	if !got.RunJobsNeverStarted || !got.NoJobStarted() {
		t.Errorf("got %+v, want a run whose failing job never ran a step", got)
	}
	if got.RunFailureReason != reason {
		t.Errorf("carried reason %q, want GitHub's own %q", got.RunFailureReason, reason)
	}
	if !strings.Contains(RunFailureNote(got), reason) {
		t.Errorf("the note a reader is shown drops GitHub's reason:\n%s", RunFailureNote(got))
	}
	if strings.Contains(RunFailureNote(got), "reproduce the failure in a checkout") {
		t.Errorf("the note still sends a reader at a failure no checkout reproduces:\n%s",
			RunFailureNote(got))
	}
	want := "/repos/o/r/check-runs/99"
	if last := transport.Calls[len(transport.Calls)-1].Path; last != want {
		t.Errorf("last call was %s, want the check run behind the job (%s)", last, want)
	}
}

// The other half of the same read: a check run whose output is empty (as
// most are) has the sentence in an annotation instead, which is what the
// banner at the top of a run page is drawn from.
func TestFailedJobLogsReadsTheReasonFromACheckRunsAnnotations(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"ci","status":"completed","conclusion":"failure"}]}`)},
		{Status: 200, Body: []byte(`{"jobs":[{"id":7,"name":"go","status":"completed",
			 "conclusion":"failure","steps":[],
			 "check_run_url":"https://api.github.com/repos/o/r/check-runs/99"}]}`)},
		{Status: 410, Body: []byte(`{"message":"Not Found"}`)},
		{Status: 200, Body: []byte(`{"output":{"title":null,"summary":null}}`)},
		{Status: 200, Body: []byte(
			`[{"title":"No runner","message":"No runner matching the labels was found"}]`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v", logs)
	}
	if !strings.Contains(logs[0].RunFailureReason, "No runner matching the labels") {
		t.Errorf("got reason %q, want the annotation's own message", logs[0].RunFailureReason)
	}
}

// The check run is a bonus on top of an answer that stands up without it,
// exactly as the logs are: reading it needs the Checks API, which no
// fine-grained token can be granted at all (see ListWorkflowRuns). A
// refusal there must leave the report as it was rather than fail it --
// and the classification, which comes from the jobs API and not from
// this, must survive it.
func TestFailedJobLogsKeepsGoingWhenTheCheckRunCannotBeRead(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"ci","status":"completed","conclusion":"failure"}]}`)},
		{Status: 200, Body: []byte(`{"jobs":[{"id":7,"name":"go","status":"completed",
			 "conclusion":"failure","steps":[],
			 "check_run_url":"https://api.github.com/repos/o/r/check-runs/99"}]}`)},
		{Status: 410, Body: []byte(`{"message":"Not Found"}`)},
		{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatalf("a refused check-run read failed the whole report: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v", logs)
	}
	if logs[0].RunFailureReason != "" {
		t.Errorf("invented a reason out of a refusal: %q", logs[0].RunFailureReason)
	}
	if !logs[0].NoJobStarted() {
		t.Error("a job with no steps stopped reading as one that never started " +
			"because its check run was refused")
	}
}

// And the case that must not move: jobs that ran steps and failed, whose
// logs are simply gone. That is a real failure of this commit's tree, the
// wording for it is different, and it is what the merge queue spends its
// one automatic repair on.
func TestFailedJobLogsStillReportsAJobThatRanAndLostItsLog(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"ci","status":"completed","conclusion":"failure"}]}`)},
		{Status: 200, Body: []byte(`{"jobs":[{"id":7,"name":"go","status":"completed",
			 "conclusion":"failure","check_run_url":"https://api.github.com/repos/o/r/check-runs/99",
			 "steps":[{"name":"Set up Go","status":"completed"},
			          {"name":"go test","status":"completed"}]}]}`)},
		{Status: 410, Body: []byte(`{"message":"Not Found"}`)},
		{Status: 200, Body: []byte(`{"output":{"title":null,"summary":null}}`)},
		{Status: 200, Body: []byte(`[]`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v", logs)
	}
	got := logs[0]
	if got.RunJobsNeverStarted || got.NoJobStarted() {
		t.Errorf("got %+v, want a job that ran steps to stay a failure the tree can own", got)
	}
	if !strings.Contains(RunFailureNote(got), "reproduce the failure in a checkout") {
		t.Errorf("a genuinely missing log lost its own wording:\n%s", RunFailureNote(got))
	}
}

// A check run's summary is free-form markdown and some actions post a
// whole report into it. What this field is for is one sentence, and it
// travels into a task body and an agent's context window, so it is cut --
// at a line boundary, and visibly.
func TestFailedJobLogsBoundsTheReasonItCarries(t *testing.T) {
	huge := strings.Repeat("a line of somebody's check-run report\n", 500)
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"ci","status":"completed","conclusion":"failure"}]}`)},
		{Status: 200, Body: []byte(`{"jobs":[{"id":7,"name":"go","status":"completed",
			 "conclusion":"failure","steps":[],
			 "check_run_url":"https://api.github.com/repos/o/r/check-runs/99"}]}`)},
		{Status: 410, Body: []byte(`{"message":"Not Found"}`)},
		{Status: 200, Body: mustJSON(t, map[string]any{
			"output": map[string]any{"title": "Report", "summary": huge},
		})},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	got := logs[0].RunFailureReason
	if len(got) > runFailureReasonBytes+len(" [...]") {
		t.Errorf("carried %d bytes of check-run summary, want at most %d",
			len(got), runFailureReasonBytes)
	}
	if !strings.HasSuffix(got, "[...]") {
		t.Errorf("cut the reason without saying so: %q", got[max(0, len(got)-60):])
	}
	if !strings.HasPrefix(got, "Report\n") {
		t.Errorf("dropped the title, which is the part most likely to be the reason: %q", got[:20])
	}
}

// OnlyStartupFailures is the question a caller with an automatic repair
// to spend asks of these entries -- "could any commit make this build
// pass" -- so what matters is exactly where it stops saying yes: a build
// with anything else red in it has something a commit could own.
func TestOnlyStartupFailures(t *testing.T) {
	startup := JobLog{Name: "tests", RunConclusion: StartupFailure}
	for _, tc := range []struct {
		name string
		logs []JobLog
		want bool
	}{
		{"one run that never started", []JobLog{startup}, true},
		{"two of them", []JobLog{startup, startup}, true},
		{
			// The only difference between this and the case above, and
			// the one that matters: jobs ran and failed, and their logs
			// were simply unreadable.
			"a failed run whose jobs have no readable log",
			[]JobLog{{Name: "tests", RunConclusion: "failure", RunJobsFailed: 2}},
			false,
		},
		{
			"a startup failure alongside a job that really failed",
			[]JobLog{startup, {Name: "go test", Log: "--- FAIL: TestThing"}},
			false,
		},
		{
			// GitHub does not answer this, but the type holds it: jobs
			// under the run mean jobs ran.
			"a startup failure with failed jobs under it",
			[]JobLog{{Name: "tests", RunConclusion: StartupFailure, RunJobsFailed: 1}},
			false,
		},
		{
			// GitHub does not call this one a startup failure -- the run
			// is plain `failure` -- but no job of it ran a step, so no
			// commit pushed at it changes the answer either.
			"a run whose failing jobs GitHub never started",
			[]JobLog{{Name: "tests", RunConclusion: "failure", RunJobsFailed: 1,
				RunJobsNeverStarted: true}},
			true,
		},
		{
			"one of those beside a job that really failed",
			[]JobLog{
				{Name: "tests", RunConclusion: "failure", RunJobsFailed: 1,
					RunJobsNeverStarted: true},
				{Name: "go test", Log: "--- FAIL: TestThing"},
			},
			false,
		},
		{
			// Not Actions at all, or a credential that may not read it.
			// The absence of an answer is not an answer.
			"nothing read", nil, false,
		},
	} {
		if got := OnlyStartupFailures(tc.logs); got != tc.want {
			t.Errorf("OnlyStartupFailures(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A job's log is the whole job, every step of it, and the thing that
// broke is at the end -- so what is kept is the tail, and it starts on a
// line boundary rather than halfway through whichever line the byte
// count landed in.
func TestFailedJobLogsKeepsTheTailAtALineBoundary(t *testing.T) {
	long := strings.Repeat("a line of build output that says nothing\n", 2000) +
		"--- FAIL: TestThing (0.00s)\nFAIL\n"
	if len(long) <= JobLogTailBytes {
		t.Fatalf("this test needs a log longer than %d bytes, got %d", JobLogTailBytes, len(long))
	}
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"tests","status":"completed","conclusion":"failure"}]}`)},
		{Status: 200, Body: []byte(
			`{"jobs":[{"id":7,"name":"go","status":"completed","conclusion":"failure"}]}`)},
		{Status: 200, Body: []byte(long)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %+v", logs)
	}
	got := logs[0]
	if !got.Truncated {
		t.Error("a log longer than the tail was not reported as truncated")
	}
	if len(got.Log) > JobLogTailBytes {
		t.Errorf("kept %d bytes, want at most %d", len(got.Log), JobLogTailBytes)
	}
	if !strings.HasPrefix(got.Log, "a line of build output") {
		t.Errorf("the tail opens mid-line: %q", got.Log[:40])
	}
	if !strings.HasSuffix(got.Log, "--- FAIL: TestThing (0.00s)\nFAIL\n") {
		t.Error("the tail dropped the end of the log, which is the part that failed")
	}
}

// What a reader is shown, rather than what crossed the wire. A job log is
// the whole job, tens of thousands of lines of setup and cache restore
// included, so what goes into a fix task's body or an mcp CI answer is
// its last few -- the part where something broke -- with the timestamp
// Actions repeats on every line taken off.
func TestJobLogExcerptKeepsTheLastLinesWithoutTheirTimestamps(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 500; i++ {
		log.WriteString("2026-01-02T03:04:05.1234567Z line ")
		log.WriteString(strconv.Itoa(i))
		log.WriteString("\n")
	}
	got := JobLogExcerpt(log.String())

	lines := strings.Split(got, "\n")
	if len(lines) != JobLogTailLines {
		t.Fatalf("kept %d lines, want %d", len(lines), JobLogTailLines)
	}
	if lines[0] != "line 420" {
		t.Errorf("first kept line = %q, want the %dth from the end", lines[0], JobLogTailLines)
	}
	if lines[len(lines)-1] != "line 499" {
		t.Errorf("last kept line = %q, want the end of the log", lines[len(lines)-1])
	}
}

// A log shorter than the cap keeps every line it has, and a log that
// isn't from Actions at all (a third-party CI's, one day) keeps its lines
// exactly as they came.
func TestJobLogExcerptLeavesAShortUnstampedLogAlone(t *testing.T) {
	const log = "--- FAIL: TestThing (0.00s)\n    a_test.go:12: got 3, want 4\nFAIL\n"
	if got := JobLogExcerpt(log); got != strings.TrimRight(log, "\n") {
		t.Fatalf("JobLogExcerpt = %q, want it unchanged", got)
	}
}

// No commit, nothing to scope to -- the same case checkRunsFor's own
// Actions fallback declines to widen into a branch-scoped read.
func TestFailedJobLogsAsksNothingWithoutAHeadSHA(t *testing.T) {
	transport := NewFakeTransport()
	client := NewClient(transport, StaticToken{strPtr("t")})
	logs, err := client.FailedJobLogs("o", "r", "")
	if err != nil || logs != nil {
		t.Fatalf("got %+v, %v", logs, err)
	}
	if len(transport.Calls) != 0 {
		t.Fatalf("asked GitHub something with no commit to ask about: %+v", transport.Calls)
	}
}

// The commit FailedJobLogs can say nothing about: a check run has
// already concluded `failure` while the workflow run behind it is still
// in progress. failedRuns filters that run out -- it is not completed --
// so the log walk finds nothing, and every caller used to report that as
// "this is not Actions" or "the log expired". ActionsState is what
// answers it properly: the run is there, it is unfinished, and the job
// that broke is already named with the step that broke it.
func TestActionsStateFindsAFailedJobUnderARunStillInProgress(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"CI","status":"in_progress","conclusion":null}]}`)},
		{Status: 200, Body: []byte(`{"jobs":[
			{"id":7,"name":"ui e2e","status":"completed","conclusion":"failure",
			 "html_url":"https://github.com/o/r/actions/runs/42/job/7",
			 "steps":[{"name":"Checkout","status":"completed","conclusion":"success"},
			          {"name":"Test e2e","status":"completed","conclusion":"failure"}]},
			{"id":8,"name":"go","status":"in_progress","conclusion":null}
		]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	state, err := client.ActionsState("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs != 1 || state.Unfinished != 1 {
		t.Errorf("got %d run(s), %d unfinished, want 1 and 1", state.Runs, state.Unfinished)
	}
	if len(state.Jobs) != 1 {
		t.Fatalf("got %+v, want only the job that has actually failed", state.Jobs)
	}
	got := state.Jobs[0]
	if got.Name != "ui e2e" || got.RunName != "CI" || got.RunFinished {
		t.Errorf("got %+v, want the failed job named under its unfinished run", got)
	}
	if got.URL != "https://github.com/o/r/actions/runs/42/job/7" {
		t.Errorf("got %q, want the job's own page", got.URL)
	}
	if len(got.FailedSteps) != 1 || got.FailedSteps[0] != "Test e2e" {
		t.Errorf("got %+v, want only the step that failed", got.FailedSteps)
	}
}

// The one answer that really does mean "the failing check is not
// Actions": no workflow run against the commit at all. One read, and no
// jobs walked, since there is nothing to walk.
func TestActionsStateReportsNoRunsWithoutOpeningAnything(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(`{"workflow_runs":[]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	state, err := client.ActionsState("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs != 0 || state.Unfinished != 0 || len(state.Jobs) != 0 {
		t.Errorf("got %+v, want an empty state", state)
	}
	if len(transport.Calls) != 1 {
		t.Errorf("made %d calls, want the runs read alone: %+v", len(transport.Calls), transport.Calls)
	}
}

// A run that finished green is not opened, for the reason
// FailedJobLogs does not open one either: on a commit whose CI is mostly
// passing this is the difference between one call and a call per job.
func TestActionsStateDoesNotOpenARunThatPassed(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: []byte(
			`{"workflow_runs":[{"id":42,"name":"CI","status":"completed","conclusion":"success"}]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	state, err := client.ActionsState("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs != 1 || len(state.Jobs) != 0 {
		t.Errorf("got %+v, want the run counted and not opened", state)
	}
	if len(transport.Calls) != 1 {
		t.Errorf("made %d calls, want the runs read alone: %+v", len(transport.Calls), transport.Calls)
	}
}

func TestActionsStateAsksNothingWithoutAHeadSHA(t *testing.T) {
	transport := NewFakeTransport()
	client := NewClient(transport, StaticToken{strPtr("t")})
	state, err := client.ActionsState("o", "r", "")
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs != 0 || len(transport.Calls) != 0 {
		t.Fatalf("got %+v after %d calls", state, len(transport.Calls))
	}
}

// mergeable_state comes free on a response grain already makes, under a
// permission it already holds. Parsed and carried, but see
// PullRequestDetail.MergeableState: measured against live pull requests
// and deliberately not trusted by the merge gate, since its "clean" is
// also what an empty check list reads.
func TestGetPullRequestReadsMergeableState(t *testing.T) {
	body := prJSON(5, "feature-branch", "main")
	body["mergeable_state"] = "unstable"
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.MergeableState != "unstable" {
		t.Fatalf("MergeableState = %q, want unstable", pr.MergeableState)
	}
}

// The head sha scopes the Actions fallback, so it has to survive the PR
// read that supplies it.
func TestGetPullRequestReadsHeadSHA(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, prJSON(5, "feature-branch", "main"))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.HeadSHA != "abc123" {
		t.Fatalf("HeadSHA = %q, want abc123", pr.HeadSHA)
	}
}

func TestListCheckRunsRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.ListCheckRuns("o", "r", "feature-x"); err == nil {
		t.Fatal("expected an error")
	}
}

func prJSON(number int, headRef, baseRef string) map[string]any {
	return map[string]any{
		"number": number, "title": "pr", "body": "please review",
		"html_url": fmt.Sprintf("https://github.com/o/r/pull/%d", number),
		"head":     map[string]any{"ref": headRef, "sha": "abc123"},
		"base":     map[string]any{"ref": baseRef},
	}
}

func TestGetPullRequestReadsMergeable(t *testing.T) {
	body := prJSON(5, "feature-branch", "main")
	body["mergeable"] = false
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Mergeable == nil || *pr.Mergeable != false {
		t.Fatalf("got %+v", pr.Mergeable)
	}
}

func TestGetPullRequestDefaultsMergeableToNilWhenAbsent(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, prJSON(5, "feature-branch", "main"))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Mergeable != nil {
		t.Fatalf("got %+v", pr.Mergeable)
	}
}

func TestGetPullRequestReadsMergedAndMergedAt(t *testing.T) {
	body := prJSON(5, "feature-branch", "main")
	body["state"] = "closed"
	body["merged"] = true
	body["merged_at"] = "2026-08-28T12:00:00Z"
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.Merged {
		t.Fatal("expected Merged to be true")
	}
	want := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	if pr.MergedAt == nil || !pr.MergedAt.Equal(want) {
		t.Fatalf("got MergedAt = %v, want %v", pr.MergedAt, want)
	}
}

func TestGetPullRequestReadsClosedWithoutMergedAt(t *testing.T) {
	body := prJSON(5, "feature-branch", "main")
	body["state"] = "closed"
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Merged {
		t.Fatal("expected Merged to be false for a PR closed without merging")
	}
	if pr.MergedAt != nil {
		t.Fatalf("got MergedAt = %v, want nil", pr.MergedAt)
	}
}

func TestGetPullRequestReadsCreatedAt(t *testing.T) {
	body := prJSON(5, "feature-branch", "main")
	body["created_at"] = "2026-08-20T09:30:00Z"
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC)
	if !pr.CreatedAt.Equal(want) {
		t.Fatalf("got CreatedAt = %v, want %v", pr.CreatedAt, want)
	}
}

// The diff's size rides on this response and no other, which is what
// makes recording it free (PullRequestDetail.Additions). A pull request
// GitHub reported nothing about the size of stays at three zeroes, which
// is the "not reported" its recorder reads.
func TestGetPullRequestReadsTheDiffSize(t *testing.T) {
	body := prJSON(5, "feature-branch", "main")
	body["additions"] = 214
	body["deletions"] = 37
	body["changed_files"] = 9
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Additions != 214 || pr.Deletions != 37 || pr.ChangedFiles != 9 {
		t.Fatalf("got +%d -%d over %d files, want +214 -37 over 9",
			pr.Additions, pr.Deletions, pr.ChangedFiles)
	}

	transport = NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, prJSON(5, "feature-branch", "main"))})
	client = NewClient(transport, StaticToken{strPtr("t")})
	pr, err = client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Additions != 0 || pr.Deletions != 0 || pr.ChangedFiles != 0 {
		t.Fatalf("got +%d -%d over %d files, want three zeroes: the response said nothing "+
			"about the size", pr.Additions, pr.Deletions, pr.ChangedFiles)
	}
}

func TestGetPullRequestReadsHeadAndBaseRef(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, prJSON(5, "feature-branch", "main"))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := PullRequestDetail{
		Number: 5, Title: "pr", Body: "please review", HTMLURL: "https://github.com/o/r/pull/5",
		HeadRef: "feature-branch", HeadSHA: "abc123", BaseRef: "main", State: "open",
	}
	if pr != want {
		t.Fatalf("got %+v want %+v", pr, want)
	}
	if transport.Calls[0].Path != "/repos/o/r/pulls/5" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

func TestGetPullRequestReadsAClosedState(t *testing.T) {
	body := prJSON(5, "feature-branch", "main")
	body["state"] = "closed"
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "closed" {
		t.Fatalf("got %s", pr.State)
	}
}

func TestGetPullRequestDefaultsStateToOpenWhenAbsent(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, prJSON(5, "feature-branch", "main"))})
	client := NewClient(transport, StaticToken{strPtr("t")})
	pr, err := client.GetPullRequest("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "open" {
		t.Fatalf("got %s", pr.State)
	}
}

func TestGetPullRequestRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.GetPullRequest("o", "r", 5); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDefaultBranchReadsTheReposOwnDefault(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, map[string]string{"default_branch": "trunk"})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	branch, err := client.DefaultBranch("o", "r")
	if err != nil || branch != "trunk" {
		t.Fatalf("branch=%q err=%v", branch, err)
	}
	if transport.Calls[0].Path != "/repos/o/r" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

func TestDefaultBranchRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.DefaultBranch("o", "r"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSetDefaultBranchPatchesTheRepository(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, map[string]string{"default_branch": "main"})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.SetDefaultBranch("o", "r", "main"); err != nil {
		t.Fatalf("SetDefaultBranch: %v", err)
	}
	if len(transport.Calls) != 1 || transport.Calls[0].Method != "PATCH" || transport.Calls[0].Path != "/repos/o/r" {
		t.Fatalf("got calls %+v", transport.Calls)
	}
	if got := string(transport.Calls[0].Body); got != `{"default_branch":"main"}` {
		t.Fatalf("body = %s, want {\"default_branch\":\"main\"}", got)
	}
}

func TestSetDefaultBranchRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 422, Body: []byte(`{"message":"Validation Failed"}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if err := client.SetDefaultBranch("o", "r", "main"); err == nil {
		t.Fatal("expected an error")
	}
}

// The name the repository has, as against the one that was asked about:
// GitHub resolves owner/name case-insensitively and follows a rename's
// redirect, so this is the only thing that can tell a deployment how a
// repo actually spells itself (ui.(*Client).AddTargetRepo).
func TestRepoFullNameReadsTheReposOwnName(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{
		Status: 200,
		Body:   mustJSON(t, map[string]string{"full_name": "bwsalmon/grain-home"}),
	})
	client := NewClient(transport, StaticToken{strPtr("t")})
	full, err := client.RepoFullName("Bwsalmon", "Grain-Home")
	if err != nil || full != "bwsalmon/grain-home" {
		t.Fatalf("full=%q err=%v", full, err)
	}
	// Asked about under the spelling given -- GitHub is what resolves
	// the two, which is the whole reason to ask it.
	if transport.Calls[0].Path != "/repos/Bwsalmon/Grain-Home" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

func TestRepoFullNameRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.RepoFullName("o", "r"); err == nil {
		t.Fatal("expected an error")
	}
}

// perRepoTokens resolves a credential per repo -- the shape a real
// per-repo credential ladder is, and what proves the token is resolved
// per call rather than fixed at construction.
type perRepoTokens map[string]string

func (p perRepoTokens) TokenFor(owner, repo string) *string {
	if t, ok := p[owner+"/"+repo]; ok {
		return &t
	}
	return nil
}

func TestATokenSourceResolvesACredentialPerRepo(t *testing.T) {
	transport := NewFakeTransport()
	client := NewClient(transport, perRepoTokens{"o/tasks": "task-token", "o/code": "code-token"})
	client.ListIssues("o", "tasks", "grain-agent")
	client.ListIssues("o", "code", "grain-agent")
	client.ListIssues("o", "unmapped", "grain-agent")
	if transport.Calls[0].Headers["Authorization"] != "token task-token" {
		t.Fatalf("got %+v", transport.Calls[0].Headers)
	}
	if transport.Calls[1].Headers["Authorization"] != "token code-token" {
		t.Fatalf("got %+v", transport.Calls[1].Headers)
	}
	if _, ok := transport.Calls[2].Headers["Authorization"]; ok {
		t.Fatal("expected no Authorization header for the unmapped repo")
	}
}

func TestABareTokenStillAppliesToEveryRepo(t *testing.T) {
	transport := NewFakeTransport()
	client := NewClient(transport, StaticToken{strPtr("t")})
	client.ListIssues("o", "one", "grain-agent")
	client.ListIssues("other", "two", "grain-agent")
	for _, call := range transport.Calls {
		if call.Headers["Authorization"] != "token t" {
			t.Fatalf("got %+v", call.Headers)
		}
	}
}

func reviewCommentJSON(id int, line *int) map[string]any {
	return map[string]any{
		"id": id, "user": map[string]string{"login": "reviewer"}, "body": "please fix this",
		"path": "src/thing.py", "line": line, "diff_hunk": "@@ -1,3 +1,3 @@",
	}
}

func TestListReviewCommentsReadsTheReviewCommentShape(t *testing.T) {
	line := 12
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, []any{reviewCommentJSON(9, &line)})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	comments, err := client.ListReviewComments("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].ID != 9 || comments[0].User != "reviewer" ||
		comments[0].Path != "src/thing.py" || comments[0].Line == nil || *comments[0].Line != 12 {
		t.Fatalf("got %+v", comments)
	}
	if transport.Calls[0].Path != "/repos/o/r/pulls/5/comments?per_page=100" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

func TestListReviewCommentsToleratesANullLineOnAnOutdatedComment(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, []any{reviewCommentJSON(9, nil)})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	comments, err := client.ListReviewComments("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if comments[0].Line != nil {
		t.Fatalf("got %+v", comments[0].Line)
	}
}

func TestListReviewCommentsFollowsLinkHeaderPagination(t *testing.T) {
	one, two := 1, 2
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/pulls/5/comments?page=2>; rel="next"`},
			Body: mustJSON(t, []any{reviewCommentJSON(1, &one)})},
		{Status: 200, Body: mustJSON(t, []any{reviewCommentJSON(2, &two)})},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	comments, err := client.ListReviewComments("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Fatalf("got %+v", comments)
	}
}

func TestListReviewCommentsRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.ListReviewComments("o", "r", 5); err == nil {
		t.Fatal("expected an error")
	}
}

// A pull request's files arrive with a patch, a status and line counts
// attached to each of them, and the caller wants the path: the merge
// queue asks "does this change touch .github/workflows/", which nothing
// but the path answers.
func TestListPullRequestFilesReturnsThePathsAlone(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, []any{
		map[string]any{"filename": ".github/workflows/ci.yml", "status": "modified",
			"additions": 3, "patch": "@@ -1 +1 @@"},
		map[string]any{"filename": "pkg/orchestrator/sync.go", "status": "modified"},
	})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	files, err := client.ListPullRequestFiles("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != ".github/workflows/ci.yml" || files[1] != "pkg/orchestrator/sync.go" {
		t.Fatalf("got %+v", files)
	}
	if transport.Calls[0].Path != "/repos/o/r/pulls/5/files?per_page=100" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

// A rename is listed under the path it left as well as the one it
// arrived at: moving a file out of pkg/gitproxy/ touches pkg/gitproxy/.
func TestListPullRequestFilesListsARenameUnderBothPaths(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, []any{
		map[string]any{"filename": "pkg/elsewhere/proxy.go", "status": "renamed",
			"previous_filename": "pkg/gitproxy/proxy.go"},
		map[string]any{"filename": "README.md", "status": "modified"},
	})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	files, err := client.ListPullRequestFiles("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pkg/elsewhere/proxy.go", "pkg/gitproxy/proxy.go", "README.md"}
	if len(files) != len(want) {
		t.Fatalf("got %+v, want %+v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("got %+v, want %+v", files, want)
		}
	}
}

// A hundred files a page, and a pull request that edits a workflow file
// among two hundred others is exactly the one this read exists for -- so
// the workflow edit cannot be allowed to fall off the first page.
func TestListPullRequestFilesFollowsLinkHeaderPagination(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/pulls/5/files?page=2>; rel="next"`},
			Body: mustJSON(t, []any{map[string]any{"filename": "README.md"}})},
		{Status: 200, Body: mustJSON(t, []any{map[string]any{"filename": ".github/workflows/ci.yml"}})},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	files, err := client.ListPullRequestFiles("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[1] != ".github/workflows/ci.yml" {
		t.Fatalf("got %+v", files)
	}
	if transport.Calls[1].Path != "/repos/o/r/pulls/5/files?page=2" {
		t.Fatalf("unexpected second call path: %s", transport.Calls[1].Path)
	}
}

func TestListPullRequestFilesOnGHEHostDoesNotDoubleApiV3OrLeakRepoHeader(t *testing.T) {
	var seen []string
	var leakedRepoHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		if v := r.Header.Get(RepoHeader); v != "" {
			leakedRepoHeader = v
		}
		switch r.URL.RequestURI() {
		case "/api/v3/repos/experimental/grain/pulls/1/files?per_page=100":
			w.Header().Set("Link", `<https://depot.code.forge.test/api/v3/repositories/80876/pulls/1/files?per_page=100&page=2>; rel="next"`)
			w.WriteHeader(200)
			w.Write(mustJSON(t, []any{map[string]any{"filename": "README.md"}}))
		case "/api/v3/repositories/80876/pulls/1/files?per_page=100&page=2":
			w.WriteHeader(200)
			w.Write(mustJSON(t, []any{map[string]any{"filename": ".github/workflows/ci.yml"}}))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	transport := &RealTransport{Host: host, PathPrefix: "/api/v3", UseTLS: false}
	client := NewClient(transport, StaticToken{strPtr("t")})
	files, err := client.ListPullRequestFiles("experimental", "grain", 1)
	if err != nil {
		t.Fatalf("ListPullRequestFiles: %v (seen: %q)", err, seen)
	}
	wantFiles := []string{"README.md", ".github/workflows/ci.yml"}
	if !slices.Equal(files, wantFiles) {
		t.Fatalf("files = %v, want %v", files, wantFiles)
	}
	wantSeen := []string{
		"/api/v3/repos/experimental/grain/pulls/1/files?per_page=100",
		"/api/v3/repositories/80876/pulls/1/files?per_page=100&page=2",
	}
	if !slices.Equal(seen, wantSeen) {
		t.Fatalf("seen = %q, want %q", seen, wantSeen)
	}
	if leakedRepoHeader != "" {
		t.Fatalf("RepoHeader leaked to upstream: %q", leakedRepoHeader)
	}

	// Calling RealTransport directly with a path that already carries
	// PathPrefix does not prepend a second /api/v3 either.
	resp, err := transport.Request("GET", "/api/v3/repositories/80876/pulls/1/files?per_page=100&page=2", nil, nil)
	if err != nil || resp.Status != 200 {
		t.Fatalf("direct /api/v3 Request = %+v, %v", resp, err)
	}
}

func TestListPullRequestFilesRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.ListPullRequestFiles("o", "r", 5); err == nil {
		t.Fatal("expected an error")
	}
}

func commentJSON(id int, user, body string) map[string]any {
	return map[string]any{"id": id, "user": map[string]string{"login": user}, "body": body}
}

func TestListCommentsReadsThePlainCommentShape(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, []any{commentJSON(9, "human", "here's my answer")})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	comments, err := client.ListComments("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := Comment{ID: 9, User: "human", Body: "here's my answer", AuthorAssociation: "NONE"}
	if len(comments) != 1 || comments[0] != want {
		t.Fatalf("got %+v want %+v", comments, want)
	}
	if transport.Calls[0].Path != "/repos/o/r/issues/5/comments?per_page=100" {
		t.Fatalf("got %s", transport.Calls[0].Path)
	}
}

func TestListCommentsFollowsLinkHeaderPagination(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/issues/5/comments?page=2>; rel="next"`},
			Body: mustJSON(t, []any{commentJSON(1, "human", "a")})},
		{Status: 200, Body: mustJSON(t, []any{commentJSON(2, "human", "b")})},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	comments, err := client.ListComments("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Fatalf("got %+v", comments)
	}
}

func TestListCommentsRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 500, Body: []byte("boom")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.ListComments("o", "r", 5); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCreateCommentPostsTheBodyAndReturnsTheNewId(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: mustJSON(t, map[string]int{"id": 999})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	id, err := client.CreateComment("o", "r", 5, "a question for you")
	if err != nil || id != 999 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/issues/5/comments" {
		t.Fatalf("got %+v", call)
	}
	var body map[string]string
	json.Unmarshal(call.Body, &body)
	if body["body"] != "a question for you" {
		t.Fatalf("got %+v", body)
	}
}

func TestCreateCommentRaisesOnANon201(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte("not found")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CreateComment("o", "r", 5, "a question"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCreateReviewPostsADraftWithNoEventKey(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, map[string]int{"id": 321})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	id, err := client.CreateReview("o", "r", 5, "looks good overall",
		[]NewReviewComment{{Path: "src/thing.py", Line: 12, Body: "nit: typo"}})
	if err != nil || id != 321 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/pulls/5/reviews" {
		t.Fatalf("got %+v", call)
	}
	var payload map[string]any
	json.Unmarshal(call.Body, &payload)
	if _, ok := payload["event"]; ok {
		t.Fatal("expected no event key")
	}
	if payload["body"] != "looks good overall" {
		t.Fatalf("got %+v", payload)
	}
	comments, _ := payload["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("got %+v", payload["comments"])
	}
	comment := comments[0].(map[string]any)
	if comment["path"] != "src/thing.py" || comment["body"] != "nit: typo" {
		t.Fatalf("got %+v", comment)
	}
}

func TestCreateReviewWithNoInlineCommentsPostsAnEmptyList(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, map[string]int{"id": 1})})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CreateReview("o", "r", 5, "LGTM", nil); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	json.Unmarshal(transport.Calls[0].Body, &payload)
	comments, ok := payload["comments"].([]any)
	if !ok || len(comments) != 0 {
		t.Fatalf("got %+v", payload["comments"])
	}
}

func TestCreateReviewRaisesOnANon200(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 422, Body: []byte("unprocessable")})
	client := NewClient(transport, StaticToken{strPtr("t")})
	if _, err := client.CreateReview("o", "r", 5, "x", nil); err == nil {
		t.Fatal("expected an error")
	}
}

// TestFakeTransportDefaultAnswersOnceResponsesAreExhausted proves the
// convenience NewFakeTransport gives every test above: a call past the
// scripted responses gets Default rather than a panic, so a test only
// scripts the calls it cares about.
func TestFakeTransportDefaultAnswersOnceResponsesAreExhausted(t *testing.T) {
	transport := NewFakeTransport()
	resp, err := transport.Request("GET", "/anything", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != "[]" {
		t.Fatalf("got %+v", resp)
	}
}

func TestIsPermissionDenied(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a 403 is a permission the credential does not hold", &Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)}, true},
		// The same refusal as GitHub phrases it for an App credential.
		{"the integration wording counts too", &Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by integration"}`)}, true},
		// 404 is how GitHub hides a private repo from a token that
		// cannot see it, which is a different problem with a different
		// fix -- widening test_repos or the token's repository scope,
		// not a permission that cannot be granted at all.
		{"a 404 is not", &Error{Status: 404}, false},
		{"a 500 is not", &Error{Status: 500}, false},
		{"a transport failure is not", errors.New("dial tcp: connection refused"), false},
		{"no error at all is not", nil, false},

		// Everything below is a 403 that is NOT a missing permission.
		// Reading any of these as one would latch
		// orchestrator.ChecksUnavailable and leave auto-merge off until
		// the daemon restarted, over a condition that clears on its own
		// or with a change the operator can make.
		{
			"a primary rate limit is not",
			&Error{Status: 403, Body: []byte(`{"message":"API rate limit exceeded for user ID 1."}`)},
			false,
		},
		{
			"a secondary rate limit is not",
			&Error{Status: 403, Body: []byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`)},
			false,
		},
		{
			"SAML enforcement is not",
			&Error{Status: 403, Body: []byte(`{"message":"Resource protected by organization SAML enforcement. You must grant your Personal Access token access to this organization."}`)},
			false,
		},
		{
			"an organization IP allow list is not",
			&Error{Status: 403, Body: []byte(`{"message":"Although you appear to have the correct authorization credentials, the organization has an IP allow list enabled, and your IP address is not permitted to access this resource."}`)},
			false,
		},
		// A body this cannot read is not one it can vouch for. Failing
		// closed here costs a retried cycle; failing open would cost
		// auto-merge until a restart.
		{"an empty body is not", &Error{Status: 403}, false},
		{"a non-JSON body is not", &Error{Status: 403, Body: []byte("<html>403 Forbidden</html>")}, false},
		{"a JSON body with no message is not", &Error{Status: 403, Body: []byte(`{"documentation_url":"https://docs.github.com"}`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPermissionDenied(tc.err); got != tc.want {
				t.Errorf("IsPermissionDenied(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The helper unwraps, so a caller that has already annotated the error
// with fmt.Errorf(...%w...) still gets the right answer.
// IsServerError is the "ask again" member of the family, and the split
// that matters is between a status GitHub answered from its own trouble
// and a status it answered from the question. A caller showing an
// operator a fault acts on the difference.
func TestIsServerError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		// The five 5xx this deployment's daemon log carried over
		// 16-17 September 2026, each of which was answered fine by the
		// next read ten seconds later.
		{"500 with no body at all", &Error{Status: 500}, true},
		{"502 with GitHub's terse message", &Error{Status: 502, Body: []byte(`{"message":"Server Error"}`)}, true},
		{"502 as GitHub's HTML error page", &Error{Status: 502, Body: []byte("<html><body>error</body></html>")}, true},
		{"504", &Error{Status: 504, Body: []byte(`{"message":"We couldn't respond to your request in time."}`)}, true},
		{"503 from the proxy in front of GitHub", &Error{Status: 503,
			Body: []byte("upstream connect error or disconnect/reset before headers ... connection refused")}, true},

		// Everything GitHub meant. Each of these is somebody's to fix,
		// and none of them clears by being asked again.
		{"404 is not", &Error{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, false},
		{"403 is not", &Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by integration"}`)}, false},
		{"401 is not", &Error{Status: 401, Body: []byte(`{"message":"Bad credentials"}`)}, false},
		{"422 is not", &Error{Status: 422, Body: []byte(`{"message":"Validation Failed"}`)}, false},
		{"200 is not", &Error{Status: 200}, false},

		// A call that never reached GitHub has no status to read, and
		// this predicate is deliberately about the status: a caller that
		// means "anything but a real answer" asks for that separately.
		{"a transport failure is not", errors.New("dial tcp: connection refused"), false},
		{"no error at all is not", nil, false},

		{"a wrapped 500 is", fmt.Errorf("forge: reading acme/widgets#1: %w", &Error{Status: 500}), true},
	} {
		if got := IsServerError(tc.err); got != tc.want {
			t.Errorf("IsServerError(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsPermissionDeniedUnwraps(t *testing.T) {
	wrapped := fmt.Errorf("reading check runs for owner/repo#1: %w",
		&Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)})
	if !IsPermissionDenied(wrapped) {
		t.Error("a wrapped 403 was not recognized")
	}
}

// The same predicate against bodies api.github.com really sent, rather
// than against the one-field bodies written from memory above.
//
// They were captured by hand on 2026-09-07, from the two endpoints the
// settings-reset notice calls, and are quoted whole -- the CRLFs, the
// documentation_url, the status field GitHub repeats as a string -- in
// docs/hand-runs/2026-09-07-settings-reset-notice.md, which is also where
// the ones that are still missing are named. None of the three is a
// permission this credential can never hold: 401 is a credential to
// replace and 404 is access to restore, and reading either as a
// permission denial would tell an operator to widen a scope that is not
// the problem.
func TestIsPermissionDeniedOnBodiesGitHubActuallySent(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			"no credential at all, posting a comment",
			&Error{Status: 401, Body: []byte("{\r\n  \"message\": \"Requires authentication\",\r\n  " +
				"\"documentation_url\": \"https://docs.github.com/rest\",\r\n  \"status\": \"401\"\r\n}")},
		},
		{
			"a credential GitHub no longer accepts",
			&Error{Status: 401, Body: []byte("{\r\n  \"message\": \"Bad credentials\",\r\n  " +
				"\"documentation_url\": \"https://docs.github.com/rest\",\r\n  \"status\": \"401\"\r\n}")},
		},
		{
			// What a credential that lost access to the repository gets
			// from the commits/{sha}/pulls lookup: GitHub hides the
			// repository rather than refusing the call.
			"a repository the credential cannot see",
			&Error{Status: 404, Body: []byte("{\r\n  \"message\": \"Not Found\",\r\n  " +
				"\"documentation_url\": \"https://docs.github.com/rest/commits/commits" +
				"#list-pull-requests-associated-with-a-commit\",\r\n  \"status\": \"404\"\r\n}")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if IsPermissionDenied(tc.err) {
				t.Errorf("IsPermissionDenied(%v) = true, want false", tc.err)
			}
		})
	}
}

// What Message makes of those same three bodies, which is the reason it
// exists: they are the refusals an operator actually meets, and what
// they meet them as is a pane showing an escaped JSON document with
// GitHub's own two words three lines down inside it.
func TestMessageSaysWhatGitHubSaidOnBodiesGitHubActuallySent(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			"no credential at all, posting a comment",
			&Error{Status: 401, Body: []byte("{\r\n  \"message\": \"Requires authentication\",\r\n  " +
				"\"documentation_url\": \"https://docs.github.com/rest\",\r\n  \"status\": \"401\"\r\n}")},
			"GitHub answered 401: Requires authentication",
		},
		{
			"a credential GitHub no longer accepts",
			&Error{Status: 401, Body: []byte("{\r\n  \"message\": \"Bad credentials\",\r\n  " +
				"\"documentation_url\": \"https://docs.github.com/rest\",\r\n  \"status\": \"401\"\r\n}")},
			"GitHub answered 401: Bad credentials",
		},
		{
			"a repository the credential cannot see",
			&Error{Status: 404, Body: []byte("{\r\n  \"message\": \"Not Found\",\r\n  " +
				"\"documentation_url\": \"https://docs.github.com/rest/commits/commits" +
				"#list-pull-requests-associated-with-a-commit\",\r\n  \"status\": \"404\"\r\n}")},
			"GitHub answered 404: Not Found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Message(tc.err); got != tc.want {
				t.Errorf("Message() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Error is left as it was, deliberately: Message is a second rendering
// for the surfaces a person reads, not a replacement for the one a log
// carries, and everything else in this repository still logs the body
// whole.
func TestErrorStillQuotesTheBodyItWasGiven(t *testing.T) {
	err := &Error{Status: 404, Body: []byte(`{"message": "Not Found"}`)}
	if got, want := err.Error(), `GitHub API error 404: "{\"message\": \"Not Found\"}"`; got != want {
		t.Errorf("Error() = %s, want %s", got, want)
	}
}

// A body Message can make no sentence of falls back to that rendering,
// so a caller never has to check what it was handed: the whole body is
// exactly what is worth having when GitHub's own wording cannot be found
// in it.
func TestMessageFallsBackToTheWholeBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"an HTML page from something in front of GitHub", &Error{Status: 502, Body: []byte("<html>502</html>")}},
		{"no body at all", &Error{Status: 500}},
		{"JSON with no message in it", &Error{Status: 422, Body: []byte(`{"documentation_url": "https://docs.github.com/rest"}`)}},
		{"a message that is not a line of text", &Error{Status: 500, Body: []byte("{\"message\": \"a\\u0007b\"}")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := Message(tc.err), tc.err.Error(); got != want {
				t.Errorf("Message() = %q, want the error's own rendering %q", got, want)
			}
		})
	}
	// An error that never came from GitHub, which a caller rendering a
	// failure through this cannot rule out either.
	if got, want := Message(errors.New("dial tcp: i/o timeout")), "dial tcp: i/o timeout"; got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
	if got := Message(nil); got != "" {
		t.Errorf("Message(nil) = %q, want nothing to say", got)
	}
	// And it unwraps, like the predicates above, so a caller that has
	// already annotated the failure still gets GitHub's words out of it.
	wrapped := fmt.Errorf("posting a comment on acme/state#7: %w",
		&Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by integration"}`)})
	if got, want := Message(wrapped), "GitHub answered 403: Resource not accessible by integration"; got != want {
		t.Errorf("Message(wrapped) = %q, want %q", got, want)
	}
}

// The 422s say nothing in their `message` -- "Validation Failed" sends
// nobody anywhere -- and everything in the per-field array beneath it,
// which is why that array is read too. A rendering that dropped it would
// be less use than the raw body it replaces.
func TestMessageCarriesWhatA422Blames(t *testing.T) {
	err := &Error{Status: 422, Body: []byte(`{"message": "Validation Failed", "errors": [` +
		`{"resource": "PullRequest", "code": "custom", "message": "No commits between main and topic"}]}`)}
	if got, want := Message(err),
		"GitHub answered 422: Validation Failed (No commits between main and topic)"; got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
	// An entry with no message of its own still names the field, which is
	// the half of "too_long" that says which field was.
	err = &Error{Status: 422, Body: []byte(`{"message": "Validation Failed", "errors": [` +
		`{"resource": "IssueComment", "field": "body", "code": "too_long"}]}`)}
	if got, want := Message(err), "GitHub answered 422: Validation Failed (body too_long)"; got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
}

// Long enough to be somebody's whole error page rather than a sentence,
// and it is bounded -- this goes into a journal line and onto a pane.
func TestMessageIsBounded(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"message": strings.Repeat("verbose ", 200)})
	got := Message(&Error{Status: 500, Body: body})
	if len(got) > 300 {
		t.Errorf("Message() is %d characters: %s", len(got), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a cut message does not say it was cut: %s", got)
	}
}

// Readable is Message for the seams that hand back an error rather than
// a string, and the whole reason it is a wrapper instead of
// errors.New(Message(err)) is the second half of this: the predicates
// have to go on answering. A seam that rendered by throwing the *Error
// away would break them somewhere downstream of itself, quietly, in
// whatever asks next.
func TestReadableRendersTheSentenceAndKeepsTheErrorUnderIt(t *testing.T) {
	denied := &Error{Status: 403, Body: []byte(
		`{"message": "Resource not accessible by personal access token", ` +
			`"documentation_url": "https://docs.github.com/rest"}`)}
	readable := Readable(denied)
	if got, want := readable.Error(),
		"GitHub answered 403: Resource not accessible by personal access token"; got != want {
		t.Errorf("Readable().Error() = %q, want %q", got, want)
	}
	if !IsPermissionDenied(readable) {
		t.Error("a wrapped permission denial stopped being one, so whatever keys on it downstream is now wrong")
	}
	var e *Error
	if !errors.As(readable, &e) || e.Status != 403 {
		t.Errorf("errors.As did not find the GitHub error under Readable: %v", readable)
	}
	// The context goes outside, and survives: this is the shape every
	// call site here uses.
	wrapped := fmt.Errorf("reading acme/grain's main branch: %w", readable)
	if got, want := wrapped.Error(),
		"reading acme/grain's main branch: GitHub answered 403: "+
			"Resource not accessible by personal access token"; got != want {
		t.Errorf("wrapped Readable = %q, want %q", got, want)
	}
	if !IsPermissionDenied(wrapped) {
		t.Error("the predicate stopped answering once the error carried context")
	}
}

// Nothing a caller might hand it is a reason to check first: an error
// that is not GitHub's comes back as itself, and nil stays nil.
func TestReadableLeavesEverythingElseAlone(t *testing.T) {
	if got := Readable(nil); got != nil {
		t.Errorf("Readable(nil) = %v, want nil", got)
	}
	own := errors.New("the sandbox has no network")
	if got := Readable(own); got != own {
		t.Errorf("Readable() rewrote an error that never came from GitHub: %v", got)
	}
	// A body no sentence can be read out of falls back to the whole of
	// it, exactly as Message does -- that body is the only evidence
	// there is, and dropping it would be worse than quoting it.
	page := &Error{Status: 502, Body: []byte("<html>502 Bad Gateway</html>")}
	if got, want := Readable(page).Error(), page.Error(); got != want {
		t.Errorf("Readable() of an unreadable body = %q, want the body itself: %q", got, want)
	}
}

// 404 is GitHub's answer both to a repository that is not there and to
// one this credential may not see, and IsNotFound is how a caller says
// so rather than asserting the first. It is not a permission denial and
// must not be read as one: IsPermissionDenied latches auto-merge off,
// and a repository that was renamed is not a scope to widen.
func TestIsNotFoundIsAnAnswerAndNotAPermissionDenial(t *testing.T) {
	missing := &Error{Status: 404, Body: []byte(`{"message": "Not Found"}`)}
	if !IsNotFound(missing) {
		t.Error("a 404 was not recognized")
	}
	if IsPermissionDenied(missing) {
		t.Error("a 404 was read as a permission this credential can never hold")
	}
	denied := &Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by integration"}`)}
	if IsNotFound(denied) {
		t.Error("a 403 was read as a 404")
	}
	if !IsNotFound(fmt.Errorf("looking up the pull request: %w", missing)) {
		t.Error("a wrapped 404 was not recognized")
	}
	if IsNotFound(errors.New("dial tcp: i/o timeout")) {
		t.Error("an error that never reached GitHub was read as its 404")
	}
}

// The bug this exists to prevent: a daemon configured with the correct
// git host (github.com) sent every REST path to https://github.com/repos/...,
// which answers 404, and BranchExists reads 404 as "no such branch". Runs
// that had just pushed were recorded as having done nothing.
func TestAPIHostSeparatesTheAPIHostFromTheGitHost(t *testing.T) {
	if got := APIHost("github.com"); got != "api.github.com" {
		t.Errorf("APIHost(github.com) = %q, want api.github.com -- REST paths do not exist on the git host", got)
	}
	// A mock (githubsim on a local port) serves API paths at its own
	// root; rewriting its host would point the client at nothing.
	for _, host := range []string{"127.0.0.1:8080", "localhost:9999", "github.example.com"} {
		if got := APIHost(host); got != host {
			t.Errorf("APIHost(%q) = %q, want it left alone", host, got)
		}
	}
}

func TestNewRealTransportAimsAtTheAPIHost(t *testing.T) {
	if got := NewRealTransport("github.com").Host; got != "api.github.com" {
		t.Errorf("NewRealTransport(github.com).Host = %q, want api.github.com", got)
	}
	if got := NewRealTransport("127.0.0.1:8080").Host; got != "127.0.0.1:8080" {
		t.Errorf("NewRealTransport(127.0.0.1:8080).Host = %q, want it left alone", got)
	}
}

func TestListReviewCommentsReadsInReplyTo(t *testing.T) {
	reply := reviewCommentJSON(10, nil)
	reply["in_reply_to_id"] = 9
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, []any{reply})})
	comments, err := NewClient(transport, nil).ListReviewComments("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if comments[0].InReplyTo != 9 {
		t.Fatalf("InReplyTo = %d, want 9", comments[0].InReplyTo)
	}
}

func TestReplyToReviewCommentPostsToTheRepliesEndpoint(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: []byte(`{"id": 31}`)})
	id, err := NewClient(transport, StaticToken{strPtr("t")}).ReplyToReviewComment("o", "r", 5, 9, "done")
	if err != nil {
		t.Fatal(err)
	}
	if id != 31 {
		t.Fatalf("id = %d, want 31", id)
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/pulls/5/comments/9/replies" {
		t.Fatalf("got %s %s", call.Method, call.Path)
	}
	if !strings.Contains(string(call.Body), `"body":"done"`) {
		t.Fatalf("body = %s", call.Body)
	}
}

func TestReplyToReviewCommentRaisesOnANon201(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)})
	_, err := NewClient(transport, nil).ReplyToReviewComment("o", "r", 5, 9, "done")
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want a 404", err)
	}
}

func TestListReviewThreadsFollowsGraphQLPagination(t *testing.T) {
	page := func(next bool, cursor, id string, resolved bool, comments ...int) []byte {
		var nodes []map[string]any
		for _, c := range comments {
			nodes = append(nodes, map[string]any{"databaseId": c})
		}
		return mustJSON(t, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"reviewThreads": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": next, "endCursor": cursor},
				"nodes": []any{map[string]any{"id": id, "isResolved": resolved, "isOutdated": false,
					"path": "a.go", "comments": map[string]any{"nodes": nodes}}},
			}}}}})
	}
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Body: page(true, "c1", "T1", false, 1, 2)},
		{Status: 200, Body: page(false, "", "T2", true, 3)},
	}}
	threads, err := NewClient(transport, StaticToken{strPtr("t")}).ListReviewThreads("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != "T1" || threads[0].Resolved || len(threads[0].CommentIDs) != 2 ||
		threads[1].ID != "T2" || !threads[1].Resolved || threads[1].CommentIDs[0] != 3 {
		t.Fatalf("got %+v", threads)
	}
	if transport.Calls[0].Method != "POST" || transport.Calls[0].Path != "/graphql" {
		t.Fatalf("got %s %s", transport.Calls[0].Method, transport.Calls[0].Path)
	}
	var second struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(transport.Calls[1].Body, &second); err != nil {
		t.Fatal(err)
	}
	if second.Variables["after"] != "c1" || second.Variables["number"] != float64(5) {
		t.Fatalf("second page variables = %v", second.Variables)
	}
}

func TestListReviewThreadsReportsAMissingPullRequestAsNotFound(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200,
		Body: []byte(`{"data":{"repository":{"pullRequest":null}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a PullRequest"}]}`)})
	_, err := NewClient(transport, nil).ListReviewThreads("o", "r", 5)
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want a 404", err)
	}
	if !strings.Contains(Readable(err).Error(), "Could not resolve to a PullRequest") {
		t.Fatalf("Readable(err) = %v, want GitHub's own message", Readable(err))
	}
}

func TestResolveReviewThreadSendsTheMutation(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200,
		Body: []byte(`{"data":{"resolveReviewThread":{"thread":{"id":"T1","isResolved":true}}}}`)})
	if err := NewClient(transport, StaticToken{strPtr("t")}).ResolveReviewThread("o", "r", "T1"); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(transport.Calls[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.Query, "resolveReviewThread") || sent.Variables["threadId"] != "T1" {
		t.Fatalf("sent %+v", sent)
	}
	if transport.Calls[0].Headers["Authorization"] != "token t" {
		t.Fatalf("Authorization = %q", transport.Calls[0].Headers["Authorization"])
	}
}

func TestResolveReviewThreadRaisesGraphQLErrors(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200,
		Body: []byte(`{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`)})
	err := NewClient(transport, nil).ResolveReviewThread("o", "r", "T1")
	if !IsPermissionDenied(err) {
		t.Fatalf("err = %v, want a permission refusal", err)
	}
}
