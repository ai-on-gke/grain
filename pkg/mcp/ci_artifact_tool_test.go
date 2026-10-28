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

package mcp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// fakeCIArtifactRunner is fakeGrainCIRunner plus the one method
// get_ci_artifact needs beyond it: a download that remembers what it was
// asked for and writes a fixed body, or fails the way a daemon hop can.
type fakeCIArtifactRunner struct {
	*fakeGrainCIRunner
	downloads []ci.ArtifactRef
	body      []byte
	err       error
}

func (f *fakeCIArtifactRunner) GrainCIRun(ctx context.Context, id string) (ci.Run, error) {
	for _, r := range f.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return f.fakeGrainCIRunner.GrainCIRun(ctx, id)
}

func (f *fakeCIArtifactRunner) DownloadGrainCIArtifact(_ context.Context, ref ci.ArtifactRef, w io.Writer) (int64, error) {
	f.downloads = append(f.downloads, ref)
	if f.err != nil {
		return 0, f.err
	}
	n, err := w.Write(f.body)
	return int64(n), err
}

func artifactRunWith(job ci.JobRun) *ci.Run {
	return &ci.Run{ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{job}}
}

func artifactTool(runner GrainCIArtifactRunner, scope CIRepoScope, root string) Tool {
	tools := NewGrainCIArtifactTools(runner, scope, root)
	if len(tools) != 1 || tools[0].Name != "get_ci_artifact" {
		panic("NewGrainCIArtifactTools did not return exactly get_ci_artifact")
	}
	return tools[0]
}

func TestGetCIArtifactWritesBytesIntoTheSandboxWorkspace(t *testing.T) {
	root := t.TempDir()
	inner := &fakeGrainCIRunner{run: artifactRunWith(ci.JobRun{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{
		{Run: "ci-1", Job: "e2e", Name: "trace", Stored: true, Bytes: int64(len("trace bytes"))},
	}})}
	runner := &fakeCIArtifactRunner{fakeGrainCIRunner: inner, body: []byte("trace bytes")}
	tool := artifactTool(runner, CIRepoScope{Repo: testCIRepo}, root)

	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if len(runner.downloads) != 1 || runner.downloads[0] != (ci.ArtifactRef{Run: "ci-1", Job: "e2e", Name: "trace"}) {
		t.Fatalf("downloaded ref = %v, want ci-1/e2e/trace", runner.downloads)
	}
	wantPath := filepath.Join(root, "ci-1-e2e-trace.tar.gz")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("artifact was not written to %s: %v", wantPath, err)
	}
	if string(data) != "trace bytes" {
		t.Fatalf("file contents = %q, want %q", data, "trace bytes")
	}
	for _, want := range []string{"trace", "ci-1", "e2e", "tar xzf", "read_file"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("result lacks %q: %s", want, result.Text)
		}
	}
}

func TestGetCIArtifactRefusesWithoutASandboxWorkspace(t *testing.T) {
	inner := &fakeGrainCIRunner{run: artifactRunWith(ci.JobRun{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{
		{Run: "ci-1", Job: "e2e", Name: "trace", Stored: true, Bytes: 3},
	}})}
	runner := &fakeCIArtifactRunner{fakeGrainCIRunner: inner, body: []byte("abc")}
	tool := artifactTool(runner, CIRepoScope{Repo: testCIRepo}, "")

	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if !result.IsError {
		t.Fatalf("expected a refusal with no sandbox workspace, got: %s", result.Text)
	}
	if len(runner.downloads) != 0 {
		t.Fatalf("downloaded despite having nowhere to write it: %v", runner.downloads)
	}
}

func TestGetCIArtifactRefusesARunOutsideScope(t *testing.T) {
	root := t.TempDir()
	inner := &fakeGrainCIRunner{run: &ci.Run{ID: "ci-1", Repo: "other/secret-app", Jobs: []ci.JobRun{
		{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{{Run: "ci-1", Job: "e2e", Name: "trace", Stored: true, Bytes: 3}}},
	}}}
	runner := &fakeCIArtifactRunner{fakeGrainCIRunner: inner, body: []byte("abc")}
	tool := artifactTool(runner, CIRepoScope{Repo: testCIRepo}, root)

	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if !result.IsError {
		t.Fatalf("expected a refusal for a run outside scope, got: %s", result.Text)
	}
	if len(runner.downloads) != 0 {
		t.Fatalf("downloaded a run outside scope: %v", runner.downloads)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("wrote a file for a run outside scope: %v", entries)
	}
}

func TestGetCIArtifactRefusesOneThatWasNotKept(t *testing.T) {
	root := t.TempDir()
	inner := &fakeGrainCIRunner{run: artifactRunWith(ci.JobRun{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{
		{Run: "ci-1", Job: "e2e", Name: "trace", Stored: false, Note: "280MB, over the 32MB an artifact may be"},
	}})}
	runner := &fakeCIArtifactRunner{fakeGrainCIRunner: inner}
	tool := artifactTool(runner, CIRepoScope{Repo: testCIRepo}, root)

	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1", "name": "trace"})
	if !result.IsError || !strings.Contains(result.Text, "280MB") {
		t.Fatalf("expected a refusal naming why it was not kept, got: %s", result.Text)
	}
	if len(runner.downloads) != 0 {
		t.Fatalf("downloaded an artifact that was never kept: %v", runner.downloads)
	}
}

func TestGetCIArtifactAsksToNameOneAmongSeveral(t *testing.T) {
	root := t.TempDir()
	inner := &fakeGrainCIRunner{run: artifactRunWith(ci.JobRun{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{
		{Run: "ci-1", Job: "e2e", Name: "trace", Stored: true, Bytes: 10},
		{Run: "ci-1", Job: "e2e", Name: "core", Stored: true, Bytes: 20},
	}})}
	runner := &fakeCIArtifactRunner{fakeGrainCIRunner: inner}
	tool := artifactTool(runner, CIRepoScope{Repo: testCIRepo}, root)

	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1"})
	if !result.IsError {
		t.Fatalf("expected ambiguity to be refused rather than guessed, got: %s", result.Text)
	}
	for _, want := range []string{"trace", "core", "name:"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("refusal lacks %q: %s", want, result.Text)
		}
	}
	if len(runner.downloads) != 0 {
		t.Fatalf("downloaded despite the ambiguity: %v", runner.downloads)
	}
}

func TestGetCIArtifactRefusesOverTheSizeCapWithoutDownloading(t *testing.T) {
	root := t.TempDir()
	inner := &fakeGrainCIRunner{run: artifactRunWith(ci.JobRun{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{
		{Run: "ci-1", Job: "e2e", Name: "trace", Stored: true, Bytes: maxCIArtifactDownloadBytes + 1},
	}})}
	runner := &fakeCIArtifactRunner{fakeGrainCIRunner: inner}
	tool := artifactTool(runner, CIRepoScope{Repo: testCIRepo}, root)

	result := tool.Handler(context.Background(), map[string]any{"run_id": "ci-1", "name": "trace"})
	if !result.IsError || !strings.Contains(result.Text, "grain ci artifact") {
		t.Fatalf("expected a refusal pointing at the operator's own download, got: %s", result.Text)
	}
	if len(runner.downloads) != 0 {
		t.Fatalf("downloaded an artifact over the size cap: %v", runner.downloads)
	}
}

func TestGetCIArtifactTakesAnExplicitPath(t *testing.T) {
	root := t.TempDir()
	inner := &fakeGrainCIRunner{run: artifactRunWith(ci.JobRun{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{
		{Run: "ci-1", Job: "e2e", Name: "trace", Stored: true, Bytes: 3},
	}})}
	runner := &fakeCIArtifactRunner{fakeGrainCIRunner: inner, body: []byte("abc")}
	tool := artifactTool(runner, CIRepoScope{Repo: testCIRepo}, root)

	result := tool.Handler(context.Background(), map[string]any{
		"run_id": "ci-1", "name": "trace", "path": "artifacts/my-trace.tar.gz",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if _, err := os.ReadFile(filepath.Join(root, "artifacts", "my-trace.tar.gz")); err != nil {
		t.Fatalf("artifact was not written to the named path: %v", err)
	}
}

func TestGetCIArtifactDefaultsToNewestRunOnBranch(t *testing.T) {
	root := t.TempDir()
	t1, t2, t3 := time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC(), time.Unix(300, 0).UTC()
	makeRun := func(id, ref string, started *time.Time) ci.Run {
		return ci.Run{
			ID: id, Repo: testCIRepo, Ref: ref, StartedAt: started, Conclusion: ci.ConclusionFailure,
			Jobs: []ci.JobRun{{JobID: "e2e", Name: "e2e", Artifacts: []ci.Artifact{
				{Run: id, Job: "e2e", Name: "trace", Stored: true, Bytes: 3},
			}}},
		}
	}
	runA1 := makeRun("branchA-old", "grain/task-1", &t1)
	runA2 := makeRun("branchA-new", "grain/task-1", &t2)
	runB := makeRun("branchB-newest", "grain/task-2", &t3)
	runs := []ci.Run{runA1, runA2, runB}

	// Task on grain/task-1 defaults to branchA-new even though branchB-newest is newer overall.
	innerA := &fakeGrainCIRunner{runs: runs}
	runnerA := &fakeCIArtifactRunner{fakeGrainCIRunner: innerA, body: []byte("abc")}
	toolsA := NewGrainCIArtifactTools(runnerA, CIRepoScope{Repo: testCIRepo}, root, "grain/task-1")
	resA := toolsA[0].Handler(context.Background(), map[string]any{})
	if resA.IsError {
		t.Fatalf("get_ci_artifact on task-1 failed: %s", resA.Text)
	}
	if len(runnerA.downloads) != 1 || runnerA.downloads[0].Run != "branchA-new" {
		t.Fatalf("downloaded run = %v, want branchA-new", runnerA.downloads)
	}

	// Task on grain/task-3 (no runs) falls back to the repo's newest run (branchB-newest).
	innerFallback := &fakeGrainCIRunner{runs: runs}
	runnerFallback := &fakeCIArtifactRunner{fakeGrainCIRunner: innerFallback, body: []byte("abc")}
	toolsFallback := NewGrainCIArtifactTools(runnerFallback, CIRepoScope{Repo: testCIRepo}, root, "grain/task-3")
	resFallback := toolsFallback[0].Handler(context.Background(), map[string]any{})
	if resFallback.IsError {
		t.Fatalf("get_ci_artifact fallback failed: %s", resFallback.Text)
	}
	if len(runnerFallback.downloads) != 1 || runnerFallback.downloads[0].Run != "branchB-newest" {
		t.Fatalf("downloaded run = %v, want branchB-newest", runnerFallback.downloads)
	}
}
