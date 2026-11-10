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
	"strconv"
	"strings"
	"testing"
	"time"

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
	store    *model.Store
	existing map[string]string
}

func (f *fakeAdopter) AdoptPullRequest(ctx context.Context, in model.AdoptPullRequestInput) (model.PullRequestAdoption, error) {
	if f.store != nil {
		if id, ok := f.existing[in.PR.String()]; ok {
			task, err := f.store.GetTask(ctx, id)
			if err != nil || task == nil {
				return model.PullRequestAdoption{}, err
			}
			if task.Approval == nil && in.Approved {
				if in.CheckApprove != nil {
					if err := in.CheckApprove(ctx, *task); err != nil {
						return model.PullRequestAdoption{}, err
					}
				}
				if err := f.store.Approve(ctx, task.ID, in.Requester, time.Now().UTC()); err != nil {
					return model.PullRequestAdoption{}, err
				}
			}
			f.adopted = append(f.adopted, in)
			state, err := f.store.State(ctx, task.ID)
			if err != nil {
				return model.PullRequestAdoption{}, err
			}
			updated, _ := f.store.GetTask(ctx, task.ID)
			autoMerge := updated != nil && updated.AutoMerge
			return model.PullRequestAdoption{
				TaskID: task.ID, State: string(state), Mode: string(in.Mode), PR: in.PR.String(),
				Existing: true, AutoMerge: autoMerge, Spec: in.Spec, Design: in.Design,
			}, nil
		}
	}
	f.adopted = append(f.adopted, in)
	state := model.StateQueued
	if !in.Approved {
		state = model.StateProposed
	}
	autoMerge := in.Approved && in.AllowAutoMerge && in.Requester.Actor.Kind == model.PrincipalHuman
	return model.PullRequestAdoption{TaskID: "77", State: string(state), Mode: string(in.Mode), PR: in.PR.String(),
		AutoMerge: autoMerge, Spec: in.Spec, Design: in.Design}, nil
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

func TestAdoptPullRequestEnforcesAutoMergeRoleApprovalWithinAndExternalApprovalRail(t *testing.T) {
	ctx := context.Background()
	installScopeSets(t,
		model.APIScopeSet{
			Name:    "widgets-adopt-admin",
			Scopes:  []string{"merge:write", "tasks:read", "tasks:write", "tasks:admin"},
			Repos:   []string{"acme/widgets"},
			Version: 1,
		},
	)
	h := newRoleHarness(t)
	enableExternalAgentAPI(t, h.client.Store)
	h.allow(t, "user:wade@example.com", "widgets-adopt-admin")
	h.allow(t, "user:operator@example.com", "grain/operator")
	h.allow(t, "user:alice@example.com", "grain/operator")
	h.allow(t, "user:jane@example.com", "grain/operator")

	fake := &fakeAdopter{store: h.client.Store, existing: map[string]string{}}
	h.client.Config.PullRequests = fake

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	gadgets := model.RepoRef{Owner: "acme", Name: "gadgets"}
	if err := h.client.Store.PutTemplate(ctx, model.Template{
		ID:     "heavy-review",
		Name:   "heavy review",
		Title:  "heavy review",
		Writes: []model.WriteTarget{{Repo: gadgets}},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. New collaborator PRs: only an unlimited human role gets AllowAutoMerge.
	code, body := h.call("operator@example.com", http.MethodPost, "/api/pull-requests/adopt", `{"pr":"acme/widgets#10"}`)
	if code != http.StatusOK || !strings.Contains(body, `"autoMerge":true`) {
		t.Fatalf("operator adopt new PR: %d %s, want 200 with autoMerge:true", code, body)
	}
	if last := fake.adopted[len(fake.adopted)-1]; !last.Approved || !last.AllowAutoMerge {
		t.Fatalf("operator adopt input = %+v, want Approved && AllowAutoMerge", last)
	}

	code, body = h.call("wade@example.com", http.MethodPost, "/api/pull-requests/adopt", `{"pr":"acme/widgets#11"}`)
	if code != http.StatusOK || !strings.Contains(body, `"autoMerge":false`) {
		t.Fatalf("limited role adopt new PR: %d %s, want 200 with autoMerge:false", code, body)
	}
	if last := fake.adopted[len(fake.adopted)-1]; !last.Approved || last.AllowAutoMerge {
		t.Fatalf("limited role adopt input = %+v, want Approved && !AllowAutoMerge", last)
	}

	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	mint := func(name, owner string, scopes ...string) string {
		t.Helper()
		issued, err := h.client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
			Name: name, Owner: owner, Scopes: scopes, Repos: []string{"acme/widgets"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Token
	}
	adminTok := mint("admin-bot", "alice@example.com", "merge:write", "tasks:read", "tasks:write", "tasks:admin")
	trustedTok := mint("trusted-bot", "jane@example.com", "merge:write", "tasks:read", "tasks:write")

	rules := []model.OwnerTrustRule{{Owner: "jane@example.com", Repo: "acme/widgets"}}
	netNone := string(model.NetworkNone)
	if _, err := h.client.UpdateSettings(humanCtx, ui.UpdateSettingsRequest{OwnerTrust: &rules, NetworkAccess: &netNone}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, tok string
		wantOwner bool
	}{
		{"external tasks:admin", adminTok, false},
		{"external owner-trust", trustedTok, true},
	} {
		rec := fileAsExternal(t, h.srv, tc.tok, "/api/pull-requests/adopt", `{"pr":"acme/widgets#12"}`, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"autoMerge":false`) {
			t.Fatalf("%s adopt new PR: %d %s, want 200 with autoMerge:false", tc.name, rec.Code, rec.Body)
		}
		last := fake.adopted[len(fake.adopted)-1]
		if !last.Approved || last.AllowAutoMerge {
			t.Fatalf("%s adopt input = %+v, want Approved && !AllowAutoMerge", tc.name, last)
		}
		if tc.wantOwner && (last.Approval == nil || last.Approval.Actor != model.OwnerTrustPrincipal) {
			t.Fatalf("%s approval = %+v, want OwnerTrustPrincipal", tc.name, last.Approval)
		}
	}

	// 2. Existing unapproved shepherd tasks: limited role is gated by roleApprovalWithin.
	seq := 0
	seedShepherd := func(prNum int, edit func(*model.Task)) string {
		t.Helper()
		seq++
		id := "8" + strconv.Itoa(1000+seq)[1:]
		ref := "acme/widgets#" + strconv.Itoa(prNum)
		task := model.Task{
			ID:     id,
			Intent: model.IntentImplement,
			Title:  "Adopt " + ref,
			Target: &widgets,
			Links: []model.Link{
				{Kind: model.LinkTakeover, Target: ref},
				{Kind: model.LinkTakeoverMode, Target: string(model.TakeoverReviewOnly)},
			},
		}
		if edit != nil {
			edit(&task)
		}
		if err := h.client.Store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		fake.existing[ref] = id
		return id
	}

	for i, tc := range []struct {
		name string
		edit func(*model.Task)
		want string
	}{
		{"autoMerge", func(tk *model.Task) { tk.AutoMerge = true }, "autoMerge"},
		{"ungrantedCapability", func(tk *model.Task) {
			tk.Grants = []model.Grant{{Capability: "api:deployment:read", Via: model.GrantByLabel}}
		}, "api:deployment:read"},
		{"outOfScopeWrites", func(tk *model.Task) { tk.Writes = []model.WriteTarget{{Repo: gadgets}} }, "acme/gadgets"},
		{"outOfScopeReads", func(tk *model.Task) { tk.Reads = []model.RepoRef{gadgets} }, "acme/gadgets"},
		{"widenedNetworkAccess", func(tk *model.Task) { tk.NetworkAccess = model.NetworkOpen }, "networkAccess"},
		{"reviewTemplateId", func(tk *model.Task) { tk.ReviewTemplateID = "heavy-review" }, "reviewTemplateId"},
	} {
		prNum := 20 + i
		id := seedShepherd(prNum, tc.edit)
		payload := `{"pr":"acme/widgets#` + strconv.Itoa(prNum) + `"}`
		code, body := h.call("wade@example.com", http.MethodPost, "/api/pull-requests/adopt", payload)
		if code != http.StatusForbidden || !strings.Contains(body, tc.want) {
			t.Errorf("limited role adopt existing %s (%s): %d %s, want 403 naming %q", tc.name, id, code, body, tc.want)
		}
		out, isErr := callControllerTool(t, h.srv, h.as("wade@example.com"), ui.ToolGrainInvoke, map[string]any{
			"tool":      "api_adopt_pull_request",
			"arguments": map[string]any{"pr": "acme/widgets#" + strconv.Itoa(prNum)},
		})
		if !isErr || !strings.Contains(out, "403") || !strings.Contains(out, tc.want) {
			t.Errorf("limited role MCP adopt existing %s (%s): isErr=%v %s, want 403 naming %q", tc.name, id, isErr, out, tc.want)
		}
		wantProposed(t, h.client, id)
	}

	plainRoleID := seedShepherd(30, nil)
	if code, body := h.call("wade@example.com", http.MethodPost, "/api/pull-requests/adopt", `{"pr":"acme/widgets#30"}`); code != http.StatusOK {
		t.Fatalf("limited role adopt plain unapproved shepherd: %d %s, want 200", code, body)
	}
	wantQueued(t, h.client, plainRoleID)

	// 3. Existing unapproved shepherd tasks: external gma_ tokens are gated by externalApprovalRail + X-Grain-Acting-Reason.
	// 3a. Owner-trusted token without tasks:admin cannot approve an existing unapproved shepherd.
	ownerTrustExistingID := seedShepherd(40, nil)
	rec := fileAsExternal(t, h.srv, trustedTok, "/api/pull-requests/adopt", `{"pr":"acme/widgets#40"}`,
		map[string]string{ui.ActingReasonHeader: "trying owner-trust on existing"})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tasks:admin") {
		t.Errorf("owner-trusted gma_ without tasks:admin on existing shepherd: %d %s, want 403 naming tasks:admin", rec.Code, rec.Body)
	}
	wantProposed(t, h.client, ownerTrustExistingID)

	// 3b. tasks:admin token without X-Grain-Acting-Reason is refused.
	noReasonID := seedShepherd(41, nil)
	rec = fileAsExternal(t, h.srv, adminTok, "/api/pull-requests/adopt", `{"pr":"acme/widgets#41"}`, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), ui.ActingReasonHeader) {
		t.Errorf("tasks:admin gma_ without reason on existing shepherd: %d %s, want 403 naming %s", rec.Code, rec.Body, ui.ActingReasonHeader)
	}
	wantProposed(t, h.client, noReasonID)

	// 3c. tasks:admin token cannot self-approve a shepherd it filed itself, or one held for a person.
	selfFiledID := seedShepherd(42, func(tk *model.Task) {
		tk.Origin.Attribution.Actor = model.Principal{
			Kind: model.PrincipalAgent, ID: "external:admin-bot", Source: model.SourceExternalToken,
		}
	})
	personHeldAutoMergeID := seedShepherd(43, func(tk *model.Task) { tk.AutoMerge = true })
	personHeldTagID := seedShepherd(44, func(tk *model.Task) { tk.Tags = append(tk.Tags, model.HoldRequestedTag) })

	for _, tc := range []struct {
		prNum int
		id    string
		want  string
	}{
		{42, selfFiledID, "it filed it"},
		{43, personHeldAutoMergeID, "auto-merge"},
		{44, personHeldTagID, "hold_for_approval"},
	} {
		payload := `{"pr":"acme/widgets#` + strconv.Itoa(tc.prNum) + `"}`
		rec = fileAsExternal(t, h.srv, adminTok, "/api/pull-requests/adopt", payload,
			map[string]string{ui.ActingReasonHeader: "testing external approval rail"})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("REST external adopt on PR #%d (%s): %d %s, want 403 naming %q", tc.prNum, tc.id, rec.Code, rec.Body, tc.want)
		}
		out, isErr := callControllerTool(t, h.srv, map[string]string{"Authorization": "Bearer " + adminTok}, ui.ToolGrainInvoke, map[string]any{
			"tool":      "api_adopt_pull_request",
			"arguments": map[string]any{"pr": "acme/widgets#" + strconv.Itoa(tc.prNum)},
			"reason":    "testing external approval rail",
		})
		if !isErr || !strings.Contains(out, "403") || !strings.Contains(out, tc.want) {
			t.Errorf("MCP external adopt on PR #%d (%s): isErr=%v %s, want 403 naming %q", tc.prNum, tc.id, isErr, out, tc.want)
		}
		wantProposed(t, h.client, tc.id)
	}

	// 3d. tasks:admin token with X-Grain-Acting-Reason approves a plain unapproved shepherd filed by someone else.
	okExtID := seedShepherd(45, func(tk *model.Task) {
		tk.Origin.Attribution.Actor = model.Principal{Kind: model.PrincipalHuman, ID: "contributor@example.com"}
	})
	rec = fileAsExternal(t, h.srv, adminTok, "/api/pull-requests/adopt", `{"pr":"acme/widgets#45"}`,
		map[string]string{ui.ActingReasonHeader: "approving contributor shepherd"})
	if rec.Code != http.StatusOK {
		t.Fatalf("tasks:admin gma_ with reason on plain shepherd: %d %s, want 200", rec.Code, rec.Body)
	}
	wantQueued(t, h.client, okExtID)
}
