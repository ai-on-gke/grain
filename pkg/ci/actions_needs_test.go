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
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ai-on-gke/grain/pkg/grit/actions"
)

// granuleCI is the shape that cost gke-labs/granule its gate: an
// ordinary Actions workflow, an ordinary `needs:`, and a `push:` that
// names main so every task branch arrives as the pull_request half.
// Reading `needs` as "this file cannot be planned" made every run of it
// a run of no jobs, which is a green tick nobody can tell from a pass.
const granuleCI = `name: CI
on:
  push:
    branches: [main]
  pull_request:
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: make lint
  test:
    runs-on: ubuntu-latest
    steps:
      - run: make test
  image:
    needs: [test]
    runs-on: ubuntu-latest
    steps:
      - run: make image
`

func TestActionsNeedsOrdersTheJobsRatherThanTurningTheRunOff(t *testing.T) {
	// Both spellings GitHub accepts, because both are written in the
	// wild and both used to do this.
	for _, needs := range []string{"needs: [test]", "needs: test"} {
		t.Run(needs, func(t *testing.T) {
			tree := fstest.MapFS{".github/workflows/ci.yml": &fstest.MapFile{
				Data: []byte(strings.Replace(granuleCI, "needs: [test]", needs, 1)),
			}}
			ws, _, err := LoadDir(tree)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			for _, ref := range []string{"main", "grain/task-387"} {
				plan, err := PlanRun(Request{Repo: "gke-labs/granule", Ref: ref, Commit: "abc", Event: EventPush}, ws)
				if err != nil {
					t.Fatalf("%s: planning: %v", ref, err)
				}
				if len(plan.Jobs) != 3 || plan.Unmatched != "" {
					t.Fatalf("%s: planned %d jobs (unmatched %q), want all three", ref, len(plan.Jobs), plan.Unmatched)
				}
				for _, job := range plan.Jobs {
					want := []string(nil)
					if job.Job.ID == "image" {
						want = []string{".github/workflows/ci.yml:test"}
					}
					if strings.Join(job.Needs, ",") != strings.Join(want, ",") {
						t.Fatalf("%s: %s needs %v, want %v", ref, job.Job.ID, job.Needs, want)
					}
				}
			}
		})
	}
}

// Selecting one job brings what it needs with it, which is the property
// that keeps a selective run from reporting a pass for work it skipped.
func TestSelectingAnActionsJobBringsItsNeedsAlong(t *testing.T) {
	ws, _, err := LoadDir(fstest.MapFS{".github/workflows/ci.yml": &fstest.MapFile{Data: []byte(granuleCI)}})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	plan, err := PlanRun(Request{Repo: "gke-labs/granule", Ref: "main", Commit: "abc", Event: EventPush, Jobs: []string{"image"}}, ws)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	var ids []string
	for _, job := range plan.Jobs {
		ids = append(ids, job.Job.ID)
	}
	if strings.Join(ids, ",") != "image,test" {
		t.Fatalf("selected %v, want image and the test it needs", ids)
	}
}

// act resolves `needs` itself: `act --job image` runs test and then
// image in one invocation. grain has already run test as a job of its
// own, so the copy act is handed for image must not say `needs` -- or
// every needed job runs twice, once on its own and once inside each
// dependant.
func TestAnActionsJobWithNeedsIsHandedAWorkflowWithoutIt(t *testing.T) {
	w, err := Parse(".github/workflows/ci.yml", []byte(granuleCI))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	byID := map[string]Job{}
	for _, job := range w.Jobs {
		byID[job.ID] = job
	}

	image := byID["image"]
	pruned := image.Actions.WorkflowText
	if pruned == "" {
		t.Fatal("a job that needs another is given no workflow of its own")
	}
	for _, line := range strings.Split(pruned, "\n") {
		if strings.Contains(line, "needs") && !strings.HasPrefix(line, "#") {
			t.Fatalf("the workflow handed to act still declares the job's needs: %q", line)
		}
	}
	if !strings.Contains(pruned, "#    needs: [test]") {
		t.Fatalf("`needs` was removed rather than commented out, which moves every line after it:\n%s", pruned)
	}
	if got, want := len(strings.Split(pruned, "\n")), len(strings.Split(granuleCI, "\n")); got != want {
		t.Fatalf("the pruned workflow has %d lines and the file has %d: act would report errors on the wrong line", got, want)
	}

	// Still the same workflow in every other respect: same jobs, same
	// steps, and every other job still keyed the same way.
	reparsed, err := Parse(".github/workflows/ci.yml", []byte(pruned))
	if err != nil {
		t.Fatalf("the workflow handed to act does not parse: %v\n%s", err, pruned)
	}
	if len(reparsed.Jobs) != len(w.Jobs) || reparsed.Name != w.Name {
		t.Fatalf("pruning changed the workflow: %d jobs named %q, want %d named %q",
			len(reparsed.Jobs), reparsed.Name, len(w.Jobs), w.Name)
	}
	for _, job := range reparsed.Jobs {
		if len(job.Needs) != 0 {
			t.Fatalf("jobs.%s still needs %v in the copy act is given", job.ID, job.Needs)
		}
	}

	// And it is that text act reads, not the file in the checkout.
	script := image.Steps[0].Run
	if !strings.Contains(script, base64.StdEncoding.EncodeToString([]byte(pruned))[:60]) {
		t.Fatalf("the job's script does not carry the pruned workflow:\n%s", script)
	}
	if !strings.Contains(script, "--workflows '"+actions.WorkflowDir+"/ci.yml'") {
		t.Fatalf("act is not pointed at the pruned workflow:\n%s", script)
	}
}

// A job that needs nothing is the job it always was, down to the bytes
// of its script and the fields of its fingerprint. That is not a
// nicety: an ActionsJob is hashed into its job's key, so a field that
// appeared on every one of them would miss the cache for every Actions
// job in every deployment.
func TestAnActionsJobThatNeedsNothingIsUnchanged(t *testing.T) {
	w, err := Parse(".github/workflows/ci.yml", []byte(granuleCI))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	for _, job := range w.Jobs {
		if job.ID == "image" {
			continue
		}
		if job.Actions.WorkflowText != "" {
			t.Fatalf("jobs.%s needs nothing but carries a workflow of its own", job.ID)
		}
		if want := actions.ActScript(".github/workflows/ci.yml", job.ID, ""); job.Steps[0].Run != want {
			t.Fatalf("jobs.%s script = %q, want %q", job.ID, job.Steps[0].Run, want)
		}
		encoded, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "WorkflowText") {
			t.Fatalf("jobs.%s fingerprints a key it does not use: %s", job.ID, encoded)
		}
	}
}

// A `needs:` this cannot cut without disturbing the rest of the file is
// an error naming the file and the job, not a mangled workflow handed to
// act. A whole job written as a flow mapping is the case: its `needs` has
// no line of its own to comment out.
func TestAnActionsNeedsThatCannotBeSeparatedIsRefusedByName(t *testing.T) {
	_, err := Parse(".github/workflows/ci.yml", []byte(
		"on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n"+
			"  image: {needs: test, runs-on: ubuntu-latest, steps: [{run: true}]}\n"))
	if err == nil || !strings.Contains(err.Error(), "jobs.image") ||
		!strings.Contains(err.Error(), "`needs:` on a line of its own") {
		t.Fatalf("error = %v, want one naming the job and what to do about it", err)
	}
}
