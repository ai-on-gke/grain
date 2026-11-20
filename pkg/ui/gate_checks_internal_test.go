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

package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestGateChecksDisplayRequiresCurrentResourceScope(t *testing.T) {
	f := gates.ChecksFacts{TaskID: "66", Subject: gates.Subject{Kind: "change", Authority: "github.com", Repository: "private/repo", ChangeKind: "pr", ID: "7"}}
	v := gates.UnavailableObservation("66", time.Now(), f)
	denied := withAccessRole(context.Background(), &accessRole{Who: "reader", Full: false})
	if observationVisible(denied, &v) {
		t.Fatal("CI evidence visible without CI read scope")
	}
	allowed := withAccessRole(context.Background(), &accessRole{Who: "operator", Full: true})
	if !observationVisible(allowed, &v) {
		t.Fatal("full operator cannot read observation")
	}
	// The same check is applied independently to historical views after a task's
	// links change; current task authorization is not authority over old repos.
	history := v
	history.Bindings = gates.Bindings("66")
	history.Results = history.Results[:2]
	if !observationVisible(denied, &history) || observationVisible(denied, &v) {
		t.Fatal("resource-aware projection changed")
	}
}

func TestGateObservationRejectsForeignNestedTaskSubjects(t *testing.T) {
	ctx := withAccessRole(context.Background(), &accessRole{Who: "operator", Full: true})
	for _, evidence := range []bool{true, false} {
		v := gates.UnavailableObservation("66", time.Now())
		subject := gates.Subject{Kind: "task", TaskID: "another-task"}
		if evidence {
			v.Results[0].Result.Evidence = append(v.Results[0].Result.Evidence, gates.Evidence{Kind: "report", ID: "foreign", Subject: subject})
		} else {
			v.Results[0].Result.Dependencies = append(v.Results[0].Result.Dependencies, gates.Dependency{Key: "foreign", Subject: subject})
		}
		if observationVisible(ctx, &v) {
			t.Fatal("foreign nested task evidence escaped its owning task projection")
		}
	}
}

func TestQualificationSuiteDisplayRequiresCIRead(t *testing.T) {
	now := time.Now()
	c := gates.QualificationCollection{Release: &gates.ReleaseFacts{TaskID: "69", Repository: "private/repo", Candidate: "v3.1.0-rc.1", Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Version: "v3.1.0", PolicyVersion: "policy", ObservedAt: now, ValidUntil: now.Add(time.Minute)}}
	v := gates.UnavailableQualifications("69", now, c)
	denied := withAccessRole(context.Background(), &accessRole{Who: "reader", Full: false})
	allowed := withAccessRole(context.Background(), &accessRole{Who: "operator", Full: true})
	if observationVisible(denied, &v) || !observationVisible(allowed, &v) {
		t.Fatal("qualification suite display ignored CI authorization")
	}
}

func TestGateObservationScopeIntersectsAllCallerRestrictions(t *testing.T) {
	_, unrestrictedRev := gateObservationScope(context.Background())

	// Mixed agentCall + limited accessRole intersects both grants and role denies,
	// and never reuses the role-only or agent-only authorization revision.
	roleGrants := []string{"api:tasks:read", "api:ci:read", "api:repos:read"}
	role := &accessRole{
		Who:    "reader@example.com",
		grants: roleGrants,
		set:    APIGrantsOf(roleGrants),
		deny:   map[APIScope]bool{"repos:read": true},
	}
	agentGrants := APIGrantsOf([]string{"api:tasks:read@acme/widgets", "api:ci:read@acme/widgets", "api:repos:read@acme/widgets", "api:releases:read"})
	roleCtx := withAccessRole(context.Background(), role)
	agentCtx := withAgentCall(context.Background(), agentCall{taskID: "1", scope: "tasks:read", grants: agentGrants})
	mixedCtx := withAgentCall(roleCtx, agentCall{taskID: "1", scope: "tasks:read", grants: agentGrants})

	_, roleRev := gateObservationScope(roleCtx)
	_, agentRev := gateObservationScope(agentCtx)
	mixedAllows, mixedRev := gateObservationScope(mixedCtx)
	if !mixedAllows("ci:read", "acme/widgets") || mixedAllows("ci:read", "acme/other") {
		t.Fatal("mixed scope did not intersect repository limits")
	}
	if mixedAllows("repos:read", "acme/widgets") || mixedAllows("releases:read", "acme/widgets") {
		t.Fatal("mixed scope allowed role-denied or role-absent scope")
	}
	if mixedRev == roleRev || mixedRev == agentRev || mixedRev == unrestrictedRev {
		t.Fatalf("mixed revision %d collided with role=%d agent=%d unrestricted=%d", mixedRev, roleRev, agentRev, unrestrictedRev)
	}

	// Direct externalTokenFromContext without agentCall enforces token grants and fails closed on invalid tokens.
	tokCtx := withExternalToken(context.Background(), &model.ExternalAgentToken{
		Name:   "ext",
		Scopes: []string{"tasks:read"},
		Repos:  []string{"acme/widgets"},
	})
	tokAllows, tokRev := gateObservationScope(tokCtx)
	if !tokAllows("tasks:read", "acme/widgets") || tokAllows("ci:read", "acme/widgets") || tokAllows("tasks:read", "acme/other") || tokRev == unrestrictedRev {
		t.Fatal("direct external token scope was not enforced")
	}
	badTokCtx := withExternalToken(context.Background(), &model.ExternalAgentToken{
		Name:   "bad",
		Scopes: []string{"not-a-valid-scope"},
	})
	badTokAllows, _ := gateObservationScope(badTokCtx)
	if badTokAllows("tasks:read", "acme/widgets") || badTokAllows("ci:read", "acme/widgets") {
		t.Fatal("invalid external token did not fail closed")
	}

	// Direct model.CIAPICallFromContext without accessRole restricts to its repo and allowed CI scopes.
	ciCtx, _ := model.WithCIAPICall(context.Background(), model.CIAPICall{
		Repo:   model.RepoRef{Owner: "acme", Name: "widgets"},
		Scopes: []model.APIScope{"tasks:read", "ci:read", "deployment:read"},
	})
	ciAllows, ciRev := gateObservationScope(ciCtx)
	if !ciAllows("ci:read", "acme/widgets") || ciAllows("ci:read", "acme/other") || ciAllows("repos:read", "acme/widgets") || ciAllows("deployment:read", "") || ciRev == unrestrictedRev {
		t.Fatal("direct CI API call scope was not enforced")
	}
	emptyCICtx, _ := model.WithCIAPICall(context.Background(), model.CIAPICall{
		Scopes: []model.APIScope{"ci:read"},
	})
	emptyCIAllows, _ := gateObservationScope(emptyCICtx)
	if emptyCIAllows("ci:read", "acme/widgets") {
		t.Fatal("CI API call with empty repo did not fail closed")
	}

	// Unscoped PrincipalAgent fails closed, even beside a Full accessRole.
	for _, ctx := range []context.Context{
		WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalAgent, ID: "unscoped"}),
		WithPrincipal(withAccessRole(context.Background(), &accessRole{Who: "op", Full: true}), model.Principal{Kind: model.PrincipalAgent, ID: "unscoped"}),
	} {
		unscopedAllows, unscopedRev := gateObservationScope(ctx)
		if unscopedAllows("tasks:read", "acme/widgets") || unscopedAllows("ci:read", "acme/widgets") || unscopedRev == unrestrictedRev {
			t.Fatal("unscoped PrincipalAgent did not fail closed")
		}
	}
}

func TestGateChecksAndObservationVisibleEnforceDirectCallerContexts(t *testing.T) {
	now := time.Now()
	checkFacts := gates.ChecksFacts{
		TaskID:  "77",
		Subject: gates.Subject{Kind: "change", Authority: "github.com", Repository: "acme/widgets", ChangeKind: "pr", ID: "9"},
	}
	checksView := gates.UnavailableObservation("77", now, checkFacts)
	suiteView := gates.UnavailableQualifications("77", now, gates.QualificationCollection{
		Release: &gates.ReleaseFacts{
			TaskID: "77", Repository: "acme/widgets", Candidate: "v3.1.0-rc.1",
			Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Version: "v3.1.0",
			PolicyVersion: "policy", ObservedAt: now, ValidUntil: now.Add(time.Minute),
		},
	})

	tokNoCI := withExternalToken(context.Background(), &model.ExternalAgentToken{
		Name: "tasks-only", Scopes: []string{"tasks:read"}, Repos: []string{"acme/widgets"},
	})
	tokWithCI := withExternalToken(context.Background(), &model.ExternalAgentToken{
		Name: "with-ci", Scopes: []string{"tasks:read", "ci:read"}, Repos: []string{"acme/widgets"},
	})
	ciNoCI, _ := model.WithCIAPICall(context.Background(), model.CIAPICall{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Scopes: []model.APIScope{"tasks:read"},
	})
	ciWithCI, _ := model.WithCIAPICall(context.Background(), model.CIAPICall{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Scopes: []model.APIScope{"tasks:read", "ci:read"},
	})
	unscopedAgent := WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalAgent, ID: "unscoped"})

	for name, ctx := range map[string]context.Context{
		"external-token-no-ci": tokNoCI,
		"ci-api-no-ci":         ciNoCI,
		"unscoped-agent":       unscopedAgent,
	} {
		if observationVisible(ctx, &checksView) {
			t.Fatalf("%s: checks observation visible without ci:read", name)
		}
		if observationVisible(ctx, &suiteView) {
			t.Fatalf("%s: qualification suite observation visible without ci:read", name)
		}
	}
	for name, ctx := range map[string]context.Context{
		"external-token-with-ci": tokWithCI,
		"ci-api-with-ci":         ciWithCI,
	} {
		if !observationVisible(ctx, &checksView) || !observationVisible(ctx, &suiteView) {
			t.Fatalf("%s: authorized CI observation was wrongly hidden", name)
		}
	}

	// Verify collectGateChecksScoped enforces gateObservationScope directly too.
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := NewClient(Config{Actor: DefaultActor("alice")}, store)
	repo := &model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(context.Background(), model.Task{
		ID:     "77",
		Title:  "Check direct caller scopes",
		Target: repo,
		Links:  []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#9"}},
	}); err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{
		"external-token-no-ci": tokNoCI,
		"ci-api-no-ci":         ciNoCI,
		"unscoped-agent":       unscopedAgent,
	} {
		if _, _, err := c.collectGateChecksScoped(ctx, "77", now); !errors.Is(err, errGateCheckDenied) {
			t.Fatalf("%s: collectGateChecksScoped err = %v, want errGateCheckDenied", name, err)
		}
	}

	// Verify redactGateHistoryScopes enforces taskReadableForContext across all
	// principal types (not just agentCallFromContext).
	if err := store.PutTask(context.Background(), model.Task{
		ID:     "78",
		Title:  "Same-repo child task",
		Target: repo,
	}); err != nil {
		t.Fatal(err)
	}
	makeChildView := func() (*gates.ObservationView, *model.GateObservationDiagnostics) {
		text := "secret child summary"
		view := gates.ObservationView{
			Results: []gates.EvaluationState{{
				Result: gates.Result{
					Status:    "met",
					Summary:   "child met",
					HumanText: &text,
					Evidence:  []gates.Evidence{{Kind: "task", ID: "78", Subject: gates.Subject{Kind: "task", TaskID: "78"}}},
				},
			}},
		}
		historyView := view
		historyView.Results = append([]gates.EvaluationState(nil), view.Results...)
		return &view, &model.GateObservationDiagnostics{
			History: []model.GateObservationSample{{View: historyView}},
		}
	}
	otherRole := &accessRole{Who: "other@example.com", set: APIGrantsOf([]string{"api:tasks:read@acme/other"})}
	otherTok := &model.ExternalAgentToken{Name: "other", Scopes: []string{"tasks:read"}, Repos: []string{"acme/other"}}
	otherCI, _ := model.WithCIAPICall(context.Background(), model.CIAPICall{
		Repo: model.RepoRef{Owner: "acme", Name: "other"}, Scopes: []model.APIScope{"tasks:read"},
	})
	for name, ctx := range map[string]context.Context{
		"limited-role":   withAccessRole(context.Background(), otherRole),
		"external-token": withExternalToken(context.Background(), otherTok),
		"ci-api":         otherCI,
		"unscoped-agent": unscopedAgent,
	} {
		v, d := makeChildView()
		c.redactGateHistoryScopes(ctx, "77", v, d)
		if v.Results[0].Result.Status != "unknown" || len(v.Results[0].Result.Evidence) != 0 || v.Results[0].Result.HumanText != nil {
			t.Fatalf("%s: current view child evidence was not redacted: %+v", name, v.Results[0].Result)
		}
		if d.History[0].View.Results[0].Result.Status != "unknown" || len(d.History[0].View.Results[0].Result.Evidence) != 0 || d.History[0].View.Results[0].Result.HumanText != nil {
			t.Fatalf("%s: history view child evidence was not redacted: %+v", name, d.History[0].View.Results[0].Result)
		}
	}
}
