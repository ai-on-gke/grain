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

package daemon

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

func TestStateRepoPullRequestClientForwardsExactHealthJobReads(t *testing.T) {
	transport := &github.FakeTransport{Responses: []github.ApiResponse{{Status: 200, Body: []byte(`{"total_count":1,"jobs":[{"id":71,"run_id":7,"name":"test","status":"completed","conclusion":"cancelled"}]}`)}}}
	var client github.Client = &StateRepoPullRequestClient{Client: github.NewClient(transport, nil)}
	client = github.WithCaller(client, "health-observations")
	reader, ok := client.(github.WorkflowRunAttemptJobsReader)
	if !ok {
		t.Fatal("daemon/caller wrapper hid the current health source reader")
	}
	jobs, err := reader.ListWorkflowRunAttemptJobs("acme", "widgets", 7, 2)
	if err != nil || len(jobs) != 1 || jobs[0].Conclusion != "cancelled" {
		t.Fatalf("jobs %+v err %v", jobs, err)
	}
	if len(transport.Calls) != 1 || transport.Calls[0].Method != "GET" || !strings.Contains(transport.Calls[0].Path, "/runs/7/attempts/2/jobs") {
		t.Fatal("wrapper changed the exact read scope")
	}
	unsupported := &StateRepoPullRequestClient{Client: struct{ github.Client }{}}
	if _, err := unsupported.ListWorkflowRunAttemptJobs("acme", "widgets", 7, 2); err == nil {
		t.Fatal("unsupported source silently supplied complete evidence")
	}
}
