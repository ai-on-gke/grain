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
	"context"
	"strings"
	"testing"
)

func TestReadCheckCollectionCompleteAttemptsAndStartupFailure(t *testing.T) {
	transport := &FakeTransport{Responses: []ApiResponse{
		{Status: 200, Headers: map[string]string{"Link": `<https://api.github.com/repos/o/r/commits/H1/check-runs?page=2&filter=all>; rel="next"`}, Body: []byte(`{"total_count":2,"check_runs":[{"id":1,"name":"go","head_sha":"H1","status":"completed","conclusion":"success","app":{"slug":"trusted"},"check_suite":{"id":1}}]}`)},
		{Status: 200, Body: []byte(`{"total_count":2,"check_runs":[{"id":2,"name":"go","head_sha":"H1","status":"completed","conclusion":"cancelled","app":{"slug":"trusted"},"check_suite":{"id":2}}]}`)},
		{Status: 200, Body: []byte(`{"total_count":1,"workflow_runs":[{"id":3,"name":"Tests","path":".github/workflows/tests.yml","head_sha":"H1","status":"completed","conclusion":"startup_failure","run_attempt":2}]}`)},
	}}
	client := NewClient(transport, StaticToken{strPtr("t")})
	collection, err := client.ReadCheckCollection(context.Background(), "o", "r", "H1", 512)
	if err != nil || !collection.Complete || !collection.ChecksComplete || len(collection.Checks) != 2 || len(collection.Workflows) != 1 || collection.Workflows[0].Conclusion != "startup_failure" {
		t.Fatalf("%+v %v", collection, err)
	}
	if !strings.Contains(transport.Calls[0].Path, "filter=all") || !strings.Contains(transport.Calls[2].Path, "head_sha=H1") {
		t.Fatal("unscoped or latest-only request", transport.Calls)
	}
	for _, call := range transport.Calls {
		if call.Method != "GET" {
			t.Fatal("mutating collector call")
		}
	}
}

func TestReadCheckCollectionIncompleteDeniedAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, checks, actions string
		status                int
	}{
		{"complete-empty", `{"total_count":0,"check_runs":[]}`, `{"total_count":0,"workflow_runs":[]}`, 200},
		{"missing-total", `{"check_runs":[]}`, "", 200},
		{"count-gap", `{"total_count":1,"check_runs":[]}`, "", 200},
		{"oversized", `{"total_count":513,"check_runs":[]}`, "", 200},
		{"foreign-head", `{"total_count":1,"check_runs":[{"id":1,"head_sha":"H0","app":{"slug":"trusted"}}]}`, "", 200},
		{"missing-app", `{"total_count":1,"check_runs":[{"id":1,"head_sha":"H1"}]}`, "", 200},
		{"denied-actions", `{"total_count":0,"check_runs":[]}`, `{"message":"Resource not accessible by integration"}`, 403},
		{"missing-actions", `{"total_count":0,"check_runs":[]}`, `{}`, 404},
		{"foreign-workflow", `{"total_count":0,"check_runs":[]}`, `{"total_count":1,"workflow_runs":[{"id":2,"path":"test.yml","head_sha":"H0","run_attempt":1}]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := []ApiResponse{{Status: 200, Body: []byte(tc.checks)}}
			if tc.actions != "" {
				responses = append(responses, ApiResponse{Status: tc.status, Body: []byte(tc.actions)})
			}
			transport := &FakeTransport{Responses: responses}
			client := NewClient(transport, StaticToken{strPtr("t")})
			result, err := client.ReadCheckCollection(context.Background(), "o", "r", "H1", 512)
			if tc.name == "complete-empty" {
				if err != nil || !result.Complete {
					t.Fatal(result, err)
				}
			} else if result.Complete {
				t.Fatal("incomplete collection claimed complete", result)
			}
			if tc.status != 200 && err == nil {
				t.Fatal("source error erased")
			}
			if len(transport.Calls) > len(responses) {
				t.Fatal("read beyond evidence bound")
			}
		})
	}
}

func TestReadCheckCollectionHonoursCancellationBeforeLookup(t *testing.T) {
	transport := NewFakeTransport(ApiResponse{Status: 200})
	client := NewClient(transport, StaticToken{strPtr("t")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ReadCheckCollection(ctx, "o", "r", "H1", 512); err != context.Canceled || len(transport.Calls) != 0 {
		t.Fatal("cancelled collection performed lookup", err, len(transport.Calls))
	}
}
