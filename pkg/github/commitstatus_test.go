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
	"unicode/utf8"
)

type fakeStatusLoginTokenSource struct {
	StaticToken
	login string
	err   error
}

func (f fakeStatusLoginTokenSource) StatusLoginFor(string, string) (string, error) {
	return f.login, f.err
}

func TestCommitStatusesReadsTheCombinedStatus(t *testing.T) {
	body := map[string]any{"state": "pending", "statuses": []any{
		map[string]any{"context": "ci/edition", "state": "success", "description": "green", "target_url": "https://ci.example/1"},
		map[string]any{"context": "lint", "state": "pending"},
	}}
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	got, err := client.CommitStatuses("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/repos/o/r/commits/abc123/status?per_page=100"; transport.Calls[0].Path != want {
		t.Errorf("path = %s, want %s", transport.Calls[0].Path, want)
	}
	if len(got) != 2 || got[0] != (CommitStatus{Context: "ci/edition", State: "success", Description: "green", TargetURL: "https://ci.example/1"}) ||
		got[1].Context != "lint" || got[1].State != "pending" {
		t.Fatalf("statuses = %+v", got)
	}
}

func TestCommitStatusesOfAnUnknownRefIsNone(t *testing.T) {
	client := NewClient(NewFakeTransport(ApiResponse{Status: 422, Body: []byte(`{"message":"No commit found"}`)}), nil)
	got, err := client.CommitStatuses("o", "r", "nope")
	if err != nil || len(got) != 0 {
		t.Fatalf("CommitStatuses = %+v, %v; want none and no error", got, err)
	}
}

func TestListPathCommitsAsksForOnePathOnOneRef(t *testing.T) {
	body := []any{
		map[string]any{"sha": "bbb", "commit": map[string]any{"message": "sync go.mod"}, "parents": []any{map[string]any{"sha": "aaa"}}},
		map[string]any{"sha": "aaa", "commit": map[string]any{"message": "merge"},
			"parents": []any{map[string]any{"sha": "111"}, map[string]any{"sha": "222"}}},
	}
	transport := NewFakeTransport(ApiResponse{Status: 200, Body: mustJSON(t, body)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	got, err := client.ListPathCommits("o", "r", "main", "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/repos/o/r/commits?sha=main&path=go.mod&per_page=100"; transport.Calls[0].Path != want {
		t.Errorf("path = %s, want %s", transport.Calls[0].Path, want)
	}
	if len(got) != 2 || got[0].SHA != "bbb" || got[0].Message != "sync go.mod" || got[0].Merge ||
		got[1].SHA != "aaa" || !got[1].Merge || len(got[1].Parents) != 2 {
		t.Fatalf("commits = %+v", got)
	}
}

func TestListCommitStatusesReadsHistoryCreatorsAndPages(t *testing.T) {
	page1 := []any{
		map[string]any{
			"id":          200,
			"context":     "grain/release-qualification",
			"state":       "success",
			"description": "passed",
			"target_url":  "https://grain.example/suites/1",
			"created_at":  "2026-11-16T12:05:00Z",
			"updated_at":  "2026-11-16T12:05:00Z",
			"creator":     map[string]any{"login": "grain-pat-bot"},
		},
	}
	page2 := []any{
		map[string]any{
			"id":          100,
			"context":     "grain/release-qualification",
			"state":       "pending",
			"description": "qualifying",
			"creator":     map[string]any{"login": "untrusted-user"},
		},
	}
	transport := NewFakeTransport(
		ApiResponse{
			Status:  200,
			Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/commits/abc123/statuses?per_page=100&page=2>; rel="next"`},
			Body:    mustJSON(t, page1),
		},
		ApiResponse{
			Status: 200,
			Body:   mustJSON(t, page2),
		},
	)
	client := NewClient(transport, StaticToken{strPtr("t")})
	got, err := client.ListCommitStatuses("o", "r", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/repos/o/r/commits/abc123/statuses?per_page=100"; transport.Calls[0].Path != want {
		t.Errorf("page 1 path = %s, want %s", transport.Calls[0].Path, want)
	}
	if len(got) != 2 {
		t.Fatalf("len(statuses) = %d, want 2: %+v", len(got), got)
	}
	if got[0].ID != 200 || got[0].Context != "grain/release-qualification" || got[0].State != "success" ||
		got[0].Description != "passed" || got[0].TargetURL != "https://grain.example/suites/1" ||
		got[0].Creator != "grain-pat-bot" || got[0].CreatedAt == nil || got[0].UpdatedAt == nil {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[1].ID != 100 || got[1].Creator != "untrusted-user" || got[1].State != "pending" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

func TestListCommitStatusesFailsClosedOnNotFound(t *testing.T) {
	client := NewClient(NewFakeTransport(ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}), nil)
	got, err := client.ListCommitStatuses("o", "r", "missing")
	if !IsNotFound(err) || len(got) != 0 {
		t.Fatalf("ListCommitStatuses on 404 = %+v, %v; want 404 *Error so rc-hold and ci checks fail closed", got, err)
	}
}

func TestCreateCommitStatusPostsAndTruncatesDescription(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 201, Body: []byte(`{"id":1}`)})
	client := NewClient(transport, StaticToken{strPtr("t")})
	longDesc := "line1\n\tline2 " + strings.Repeat("a", 200)
	err := client.CreateCommitStatus("o", "r", "abc123", CommitStatusWrite{
		State:       "pending",
		Context:     "grain/release-qualification",
		Description: longDesc,
		TargetURL:   "https://grain.example/suites/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(transport.Calls))
	}
	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/repos/o/r/statuses/abc123" {
		t.Fatalf("call = %s %s", call.Method, call.Path)
	}
	var sent map[string]string
	if err := json.Unmarshal(call.Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["state"] != "pending" || sent["context"] != "grain/release-qualification" || sent["target_url"] != "https://grain.example/suites/1" {
		t.Fatalf("sent = %+v", sent)
	}
	if utf8.RuneCountInString(sent["description"]) != maxCommitStatusDescription {
		t.Errorf("description rune count = %d, want %d (%q)", utf8.RuneCountInString(sent["description"]), maxCommitStatusDescription, sent["description"])
	}
	if !strings.HasPrefix(sent["description"], "line1 line2 ") || !strings.HasSuffix(sent["description"], "...") {
		t.Errorf("unexpected description = %q", sent["description"])
	}
}

func TestStatusLoginReadsFromTokenSource(t *testing.T) {
	client := NewClient(NewFakeTransport(), fakeStatusLoginTokenSource{
		StaticToken: StaticToken{strPtr("t")},
		login:       "grain-pat-bot",
	})
	login, err := client.StatusLogin("o", "r")
	if err != nil || login != "grain-pat-bot" {
		t.Fatalf("StatusLogin = %q, %v; want grain-pat-bot", login, err)
	}
	plain := NewClient(NewFakeTransport(), StaticToken{strPtr("t")})
	if login, err := plain.StatusLogin("o", "r"); err != nil || login != "" {
		t.Fatalf("plain StatusLogin = %q, %v; want empty", login, err)
	}
}
