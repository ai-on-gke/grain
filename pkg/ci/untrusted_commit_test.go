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

package ci

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// historySource is staticSource that can also answer BranchSource, with
// onBranch the commits it says are in the branch's history.
type historySource struct {
	staticSource
	onBranch []string
	asked    []string
}

func (s *historySource) OnBranch(_ context.Context, _, _, commit string) (bool, error) {
	s.asked = append(s.asked, commit)
	for _, c := range s.onBranch {
		if c == commit {
			return true, nil
		}
	}
	return false, nil
}

const untrustedCommitWorkflow = "on:\n  push:\n  workflow_dispatch:\njobs:\n" +
	"  build:\n    cache: false\n    capabilities: [gcp-key]\n    steps:\n      - run: true\n" +
	"  lint:\n    cache: false\n    steps:\n      - run: true\n" +
	"  publish:\n    cache: false\n    secrets: [NPM_TOKEN]\n    steps:\n      - run: true\n"

func untrustedCommitService(t *testing.T, backend *fakeSandboxes) *testService {
	t.Helper()
	service := newTestService(t, backend, map[string]string{".grain/workflows/ci.yml": untrustedCommitWorkflow})
	service.Capabilities = func(context.Context, string) ([]string, error) { return []string{"gcp-key"}, nil }
	service.ReleaseSecrets = func(context.Context, string) ([]string, error) { return []string{"NPM_TOKEN"}, nil }
	return service
}

func jobsByID(run Run) map[string]JobRun {
	out := map[string]JobRun{}
	for _, job := range run.Jobs {
		out[job.JobID] = job
	}
	return out
}

// A run pinned to a commit that is not on its branch -- a fork's pull
// request head, which GitHub serves by commit -- runs the way a fork's
// pull_request would on GitHub: with none of the repository's
// capabilities or release secrets. A job that declared one errors and
// says why; a job that declared nothing, which would otherwise inherit
// every capability the repository grants, gets nothing.
func TestServiceWithholdsCapabilitiesFromACommitNotOnTheBranch(t *testing.T) {
	backend := &fakeSandboxes{}
	service := untrustedCommitService(t, backend)

	started, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Commit: "f00df00df00df00d", Event: EventManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := jobsByID(waitForRun(t, service, started.ID))
	for _, id := range []string{"build", "publish"} {
		job := got[id]
		if job.Conclusion != ConclusionErrored ||
			!strings.Contains(job.Error, "withholds every capability and release secret") ||
			!strings.Contains(job.Error, "f00df00df00df00d is not on acme/widgets's branch main") {
			t.Fatalf("%s = %+v, want an error naming the untrusted commit", id, job)
		}
	}
	if got["lint"].Conclusion != ConclusionSuccess {
		t.Fatalf("lint = %+v, want it to run", got["lint"])
	}
	if len(backend.acquired) != 1 {
		t.Fatalf("acquired %d sandboxes, want only lint's", len(backend.acquired))
	}
	if caps, secrets := backend.acquired[0].Capabilities, backend.acquired[0].ReleaseSecrets; len(caps) != 0 || len(secrets) != 0 {
		t.Fatalf("lint was handed capabilities %v and secrets %v", caps, secrets)
	}
}

// The branch's own tip, whole or abbreviated, is the tree the
// repository trusts, and is never asked about.
func TestServiceTrustsTheBranchTip(t *testing.T) {
	for _, commit := range []string{"", "resolved-commit", "resolved"} {
		backend := &fakeSandboxes{}
		service := untrustedCommitService(t, backend)
		source := &historySource{staticSource: service.Source.(staticSource)}
		service.Source = source

		started, err := service.Start(context.Background(), Request{
			Repo: "acme/widgets", Ref: "main", Commit: commit, Event: EventManual,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := jobsByID(waitForRun(t, service, started.ID))
		for id, job := range got {
			if job.Conclusion != ConclusionSuccess {
				t.Fatalf("commit %q: %s = %+v", commit, id, job)
			}
		}
		if len(source.asked) != 0 {
			t.Fatalf("commit %q: asked the source about %v", commit, source.asked)
		}
		for _, sb := range backend.acquired {
			if !reflect.DeepEqual(sb.Capabilities, []string{"gcp-key"}) {
				t.Fatalf("commit %q: capabilities %v", commit, sb.Capabilities)
			}
		}
	}
}

// An older commit the source places on the branch -- a run started for
// a push the branch has since moved past -- keeps its capabilities.
func TestServiceTrustsACommitInTheBranchHistory(t *testing.T) {
	backend := &fakeSandboxes{}
	service := untrustedCommitService(t, backend)
	source := &historySource{staticSource: service.Source.(staticSource), onBranch: []string{"0123456789abcdef"}}
	service.Source = source

	started, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Commit: "0123456789abcdef", Event: EventManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, job := range jobsByID(waitForRun(t, service, started.ID)) {
		if job.Conclusion != ConclusionSuccess {
			t.Fatalf("%s = %+v", id, job)
		}
	}
	if !reflect.DeepEqual(source.asked, []string{"0123456789abcdef"}) {
		t.Fatalf("asked %v", source.asked)
	}
}

// A release secret withheld from somebody who may not fire a release
// says so in the caller's words rather than the guest-cache warmer's.
func TestAWithheldReasonIsWhatTheJobIsTold(t *testing.T) {
	backend := &fakeSandboxes{}
	service := untrustedCommitService(t, backend)
	started, err := service.Start(context.Background(), Request{
		Repo: "acme/widgets", Ref: "main", Event: EventManual, Jobs: []string{"publish"},
		WithholdReleaseSecrets: true, WithholdReason: "you are not an admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	job := jobsByID(waitForRun(t, service, started.ID))["publish"]
	if job.Conclusion != ConclusionErrored || !strings.HasSuffix(job.Error, `"NPM_TOKEN", and you are not an admin`) {
		t.Fatalf("publish = %+v", job)
	}
}

// A run a restarted daemon re-plans from Source, having no persisted
// plan for it, is granted only what Start would grant: a pinned commit
// the branch does not hold gets no capabilities, and a release, schedule
// or manual run -- whose trust and withheld secrets its record does not
// carry -- is refused rather than re-planned with trust it may never
// have had.
func TestReconstructPlanGrantsNoMoreThanStart(t *testing.T) {
	service := untrustedCommitService(t, &fakeSandboxes{})

	plan, err := service.ReconstructPlan(context.Background(), Run{
		ID: "r1", Repo: "acme/widgets", Ref: "main", Commit: "f00df00df00df00d", Event: EventPush,
	})
	if err != nil {
		t.Fatal(err)
	}
	if caps := plan.Request.Capabilities; len(caps) != 0 {
		t.Fatalf("a commit not on the branch was re-planned with capabilities %v", caps)
	}
	if len(plan.Request.ReleaseSecrets) != 0 || plan.Request.TrustedRelease || plan.Request.TrustedSchedule {
		t.Fatalf("a push run was re-planned with trust: %+v", plan.Request)
	}

	// Resume concludes a run on the plan's jobs alone, so a re-plan that
	// lost one of the recorded jobs -- a cached failure, say -- would be a
	// verdict without it.
	recorded := func(ids ...string) []JobRun {
		var jobs []JobRun
		for _, id := range ids {
			jobs = append(jobs, JobRun{WorkflowPath: ".grain/workflows/ci.yml", JobID: id})
		}
		return jobs
	}
	if _, err := service.ReconstructPlan(context.Background(), Run{
		ID: "r2", Repo: "acme/widgets", Ref: "main", Event: EventPush, Jobs: recorded("build", "lint", "publish"),
	}); err != nil {
		t.Fatalf("a run recording exactly the planned jobs was refused: %v", err)
	}
	for name, jobs := range map[string][]JobRun{
		"a job the re-plan no longer has": recorded("build", "lint", "publish", "deploy"),
		"a subset of the planned jobs":    recorded("build"),
	} {
		if _, err := service.ReconstructPlan(context.Background(), Run{
			ID: "r3", Repo: "acme/widgets", Ref: "main", Event: EventPush, Jobs: jobs,
		}); err == nil {
			t.Errorf("%s: re-planned rather than refused", name)
		}
	}

	for _, event := range []Event{EventRelease, EventSchedule, EventManual} {
		if _, err := service.ReconstructPlan(context.Background(), Run{
			ID: "r-" + string(event), Repo: "acme/widgets", Ref: "main", Event: event,
		}); err == nil {
			t.Errorf("a %s run with no persisted plan was re-planned", event)
		}
	}
}
