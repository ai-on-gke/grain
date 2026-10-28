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

// The one claim grain/task-461 rests on, asserted against the function
// that decides it rather than against a run: **no configuration of a
// repository produces a judging run that is a trusted release.**
//
// Everything else about the change is visible from outside -- the two
// runs, the row the queue cannot see, the release that fails and holds
// nothing -- and is pinned in basebranchrelease_test.go. This is the
// structural half, and it is here because the structure is a field that
// no longer exists: baseBranchCIPolicy carries no event, so there is
// nothing for a future branch of this function to set to ci.EventRelease
// by accident. A test cannot assert the absence of a field, so it
// asserts the consequence over every shape of repository the function
// can be handed.

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestNoRepositoryIsEverJudgedByATrustedRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)

	cases := []struct {
		name string
		repo model.RepoRef
		cfg  *model.RepoConfig
	}{
		{
			name: "releases on, nothing named -- the default every repository falls into",
			repo: model.RepoRef{Owner: "acme", Name: "releasing"},
			cfg: &model.RepoConfig{
				Repo:   model.RepoRef{Owner: "acme", Name: "releasing"},
				CIMode: model.CIModeGrain,
			},
		},
		{
			name: "releases on, judging jobs named -- which used to be ignored",
			repo: model.RepoRef{Owner: "acme", Name: "narrowed"},
			cfg: &model.RepoConfig{
				Repo:             model.RepoRef{Owner: "acme", Name: "narrowed"},
				CIMode:           model.CIModeGrain,
				BaseBranchCIJobs: []string{".grain/workflows/tests.yml:go"},
			},
		},
		{
			name: "releases off",
			repo: model.RepoRef{Owner: "acme", Name: "quiet"},
			cfg: &model.RepoConfig{
				Repo:   model.RepoRef{Owner: "acme", Name: "quiet"},
				CIMode: model.CIModeGrain,
				Work:   model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
			},
		},
		{
			name: "no configuration row at all",
			repo: model.RepoRef{Owner: "acme", Name: "unconfigured"},
		},
	}
	for _, c := range cases {
		if c.cfg != nil {
			if err := store.PutRepoConfig(ctx, *c.cfg); err != nil {
				t.Fatal(err)
			}
		}
		policy, err := baseBranchCIWanted(ctx, store, c.repo)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		req := policy.request(c.repo, "main", "0123456789abcdef", "base:test")
		if req.Event != ci.EventPush || req.TrustedRelease {
			t.Errorf("%s: the judging run is %+v, want an untrusted push. A judging run that is a "+
				"release makes the publisher's conclusion the merge queue's verdict, which is what "+
				"grain/task-461 took apart", c.name, req)
		}
		if len(req.ReleaseSecrets) != 0 {
			t.Errorf("%s: the judging run names release secrets %v", c.name, req.ReleaseSecrets)
		}
	}
}

// BaseBranchCIJobs narrows the judging run whether or not releases are
// on. It used to be read only with releases off, because the run it
// would have narrowed was also the publishing; now that the publishing
// is a run of its own there is nothing left to condition it on, and a
// repository that named its judging jobs while releasing was paying for
// a full suite it had already said it did not want.
func TestBaseBranchCIJobsNarrowTheJudgingRunWithReleasesOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		BaseBranchCIJobs: []string{".grain/workflows/tests.yml:go"},
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := baseBranchCIWanted(ctx, store, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Jobs) != 1 || policy.Jobs[0] != ".grain/workflows/tests.yml:go" {
		t.Fatalf("the judging run asks for %v, want only the jobs the repository named", policy.Jobs)
	}
	if policy.Refusal != "" {
		t.Fatalf("grain is not judging this repository at all: %q", policy.Refusal)
	}
}

// What the repair agent is told about the urgency has to be a thing that
// is true (grain/task-424). It used to be "the queue has stopped merging
// until you land", which grain/task-471 made false without rewriting the
// sentence -- so the task grain filed and the note grain put on every
// queued change (baseBrokenStatus, "merges are not being held for it")
// said opposite things about the same row.
//
// Pinned by the words rather than by a behaviour because the sentence is
// the whole of what is wrong when this regresses: nothing an agent does
// differently is observable, it just works on a false premise about how
// much time it has.
func TestTheRepairIsNotToldTheQueueIsWaitingForIt(t *testing.T) {
	t.Parallel()
	row := model.BaseBranchCI{
		Repo:   model.RepoRef{Owner: "acme", Name: "widgets"},
		Branch: "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Detail: "the go job failed",
	}
	body := baseBranchRepairBody(row, nil)

	// The claims the hold used to license, each of which is now false.
	for _, claim := range []string{
		"has stopped merging",
		"waits behind this task",
		"merges are being held",
		"the exception",
	} {
		if strings.Contains(body, claim) {
			t.Errorf("the repair task claims %q, and a red base branch has held nothing "+
				"since grain/task-471:\n%s", claim, body)
		}
	}
	// And the true urgency is still stated, since dropping the sentence
	// rather than correcting it is the other way to get this wrong.
	if !strings.Contains(body, "still merging green changes into `main`") ||
		!strings.Contains(body, "depend on this task") {
		t.Errorf("the repair task does not say the queue is still merging onto the red "+
			"branch, which is why fixing it is urgent:\n%s", body)
	}
}

func TestBaseBranchRepairPromptHolisticInstructions(t *testing.T) {
	t.Parallel()
	row := model.BaseBranchCI{
		Repo:   model.RepoRef{Owner: "acme", Name: "widgets"},
		Branch: "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Detail: "multiple jobs failed",
	}
	body := baseBranchRepairBody(row, nil)

	if strings.Contains(body, "smallest change that turns the suite green") {
		t.Errorf("the repair task should not encourage narrow smallest change fixes, got:\n%s", body)
	}
	for _, want := range []string{
		"repairing the root base branch itself",
		"Take a holistic approach",
		"inspect and resolve all failing jobs in the suite",
		"shared base faults",
		"source of truth for `main`",
		"Iterate and re-run CI until all required jobs in the suite pass",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the repair prompt is missing instruction %q:\n%s", want, body)
		}
	}
}
