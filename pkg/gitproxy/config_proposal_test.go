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

package gitproxy

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

type stubConfigProposalLookup struct {
	stubBranchLookup
	prefix string
}

func (s stubConfigProposalLookup) GitConfigProposalPrefix(context.Context, string) (string, error) {
	return s.prefix, nil
}

func TestAuthorizeRefUpdatesAllowsConfigProposalBranchesButNeverMain(t *testing.T) {
	config := model.ConfigRepo()
	a := &ModelAuthorizer{
		Store: stubScopeLookup{writes: []model.RepoRef{config}},
		Branches: stubConfigProposalLookup{
			stubBranchLookup: stubBranchLookup{branch: "grain/task-7", live: true},
			prefix:           model.ConfigProposalBranchPrefix("7"),
		},
	}
	ctx := context.Background()
	for _, ok := range [][]RefUpdate{
		{{zero, oidB, "refs/heads/grain/config/7/raise-max-parallel"}},
		{{oidA, oidB, "refs/heads/grain/config/7/raise-max-parallel"}},
		{{oidA, zero, "refs/heads/grain/config/7/raise-max-parallel"}},
		{{oidA, oidB, "refs/heads/grain/task-7"}, {zero, oidB, "refs/heads/grain/config/7/x"}},
	} {
		if reason, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", "grain", "config", ok); err != nil || reason != "" {
			t.Errorf("%v: refused (%q, %v), want allowed", ok, reason, err)
		}
	}
	for _, refused := range [][]RefUpdate{
		{{oidA, oidB, "refs/heads/main"}},
		{{zero, oidB, "refs/heads/grain/config/7/"}},
		{{zero, oidB, "refs/heads/grain/config/8/other-session"}},
		{{zero, oidB, "refs/heads/grain/config/7/x"}, {oidA, oidB, "refs/heads/main"}},
	} {
		if reason, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", "grain", "config", refused); err != nil || reason == "" {
			t.Errorf("%v: allowed (%q, %v), want refused", refused, reason, err)
		}
	}
	// The prefix is a grain/config grant: the same ref in another
	// repository the run may push is still not its own branch.
	other := []RefUpdate{{zero, oidB, "refs/heads/grain/config/7/x"}}
	if reason, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", other); err != nil || reason == "" {
		t.Errorf("acme/widgets: allowed (%q, %v), want refused", reason, err)
	}
}

func TestAuthorizeRefUpdatesRefusesConfigProposalsWithoutTheGrant(t *testing.T) {
	a := &ModelAuthorizer{
		Store: stubScopeLookup{},
		Branches: stubConfigProposalLookup{
			stubBranchLookup: stubBranchLookup{branch: "grain/task-7", live: true},
		},
	}
	update := []RefUpdate{{zero, oidB, "refs/heads/grain/config/7/x"}}
	if reason, err := a.AuthorizeRefUpdates(context.Background(), "sandbox-0", "grain", "config", update); err != nil || reason == "" {
		t.Errorf("allowed (%q, %v), want refused", reason, err)
	}
}
