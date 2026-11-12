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
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

type fakeHostBranchAndCollaborator struct {
	github.Client
	branchOwners []string
	perms        map[string]string
}

func (f *fakeHostBranchAndCollaborator) BranchExists(owner, repo, branch string) (bool, error) {
	f.branchOwners = append(f.branchOwners, owner)
	return true, nil
}

func (f *fakeHostBranchAndCollaborator) CollaboratorPermission(owner, repo, username string) (string, error) {
	return f.perms[owner+"/"+repo+":"+username], nil
}

func TestSameRepoHeadBranchAndHeadPushableAcceptBareHeadRepoOffDefaultHost(t *testing.T) {
	t.Parallel()
	ghe := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	sameRepoDetail := github.PullRequestDetail{
		HeadRef:  "grain/task-1",
		BaseRef:  "main",
		HeadRepo: "acme/widgets",
	}
	if !sameRepoHeadBranch(ghe, sameRepoDetail) {
		t.Fatal("sameRepoHeadBranch rejected a same-repo PR whose HeadRepo is bare owner/name on a non-default host")
	}
	forkDetail := sameRepoDetail
	forkDetail.HeadRepo = "bob/widgets"
	if sameRepoHeadBranch(ghe, forkDetail) {
		t.Fatal("sameRepoHeadBranch accepted a fork PR")
	}

	fake := &fakeHostBranchAndCollaborator{}
	if !headPushable(fake, ghe, sameRepoDetail) {
		t.Fatal("headPushable rejected a same-repo PR whose HeadRepo is bare owner/name on a non-default host")
	}
	if len(fake.branchOwners) != 1 || fake.branchOwners[0] != "ghe.example.com/acme" {
		t.Fatalf("BranchExists owners = %v, want [ghe.example.com/acme]", fake.branchOwners)
	}
	if headPushable(fake, ghe, forkDetail) {
		t.Fatal("headPushable accepted a fork PR")
	}
}

func TestSenderTrustedAndSyncPollKeySeparateSameRepoAcrossHosts(t *testing.T) {
	t.Parallel()
	def := model.RepoRef{Owner: "acme", Name: "widgets"}
	ghe := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	fake := &fakeHostBranchAndCollaborator{
		perms: map[string]string{
			"ghe.example.com/acme/widgets:alice": "write",
			"acme/widgets:alice":                 "none",
		},
	}
	cache := map[string]bool{}
	trustedGHE, err := senderTrusted(fake, cache, ghe, "alice")
	if err != nil || !trustedGHE {
		t.Fatalf("senderTrusted(ghe) = %v, %v; want true, nil", trustedGHE, err)
	}
	trustedDef, err := senderTrusted(fake, cache, def, "alice")
	if err != nil || trustedDef {
		t.Fatalf("senderTrusted(def) = %v, %v; want false, nil (GHE trust leaked into default host cache)", trustedDef, err)
	}

	defKey := syncPollKey(nil, model.PullRequestRef{Repo: def, Number: 7})
	gheKey := syncPollKey(nil, model.PullRequestRef{Repo: ghe, Number: 7})
	if defKey == gheKey {
		t.Fatalf("syncPollKey collided across hosts: %+v", defKey)
	}
}
