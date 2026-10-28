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
