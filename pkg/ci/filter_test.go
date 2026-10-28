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
	"strings"
	"testing"
	"time"
)

// The filter vocabulary is shared between the CI pane and list_ci_runs
// (filter.go's own note on why), so these are tests of the words rather
// than of either reader: what each one matches, what happens when
// several are asked at once, and what an order does with the runs that
// have nothing to be ordered by.

var filterNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func at(minutesAgo int) *time.Time {
	t := filterNow.Add(-time.Duration(minutesAgo) * time.Minute)
	return &t
}

// runs is one deployment's memory as these tests ask about it: two
// repositories, three branches, a run of every interesting shape.
func filterRuns() []Run {
	return []Run{
		{ID: "red-main", Repo: "acme/widgets", Ref: "main", Conclusion: ConclusionFailure,
			StartedAt: at(30), FinishedAt: at(20),
			Jobs: []JobRun{
				{Name: "go tests", JobID: "go", Conclusion: ConclusionSuccess, StartedAt: at(30), FinishedAt: at(28)},
				{Name: "ui e2e", JobID: "e2e", Conclusion: ConclusionFailure, StartedAt: at(30), FinishedAt: at(20)},
			}},
		{ID: "green-main", Repo: "acme/widgets", Ref: "main", Conclusion: ConclusionSuccess,
			StartedAt: at(90), FinishedAt: at(85),
			Jobs: []JobRun{{Name: "ui e2e", JobID: "e2e", Conclusion: ConclusionSuccess, StartedAt: at(90), FinishedAt: at(85)}}},
		{ID: "errored-branch", Repo: "acme/widgets", Ref: "grain/task-231", Conclusion: ConclusionErrored,
			StartedAt: at(4000), FinishedAt: at(3999),
			Jobs: []JobRun{{Name: "go tests", JobID: "go", Conclusion: ConclusionErrored, StartedAt: at(4000)}}},
		{ID: "running", Repo: "acme/gadgets", Ref: "main", StartedAt: at(10),
			Jobs: []JobRun{{Name: "ui e2e", JobID: "e2e", StartedAt: at(9)}}},
		{ID: "queued", Repo: "acme/gadgets", Ref: "main", StartedAt: at(1),
			Jobs: []JobRun{{Name: "ui e2e", JobID: "e2e"}}},
	}
}

func ids(runs []Run) string {
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		out = append(out, run.ID)
	}
	return strings.Join(out, ",")
}

func TestRunFilterConditionsCompose(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter RunFilter
		want   string
	}{
		{"nothing asked is everything", RunFilter{}, "red-main,green-main,errored-branch,running,queued"},
		{"one repo", RunFilter{Repo: "acme/gadgets"}, "running,queued"},
		{"repo is not case-sensitive", RunFilter{Repo: "ACME/Gadgets"}, "running,queued"},
		{"a ref by any part of it", RunFilter{Ref: "task-231"}, "errored-branch"},
		{"what is red right now", RunFilter{States: []string{"failure", "errored"}}, "red-main,errored-branch"},
		{"one job, whatever it did", RunFilter{Job: "go tests"}, "red-main,errored-branch"},
		{"a job by its id", RunFilter{Job: "e2e"}, "red-main,green-main,running,queued"},
		{"a window", RunFilter{Since: filterNow.Add(-time.Hour)}, "red-main,running,queued"},
		{"a window with an end", RunFilter{Since: filterNow.Add(-2 * time.Hour), Until: filterNow.Add(-15 * time.Minute)},
			"red-main,green-main"},
		// The one this exists for: three conditions at once, each of
		// which on its own would answer with more.
		{"repo and result and job together", RunFilter{
			Repo: "acme/widgets", States: []string{"failure"}, Job: "ui e2e",
		}, "red-main"},
		{"conditions that cannot all hold match nothing", RunFilter{
			Repo: "acme/gadgets", States: []string{"success"},
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(FilterRuns(filterRuns(), tc.filter)); got != tc.want {
				t.Errorf("matched %q, want %q", got, tc.want)
			}
		})
	}
}

// A run that has not finished has no conclusion, and the two ways of
// not having one are a question about this deployment rather than about
// any commit: on a machine, or waiting for one. Neither may be dropped
// from a list for want of a conclusion to file it under.
func TestUnfinishedRunsAreFilterableAsRunningAndPending(t *testing.T) {
	if got := ids(FilterRuns(filterRuns(), RunFilter{States: []string{StateRunning}})); got != "running" {
		t.Errorf("running matched %q, want the run whose job is on a machine", got)
	}
	if got := ids(FilterRuns(filterRuns(), RunFilter{States: []string{StatePending}})); got != "queued" {
		t.Errorf("pending matched %q, want the run waiting for a sandbox", got)
	}
	// And it is still a run with no conclusion at all, rather than one
	// this vocabulary has quietly given a fake one.
	for _, run := range FilterRuns(filterRuns(), RunFilter{States: []string{StateRunning, StatePending}}) {
		if run.Conclusion != ConclusionPending {
			t.Errorf("%s came back with conclusion %q", run.ID, run.Conclusion)
		}
	}
}

// A run this deployment has only just queued has no start of its own to
// compare, and it is exactly the run "what is waiting for a sandbox" is
// about -- so a window keeps it rather than filing it before the window
// began.
func TestSinceKeepsARunThatHasNotStarted(t *testing.T) {
	runs := []Run{{ID: "fresh", Repo: "acme/widgets", Ref: "main"}}
	if got := ids(FilterRuns(runs, RunFilter{Since: filterNow.Add(-time.Minute)})); got != "fresh" {
		t.Errorf("a window dropped a run that has not started: %q", got)
	}
}

func TestSortRunsByEachKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter RunFilter
		want   string
	}{
		{"newest first is the default", RunFilter{Descending: true}, "queued,running,red-main,green-main,errored-branch"},
		{"oldest first", RunFilter{}, "errored-branch,green-main,red-main,running,queued"},
		// Longest first is what tells a forty-minute timeout from a
		// two-minute assertion failure. The run still going is measured
		// against now, which is what puts it second on ten minutes --
		// level with the finished run above it, which keeps its place
		// because the order is stable.
		{"longest first", RunFilter{Sort: SortRunsByDuration, Descending: true},
			"red-main,running,green-main,errored-branch,queued"},
		{"latest to finish first", RunFilter{Sort: SortRunsByFinished, Descending: true},
			"red-main,green-main,errored-branch,running,queued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(SortRuns(filterRuns(), tc.filter, filterNow)); got != tc.want {
				t.Errorf("ordered %q, want %q", got, tc.want)
			}
		})
	}
}

// Runs that compare equal keep the order the store handed them over in,
// whichever way the sort points -- otherwise a pane polling every five
// seconds would reshuffle an unchanged list under its reader.
func TestSortRunsIsStableAcrossEqualKeys(t *testing.T) {
	same := at(60)
	runs := []Run{
		{ID: "first", StartedAt: same, FinishedAt: at(50)},
		{ID: "second", StartedAt: same, FinishedAt: at(50)},
		{ID: "third", StartedAt: same, FinishedAt: at(50)},
	}
	for _, desc := range []bool{false, true} {
		for _, key := range RunSortKeys {
			filter := RunFilter{Sort: key, Descending: desc}
			if got := ids(SortRuns(runs, filter, filterNow)); got != "first,second,third" {
				t.Errorf("%s descending=%v reordered equal runs: %q", key, desc, got)
			}
		}
	}
}

func TestParseSinceTakesASpanOrADate(t *testing.T) {
	for _, tc := range []struct {
		text string
		want time.Time
	}{
		{"24h", filterNow.Add(-24 * time.Hour)},
		{"90m", filterNow.Add(-90 * time.Minute)},
		// Go's own ParseDuration has no unit for a day, and seven days
		// of CI history is how the question is asked.
		{"7d", filterNow.Add(-7 * 24 * time.Hour)},
		{"2026-09-17", time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
		{"2026-09-17T08:30:00Z", time.Date(2026, 9, 17, 8, 30, 0, 0, time.UTC)},
		{"", time.Time{}},
	} {
		got, err := ParseSince(tc.text, filterNow)
		if err != nil {
			t.Errorf("ParseSince(%q): %v", tc.text, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseSince(%q) = %s, want %s", tc.text, got, tc.want)
		}
	}
	if _, err := ParseSince("last tuesday", filterNow); err == nil {
		t.Error("ParseSince took a phrase it cannot honour")
	}
}

// A word the vocabulary does not have is refused with the words it does,
// rather than answered with an empty list -- which is indistinguishable
// from "nothing is red" and is the mistake this filtering exists to stop
// somebody making.
func TestParseRunStateNamesTheVocabularyItRefuses(t *testing.T) {
	if got, err := ParseRunState("FAILURE"); err != nil || got != "failure" {
		t.Errorf("ParseRunState(FAILURE) = %q, %v", got, err)
	}
	err := func() error { _, err := ParseRunState("failed"); return err }()
	if err == nil {
		t.Fatal("ParseRunState took a word that is not in the vocabulary")
	}
	for _, word := range RunStates {
		if !strings.Contains(err.Error(), word) {
			t.Errorf("the refusal does not name %q: %s", word, err)
		}
	}
	if _, err := ParseRunSortKey("length"); err == nil {
		t.Error("ParseRunSortKey took a word that is not an order")
	}
}

func TestDescribeSaysWhatWasAsked(t *testing.T) {
	f := RunFilter{Repo: "acme/widgets", Ref: "main", States: []string{"failure", "errored"},
		Job: "ui e2e", Since: time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)}
	want := `repo acme/widgets, ref main, result failure or errored, job "ui e2e", started since 2026-09-17T08:00:00Z`
	if got := f.Describe(); got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
	if !f.Narrowed() {
		t.Error("a filter asking five things says it narrows nothing")
	}
	if (RunFilter{Sort: SortRunsByDuration, Descending: true}).Narrowed() {
		t.Error("an order alone is not a narrowing")
	}
}

func TestJobStateOf(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		job  JobRun
		want string
	}{
		{"success conclusion", JobRun{Conclusion: ConclusionSuccess}, "success"},
		{"failure conclusion", JobRun{Conclusion: ConclusionFailure}, "failure"},
		{"running job", JobRun{Conclusion: ConclusionPending, StartedAt: &now}, StateRunning},
		{"pending job", JobRun{Conclusion: ConclusionPending, StartedAt: nil}, StatePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := JobStateOf(tc.job); got != tc.want {
				t.Errorf("JobStateOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMatchingJobs(t *testing.T) {
	run := Run{
		Jobs: []JobRun{
			{Name: "Build", JobID: "build-step"},
			{Name: "Unit Tests", JobID: "test"},
			{Name: "Integration Tests", JobID: "test"},
		},
	}

	if got := MatchingJobs(run, ""); len(got) != 0 {
		t.Errorf("MatchingJobs with empty name = %v, want empty", got)
	}
	if got := MatchingJobs(run, "build"); len(got) != 1 || got[0].Name != "Build" {
		t.Errorf("MatchingJobs(build) = %v", got)
	}
	if got := MatchingJobs(run, "build-step"); len(got) != 1 || got[0].Name != "Build" {
		t.Errorf("MatchingJobs(build-step) = %v", got)
	}
	if got := MatchingJobs(run, "test"); len(got) != 2 {
		t.Errorf("MatchingJobs(test) returned %d jobs, want 2", len(got))
	}
	if got := MatchingJobs(run, "nonexistent"); len(got) != 0 {
		t.Errorf("MatchingJobs(nonexistent) = %v, want empty", got)
	}
}

func TestParseRunSortKey(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  RunSortKey
		fails bool
	}{
		{"started", SortRunsByStarted, false},
		{"STARTED", SortRunsByStarted, false},
		{"finished", SortRunsByFinished, false},
		{"Finished", SortRunsByFinished, false},
		{"completed", SortRunsByFinished, false},
		{"Completed", SortRunsByFinished, false},
		{"duration", SortRunsByDuration, false},
		{"invalid", "", true},
		{"", "", true},
	} {
		got, err := ParseRunSortKey(tc.input)
		if tc.fails {
			if err == nil {
				t.Errorf("ParseRunSortKey(%q) = %q, want error", tc.input, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseRunSortKey(%q) = %q, %v, want %q", tc.input, got, err, tc.want)
		}
	}
}
