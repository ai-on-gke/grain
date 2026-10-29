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

package orchestrator_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The whole inbound path the GitHub App bot runs on, end to end: raw
// GitHub payloads POSTed to the github-webhook gateway's own handler
// (what cmd/github-webhook serves), read back over its signed relay by
// ghwebhook.Client (what the daemon's -github-webhook-url consumer
// does), and reconciled into the store. The other tests in this package
// hand the reconciler ready-made ghwebhook.Events; this one makes sure
// the payloads GitHub actually sends come out the far end as the same
// CI jobs, metrics and takeovers -- and that a delivery the gateway
// refuses never reaches grain at all.

const (
	relayTestWebhookSecret = "test-webhook-secret"
	relayTestRelaySecret   = "test-relay-secret"
)

// relaySource is a GitHubEventSource over the gateway's signed relay.
type relaySource struct {
	client *ghwebhook.Client
	acked  uint64
}

func (r *relaySource) Pending() ([]ghwebhook.Event, error) {
	resp, err := r.client.Poll(context.Background(), r.acked, 100, 0)
	return resp.Events, err
}

func (r *relaySource) Ack(seq uint64) error {
	if seq > r.acked {
		r.acked = seq
	}
	return nil
}

func postWebhook(t *testing.T, url, event, delivery, secret string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(ghwebhook.GitHubEventHeader, event)
	req.Header.Set(ghwebhook.GitHubDeliveryHeader, delivery)
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set(ghwebhook.GitHubSignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func workflowJobPayload(id int64, name, conclusion, sha string, started, done time.Time) []byte {
	return []byte(fmt.Sprintf(`{"action":"completed","repository":{"full_name":"acme/widgets"},
"sender":{"login":"github-actions[bot]","type":"Bot"},
"workflow_job":{"id":%d,"run_id":555,"run_attempt":1,"workflow_name":"CI","head_branch":"feature/webhooks",
"head_sha":%q,"name":%q,"status":"completed","conclusion":%q,"started_at":%q,"completed_at":%q,
"html_url":"https://github.com/acme/widgets/actions/runs/555/job/%d",
"check_run_url":"https://api.github.com/repos/acme/widgets/check-runs/%d",
"steps":[{"name":"checkout","status":"completed","conclusion":"success"},
{"name":"go test","status":"completed","conclusion":%q}]}}`,
		id, sha, name, conclusion, started.Format(time.RFC3339), done.Format(time.RFC3339), id, id, conclusion))
}

func TestGitHubWebhookGatewayRelayToMetricsAndTakeover(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t, "bob")

	st, err := ghwebhook.OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	gw := httptest.NewServer(ghwebhook.NewServer(ghwebhook.ServerConfig{
		Store: st, WebhookSecret: relayTestWebhookSecret, RelaySecret: relayTestRelaySecret,
	}))
	t.Cleanup(gw.Close)
	source := &relaySource{client: &ghwebhook.Client{BaseURL: gw.URL, RelaySecret: relayTestRelaySecret}}
	deps := f.deps
	deps.GitHubEvents = source

	// The relay itself refuses a reader without the relay secret.
	bad := &ghwebhook.Client{BaseURL: gw.URL, RelaySecret: "not-the-relay-secret"}
	if _, err := bad.Poll(f.ctx, 0, 10, 0); err == nil {
		t.Fatal("relay answered a reader signing with the wrong secret")
	}

	started := baseTime.Add(-10 * time.Minute)
	done := baseTime.Add(-5 * time.Minute)
	takeOver := []byte(fmt.Sprintf(`{"action":"created","repository":{"full_name":"acme/widgets"},
"sender":{"login":"alice","type":"User"},
"issue":{"number":%d,"title":"Human PR","state":"open","user":{"login":"bob"},"pull_request":{"url":"x"}},
"comment":{"id":7001,"body":"@grain-bot take over"}}`, f.pr))

	// Refused at the gateway: unsigned, and signed with the wrong secret.
	// Neither may file anything, take anything over or record a job.
	for _, secret := range []string{"", "wrong-secret"} {
		if code := postWebhook(t, gw.URL, "issue_comment", "refused-"+secret, secret, takeOver); code != http.StatusUnauthorized {
			t.Fatalf("issue_comment signed with %q: status %d, want 401", secret, code)
		}
		if code := postWebhook(t, gw.URL, "workflow_job", "refused-job-"+secret, secret,
			workflowJobPayload(8000, "test", "failure", "feedbeef0001", started, done)); code != http.StatusUnauthorized {
			t.Fatalf("workflow_job signed with %q: status %d, want 401", secret, code)
		}
	}
	runGitHubEventsReconciler(t, f.ctx, deps, baseTime)
	if tasks, err := f.store.TasksWithOrigin(f.ctx, model.GitHubOrigin(f.repo, f.pr)); err != nil || len(tasks) != 0 {
		t.Fatalf("refused deliveries filed tasks %+v (%v)", tasks, err)
	}

	// Accepted: a failed and a passed Actions job, the failed job's own
	// check_run delivery (the same job, not a second one), a third-party
	// check run, a redelivery of the first, and the take-over comment.
	accepted := []struct {
		event, delivery string
		body            []byte
	}{
		{"workflow_job", "d-test", workflowJobPayload(9001, "test", "failure", "feedbeef0001", started, done)},
		{"workflow_job", "d-build", workflowJobPayload(9002, "build", "success", "feedbeef0001", started, done)},
		{"check_run", "d-test-cr", []byte(`{"action":"completed","repository":{"full_name":"acme/widgets"},
"check_run":{"id":9001,"name":"test","head_sha":"feedbeef0001","status":"completed","conclusion":"failure",
"app":{"slug":"github-actions"},"check_suite":{"head_branch":"feature/webhooks"}}}`)},
		{"check_run", "d-codecov", []byte(fmt.Sprintf(`{"action":"completed","repository":{"full_name":"acme/widgets"},
"check_run":{"id":9100,"name":"codecov/project","head_sha":"feedbeef0001","status":"completed","conclusion":"failure",
"started_at":%q,"completed_at":%q,"app":{"slug":"codecov"},
"output":{"title":"Coverage 71%%","summary":"Coverage dropped below 80%%","text":null},
"check_suite":{"head_branch":"feature/webhooks"}}}`, started.Format(time.RFC3339), done.Format(time.RFC3339)))},
		{"workflow_job", "d-test", workflowJobPayload(9001, "test", "failure", "feedbeef0001", started, done)},
		{"issue_comment", "d-take-over", takeOver},
	}
	for _, d := range accepted {
		if code := postWebhook(t, gw.URL, d.event, d.delivery, relayTestWebhookSecret, d.body); code != http.StatusAccepted {
			t.Fatalf("%s %s: status %d, want 202", d.event, d.delivery, code)
		}
	}
	runGitHubEventsReconciler(t, f.ctx, deps, baseTime)
	if source.acked != 5 {
		t.Errorf("relay cursor = %d, want 5 (the redelivery is deduplicated at the gateway)", source.acked)
	}

	jobs, err := f.store.ListGitHubCIJobs(f.ctx, model.GitHubCIFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("recorded %d GitHub CI jobs, want 3: %+v", len(jobs), jobs)
	}
	test, _ := f.store.GetGitHubCIJob(f.ctx, "acme/widgets", 9001)
	if test == nil || test.Workflow != "CI" || test.FailingStep != "go test" || test.Ref != "feature/webhooks" ||
		test.Commit != "feedbeef0001" || test.Via != "webhook" || test.CompletedAt == nil || !test.CompletedAt.Equal(done) {
		t.Fatalf("Actions job from a raw payload = %+v", test)
	}
	if cov, _ := f.store.GetGitHubCIJob(f.ctx, "acme/widgets", 9100); cov == nil || cov.App != "codecov" ||
		cov.FailureTail == "" {
		t.Fatalf("third-party check from a raw payload = %+v", cov)
	}

	// What /api/metrics, grain metrics and the Metrics pane all draw.
	outcomes, err := f.store.GitHubCIOutcomes(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	rep := metrics.Compute(metrics.Input{
		Window:       metrics.Window{Since: baseTime.Add(-time.Hour), Until: baseTime},
		GitHubCIJobs: outcomes,
	})
	g := rep.GitHubCI
	if g.Source != "github" || g.Jobs != 3 || g.Passed != 1 || g.Failed != 2 {
		t.Fatalf("GitHub CI metrics = source %q, %d jobs, %d passed, %d failed; want github, 3, 1, 2",
			g.Source, g.Jobs, g.Passed, g.Failed)
	}

	// The take-over comment: a shepherd, waiting on its reviews.
	sh := f.shepherd()
	if !hasLink(sh, model.LinkTakeover, model.PullRequestRef{Repo: f.repo, Number: f.pr}.String()) {
		t.Fatalf("shepherd links = %+v, want takes-over", sh.Links)
	}
	reviews := f.reviews()
	if len(reviews) == 0 {
		t.Fatal("take-over filed no review")
	}
	for _, r := range reviews {
		if !hasLink(sh, model.LinkDependsOn, r.ID) {
			t.Errorf("shepherd does not wait on review %s", r.ID)
		}
	}
	if !f.mergeHeld() {
		t.Error("merge queue does not hold the taken-over pull request before its review")
	}
}
