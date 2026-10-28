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

package gcpsetup

import (
	"errors"
	"fmt"
	"testing"

	crm "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	iam "google.golang.org/api/iam/v1"
)

func TestAddBindingCreatesTheBindingWhenNoneExistsForTheRole(t *testing.T) {
	policy := &iam.Policy{}
	changed := addBinding(policy, "roles/foo", "user:a@example.com")
	if !changed {
		t.Fatal("addBinding = false, want true for a brand-new role")
	}
	if len(policy.Bindings) != 1 {
		t.Fatalf("Bindings = %v, want exactly one", policy.Bindings)
	}
	b := policy.Bindings[0]
	if b.Role != "roles/foo" || len(b.Members) != 1 || b.Members[0] != "user:a@example.com" {
		t.Errorf("binding = %+v, want role roles/foo with member user:a@example.com", b)
	}
}

func TestAddBindingAppendsToAnExistingRoleMissingTheMember(t *testing.T) {
	policy := &iam.Policy{Bindings: []*iam.Binding{
		{Role: "roles/foo", Members: []string{"user:a@example.com"}},
	}}
	changed := addBinding(policy, "roles/foo", "user:b@example.com")
	if !changed {
		t.Fatal("addBinding = false, want true when adding a new member to an existing role")
	}
	if len(policy.Bindings) != 1 {
		t.Fatalf("Bindings = %v, want still exactly one binding", policy.Bindings)
	}
	want := []string{"user:a@example.com", "user:b@example.com"}
	if !equalStrings(policy.Bindings[0].Members, want) {
		t.Errorf("Members = %v, want %v", policy.Bindings[0].Members, want)
	}
}

func TestAddBindingIsANoOpWhenMemberAlreadyHoldsTheRole(t *testing.T) {
	policy := &iam.Policy{Bindings: []*iam.Binding{
		{Role: "roles/foo", Members: []string{"user:a@example.com"}},
	}}
	changed := addBinding(policy, "roles/foo", "user:a@example.com")
	if changed {
		t.Error("addBinding = true, want false: member already held the role")
	}
	if len(policy.Bindings[0].Members) != 1 {
		t.Errorf("Members = %v, want unchanged", policy.Bindings[0].Members)
	}
}

func TestAddBindingLeavesOtherRolesAlone(t *testing.T) {
	policy := &iam.Policy{Bindings: []*iam.Binding{
		{Role: "roles/other", Members: []string{"user:a@example.com"}},
	}}
	changed := addBinding(policy, "roles/foo", "user:b@example.com")
	if !changed {
		t.Fatal("addBinding = false, want true for a new role")
	}
	if len(policy.Bindings) != 2 {
		t.Fatalf("Bindings = %v, want the existing binding plus a new one", policy.Bindings)
	}
	if policy.Bindings[0].Role != "roles/other" || len(policy.Bindings[0].Members) != 1 {
		t.Errorf("existing binding was mutated: %+v", policy.Bindings[0])
	}
}

func TestAddProjectBindingCreatesTheBindingWhenNoneExistsForTheRole(t *testing.T) {
	policy := &crm.Policy{}
	changed := addProjectBinding(policy, "roles/foo", "serviceAccount:a@proj.iam.gserviceaccount.com")
	if !changed {
		t.Fatal("addProjectBinding = false, want true for a brand-new role")
	}
	if len(policy.Bindings) != 1 {
		t.Fatalf("Bindings = %v, want exactly one", policy.Bindings)
	}
	b := policy.Bindings[0]
	if b.Role != "roles/foo" || len(b.Members) != 1 || b.Members[0] != "serviceAccount:a@proj.iam.gserviceaccount.com" {
		t.Errorf("binding = %+v", b)
	}
}

func TestAddProjectBindingAppendsToAnExistingRoleMissingTheMember(t *testing.T) {
	policy := &crm.Policy{Bindings: []*crm.Binding{
		{Role: "roles/foo", Members: []string{"serviceAccount:a@proj.iam.gserviceaccount.com"}},
	}}
	changed := addProjectBinding(policy, "roles/foo", "serviceAccount:b@proj.iam.gserviceaccount.com")
	if !changed {
		t.Fatal("addProjectBinding = false, want true when adding a new member to an existing role")
	}
	want := []string{"serviceAccount:a@proj.iam.gserviceaccount.com", "serviceAccount:b@proj.iam.gserviceaccount.com"}
	if !equalStrings(policy.Bindings[0].Members, want) {
		t.Errorf("Members = %v, want %v", policy.Bindings[0].Members, want)
	}
}

func TestAddProjectBindingIsANoOpWhenMemberAlreadyHoldsTheRole(t *testing.T) {
	policy := &crm.Policy{Bindings: []*crm.Binding{
		{Role: "roles/foo", Members: []string{"serviceAccount:a@proj.iam.gserviceaccount.com"}},
	}}
	changed := addProjectBinding(policy, "roles/foo", "serviceAccount:a@proj.iam.gserviceaccount.com")
	if changed {
		t.Error("addProjectBinding = true, want false: member already held the role")
	}
	if len(policy.Bindings[0].Members) != 1 {
		t.Errorf("Members = %v, want unchanged", policy.Bindings[0].Members)
	}
}

func TestAddProjectBindingLeavesOtherRolesAlone(t *testing.T) {
	policy := &crm.Policy{Bindings: []*crm.Binding{
		{Role: "roles/other", Members: []string{"serviceAccount:a@proj.iam.gserviceaccount.com"}},
	}}
	changed := addProjectBinding(policy, "roles/foo", "serviceAccount:b@proj.iam.gserviceaccount.com")
	if !changed {
		t.Fatal("addProjectBinding = false, want true for a new role")
	}
	if len(policy.Bindings) != 2 {
		t.Fatalf("Bindings = %v, want the existing binding plus a new one", policy.Bindings)
	}
	if policy.Bindings[0].Role != "roles/other" || len(policy.Bindings[0].Members) != 1 {
		t.Errorf("existing binding was mutated: %+v", policy.Bindings[0])
	}
}

func TestIsAlreadyExistsOnA409GoogleapiError(t *testing.T) {
	err := &googleapi.Error{Code: 409, Message: "already exists"}
	if !isAlreadyExists(err) {
		t.Error("isAlreadyExists = false, want true for a 409")
	}
}

func TestIsAlreadyExistsOnA409WrappedError(t *testing.T) {
	err := fmt.Errorf("creating service account: %w", &googleapi.Error{Code: 409, Message: "already exists"})
	if !isAlreadyExists(err) {
		t.Error("isAlreadyExists = false, want true for a wrapped 409")
	}
}

func TestIsAlreadyExistsRejectsOtherGoogleapiCodes(t *testing.T) {
	err := &googleapi.Error{Code: 403, Message: "permission denied"}
	if isAlreadyExists(err) {
		t.Error("isAlreadyExists = true, want false for a 403")
	}
}

func TestIsAlreadyExistsRejectsNonGoogleapiErrors(t *testing.T) {
	if isAlreadyExists(errors.New("network is down")) {
		t.Error("isAlreadyExists = true, want false for a plain error")
	}
}

func TestIsAlreadyExistsRejectsNil(t *testing.T) {
	if isAlreadyExists(nil) {
		t.Error("isAlreadyExists = true, want false for nil")
	}
}

func TestAddBindingIgnoresConditionalBindingsForTheRole(t *testing.T) {
	cond := &iam.Expr{Expression: "request.time < timestamp('2020-01-01T00:00:00Z')"}
	policy := &iam.Policy{Bindings: []*iam.Binding{
		{Role: "roles/foo", Members: []string{"user:a@example.com"}, Condition: cond},
	}}
	if !addBinding(policy, "roles/foo", "user:a@example.com") {
		t.Fatal("addBinding = false, want true: a conditional grant is not the role")
	}
	if len(policy.Bindings) != 2 {
		t.Fatalf("Bindings = %v, want the conditional one untouched plus a new one", policy.Bindings)
	}
	if got := policy.Bindings[0].Members; len(got) != 1 {
		t.Errorf("conditional binding members = %v, want it left alone", got)
	}
	if b := policy.Bindings[1]; b.Condition != nil || b.Role != "roles/foo" || len(b.Members) != 1 || b.Members[0] != "user:a@example.com" {
		t.Errorf("new binding = %+v, want an unconditional roles/foo for user:a@example.com", b)
	}
}

func TestAddProjectBindingIgnoresConditionalBindingsForTheRole(t *testing.T) {
	cond := &crm.Expr{Expression: "resource.name.startsWith('x')"}
	policy := &crm.Policy{Bindings: []*crm.Binding{
		{Role: "roles/foo", Members: []string{"user:a@example.com"}, Condition: cond},
	}}
	if !addProjectBinding(policy, "roles/foo", "user:b@example.com") {
		t.Fatal("addProjectBinding = false, want true")
	}
	if len(policy.Bindings) != 2 || len(policy.Bindings[0].Members) != 1 {
		t.Fatalf("Bindings = %v, want the conditional one untouched plus a new one", policy.Bindings)
	}
	if b := policy.Bindings[1]; b.Condition != nil || b.Members[0] != "user:b@example.com" {
		t.Errorf("new binding = %+v, want unconditional user:b@example.com", b)
	}
}
