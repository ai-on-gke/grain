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

import "testing"

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
