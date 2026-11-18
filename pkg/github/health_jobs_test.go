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
	"strings"
	"testing"
)

func TestExactAttemptJobsRequireCountedCompletePagination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []ApiResponse
		want      int
		fail      bool
	}{
		{"complete", []ApiResponse{
			{Status: 200, Body: []byte(`{"total_count":2,"jobs":[{"id":1,"run_id":7,"name":"test","status":"completed","conclusion":"success"}]}`), Headers: map[string]string{"Link": `<https://api.github.com/repos/acme/widgets/actions/runs/7/attempts/2/jobs?per_page=100&page=2>; rel="next"`}},
			{Status: 200, Body: []byte(`{"total_count":2,"jobs":[{"id":2,"run_id":7,"name":"lint","status":"completed","conclusion":"neutral"}]}`)},
		}, 2, false},
		{"truncated", []ApiResponse{{Status: 200, Body: []byte(`{"total_count":2,"jobs":[{"id":1}]}`)}}, 0, true},
		{"missing-count", []ApiResponse{{Status: 200, Body: []byte(`{"jobs":[{"id":1}]}`)}}, 0, true},
		{"oversized", []ApiResponse{{Status: 200, Body: []byte(`{"total_count":513,"jobs":[]}`)}}, 0, true},
		{"denied", []ApiResponse{{Status: 403, Body: []byte(`{"message":"forbidden"}`)}}, 0, true},
		{"missing", []ApiResponse{{Status: 404, Body: []byte(`{}`)}}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &FakeTransport{Responses: tc.responses}
			client := NewClient(transport, StaticToken{strPtr("t")})
			jobs, err := client.ListWorkflowRunAttemptJobs("acme", "widgets", 7, 2)
			if (err != nil) != tc.fail || len(jobs) != tc.want {
				t.Fatalf("jobs %d err %v", len(jobs), err)
			}
			for _, call := range transport.Calls {
				if call.Method != "GET" || !strings.Contains(call.Path, "/runs/7/attempts/2/jobs") {
					t.Fatalf("not an exact read: %+v", call)
				}
			}
		})
	}
	// Legacy source reads retain their existing loose response contract.
	transport := &FakeTransport{Responses: []ApiResponse{{Status: 200, Body: []byte(`{"jobs":[{"id":1}]}`)}}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	if jobs, err := client.ListWorkflowRunJobs("acme", "widgets", 7); err != nil || len(jobs) != 1 {
		t.Fatal("legacy listing changed")
	}
}
