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
	"fmt"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// rerunRecordingClient is a forge client that can re-run Actions runs and
// records each re-run it was asked for.
type rerunRecordingClient struct {
	github.Client
	calls []string
}

func (c *rerunRecordingClient) RerunFailedJobs(owner, repo string, runID int64) error {
	c.calls = append(c.calls, fmt.Sprintf("failed %s/%s %d", owner, repo, runID))
	return nil
}

func (c *rerunRecordingClient) RerunWorkflowRun(owner, repo string, runID int64) error {
	c.calls = append(c.calls, fmt.Sprintf("run %s/%s %d", owner, repo, runID))
	return nil
}

func (c *rerunRecordingClient) RerunWorkflowJob(owner, repo string, jobID int64) error {
	c.calls = append(c.calls, fmt.Sprintf("job %s/%s %d", owner, repo, jobID))
	return nil
}

// The daemon hands the orchestrator its forge client wrapped in
// StateRepoPullRequestClient, and the orchestrator finds re-runs by
// asserting the optional interfaces on that client. The wrapper used to
// have none of them, so in a real daemon the merge queue's wait-out of a
// runner DNS outage (prinfra.go) always fell through to a repair
// (docs/test-runs/2026-11-13-actions-ci-for-agents.md, F1).
func TestStateRepoPullRequestClientPassesActionsRerunsThrough(t *testing.T) {
	inner := &rerunRecordingClient{}
	var client github.Client = &StateRepoPullRequestClient{Client: inner}

	for _, c := range []github.Client{client, github.WithCaller(client, "merge-queue")} {
		rerunner, ok := c.(github.WorkflowRunRerunner)
		if !ok {
			t.Fatalf("%T is not a github.WorkflowRunRerunner: the merge queue cannot re-run an infrastructure failure", c)
		}
		if err := rerunner.RerunFailedJobs("acme", "widgets", 8101); err != nil {
			t.Fatalf("RerunFailedJobs: %v", err)
		}
		full, ok := c.(github.ActionsRerunner)
		if !ok {
			t.Fatalf("%T is not a github.ActionsRerunner", c)
		}
		if err := full.RerunWorkflowRun("acme", "widgets", 8101); err != nil {
			t.Fatalf("RerunWorkflowRun: %v", err)
		}
		if err := full.RerunWorkflowJob("acme", "widgets", 8101000); err != nil {
			t.Fatalf("RerunWorkflowJob: %v", err)
		}
	}
	want := []string{
		"failed acme/widgets 8101", "run acme/widgets 8101", "job acme/widgets 8101000",
		"failed acme/widgets 8101", "run acme/widgets 8101", "job acme/widgets 8101000",
	}
	if !slices.Equal(inner.calls, want) {
		t.Fatalf("re-runs reaching the forge = %q, want %q", inner.calls, want)
	}
}

// A wrapped client that cannot re-run answers with an error rather than
// pretending to have re-run anything.
func TestStateRepoPullRequestClientRerunWithoutCapabilityRefuses(t *testing.T) {
	client := &StateRepoPullRequestClient{Client: struct{ github.Client }{}}
	if err := client.RerunFailedJobs("acme", "widgets", 1); err == nil {
		t.Fatal("RerunFailedJobs through a client that cannot re-run succeeded")
	}
	if err := client.RerunWorkflowRun("acme", "widgets", 1); err == nil {
		t.Fatal("RerunWorkflowRun through a client that cannot re-run succeeded")
	}
	if err := client.RerunWorkflowJob("acme", "widgets", 1); err == nil {
		t.Fatal("RerunWorkflowJob through a client that cannot re-run succeeded")
	}
}
