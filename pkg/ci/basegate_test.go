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
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// branchSource serves a different set of workflows per ref, "" being the
// default branch -- the shape a head and its base actually have.
type branchSource map[string][]Workflow

func (s branchSource) Workflows(_ context.Context, _, ref string) ([]Workflow, string, error) {
	workflows, ok := s[ref]
	if !ok {
		return nil, "", errors.New("no such branch: " + ref)
	}
	return workflows, "commit-of-" + ref, nil
}

const (
	gateTests = "on: push\njobs:\n  test:\n    steps:\n      - run: go test ./...\n" +
		"  lint:\n    steps:\n      - run: go vet ./...\n"
	gateTestOnly = "on: push\njobs:\n  test:\n    steps:\n      - run: go test ./...\n"
	gateHollow   = "on: push\njobs:\n  test:\n    steps:\n      - run: true\n" +
		"  lint:\n    steps:\n      - run: true\n"
)

func gateService(t *testing.T, head, base []Workflow) *testService {
	t.Helper()
	svc := newTestService(t, &fakeSandboxes{}, nil)
	svc.Source = branchSource{"grain/task-1": head, "": base, "main": base}
	return svc
}

func gatedRequest() Request {
	return Request{Repo: "o/r", Ref: "grain/task-1", Event: EventPush,
		RequestedBy: "task-1", GateOnBase: true}
}

// bwsalmon/grain#2390: a branch that deletes every workflow it has used to
// plan zero jobs and pass, and the merge queue read that as green.
func TestBaseGateRefusesAHeadThatDeletesEveryWorkflow(t *testing.T) {
	base := []Workflow{mustParse(t, ".grain/workflows/ci.yml", gateTests)}
	svc := gateService(t, nil, base)

	run, err := svc.Start(context.Background(), gatedRequest())
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if run.Conclusion != ConclusionFailure {
		t.Fatalf("conclusion = %q, want failure for a head that switched its own gate off", run.Conclusion)
	}
	for _, want := range []string{".grain/workflows/ci.yml:lint", ".grain/workflows/ci.yml:test", "(the default branch)"} {
		if !strings.Contains(run.Error, want) {
			t.Errorf("error does not name %q:\n%s", want, run.Error)
		}
	}
}

func TestBaseGateRefusesAHeadThatDropsOneJob(t *testing.T) {
	base := []Workflow{mustParse(t, ".grain/workflows/ci.yml", gateTests)}
	head := []Workflow{mustParse(t, ".grain/workflows/ci.yml", gateTestOnly)}
	svc := gateService(t, head, base)

	req := gatedRequest()
	req.BaseRef = "refs/heads/main"
	run, err := svc.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if run.Conclusion != ConclusionFailure || !strings.Contains(run.Error, "ci.yml:lint") ||
		strings.Contains(run.Error, "ci.yml:test") || !strings.Contains(run.Error, "base main") {
		t.Fatalf("want a failure naming only the dropped job against main, got %q: %s", run.Conclusion, run.Error)
	}
}

// What a head may still do: add jobs and change what its jobs run. The run
// is the head's own, with the head's own definitions.
func TestBaseGateLetsAHeadAddAndChangeJobs(t *testing.T) {
	base := []Workflow{mustParse(t, ".grain/workflows/ci.yml", gateTestOnly)}
	head := []Workflow{
		mustParse(t, ".grain/workflows/ci.yml", gateHollow),
		mustParse(t, ".grain/workflows/extra.yml", gateTestOnly),
	}
	svc := gateService(t, head, base)

	started, err := svc.Start(context.Background(), gatedRequest())
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if len(started.Jobs) != 3 {
		t.Fatalf("want the head's three jobs planned, got %+v", started.Jobs)
	}
	if finished := waitForRun(t, svc, started.ID); finished.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q (%s)", finished.Conclusion, finished.Error)
	}
}

// A repository with no CI on its base either is still the quiet empty
// pass PlanRun documents: the branch has removed nothing.
func TestBaseGateLeavesARepositoryWithNoWorkflowsAlone(t *testing.T) {
	svc := gateService(t, nil, nil)
	started, err := svc.Start(context.Background(), gatedRequest())
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	if finished := waitForRun(t, svc, started.ID); finished.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q (%s)", finished.Conclusion, finished.Error)
	}
}

// Ungated and narrowed runs are not the required verdict and are not
// compared: wait_for_checks with jobs, a manual dispatch, an operator.
func TestBaseGateOnlyAppliesToAGatedFullSuite(t *testing.T) {
	base := []Workflow{mustParse(t, ".grain/workflows/ci.yml", gateTests)}
	head := []Workflow{mustParse(t, ".grain/workflows/ci.yml", gateTestOnly)}

	for name, req := range map[string]Request{
		"ungated": {Repo: "o/r", Ref: "grain/task-1", Event: EventPush},
		"narrowed": {Repo: "o/r", Ref: "grain/task-1", Event: EventPush,
			GateOnBase: true, Jobs: []string{"test"}},
	} {
		svc := gateService(t, head, base)
		started, err := svc.Start(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: starting: %v", name, err)
		}
		if finished := waitForRun(t, svc, started.ID); finished.Conclusion != ConclusionSuccess {
			t.Fatalf("%s: conclusion = %q (%s)", name, finished.Conclusion, finished.Error)
		}
	}
}

// Not being able to read the base is not a verdict on the head: it is a
// source failure, retried like any other, and never a pass.
func TestBaseGateUnreadableBaseIsAnErrorNotAPass(t *testing.T) {
	svc := newTestService(t, &fakeSandboxes{}, nil)
	svc.Source = branchSource{"grain/task-1": nil}
	req := gatedRequest()
	req.BaseRef = "main"
	if run, err := svc.Start(context.Background(), req); err == nil {
		t.Fatalf("want an error, got run %+v", run)
	}
}

// A workflow definition error on the base branch must not fail a gated run on
// the head branch, or the pull request that repairs the broken workflow file
// on the base branch cannot pass its own merge gate. Any other workflow files
// on the base that do parse must still be enforced against the head.
func TestBaseGateBrokenWorkflowOnBaseDoesNotDeadlockRepair(t *testing.T) {
	baseTree := fstest.MapFS{
		".grain/workflows/ci.yml": &fstest.MapFile{Data: []byte(gateTests)},
		".grain/workflows/gcp-smoke.yml": &fstest.MapFile{Data: []byte(
			"on: workflow_dispatch\njobs:\n  smoke:\n    capabilities: [gcp-key]\n    capabilities: [gcp-key]\n    steps:\n      - run: true\n")},
	}
	fixedSmoke := "on: workflow_dispatch\njobs:\n  smoke:\n    capabilities: [gcp-key]\n    steps:\n      - run: true\n"

	t.Run("repair keeping base jobs passes", func(t *testing.T) {
		headTree := fstest.MapFS{
			".grain/workflows/ci.yml":        &fstest.MapFile{Data: []byte(gateHollow)},
			".grain/workflows/gcp-smoke.yml": &fstest.MapFile{Data: []byte(fixedSmoke)},
		}
		svc := newTestService(t, &fakeSandboxes{}, nil)
		svc.Source = SourceFunc(func(_ context.Context, _, ref string) ([]Workflow, string, error) {
			if ref == "main" {
				return LoadDir(baseTree)
			}
			return LoadDir(headTree)
		})
		req := gatedRequest()
		req.BaseRef = "main"
		started, err := svc.Start(context.Background(), req)
		if err != nil {
			t.Fatalf("starting: %v", err)
		}
		if finished := waitForRun(t, svc, started.ID); finished.Conclusion != ConclusionSuccess {
			t.Fatalf("conclusion = %q (%s), want success when head fixes base's broken workflow", finished.Conclusion, finished.Error)
		}
	})

	t.Run("broken file on base does not waive intact base workflows", func(t *testing.T) {
		headTree := fstest.MapFS{
			".grain/workflows/ci.yml":        &fstest.MapFile{Data: []byte(gateTestOnly)},
			".grain/workflows/gcp-smoke.yml": &fstest.MapFile{Data: []byte(fixedSmoke)},
		}
		svc := newTestService(t, &fakeSandboxes{}, nil)
		svc.Source = SourceFunc(func(_ context.Context, _, ref string) ([]Workflow, string, error) {
			if ref == "main" {
				return LoadDir(baseTree)
			}
			return LoadDir(headTree)
		})
		req := gatedRequest()
		req.BaseRef = "main"
		run, err := svc.Start(context.Background(), req)
		if err != nil {
			t.Fatalf("starting: %v", err)
		}
		if run.Conclusion != ConclusionFailure || !strings.Contains(run.Error, ".grain/workflows/ci.yml:lint") {
			t.Fatalf("want a failure naming the dropped job from the intact workflow, got %q: %s", run.Conclusion, run.Error)
		}
	})
}

// Which jobs count is decided by the same `on:` matching PlanRun uses, so
// a base workflow that would never run on this branch is not required.
func TestRemovedGateJobsOnlyCountsWorkflowsThatAnswer(t *testing.T) {
	base := []Workflow{
		mustParse(t, ".grain/workflows/ci.yml", gateTestOnly),
		mustParse(t, ".grain/workflows/release.yml",
			"on:\n  push:\n    branches: [main]\njobs:\n  publish:\n    steps:\n      - run: true\n"),
	}
	head := []Workflow{mustParse(t, ".grain/workflows/ci.yml", gateTestOnly)}
	if removed := RemovedGateJobs(gatedRequest(), base, head); len(removed) != 0 {
		t.Fatalf("removed = %v, want none", removed)
	}
}
