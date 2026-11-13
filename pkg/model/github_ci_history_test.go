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

package model_test

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

// eachGitHubCIBackend runs fn against a SQLite store and a Postgres one:
// the prune and the narrowed listing are SQL (LIKE ... ESCAPE among it),
// which the two dialects are free to read differently.
func eachGitHubCIBackend(t *testing.T, fn func(t *testing.T, st *model.Store)) {
	t.Run("sqlite", func(t *testing.T) { fn(t, openTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		st, _ := postgrestest.Open(t)
		if st == nil {
			return
		}
		fn(t, st)
	})
}

func TestFailedTestNamesReadsGoAndPlaywrightFailures(t *testing.T) {
	tail := "2026-11-05T10:00:01.1234567Z --- FAIL: TestMergeQueue (1.20s)\n" +
		"    --- FAIL: TestMergeQueue/conflict (0.40s)\n" +
		"--- PASS: TestOther (0.01s)\n" +
		"FAIL\tgithub.com/acme/widgets/pkg/queue\t1.3s\n" +
		"  \x1b[31m✘\x1b[39m  3 [chromium] › e2e/next-inbox.spec.js:41:5 › Inbox › shows the newest first (12.4s)\n" +
		"  ✘  7 [chromium] › e2e/next-inbox.spec.js:41:5 › Inbox › shows the newest first (retry #1) (11.0s)\n" +
		"  ✘  e2e/tasks.spec.ts:10:3 › opens a task\n" +
		"  ✓  2 [chromium] › e2e/next-inbox.spec.js:12:3 › Inbox › loads (2.0s)\n" +
		"--- FAIL: TestMergeQueue (1.20s)\n"
	got := model.FailedTestNames(tail)
	want := []string{
		"TestMergeQueue",
		"TestMergeQueue/conflict",
		"e2e/next-inbox.spec.js › Inbox › shows the newest first",
		"e2e/tasks.spec.ts › opens a task",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FailedTestNames =\n%q\nwant\n%q", got, want)
	}
	if got := model.FailedTestNames("ok\tall passed\n"); len(got) != 0 {
		t.Fatalf("a passing tail names %q", got)
	}
}

// The recorded jobs are a verdict history of the default branch and of
// Stage 1 for weeks, and a record of which tests failed anywhere, while
// the bulk -- task branches' passing jobs, and every log tail -- goes after
// the short window.
func TestPruneGitHubCIJobsKeepsVerdictHistoryAndFailingTests(t *testing.T) {
	eachGitHubCIBackend(t, testPruneGitHubCIJobsKeepsVerdictHistory)
}

func testPruneGitHubCIJobsKeepsVerdictHistory(t *testing.T, st *model.Store) {
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	fiveWeeks := now.Add(-35 * 24 * time.Hour)
	tenWeeks := now.Add(-70 * 24 * time.Hour)
	yesterday := now.Add(-24 * time.Hour)
	tail := "--- FAIL: TestFlaky (0.1s)\n"
	jobs := []model.GitHubCIJob{
		// Five weeks old: kept, or not, by what they are.
		{CheckRunID: 1, Ref: "main", JobName: "go", Conclusion: "success", UpdatedAt: fiveWeeks},
		{CheckRunID: 2, Ref: "v1.4.0-rc.2", Workflow: "Nightly", JobName: "stage-1", Conclusion: "success", UpdatedAt: fiveWeeks},
		{CheckRunID: 3, Ref: "grain/task-9", JobName: "go", Conclusion: "success", UpdatedAt: fiveWeeks},
		{CheckRunID: 4, Ref: "grain/task-9", JobName: "go", Conclusion: "failure", FailureTail: tail, UpdatedAt: fiveWeeks},
		{CheckRunID: 5, Ref: "trunk", JobName: "go", Conclusion: "success", UpdatedAt: fiveWeeks},
		{CheckRunID: 6, Ref: "grain/task-9", JobName: "lint", Conclusion: "cancelled", UpdatedAt: fiveWeeks},
		// Past the long window: gone whatever it is.
		{CheckRunID: 7, Ref: "main", JobName: "go", Conclusion: "failure", FailureTail: tail, UpdatedAt: tenWeeks},
		// Recent: kept whole.
		{CheckRunID: 8, Ref: "grain/task-10", JobName: "go", Conclusion: "success", UpdatedAt: yesterday},
		{CheckRunID: 9, Ref: "grain/task-10", JobName: "go", Conclusion: "failure", FailureTail: tail, UpdatedAt: yesterday},
	}
	for _, j := range jobs {
		j.Repo, j.RunID = "o/r", 100+j.CheckRunID
		if err := st.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.PruneGitHubCIJobs(ctx, model.GitHubCIPrune{
		Before:   now.Add(-56 * 24 * time.Hour),
		Compact:  now.Add(-14 * 24 * time.Hour),
		KeepRefs: []string{"main", "trunk"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pruned %d jobs, want 3 (the ten-week-old one, and the old task branch's pass and cancel)", n)
	}
	left, err := st.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	byID := map[int64]model.GitHubCIJob{}
	for _, j := range left {
		ids = append(ids, int(j.CheckRunID))
		byID[j.CheckRunID] = j
	}
	sort.Ints(ids)
	if want := []int{1, 2, 4, 5, 8, 9}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("kept check runs %v, want %v", ids, want)
	}
	old := byID[4]
	if old.FailureTail != "" || !reflect.DeepEqual(old.FailedTests, []string{"TestFlaky"}) || old.Conclusion != "failure" {
		t.Errorf("an old failure keeps its verdict and failing tests, not its tail: %+v", old)
	}
	if recent := byID[9]; recent.FailureTail != tail {
		t.Errorf("a recent failure keeps its tail: %+v", recent)
	}
	// A prune with no Compact forgets only by Before, as it always did.
	if _, err := st.PruneGitHubCIJobs(ctx, model.GitHubCIPrune{Before: now.Add(-56 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.ListGitHubCIJobs(ctx, model.GitHubCIFilter{Repo: "o/r"}); len(again) != 6 {
		t.Errorf("a Before-only prune removed more: %d left", len(again))
	}
}

// Until, RefContains and FailedTest are asked of the store, so a window
// weeks back or one branch is not the newest Limit jobs of everything,
// narrowed afterwards to nothing.
func TestListGitHubCIJobsNarrowsByWindowRefAndFailedTest(t *testing.T) {
	eachGitHubCIBackend(t, testListGitHubCIJobsNarrows)
}

func testListGitHubCIJobsNarrows(t *testing.T, st *model.Store) {
	ctx := context.Background()
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		started := base.Add(time.Duration(i) * 24 * time.Hour)
		done := started.Add(10 * time.Minute)
		ref, conclusion, tail := "main", "success", ""
		if i%2 == 1 {
			ref = "grain/task-" + string(rune('a'+i))
		}
		if i == 1 || i == 4 {
			conclusion, tail = "failure", "--- FAIL: TestWidget_50%/sub (1s)\n"
		}
		if err := st.PutGitHubCIJob(ctx, model.GitHubCIJob{
			Repo: "o/r", CheckRunID: int64(i + 1), RunID: int64(100 + i), Ref: ref, JobName: "go",
			Conclusion: conclusion, FailureTail: tail, StartedAt: &started, CompletedAt: &done, UpdatedAt: done,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(f model.GitHubCIFilter) []int64 {
		t.Helper()
		f.Repo = "o/r"
		jobs, err := st.ListGitHubCIJobs(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, j := range jobs {
			out = append(out, j.CheckRunID)
		}
		return out
	}
	until := base.Add(3 * 24 * time.Hour)
	if got, want := ids(model.GitHubCIFilter{Until: until}), []int64{3, 2, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("until %v = %v, want %v", until, got, want)
	}
	if got, want := ids(model.GitHubCIFilter{Until: until, RefContains: "MAIN"}), []int64{3, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("until and ref main = %v, want %v", got, want)
	}
	if got, want := ids(model.GitHubCIFilter{Since: base.Add(24 * time.Hour), Until: until, Limit: 1}), []int64{3}; !reflect.DeepEqual(got, want) {
		t.Errorf("window with limit 1 = %v, want %v", got, want)
	}
	if got, want := ids(model.GitHubCIFilter{FailedTest: "testwidget_50%"}), []int64{5, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("failed test = %v, want %v", got, want)
	}
	// LIKE's wildcards in a name are matched as themselves.
	if got := ids(model.GitHubCIFilter{FailedTest: "TestWidget_5_%"}); len(got) != 0 {
		t.Errorf("a wildcard matched as a wildcard: %v", got)
	}
}
