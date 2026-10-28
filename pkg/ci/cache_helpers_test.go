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
	"testing"
	"time"
)

// aResult and openCache are pkg/grit/cache's test helpers, restated for
// this package's tests of the engine and the service, which use a
// DirCache without testing it.

func aResult(name, output string) JobRun {
	return JobRun{
		Workflow:     "build",
		WorkflowPath: ".grain/workflows/a.yml",
		JobID:        name,
		Name:         name,
		Conclusion:   ConclusionSuccess,
		StartedAt:    timePtr(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)),
		FinishedAt:   timePtr(time.Date(2026, 9, 1, 12, 1, 0, 0, time.UTC)),
		Steps:        []StepRun{{Name: "make test", Conclusion: ConclusionSuccess, Output: output}},
	}
}

func openCache(t *testing.T, opts DirCacheOptions) *DirCache {
	t.Helper()
	if opts.Log == nil {
		opts.Log = func(format string, args ...any) { t.Logf(format, args...) }
	}
	cache, err := OpenDirCache(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("opening a cache: %v", err)
	}
	return cache
}

// The engine end to end over a real directory: the same plan run twice
// builds a sandbox the first time and none the second.
func TestDirCacheMeansOneCommitIsTestedOnce(t *testing.T) {
	cache := openCache(t, DirCacheOptions{})
	sandboxes := &fakeSandboxes{}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	engine := &Engine{Sandboxes: sandboxes, Cache: cache}

	first := engine.Run(context.Background(), "r1", plan, nil)
	if first.Conclusion != ConclusionSuccess || first.Jobs[0].Cached {
		t.Fatalf("first run: %q cached=%v", first.Conclusion, first.Jobs[0].Cached)
	}
	second := engine.Run(context.Background(), "r2", plan, nil)
	if second.Conclusion != ConclusionSuccess || !second.Jobs[0].Cached {
		t.Fatalf("second run: %q cached=%v", second.Conclusion, second.Jobs[0].Cached)
	}
	if second.Jobs[0].Sandbox != "" {
		t.Errorf("a job served from cache names a sandbox it never had: %q", second.Jobs[0].Sandbox)
	}
	if sandboxes.sandboxes != 1 {
		t.Errorf("sandboxes built for two runs of one commit: %d", sandboxes.sandboxes)
	}
}

// grain/task-304: what an operator can see and do to the cache from
// outside the daemon's journal.
