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

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// prDaemon is a fake daemon for `grain pr`: it records each request and
// answers it from the vocabulary in pkg/model/pull_request_adopt.go.
type prDaemon struct {
	seen   []string
	bodies []string
}

func (d *prDaemon) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	d.seen = append(d.seen, r.Method+" "+r.URL.RequestURI())
	d.bodies = append(d.bodies, string(body))
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/pull-requests/adopt":
		var req ui.AdoptPullRequestRequest
		_ = json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(model.PullRequestAdoption{TaskID: "77", State: "proposed", Mode: req.Mode, PR: "acme/widgets#7", Spec: req.Spec})
	case "/api/pull-requests/review":
		var req ui.ReviewPullRequestRequest
		_ = json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(model.PullRequestReviewFiling{TaskID: "78", State: "queued", PR: "acme/widgets#7", Design: req.Design})
	case "/api/pull-requests/status":
		report := model.PullRequestReport{
			PR: "acme/widgets#7", Head: "abc123", State: "open",
			Shepherd:   &model.PullRequestShepherd{TaskID: "77", Mode: "review-only", State: "running"},
			CI:         &model.PullRequestCI{RunID: "run-9", Status: "completed", Conclusion: "failure", FailingJobs: []string{"go unit"}},
			Reviews:    []model.PullRequestReviewInfo{{TaskID: "78", Kind: "design", State: "findings", Findings: 1, UnresolvedThreads: 1}},
			MergeQueue: "held: ci",
		}
		if r.URL.Query().Get("threads") == "1" {
			report.Threads = []model.PullRequestThread{{Path: "design/design/x.design.md", Line: 4, Body: "overlaps y.design.md"}}
		}
		json.NewEncoder(w).Encode(report)
	case "/api/pull-requests/submit":
		var req ui.SubmitPullRequestRequest
		_ = json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(ui.SubmitPullRequestResult{
			PR: "acme/widgets#7", TaskID: "77", State: "awaiting_submit",
			Approved: len(req.OverrideGates) == 0, Overridden: req.OverrideGates,
			Gates: []model.MergeGate{
				{ID: model.GateCI, Label: "CI", State: model.GateStatePassed, Detail: "passed"},
				{ID: model.GateSecurityReview, Label: "Security review", State: model.GateStateRunning, Detail: "running (task #78)"},
				{ID: model.GateApproval, Label: "Approval", State: model.GateStatePassed, Detail: "given by alice"},
			},
		})
	default:
		http.NotFound(w, r)
	}
}

func runPR(t *testing.T, d *prDaemon, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(d.handler))
	t.Cleanup(srv.Close)
	var err error
	out := captureStdout(t, func() {
		err = cmdPR(context.Background(), ui.NewHTTPClient(srv.URL), &printer{}, args)
	})
	return out, err
}

func TestGrainPRReviewDesignAndPRStatusThreadsJSON(t *testing.T) {
	d := &prDaemon{}

	// review --design, with the flag after the pull request.
	out, err := runPR(t, d, "review", "https://github.com/acme/widgets/pull/7", "--design", "--json")
	if err != nil {
		t.Fatalf("grain pr review: %v", err)
	}
	if d.seen[0] != "POST /api/pull-requests/review" ||
		!strings.Contains(d.bodies[0], `"pr":"https://github.com/acme/widgets/pull/7"`) || !strings.Contains(d.bodies[0], `"design":true`) {
		t.Fatalf("grain pr review sent %s %s", d.seen[0], d.bodies[0])
	}
	var filing model.PullRequestReviewFiling
	if err := json.Unmarshal([]byte(out), &filing); err != nil || filing.TaskID != "78" || !filing.Design {
		t.Fatalf("grain pr review --json printed %q (%v)", out, err)
	}

	// status --threads --json carries every part of the report.
	out, err = runPR(t, d, "status", "acme/widgets#7", "--threads", "--json")
	if err != nil {
		t.Fatalf("grain pr status: %v", err)
	}
	if got := d.seen[len(d.seen)-1]; got != "GET /api/pull-requests/status?pr=acme%2Fwidgets%237&threads=1" {
		t.Fatalf("grain pr status asked %q", got)
	}
	var report model.PullRequestReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("grain pr status --json printed %q: %v", out, err)
	}
	if report.Head != "abc123" || report.Shepherd == nil || report.Shepherd.Mode != "review-only" ||
		report.CI == nil || report.CI.FailingJobs[0] != "go unit" || len(report.Reviews) != 1 ||
		len(report.Threads) != 1 || report.Threads[0].Line != 4 || report.MergeQueue != "held: ci" {
		t.Errorf("grain pr status --json = %+v", report)
	}

	// The human-readable form says the same.
	out, err = runPR(t, d, "status", "acme/widgets#7", "-threads")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"head:        abc123", "task 77, review-only", "run-9 failure (failing: go unit)",
		"merge queue: held: ci", "design    findings  1 findings, 1 unresolved threads",
		"design/design/x.design.md:4 [open]", "overlaps y.design.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("grain pr status output lacks %q:\n%s", want, out)
		}
	}

	// adopt defaults to review-only and says a proposal waits for a person.
	out, err = runPR(t, d, "adopt", "acme/widgets#7", "-spec", "design/spec/x/x.spec.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.bodies[len(d.bodies)-1], `"mode":"review-only"`) ||
		!strings.Contains(out, "task 77 adopted acme/widgets#7") || !strings.Contains(out, "waits for a person") {
		t.Errorf("grain pr adopt sent %s and printed:\n%s", d.bodies[len(d.bodies)-1], out)
	}

	// Usage errors never reach the daemon.
	before := len(d.seen)
	for _, args := range [][]string{{"status"}, {"status", "widgets"}, {"adopt", "acme/widgets#7", "-mode", "yolo"}, {"merge", "acme/widgets#7"}} {
		if _, err := runPR(t, d, args...); err == nil {
			t.Errorf("grain pr %v succeeded, want a usage error", args)
		}
	}
	if len(d.seen) != before {
		t.Errorf("usage errors reached the daemon: %v", d.seen[before:])
	}
}

func TestGrainPRSubmitAndGateOverride(t *testing.T) {
	d := &prDaemon{}

	// Plain submit records approval without overriding gates.
	out, err := runPR(t, d, "submit", "acme/widgets#7")
	if err != nil {
		t.Fatalf("grain pr submit: %v", err)
	}
	if !strings.Contains(out, "approval recorded") || !strings.Contains(out, "security-review") {
		t.Errorf("grain pr submit output = %q", out)
	}

	// Override requires both explicit --override-gates and --reason; no wildcard allowed.
	before := len(d.seen)
	for _, bad := range [][]string{
		{"submit", "acme/widgets#7", "--override-gates=security-review"},
		{"submit", "acme/widgets#7", "--reason", "hotfix"},
		{"submit", "acme/widgets#7", "--override-gates=all", "--reason", "hotfix"},
	} {
		if _, err := runPR(t, d, bad...); err == nil {
			t.Errorf("grain pr %v succeeded, want usage error", bad)
		}
	}
	if len(d.seen) != before {
		t.Errorf("invalid submit args reached daemon: %v", d.seen[before:])
	}

	// Explicit --override-gates=security-review --reason succeeds.
	out, err = runPR(t, d, "submit", "acme/widgets#7", "--override-gates=security-review", "--reason", "hotfix verified manually")
	if err != nil {
		t.Fatalf("grain pr submit --override-gates: %v", err)
	}
	if !strings.Contains(d.bodies[len(d.bodies)-1], `"overrideGates":["security-review"]`) ||
		!strings.Contains(d.bodies[len(d.bodies)-1], `"reason":"hotfix verified manually"`) ||
		!strings.Contains(out, "overrode gate(s) security-review") {
		t.Errorf("grain pr submit override sent %s and printed:\n%s", d.bodies[len(d.bodies)-1], out)
	}
}
