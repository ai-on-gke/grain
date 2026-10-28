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

package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

func TestLocalPolicyHoldsARefusedTaskAndSaysWhy(t *testing.T) {
	store, ctx := openTreeStore(t)
	p, err := policy.Parse([]byte("repos: [acme/widgets]\nlocal_requesters: [owner@example.com]\n"))
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: store, Policy: p}

	put := func(task model.Task) {
		t.Helper()
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	put(model.Task{ID: "1", Title: "ours", Target: &widgets, Creator: "owner@example.com"})
	put(model.Task{ID: "2", Title: "elsewhere", Target: &model.RepoRef{Owner: "acme", Name: "gadgets"}, Creator: "owner@example.com"})
	put(model.Task{ID: "3", Title: "unapproved", Target: &widgets, Creator: "stranger@example.com"})

	held := heldByPolicy(ctx, deps)
	if held("1") {
		t.Error("the owner's own task on an attached repo was held")
	}
	for id, want := range map[string]string{"2": "repository acme/gadgets", "3": "no human approval"} {
		if !held(id) {
			t.Errorf("task %s was not held", id)
			continue
		}
		wait, err := store.GrantWaitOf(ctx, id)
		if err != nil || wait == nil || wait.Capability != model.PolicyWaitCapability || !strings.Contains(wait.Resource, want) {
			t.Errorf("task %s's wait = %+v, %v; want a local-policy wait naming %q", id, wait, err, want)
		}
	}
	if !held("missing") {
		t.Error("a task that cannot be read was not held: the policy must fail closed")
	}
	// The reason dispatch logs a skip with names the rule, not just that
	// the task was passed over (grain/task-4501).
	if why := policyHold(ctx, deps)("3"); !strings.HasPrefix(why, "held by local policy: ") || !strings.Contains(why, "no human approval") {
		t.Errorf("policyHold(3) = %q, want the local-policy refusal", why)
	}
	if why := policyHold(ctx, deps)("1"); why != "" {
		t.Errorf("policyHold(1) = %q for a task the policy allows", why)
	}

	// Approved by somebody other than its requester, task 3 fits the
	// policy, and the wait it was held under goes with it.
	task, _ := store.GetTask(ctx, "3")
	task.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}}
	put(*task)
	if held("3") {
		t.Error("an approved external task was still held")
	}
	if wait, _ := store.GrantWaitOf(ctx, "3"); wait != nil {
		t.Errorf("the policy wait outlived the refusal: %+v", wait)
	}
}

func TestLineageTrailersCarryTheRequesterAndApprover(t *testing.T) {
	task := model.Task{
		ID: "7", Creator: "requester@example.com",
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}},
	}
	if got := strings.Join(CommitTrailers(task), "\n"); got != "Requested-by: requester@example.com\nApproved-by: lead@example.com" {
		t.Errorf("trailers = %q", got)
	}
	task.Approval = &model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
	if got := strings.Join(CommitTrailers(task), "\n"); got != "Requested-by: requester@example.com" {
		t.Errorf("trailers with grain's own approval = %q", got)
	}
}

func TestIdentityTrailersRideOnlyWithLineageOnTheMergeCommit(t *testing.T) {
	store, ctx, _ := plantTree(t)
	approval := &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "lead@example.com"}}

	// A task in a tree: its merge message is grain's anyway, and carries
	// who asked and who approved beside its lineage.
	task, _ := store.GetTask(ctx, "12")
	task.Creator, task.Approval = "requester@example.com", approval
	if err := store.PutTask(ctx, *task); err != nil {
		t.Fatal(err)
	}
	client := &treeClient{bodies: map[int]string{}}
	if err := mergeTaskPullRequest(ctx, store, client, widgets, 21, "abc", "12", "Do the thing"); err != nil {
		t.Fatal(err)
	}
	want := "Do the thing\n\nOrigin: acme/widgets#5\nFiled-by: task 11\nRequested-by: requester@example.com\nApproved-by: lead@example.com"
	if len(client.merged) != 1 || client.merged[0] != want {
		t.Errorf("merge message = %q, want %q", client.merged, want)
	}

	// A task with no lineage keeps the forge's own merge message.
	if err := store.PutTask(ctx, model.Task{ID: "40", Title: "lone", Target: &widgets, Creator: "requester@example.com", Approval: approval}); err != nil {
		t.Fatal(err)
	}
	client = &treeClient{bodies: map[int]string{}}
	if err := mergeTaskPullRequest(ctx, store, client, widgets, 22, "abc", "40", "Lone"); err != nil {
		t.Fatal(err)
	}
	if len(client.merged) != 1 || client.merged[0] != "" {
		t.Errorf("a task with no lineage had its merge message rewritten: %q", client.merged)
	}
}

// mintCountingCapability is a scarce capability that counts what it
// mints, so a test can say none was minted for a task policy refuses.
type mintCountingCapability struct {
	model.BaseCapability
	name   string
	minted int
}

func (c *mintCountingCapability) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{Name: c.name, Provision: model.ProvisionMint}
}

func (*mintCountingCapability) ScarceResourceProvider() {}

func (c *mintCountingCapability) Materialize(context.Context, model.CapabilityContext) (model.Materialization, error) {
	c.minted++
	return model.Materialization{Lease: &model.Lease{Capability: c.name, Resource: "lease-1"}}, nil
}

func admitOnce(t *testing.T, ctx context.Context, deps Deps, taskID string) dispatch.Admission {
	t.Helper()
	admission, err := grantAdmitter(ctx, deps, map[string][]model.Materialized{}, time.Now())(ctx,
		dispatch.Candidate{TaskID: taskID, RunID: dispatch.RunID(taskID, 1), Attempt: 1})
	if err != nil {
		t.Fatalf("admitting %s: %v", taskID, err)
	}
	return admission
}

func wantPolicyWait(t *testing.T, ctx context.Context, store *model.Store, taskID, naming string) {
	t.Helper()
	wait, err := store.GrantWaitOf(ctx, taskID)
	if err != nil || wait == nil || wait.Capability != model.PolicyWaitCapability || !strings.Contains(wait.Resource, naming) {
		t.Errorf("task %s's wait = %+v, %v; want a local-policy wait naming %q", taskID, wait, err, naming)
	}
}

// A grant added after the dispatch hold read the task is judged by the
// admitter before it is minted, not by the run after it has been.
func TestLocalPolicyRefusesAGrantAddedAfterTheHoldBeforeMintingIt(t *testing.T) {
	store, ctx := openTreeStore(t)
	p, err := policy.Parse([]byte("repos: [acme/widgets]\nlocal_requesters: [owner@example.com]\ncapabilities:\n  deny: [keyed]\n"))
	if err != nil {
		t.Fatal(err)
	}
	keyed := &mintCountingCapability{name: "keyed"}
	deps := Deps{Store: store, Policy: p, Config: Config{Capabilities: model.NewCapabilityRegistry(keyed)}}
	task := model.Task{ID: "1", Title: "ours", Target: &widgets, Creator: "owner@example.com"}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if heldByPolicy(ctx, deps)("1") {
		t.Fatal("a task with no grants was held")
	}
	task.Grants = []model.Grant{{Capability: "keyed", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if admitOnce(t, ctx, deps, "1").Admitted {
		t.Error("a task granted a denied capability was admitted")
	}
	if keyed.minted != 0 {
		t.Errorf("minted %d leases for a task local policy refuses", keyed.minted)
	}
	wantPolicyWait(t, ctx, store, "1", `"keyed"`)

	// Once policy allows it, the same admission mints the grant and the
	// wait it held under goes.
	deps.Policy, err = policy.Parse([]byte("repos: [acme/widgets]\nlocal_requesters: [owner@example.com]\n"))
	if err != nil {
		t.Fatal(err)
	}
	admission := admitOnce(t, ctx, deps, "1")
	if !admission.Admitted || keyed.minted != 1 {
		t.Errorf("admitted = %v with %d leases minted, want admitted with one", admission.Admitted, keyed.minted)
	}
	if admission.Release != nil {
		admission.Release()
	}
	if wait, _ := store.GrantWaitOf(ctx, "1"); wait != nil {
		t.Errorf("the policy wait outlived the refusal: %+v", wait)
	}
}

// The grain-ci grant the admitter adds by default is judged too: a
// policy whose allow omits it holds the task with that reason rather
// than letting the run fail setup.
func TestLocalPolicyHoldsATaskOnTheGrainCIGrantTheAdmitterAdds(t *testing.T) {
	store, ctx := openTreeStore(t)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse([]byte("repos: [acme/widgets]\nlocal_requesters: [owner@example.com]\ncapabilities:\n  allow: [notes]\n"))
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: store, Policy: p}
	if err := store.PutTask(ctx, model.Task{ID: "1", Title: "ours", Target: &widgets, Creator: "owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	if heldByPolicy(ctx, deps)("1") {
		t.Fatal("a task with no grants was held")
	}
	if admitOnce(t, ctx, deps, "1").Admitted {
		t.Error("a task granted grain-ci by default was admitted under a policy that does not allow it")
	}
	wantPolicyWait(t, ctx, store, "1", grainci.CapabilityName)
}

// The admitter clears the waits it owns and leaves a hold's own reason
// alone.
func TestGrantAdmitterClearsOnlyTheWaitsItOwns(t *testing.T) {
	store, ctx := openTreeStore(t)
	deps := Deps{Store: store}
	for id, capability := range map[string]string{
		"1": model.TeamSetupWaitCapability, "2": model.BranchWaitCapability,
		"3": model.ModelWaitCapability, "4": model.PolicyWaitCapability, "5": "keyed",
	} {
		if err := store.PutTask(ctx, model.Task{ID: id, Title: "t", Target: &widgets}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetGrantWait(ctx, model.GrantWait{TaskID: id, Capability: capability, Resource: "why", WaitingAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if !admitOnce(t, ctx, deps, id).Admitted {
			t.Fatalf("task %s was not admitted", id)
		}
		wait, err := store.GrantWaitOf(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		owned := capability != model.TeamSetupWaitCapability && capability != model.BranchWaitCapability
		if owned && wait != nil {
			t.Errorf("the admitter left its own %s wait standing: %+v", capability, wait)
		}
		if !owned && (wait == nil || wait.Capability != capability) {
			t.Errorf("the admitter cleared a hold's %s wait: %+v", capability, wait)
		}
	}
}
