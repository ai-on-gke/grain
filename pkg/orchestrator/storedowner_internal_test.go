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

type stubCollaboratorClient struct {
	github.Client
	perms        map[string]string
	calls        []string
	branchOwners []string
}

func (c *stubCollaboratorClient) CollaboratorPermission(owner, repo, username string) (string, error) {
	key := owner + "/" + repo + ":" + username
	c.calls = append(c.calls, key)
	if p, ok := c.perms[key]; ok {
		return p, nil
	}
	return "none", nil
}

func (c *stubCollaboratorClient) BranchExists(owner, repo, branch string) (bool, error) {
	c.branchOwners = append(c.branchOwners, owner)
	return true, nil
}

func TestSenderTrustedSeparatesHostsSharingOwnerAndName(t *testing.T) {
	t.Parallel()
	client := &stubCollaboratorClient{
		perms: map[string]string{
			"ghe.example.com/acme/widgets:alice": "write",
			"acme/widgets:alice":                 "none",
		},
	}
	cache := map[string]bool{}
	gheRepo := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	defRepo := model.RepoRef{Owner: "acme", Name: "widgets"}

	gheTrusted, err := senderTrusted(client, cache, gheRepo, "alice")
	if err != nil || !gheTrusted {
		t.Fatalf("senderTrusted on ghe = (%v, %v), want (true, nil)", gheTrusted, err)
	}
	defTrusted, err := senderTrusted(client, cache, defRepo, "alice")
	if err != nil || defTrusted {
		t.Fatalf("senderTrusted on default host = (%v, %v), want (false, nil); calls=%v", defTrusted, err, client.calls)
	}
	if len(client.calls) != 2 {
		t.Fatalf("CollaboratorPermission calls = %v, want one per host", client.calls)
	}
}

func TestSyncPollKeySeparatesHostsSharingOwnerNameAndNumber(t *testing.T) {
	t.Parallel()
	gheRef := model.PullRequestRef{
		Repo:   model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"},
		Number: 7,
	}
	defRef := model.PullRequestRef{
		Repo:   model.RepoRef{Owner: "acme", Name: "widgets"},
		Number: 7,
	}
	gheKey := syncPollKey(nil, gheRef)
	defKey := syncPollKey(nil, defRef)
	if gheKey == defKey {
		t.Fatalf("syncPollKey collided across hosts: both = %+v", gheKey)
	}
	if gheKey.Subject != "ghe.example.com/acme/widgets#7" {
		t.Errorf("ghe Subject = %q, want ghe.example.com/acme/widgets#7", gheKey.Subject)
	}
	if defKey.Subject != "acme/widgets#7" {
		t.Errorf("default Subject = %q, want acme/widgets#7", defKey.Subject)
	}
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

	fake := &stubCollaboratorClient{}
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
