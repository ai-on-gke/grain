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

package ui_test

// POST /api/pull-requests/adopt, POST /api/pull-requests/review and GET
// /api/pull-requests/status (#XC-28, #XC-29). What adopts and reviews is
// orchestrator's (takeover_adopt_test.go covers it); these tests stand a
// fake in its place and assert on the routes around it: what they parse,
// which scopes they ask for on the pull request's repository, and whether
// what they file lands approved.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeAdopter is a ui.PullRequests that is also a ui.PullRequestAdopter.
type fakeAdopter struct {
	fakePullRequests
	adopted  []model.AdoptPullRequestInput
	reviewed []model.ReviewPullRequestInput
	statuses []string
	report   model.PullRequestReport
}

func (f *fakeAdopter) AdoptPullRequest(_ context.Context, in model.AdoptPullRequestInput) (model.PullRequestAdoption, error) {
	f.adopted = append(f.adopted, in)
	state := model.StateQueued
	if !in.Approved {
		state = model.StateProposed
	}
	return model.PullRequestAdoption{TaskID: "77", State: string(state), Mode: string(in.Mode), PR: in.PR.String(),
		Spec: in.Spec, Design: in.Design}, nil
}

func (f *fakeAdopter) ReviewPullRequest(_ context.Context, in model.ReviewPullRequestInput) (model.PullRequestReviewFiling, error) {
	f.reviewed = append(f.reviewed, in)
	return model.PullRequestReviewFiling{TaskID: "78", State: string(model.StateQueued), PR: in.PR.String(),
		Design: in.Design, Spec: in.Spec}, nil
}

func (f *fakeAdopter) PullRequestStatus(_ context.Context, ref model.PullRequestRef, threads bool) (model.PullRequestReport, error) {
	f.statuses = append(f.statuses, ref.String())
	out := f.report
	out.PR = ref.String()
	if !threads {
		out.Threads = nil
	}
	return out, nil
}

// lastRequester is who the fake's newest filing was for, for the
// attribution matrix (mutating_routes_test.go).
func (f *fakeAdopter) lastRequester() model.Principal {
	switch {
	case len(f.adopted) > 0:
		return f.adopted[len(f.adopted)-1].Requester.Actor
	case len(f.reviewed) > 0:
		return f.reviewed[len(f.reviewed)-1].Requester.Actor
	}
	return model.Principal{}
}

func sendPR(t *testing.T, srv *ui.Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestGrainPRReviewDesignAndPRStatusThreadsJSON(t *testing.T) {
	srv, client := testServer(t)
	fake := &fakeAdopter{report: model.PullRequestReport{
		Head:       "abc123",
		Shepherd:   &model.PullRequestShepherd{TaskID: "77", Mode: "review-only", State: "running"},
		CI:         &model.PullRequestCI{RunID: "run-9", Status: "completed", Conclusion: "failure", FailingJobs: []string{"go unit"}},
		Reviews:    []model.PullRequestReviewInfo{{TaskID: "78", Kind: "design", State: "findings", Findings: 1, UnresolvedThreads: 1}},
		Threads:    []model.PullRequestThread{{Path: "design/design/x.design.md", Line: 4, Body: "overlaps y.design.md"}},
		MergeQueue: "held: ci",
	}}
	client.Config.PullRequests = fake
	ctx := context.Background()

	// A person's design review of a PR named by its URL.
	rec := sendPR(t, srv, http.MethodPost, "/api/pull-requests/review", "",
		`{"pr":"https://github.com/acme/widgets/pull/7","design":true,"spec":"design/spec/x/x.spec.md"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("review answered %d: %s", rec.Code, rec.Body)
	}
	var filing model.PullRequestReviewFiling
	if err := json.Unmarshal(rec.Body.Bytes(), &filing); err != nil || filing.TaskID != "78" || !filing.Design {
		t.Fatalf("review answer = %s (%v)", rec.Body, err)
	}
	if len(fake.reviewed) != 1 || fake.reviewed[0].PR.String() != "acme/widgets#7" || !fake.reviewed[0].Design ||
		fake.reviewed[0].Spec != "design/spec/x/x.spec.md" || !fake.reviewed[0].Approved {
		t.Fatalf("review asked = %+v, want an approved design review of acme/widgets#7", fake.reviewed)
	}

	// Status with and without threads.
	q := url.Values{"pr": {"acme/widgets#7"}, "threads": {"1"}}
	rec = sendPR(t, srv, http.MethodGet, "/api/pull-requests/status?"+q.Encode(), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status answered %d: %s", rec.Code, rec.Body)
	}
	var report map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"head", "shepherd", "ci", "reviews", "threads", "mergeQueue"} {
		if _, ok := report[key]; !ok {
			t.Errorf("status JSON has no %q: %s", key, rec.Body)
		}
	}
	if !strings.Contains(rec.Body.String(), `"failingJobs":["go unit"]`) || !strings.Contains(rec.Body.String(), `"line":4`) {
		t.Errorf("status JSON lacks failing jobs or thread lines: %s", rec.Body)
	}
	rec = sendPR(t, srv, http.MethodGet, "/api/pull-requests/status?pr=acme/widgets%237", "", "")
	if strings.Contains(rec.Body.String(), `"threads"`) {
		t.Errorf("status without threads=1 still lists threads: %s", rec.Body)
	}
	if rec = sendPR(t, srv, http.MethodGet, "/api/pull-requests/status?pr=widgets", "", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("status of a malformed pr answered %d, want 400", rec.Code)
	}

	// External agents: each route's second scope is asked for on the
	// PR's own repository, and what they adopt lands proposed (#XC-17).
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	token := func(name string, scopes ...string) string {
		issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
			Name: name, Scopes: scopes, Repos: []string{"acme/widgets"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Token
	}
	ciOnly := token("ci-only", "ci:read")
	if rec = sendPR(t, srv, http.MethodGet, "/api/pull-requests/status?pr=acme/widgets%237", ciOnly, ""); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "tasks:read") {
		t.Errorf("status with ci:read alone answered %d %s, want 403 naming tasks:read", rec.Code, rec.Body)
	}
	reader := token("reader", "ci:read", "tasks:read")
	if rec = sendPR(t, srv, http.MethodGet, "/api/pull-requests/status?pr=acme/widgets%237", reader, ""); rec.Code != http.StatusOK {
		t.Errorf("status with ci:read and tasks:read answered %d: %s", rec.Code, rec.Body)
	}
	if rec = sendPR(t, srv, http.MethodGet, "/api/pull-requests/status?pr=other/repo%231", reader, ""); rec.Code != http.StatusForbidden {
		t.Errorf("status of another repository's PR answered %d, want 403", rec.Code)
	}
	mergeOnly := token("merge-only", "merge:write")
	if rec = sendPR(t, srv, http.MethodPost, "/api/pull-requests/adopt", mergeOnly, `{"pr":"acme/widgets#7"}`); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "tasks:write") {
		t.Errorf("adopt with merge:write alone answered %d %s, want 403 naming tasks:write", rec.Code, rec.Body)
	}
	adopter := token("adopter", "merge:write", "tasks:write")
	rec = sendPR(t, srv, http.MethodPost, "/api/pull-requests/adopt", adopter, `{"pr":"acme/widgets#7","mode":"finish","spec":"design/spec/x/x.spec.md"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt with merge:write and tasks:write answered %d: %s", rec.Code, rec.Body)
	}
	if len(fake.adopted) != 1 || fake.adopted[0].Approved || fake.adopted[0].Mode != model.TakeoverFinish ||
		fake.adopted[0].Requester.Actor.Kind != model.PrincipalAgent {
		t.Errorf("adopt asked = %+v, want an unapproved finish adoption for the agent", fake.adopted)
	}
	if rec = sendPR(t, srv, http.MethodPost, "/api/pull-requests/adopt", adopter, `{"pr":"acme/widgets#7","mode":"yolo"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("adopt with a bad mode answered %d, want 400", rec.Code)
	}
}
