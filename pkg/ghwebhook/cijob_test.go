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

package ghwebhook

import "testing"

func TestParseWorkflowJobAndCheckRunDeliveries(t *testing.T) {
	ev, err := ParsePayload("workflow_job", "d-1", []byte(`{
		"action": "completed",
		"repository": {"full_name": "acme/widgets"},
		"workflow_job": {
			"id": 4242, "run_id": 77, "run_attempt": 2, "workflow_name": "ci",
			"head_branch": "feature-a", "head_sha": "abc123", "name": "unit",
			"status": "completed", "conclusion": "failure",
			"started_at": "2026-10-27T09:00:00Z", "completed_at": "2026-10-27T09:05:00Z",
			"html_url": "https://github.com/acme/widgets/actions/runs/77/job/4242",
			"check_run_url": "https://api.github.com/repos/acme/widgets/check-runs/4242",
			"steps": [
				{"name": "checkout", "status": "completed", "conclusion": "success"},
				{"name": "go test", "status": "completed", "conclusion": "failure"},
				{"name": "upload", "status": "completed", "conclusion": "skipped"}
			]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.CheckRunID != 4242 || ev.WorkflowName != "ci" || ev.WorkflowRun != 77 || ev.RunAttempt != 2 ||
		ev.HeadRef != "feature-a" || ev.HeadSHA != "abc123" || ev.CheckName != "unit" ||
		ev.CheckConclusion != "failure" || ev.FailingStep != "go test" || ev.CheckApp != "github-actions" ||
		ev.CheckStartedAt == nil || ev.CheckCompletedAt == nil {
		t.Fatalf("workflow_job event = %+v", ev)
	}

	ev, err = ParsePayload("check_run", "d-2", []byte(`{
		"action": "completed",
		"repository": {"full_name": "acme/widgets"},
		"check_run": {
			"id": 5000, "name": "coverage", "head_sha": "abc123", "status": "completed",
			"conclusion": "failure", "app": {"slug": "codecov"},
			"output": {"title": "Coverage dropped", "summary": "71.2% < 80%", "text": null},
			"check_suite": {"head_branch": "feature-a"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.CheckRunID != 5000 || ev.CheckApp != "codecov" || ev.HeadRef != "feature-a" ||
		ev.CheckOutput != "Coverage dropped\n\n71.2% < 80%" {
		t.Fatalf("check_run event = %+v", ev)
	}
}

func TestParseWorkflowRunDeliveryCarriesHeadRepository(t *testing.T) {
	ev, err := ParsePayload("workflow_run", "d-3", []byte(`{
		"action": "completed",
		"repository": {"full_name": "acme/widgets"},
		"workflow_run": {
			"id": 7001,
			"name": "publish",
			"path": ".github/workflows/publish.yml",
			"event": "pull_request",
			"run_attempt": 1,
			"head_branch": "main",
			"head_sha": "a77ac4e2a77ac4e2",
			"head_repository": {"full_name": "mallory/widgets"},
			"status": "completed",
			"conclusion": "failure",
			"html_url": "https://github.com/acme/widgets/actions/runs/7001",
			"run_started_at": "2026-10-31T01:00:00Z",
			"updated_at": "2026-10-31T01:02:00Z"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.WorkflowRun != 7001 || ev.WorkflowName != "publish" ||
		ev.WorkflowPath != ".github/workflows/publish.yml" ||
		ev.WorkflowEvent != "pull_request" || ev.HeadRef != "main" ||
		ev.HeadSHA != "a77ac4e2a77ac4e2" || ev.HeadRepo != "mallory/widgets" ||
		ev.CheckConclusion != "failure" {
		t.Fatalf("workflow_run event = %+v", ev)
	}
}

func TestParseWorkflowRunTimedOutStepAndStatusPushDeliveries(t *testing.T) {
	// 1. workflow_run delivery with run_attempt=0 defaults RunAttempt to 1 and populates workflow metadata.
	ev, err := ParsePayload("workflow_run", "d-wr-1", []byte(`{
		"action": "completed",
		"repository": {"full_name": "ai-on-gke/grain"},
		"workflow_run": {
			"id": 9001,
			"name": "Nightly",
			"path": ".github/workflows/nightly.yml",
			"event": "schedule",
			"head_branch": "main",
			"head_sha": "deadbeef1234",
			"status": "completed",
			"conclusion": "timed_out",
			"run_attempt": 0,
			"html_url": "https://github.com/ai-on-gke/grain/actions/runs/9001",
			"run_started_at": "2026-10-31T01:00:00Z",
			"updated_at": "2026-10-31T02:00:00Z"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.WorkflowRun != 9001 || ev.RunAttempt != 1 || ev.WorkflowName != "Nightly" ||
		ev.WorkflowPath != ".github/workflows/nightly.yml" || ev.WorkflowEvent != "schedule" ||
		ev.HeadRef != "main" || ev.HeadSHA != "deadbeef1234" || ev.CheckConclusion != "timed_out" ||
		ev.CheckApp != "github-actions" || ev.CheckStartedAt == nil || ev.CheckCompletedAt == nil {
		t.Fatalf("workflow_run event = %+v", ev)
	}

	// 2. workflow_job with a timed_out step records that step as FailingStep and falls back to job.ID when check_run_url is absent.
	ev, err = ParsePayload("workflow_job", "d-wj-timeout", []byte(`{
		"action": "completed",
		"repository": {"full_name": "ai-on-gke/grain"},
		"workflow_job": {
			"id": 7777,
			"run_id": 9001,
			"run_attempt": 1,
			"workflow_name": "Nightly",
			"head_branch": "main",
			"head_sha": "deadbeef1234",
			"name": "go-long",
			"status": "completed",
			"conclusion": "timed_out",
			"steps": [
				{"name": "Checkout", "status": "completed", "conclusion": "success"},
				{"name": "Run go-long shard", "status": "completed", "conclusion": "timed_out"}
			]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.CheckRunID != 7777 || ev.FailingStep != "Run go-long shard" || ev.CheckConclusion != "timed_out" {
		t.Fatalf("timed_out workflow_job event = %+v", ev)
	}

	// 3. push and status deliveries populate HeadRef/HeadSHA and CheckName/CheckConclusion.
	pushEv, err := ParsePayload("push", "d-push", []byte(`{
		"ref": "refs/heads/main",
		"after": "cafebabe5678",
		"repository": {"full_name": "ai-on-gke/grain"}
	}`))
	if err != nil || pushEv.HeadRef != "main" || pushEv.HeadSHA != "cafebabe5678" {
		t.Fatalf("push event = %+v, err = %v", pushEv, err)
	}

	statusEv, err := ParsePayload("status", "d-status", []byte(`{
		"sha": "cafebabe5678",
		"context": "cla/google",
		"state": "success",
		"repository": {"full_name": "ai-on-gke/grain"}
	}`))
	if err != nil || statusEv.HeadSHA != "cafebabe5678" || statusEv.CheckName != "cla/google" || statusEv.CheckConclusion != "success" {
		t.Fatalf("status event = %+v, err = %v", statusEv, err)
	}
}
