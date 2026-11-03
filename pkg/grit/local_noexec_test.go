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

package grit

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gritapi"
)

// TestDefaultExecutorNeverPassesAJobItDidNotRun is the regression for the
// 2026-10-29 walkthrough's F1: grit serve's default local executor reported
// "success" (and signed it) for any tree without .grain/grit-job.sh, so a
// daemon running CI through -grit-url recorded every commit green, failing
// workflow or not. A job with nothing to execute must not pass.
func TestDefaultExecutorNeverPassesAJobItDidNotRun(t *testing.T) {
	signer, ks := newTestSigner(t)
	h := startHarness(t, "local", t.TempDir(), signer, ks, nil, nil, "")
	defer h.close()

	ctx := context.Background()
	treeID, archive := makeSampleTree(t, map[string]string{
		".grain/workflows/ci.yml": "jobs:\n  test:\n    steps:\n      - run: exit 1\n",
	})
	if err := h.backend.UploadTree(ctx, treeID.SHA256, archive); err != nil {
		t.Fatal(err)
	}
	run, err := h.backend.StartRun(ctx, gritapi.StartRun{
		Repo:   "acme/widgets",
		Ref:    "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Tree:   treeID,
		Event:  "push",
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForRunCompleted(t, h.backend, run.ID)
	if done.Conclusion == "success" {
		t.Fatalf("run conclusion = success for a tree grit has no executor for; want errored")
	}
	for _, j := range done.Jobs {
		if j.Conclusion != "errored" {
			t.Errorf("job %s conclusion = %q, want errored", j.ID, j.Conclusion)
		}
		log, _, _, err := h.backend.GetLog(ctx, run.ID, j.ID, 0, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(log), "was not run") {
			t.Errorf("job %s log = %q, want it to say the job was not run", j.ID, log)
		}
	}
}

// TestDefaultExecutorRunsGritJobScript keeps the one thing the default
// executor does run: a tree's .grain/grit-job.sh, whose exit status is the
// job's verdict.
func TestDefaultExecutorRunsGritJobScript(t *testing.T) {
	signer, ks := newTestSigner(t)
	h := startHarness(t, "local", t.TempDir(), signer, ks, nil, nil, "")
	defer h.close()

	ctx := context.Background()
	treeID, archive := makeSampleTree(t, map[string]string{
		".grain/grit-job.sh": "echo running \"$1\"\n[ \"$1\" = good ]\n",
	})
	if err := h.backend.UploadTree(ctx, treeID.SHA256, archive); err != nil {
		t.Fatal(err)
	}
	run, err := h.backend.StartRun(ctx, gritapi.StartRun{
		Repo:   "acme/widgets",
		Ref:    "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Tree:   treeID,
		Event:  "push",
		Jobs:   []string{"good", "bad"},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForRunCompleted(t, h.backend, run.ID)
	got := map[string]string{}
	for _, j := range done.Jobs {
		got[j.ID] = j.Conclusion
	}
	if got["good"] != "success" || got["bad"] != "failure" || done.Conclusion != "failure" {
		t.Fatalf("conclusions = %v (run %s), want good=success bad=failure run=failure", got, done.Conclusion)
	}
}
