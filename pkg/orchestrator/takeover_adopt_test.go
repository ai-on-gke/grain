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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var adoptOperator = model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}}

func (f *takeoverFixture) adopt(in model.AdoptPullRequestInput, now time.Time) model.PullRequestAdoption {
	f.t.Helper()
	in.PR = model.PullRequestRef{Repo: f.repo, Number: f.pr}
	if in.Requester.Actor.ID == "" {
		in.Requester = adoptOperator
		in.AllowAutoMerge = true
	}
	got, err := orchestrator.AdoptPullRequest(f.ctx, f.deps, in, now)
	if err != nil {
		f.t.Fatalf("AdoptPullRequest(%+v): %v", in, err)
	}
	return got
}

func (f *takeoverFixture) setPRBody(body string) {
	for i := range f.sim.PullRequests {
		if f.sim.PullRequests[i].Number == f.pr {
			f.sim.PullRequests[i].Body = body
		}
	}
}

// #XC-28: review-only is the default, reviews and gates without pushing,
// and is never answered on the author's behalf; asking for finish
// upgrades the same shepherd rather than filing a second; asking for
// review-only again never downgrades it.
func TestAdoptPullRequestReviewOnlyVsFinishUpgradeInPlace(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t, "alice")

	got := f.adopt(model.AdoptPullRequestInput{Approved: true}, baseTime)
	sh := f.shepherd()
	ref := model.PullRequestRef{Repo: f.repo, Number: f.pr}.String()
	if got.TaskID != sh.ID || got.Mode != string(model.TakeoverReviewOnly) || got.Existing || got.Upgraded {
		t.Fatalf("adoption = %+v, want a new review-only shepherd %s", got, sh.ID)
	}
	if got.State == string(model.StateProposed) {
		t.Errorf("an approved adoption landed proposed")
	}
	if !got.AutoMerge || !sh.AutoMerge {
		t.Errorf("an approved adoption of a collaborator's PR should ask for auto-merge (trusted-author rule)")
	}
	if !hasLink(sh, model.LinkTakeover, ref) || !hasLink(sh, model.LinkFixes, ref) ||
		!hasLink(sh, model.LinkTakeoverMode, string(model.TakeoverReviewOnly)) {
		t.Errorf("shepherd links = %+v, want takes-over, fixes and takeover-mode review-only", sh.Links)
	}
	if !strings.Contains(sh.Body, "review-only: do not push") || strings.Contains(sh.Body, "Never force-push") {
		t.Errorf("review-only shepherd body does not forbid pushing:\n%s", sh.Body)
	}
	// The git proxy lets a sandbox push its task's Branch: a review-only
	// shepherd must not hold the author's head even where grain could push it.
	if sh.Branch == "feat/human-pr" {
		t.Errorf("review-only shepherd branch = %q, the author's head: it could push it", sh.Branch)
	}
	if n := len(f.reviews()); n != 1 {
		t.Fatalf("reviews filed = %d, want the code review", n)
	}

	// A finding holds the gate, and nobody is woken to fix it for the
	// author: the shepherd's conversation stays empty.
	f.finishReviews(baseTime.Add(time.Minute), reviewFinding("missing nil check", "handler.go", 3))
	f.reconcileTakeovers(baseTime.Add(2 * time.Minute))
	if !f.mergeHeld() {
		t.Fatal("review-only gate released a head whose review left findings")
	}
	if comments, _ := f.store.Comments(f.ctx, sh.ID); len(comments) != 0 {
		t.Errorf("review-only shepherd was woken for findings: %+v", comments)
	}

	// An unapproved request (an agent without tasks:admin) may not turn
	// the approved review-only shepherd into one that pushes.
	agentOwner := model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}
	if _, err := orchestrator.AdoptPullRequest(f.ctx, f.deps, model.AdoptPullRequestInput{
		PR: model.PullRequestRef{Repo: f.repo, Number: f.pr}, Mode: model.TakeoverFinish,
		Requester: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "external:laptop"}, OnBehalfOf: &agentOwner},
	}, baseTime.Add(150*time.Second)); !errors.Is(err, model.ErrPullRequestForbidden) {
		t.Fatalf("unapproved upgrade to finish = %v, want ErrPullRequestForbidden", err)
	}
	if sh = f.shepherd(); !hasLink(sh, model.LinkTakeoverMode, string(model.TakeoverReviewOnly)) || sh.Branch == "feat/human-pr" {
		t.Fatalf("an unapproved upgrade changed the shepherd: links %+v, branch %q", sh.Links, sh.Branch)
	}

	// --mode finish upgrades the same task in place.
	up := f.adopt(model.AdoptPullRequestInput{Mode: model.TakeoverFinish, Approved: true}, baseTime.Add(3*time.Minute))
	if up.TaskID != sh.ID || !up.Existing || !up.Upgraded || up.Mode != string(model.TakeoverFinish) {
		t.Fatalf("upgrade = %+v, want shepherd %s upgraded in place to finish", up, sh.ID)
	}
	sh = f.shepherd() // fails unless there is still exactly one
	if !hasLink(sh, model.LinkTakeoverMode, string(model.TakeoverFinish)) || hasLink(sh, model.LinkTakeoverMode, string(model.TakeoverReviewOnly)) {
		t.Errorf("upgraded shepherd links = %+v, want only takeover-mode finish", sh.Links)
	}
	if sh.Branch != "feat/human-pr" || !strings.Contains(sh.Body, "Never force-push") {
		t.Errorf("upgraded shepherd branch %q / body do not let it push (without force):\n%s", sh.Branch, sh.Body)
	}
	if !strings.HasPrefix(sh.Title, "Take over ") {
		t.Errorf("upgraded shepherd title = %q", sh.Title)
	}

	// review-only again is not a downgrade.
	again := f.adopt(model.AdoptPullRequestInput{Approved: true}, baseTime.Add(4*time.Minute))
	if again.TaskID != sh.ID || again.Upgraded || again.Mode != string(model.TakeoverFinish) {
		t.Fatalf("re-adopting review-only = %+v, want the finish shepherd unchanged", again)
	}

	// And on a second pull request, clean reviews clear a review-only
	// gate (this repository runs no grain CI, so its own checks gate the
	// merge queue instead).
	g := newTakeoverFixture(t, "alice")
	g.adopt(model.AdoptPullRequestInput{Approved: true}, baseTime)
	g.finishReviews(baseTime.Add(time.Minute))
	g.reconcileTakeovers(baseTime.Add(2 * time.Minute))
	if g.mergeHeld() {
		t.Fatalf("review-only gate still holds after clean reviews; links %+v", g.shepherd().Links)
	}
}

// #XC-28 and #XC-17: --spec and the description's "Design:" line are
// linked on the shepherd and on every review, which is asked to check
// conformance; an unapproved adoption lands proposed and files nothing
// until approved; a bad path is refused.
func TestAdoptPullRequestLinksSpecAndDesignIntoConformanceReviewAndProposalGate(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t, "bob")
	f.setPRBody("Adds the adopt route.\n\nDesign: `design/design/external-cli.design.md`\nImplements: ../../etc/passwd\n")
	const spec = "design/spec/external-cli/external-cli.spec.md"
	const design = "design/design/external-cli.design.md"

	if _, err := orchestrator.AdoptPullRequest(f.ctx, f.deps, model.AdoptPullRequestInput{
		PR: model.PullRequestRef{Repo: f.repo, Number: f.pr}, Spec: "../secrets.md", Requester: adoptOperator,
	}, baseTime); !errors.Is(err, model.ErrPullRequestInvalid) {
		t.Fatalf("adopting with spec ../secrets.md = %v, want ErrPullRequestInvalid", err)
	}

	agentOwner := model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}
	got := f.adopt(model.AdoptPullRequestInput{
		Spec:      spec,
		Requester: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "external:laptop"}, OnBehalfOf: &agentOwner},
	}, baseTime)
	if got.State != string(model.StateProposed) || got.AutoMerge {
		t.Fatalf("unapproved adoption = %+v, want proposed without auto-merge", got)
	}
	if got.Spec != spec || got.Design != design {
		t.Errorf("adoption docs = %q / %q, want %q (flag) and %q (description)", got.Spec, got.Design, spec, design)
	}
	sh := f.shepherd()
	if !hasLink(sh, model.LinkImplementsSpec, spec) || !hasLink(sh, model.LinkDesignDoc, design) {
		t.Errorf("shepherd links = %+v, want implements-spec and design-doc", sh.Links)
	}
	if !strings.Contains(sh.Body, spec) || !strings.Contains(sh.Body, "owner@example.com") {
		t.Errorf("shepherd body does not name its spec or requester:\n%s", sh.Body)
	}
	f.reconcileTakeovers(baseTime.Add(time.Minute))
	if n := len(f.reviews()); n != 0 {
		t.Fatalf("a proposed adoption filed %d reviews before approval", n)
	}

	if err := f.store.Approve(f.ctx, sh.ID, adoptOperator, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.reconcileTakeovers(baseTime.Add(3 * time.Minute))
	reviews := f.reviews()
	if len(reviews) != 2 {
		t.Fatalf("reviews after approval = %d, want code + security (bob is no collaborator)", len(reviews))
	}
	for _, r := range reviews {
		if !hasLink(r, model.LinkImplementsSpec, spec) || !hasLink(r, model.LinkDesignDoc, design) {
			t.Errorf("review %s links = %+v, want the spec and design in its provenance", r.ID, r.Links)
		}
		if !strings.Contains(r.Body, "verify conformance") || !strings.Contains(r.Body, spec) ||
			!strings.Contains(r.Body, design) || !strings.Contains(r.Body, "tests it requires") {
			t.Errorf("review %s does not ask for conformance against %s and %s:\n%s", r.ID, spec, design, r.Body)
		}
	}
	if !f.mergeHeld() {
		t.Error("merge queue does not hold an adopted PR whose reviews are pending")
	}

	// Adopting again with a design flag updates the same task.
	again := f.adopt(model.AdoptPullRequestInput{Design: "design/design/other.design.md", Approved: true}, baseTime.Add(4*time.Minute))
	if again.TaskID != sh.ID || !again.Existing || again.Design != "design/design/other.design.md" {
		t.Errorf("re-adoption = %+v, want shepherd %s with the new design", again, sh.ID)
	}
}

// #XC-29: a design review asks for contributions.spec.md conventions and
// overlapping designs, and status reports the shepherd, the reviews and
// every thread.
func TestPullRequestReviewDesignAndStatusThreads(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t, "alice")
	ref := model.PullRequestRef{Repo: f.repo, Number: f.pr}

	filed, err := orchestrator.ReviewPullRequest(f.ctx, f.deps, model.ReviewPullRequestInput{
		PR: ref, Design: true, Requester: adoptOperator, Approved: true,
	}, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.store.GetTask(f.ctx, filed.TaskID)
	if err != nil || review == nil {
		t.Fatalf("reading review %s: %v", filed.TaskID, err)
	}
	if !strings.HasPrefix(review.Title, "Design review") || !strings.Contains(review.Body, "contributions.spec.md") ||
		!strings.Contains(review.Body, "overlaps") {
		t.Errorf("design review %q does not check design conventions:\n%s", review.Title, review.Body)
	}
	if review.Origin.Attribution.Actor.ID != adoptOperator.Actor.ID || review.Approval == nil {
		t.Errorf("design review origin = %+v approval = %+v, want the operator's, approved", review.Origin, review.Approval)
	}

	f.adopt(model.AdoptPullRequestInput{Approved: true}, baseTime)
	f.finishReviews(baseTime.Add(time.Minute), reviewFinding("missing nil check", "handler.go", 3))

	report, err := orchestrator.PullRequestStatusOf(f.ctx, f.deps, ref, true)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := f.client.GetPullRequest("acme", "widgets", f.pr)
	if report.Head != detail.HeadSHA || report.Shepherd == nil || report.Shepherd.Mode != "review-only" {
		t.Fatalf("status = %+v, want head %s and a review-only shepherd", report, detail.HeadSHA)
	}
	kinds := map[string]bool{}
	for _, r := range report.Reviews {
		kinds[r.Kind] = true
	}
	if !kinds["code"] || !kinds["design"] {
		t.Errorf("status reviews = %+v, want the code and design reviews", report.Reviews)
	}
	if len(report.Threads) == 0 || report.Threads[0].Path != "handler.go" || report.Threads[0].Line != 3 ||
		!strings.Contains(report.Threads[0].Body, "missing nil check") {
		t.Errorf("status threads = %+v, want the handler.go:3 finding", report.Threads)
	}
	if report.MergeQueue != "held: review" {
		t.Errorf("merge queue = %q, want held: review", report.MergeQueue)
	}
}

// A pull request GitHub has no record of is the caller's mistake, not
// grain failing: adopt and status say not-found (the routes answer 404)
// rather than a 500 (docs/test-runs/2026-11-08-cli-install-skill-mcp.md).
func TestAdoptPullRequestOfAMissingPullRequestIsNotFound(t *testing.T) {
	t.Parallel()
	f := newTakeoverFixture(t, "alice")
	missing := model.PullRequestRef{Repo: f.repo, Number: f.pr + 1000}
	_, err := orchestrator.AdoptPullRequest(f.ctx, f.deps, model.AdoptPullRequestInput{PR: missing, Requester: adoptOperator, Approved: true}, baseTime)
	if !errors.Is(err, model.ErrPullRequestNotFound) {
		t.Fatalf("AdoptPullRequest(%s) = %v, want ErrPullRequestNotFound", missing, err)
	}
	if _, err := orchestrator.PullRequestStatusOf(f.ctx, f.deps, missing, false); !errors.Is(err, model.ErrPullRequestNotFound) {
		t.Fatalf("PullRequestStatusOf(%s) = %v, want ErrPullRequestNotFound", missing, err)
	}
}

func TestAdoptPullRequestByAnAgentOrLimitedRoleNeverTurnsOnAutoMerge(t *testing.T) {
	t.Parallel()
	owner := model.Principal{Kind: model.PrincipalHuman, ID: "jane@example.com"}

	// Limited human role (AllowAutoMerge == false): queued, never auto-merge.
	fLimited := newTakeoverFixture(t, "alice")
	got := fLimited.adopt(model.AdoptPullRequestInput{
		Requester:      model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "wade@example.com"}},
		Approved:       true,
		AllowAutoMerge: false,
	}, baseTime)
	if got.State != string(model.StateQueued) || got.AutoMerge || fLimited.shepherd().AutoMerge {
		t.Fatalf("limited-role collaborator adoption = %+v (task AutoMerge=%v), want queued with AutoMerge=false",
			got, fLimited.shepherd().AutoMerge)
	}

	// Agent principal: never auto-merge even if AllowAutoMerge were set true.
	fAgent := newTakeoverFixture(t, "alice")
	got = fAgent.adopt(model.AdoptPullRequestInput{
		Mode:           model.TakeoverFinish,
		Requester:      model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "external:bot"}, OnBehalfOf: &owner},
		Approved:       true,
		AllowAutoMerge: true,
	}, baseTime)
	if got.State != string(model.StateQueued) || got.AutoMerge || fAgent.shepherd().AutoMerge {
		t.Fatalf("agent collaborator adoption = %+v (task AutoMerge=%v), want queued with AutoMerge=false",
			got, fAgent.shepherd().AutoMerge)
	}

	// Owner-trust approval records OwnerTrustPrincipal on new filings and never auto-merges.
	fTrusted := newTakeoverFixture(t, "alice")
	otApproval := model.Attribution{Actor: model.OwnerTrustPrincipal, OnBehalfOf: &owner}
	got = fTrusted.adopt(model.AdoptPullRequestInput{
		Requester: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "external:trusted"}, OnBehalfOf: &owner},
		Approval:  &otApproval,
		Approved:  true,
	}, baseTime)
	sh := fTrusted.shepherd()
	if got.AutoMerge || sh.AutoMerge || sh.Approval == nil || sh.Approval.Actor != model.OwnerTrustPrincipal {
		t.Fatalf("owner-trust adoption = %+v, approval = %+v, want AutoMerge=false and OwnerTrustPrincipal", got, sh.Approval)
	}

	// Upgrading an auto-merge review-only shepherd to finish requires an unlimited human.
	fUpgrade := newTakeoverFixture(t, "alice")
	fUpgrade.adopt(model.AdoptPullRequestInput{Approved: true}, baseTime)
	if !fUpgrade.shepherd().AutoMerge {
		t.Fatal("unlimited operator review-only adoption of collaborator PR should have AutoMerge=true")
	}
	for _, caller := range []model.AdoptPullRequestInput{
		{
			PR:        model.PullRequestRef{Repo: fUpgrade.repo, Number: fUpgrade.pr},
			Mode:      model.TakeoverFinish,
			Requester: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "wade@example.com"}},
			Approved:  true,
		},
		{
			PR:             model.PullRequestRef{Repo: fUpgrade.repo, Number: fUpgrade.pr},
			Mode:           model.TakeoverFinish,
			Requester:      model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "external:bot"}},
			Approved:       true,
			AllowAutoMerge: true,
		},
	} {
		if _, err := orchestrator.AdoptPullRequest(fUpgrade.ctx, fUpgrade.deps, caller, baseTime.Add(time.Minute)); !errors.Is(err, model.ErrPullRequestForbidden) {
			t.Errorf("upgrading auto-merge shepherd as %+v = %v, want ErrPullRequestForbidden", caller.Requester.Actor, err)
		}
		if hasLink(fUpgrade.shepherd(), model.LinkTakeoverMode, string(model.TakeoverFinish)) {
			t.Fatalf("refused upgrade modified shepherd links: %+v", fUpgrade.shepherd().Links)
		}
	}
}

type realOrchestratorAdopter struct {
	deps orchestrator.Deps
	now  time.Time
}

func (a realOrchestratorAdopter) OpenForTask(context.Context, string) (ui.PullRequestStatus, error) {
	return ui.PullRequestStatus{}, nil
}

func (a realOrchestratorAdopter) AdoptPullRequest(ctx context.Context, in model.AdoptPullRequestInput) (model.PullRequestAdoption, error) {
	return orchestrator.AdoptPullRequest(ctx, a.deps, in, a.now)
}

func (a realOrchestratorAdopter) ReviewPullRequest(ctx context.Context, in model.ReviewPullRequestInput) (model.PullRequestReviewFiling, error) {
	return orchestrator.ReviewPullRequest(ctx, a.deps, in, a.now)
}

func (a realOrchestratorAdopter) PullRequestStatus(ctx context.Context, ref model.PullRequestRef, threads bool) (model.PullRequestReport, error) {
	return orchestrator.PullRequestStatusOf(ctx, a.deps, ref, threads)
}

func TestAdoptPullRequestEndToEndExternalAndUnapprovedShepherdRails(t *testing.T) {
	t.Parallel()
	newServer := func(t *testing.T) (*takeoverFixture, *ui.Server, *ui.Client) {
		t.Helper()
		f := newTakeoverFixture(t, "alice")
		client := ui.NewClient(ui.Config{
			Actor:         ui.DefaultActor("operator@example.com"),
			DefaultTarget: &f.repo,
			Capabilities:  ui.OfferedCapabilities(),
			PullRequests:  realOrchestratorAdopter{deps: f.deps, now: baseTime},
		}, f.store)
		client.Now = func() time.Time { return baseTime }
		cfg, err := f.store.GetConfig(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil {
			d := model.DefaultConfig()
			cfg = &d
		}
		cfg.ExternalAgentAPI = true
		cfg.OwnerTrust = []model.OwnerTrustRule{{Owner: "jane@example.com", Repo: f.repo.String()}}
		if err := f.store.PutConfig(f.ctx, *cfg); err != nil {
			t.Fatal(err)
		}
		return f, ui.NewServerWithClient(client), client
	}
	mint := func(t *testing.T, client *ui.Client, name, owner string, scopes ...string) string {
		t.Helper()
		issued, err := client.CreateExternalAgentToken(context.Background(), ui.CreateExternalAgentTokenRequest{
			Name: name, Owner: owner, Scopes: scopes, Repos: []string{"acme/widgets"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Token
	}
	postAdopt := func(srv *ui.Server, token, body, reason string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/pull-requests/adopt", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if reason != "" {
			req.Header.Set(ui.ActingReasonHeader, reason)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// 1. External tasks:admin token on collaborator PR: queued, AutoMerge=false.
	{
		f, srv, client := newServer(t)
		prRef := model.PullRequestRef{Repo: f.repo, Number: f.pr}.String()
		adminTok := mint(t, client, "admin", "alice@example.com", "merge:write", "tasks:write", "tasks:admin")
		rec := postAdopt(srv, adminTok, `{"pr":"`+prRef+`","mode":"finish"}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("tasks:admin adopt new collaborator PR: %d %s", rec.Code, rec.Body)
		}
		var out model.PullRequestAdoption
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		sh := f.shepherd()
		if out.State != string(model.StateQueued) || out.AutoMerge || sh.AutoMerge {
			t.Fatalf("tasks:admin adoption = %+v (task AutoMerge=%v), want queued with AutoMerge=false", out, sh.AutoMerge)
		}
	}

	// 2. Owner-trusted external token on collaborator PR: queued under OwnerTrustPrincipal, AutoMerge=false.
	{
		f, srv, client := newServer(t)
		prRef := model.PullRequestRef{Repo: f.repo, Number: f.pr}.String()
		trustedTok := mint(t, client, "trusted", "jane@example.com", "merge:write", "tasks:write")
		rec := postAdopt(srv, trustedTok, `{"pr":"`+prRef+`","mode":"finish"}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("owner-trusted adopt new collaborator PR: %d %s", rec.Code, rec.Body)
		}
		sh := f.shepherd()
		if sh.AutoMerge || sh.Approval == nil || sh.Approval.Actor != model.OwnerTrustPrincipal {
			t.Fatalf("owner-trusted shepherd AutoMerge=%v Approval=%+v, want AutoMerge=false and OwnerTrustPrincipal",
				sh.AutoMerge, sh.Approval)
		}
	}

	// 3. Existing unapproved shepherd: refuses owner-trust without tasks:admin, missing reason, self-filed, and person-held;
	// allows another credential with tasks:admin + X-Grain-Acting-Reason on a plain proposed shepherd.
	{
		f, srv, client := newServer(t)
		prRef := model.PullRequestRef{Repo: f.repo, Number: f.pr}.String()
		proposerTok := mint(t, client, "proposer", "bob@example.com", "merge:write", "tasks:write")
		trustedTok := mint(t, client, "trusted", "jane@example.com", "merge:write", "tasks:write")
		adminTok := mint(t, client, "admin", "alice@example.com", "merge:write", "tasks:write", "tasks:admin")

		// Proposer files an unapproved review-only shepherd.
		rec := postAdopt(srv, proposerTok, `{"pr":"`+prRef+`"}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("proposer adopt: %d %s", rec.Code, rec.Body)
		}
		sh := f.shepherd()
		if sh.Approval != nil {
			t.Fatalf("proposer shepherd should be unapproved, got %+v", sh.Approval)
		}

		// Owner-trust without tasks:admin is refused 403 and does not upgrade or approve.
		if rec = postAdopt(srv, trustedTok, `{"pr":"`+prRef+`","mode":"finish"}`, "owner-trust try"); rec.Code != http.StatusForbidden {
			t.Fatalf("owner-trusted adopt existing proposed shepherd: %d %s, want 403", rec.Code, rec.Body)
		}
		// tasks:admin without X-Grain-Acting-Reason is refused 403 and does not upgrade or approve.
		if rec = postAdopt(srv, adminTok, `{"pr":"`+prRef+`","mode":"finish"}`, ""); rec.Code != http.StatusForbidden ||
			!strings.Contains(rec.Body.String(), ui.ActingReasonHeader) {
			t.Fatalf("tasks:admin adopt existing without reason: %d %s, want 403 naming %s", rec.Code, rec.Body, ui.ActingReasonHeader)
		}
		if sh = f.shepherd(); sh.Approval != nil || hasLink(sh, model.LinkTakeoverMode, string(model.TakeoverFinish)) {
			t.Fatalf("refused adopt mutated shepherd: approval=%+v links=%+v", sh.Approval, sh.Links)
		}

		// Self-approval rail: if external:admin filed the proposed shepherd itself, it is refused 403 even with reason.
		if err := f.store.UpdateTask(f.ctx, sh.ID, func(tk *model.Task) error {
			tk.Origin.Attribution.Actor = model.Principal{
				Kind: model.PrincipalAgent, ID: "external:admin", Source: model.SourceExternalToken,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if rec = postAdopt(srv, adminTok, `{"pr":"`+prRef+`","mode":"finish"}`, "self approve try"); rec.Code != http.StatusForbidden ||
			!strings.Contains(rec.Body.String(), "it filed it") {
			t.Fatalf("self-approval on adopt: %d %s, want 403 naming 'it filed it'", rec.Code, rec.Body)
		}

		// Person-hold rail: restore proposer origin, set AutoMerge=true on stored proposed task -> refused 403.
		if err := f.store.UpdateTask(f.ctx, sh.ID, func(tk *model.Task) error {
			tk.Origin.Attribution.Actor = model.Principal{
				Kind: model.PrincipalAgent, ID: "external:proposer", Source: model.SourceExternalToken,
			}
			tk.AutoMerge = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if rec = postAdopt(srv, adminTok, `{"pr":"`+prRef+`"}`, "person hold try"); rec.Code != http.StatusForbidden ||
			!strings.Contains(rec.Body.String(), "auto-merge") {
			t.Fatalf("person-held adopt: %d %s, want 403 naming auto-merge", rec.Code, rec.Body)
		}

		// Clear AutoMerge: tasks:admin with X-Grain-Acting-Reason approves and upgrades in place without AutoMerge.
		if err := f.store.UpdateTask(f.ctx, sh.ID, func(tk *model.Task) error {
			tk.AutoMerge = false
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		rec = postAdopt(srv, adminTok, `{"pr":"`+prRef+`","mode":"finish"}`, "approving proposer's shepherd")
		if rec.Code != http.StatusOK {
			t.Fatalf("valid tasks:admin adopt of existing shepherd: %d %s", rec.Code, rec.Body)
		}
		sh = f.shepherd()
		if sh.Approval == nil || sh.AutoMerge || !hasLink(sh, model.LinkTakeoverMode, string(model.TakeoverFinish)) {
			t.Fatalf("approved shepherd = %+v (links=%+v), want approved finish shepherd with AutoMerge=false", sh, sh.Links)
		}
	}
}
