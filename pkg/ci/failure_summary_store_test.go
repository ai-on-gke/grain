package ci

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A job that passed only because its failing test is quarantined, and a
// job that passed only on its retry, read back after a restart as what
// they were -- rather than as two clean passes (grain/task-1512).
func TestAStoredRunKeepsHowItsJobsPassed(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().UTC().Truncate(time.Millisecond)
	run := storedTestRun("3f2a1c", started)
	run.Jobs[0].Conclusion = ConclusionSuccess
	run.Jobs[0].Quarantined = true
	run.Jobs[0].QuarantinedTests = []string{"TestFooBar"}
	run.Jobs[0].Status = "passed (quarantined)"
	run.Jobs[0].Warning = "passed (quarantined): 1 test(s) failed but are quarantined: TestFooBar"
	run.Jobs[0].FailureSummary = &FailureSummary{Tests: []TestFailure{
		{Name: "TestFooBar", Location: "foo_test.go:7", Message: "boom", Quarantined: true},
	}}

	flaky := run.Jobs[0]
	flaky.JobID, flaky.Name, flaky.Key = "e2e", "e2e", "job-e2e"
	flaky.Quarantined, flaky.QuarantinedTests = false, nil
	flaky.Flake, flaky.Retried, flaky.RetryCount = true, true, 1
	flaky.RetryReason = `step "go test": --- FAIL: TestFlaky`
	flaky.Status = "passed (retry)"
	flaky.Warning = "passed (retry): flaky failure on initial attempt: " + flaky.RetryReason
	flaky.FailureSummary = &FailureSummary{Tests: []TestFailure{
		{Name: "TestFlaky", Location: "flaky_test.go:12", Message: "timed out"},
	}}
	run.Jobs = append(run.Jobs, flaky)
	run.Quarantined = true
	run.Conclusion = ConclusionSuccess
	run.FinishedAt = timePtr(started.Add(time.Minute))
	storedTestStore(t, dir, StoredRunStoreOptions{}).Put(run)

	restarted := storedTestStore(t, dir, StoredRunStoreOptions{})
	got, ok := restarted.Get("3f2a1c")
	if !ok || len(got.Jobs) != 2 {
		t.Fatalf("the run came back as %+v (%v)", got, ok)
	}
	outcome := func(j JobRun) JobRun {
		return JobRun{Flake: j.Flake, Quarantined: j.Quarantined, QuarantinedTests: j.QuarantinedTests,
			Status: j.Status, Warning: j.Warning, FailureSummary: j.FailureSummary,
			Retried: j.Retried, RetryCount: j.RetryCount, RetryReason: j.RetryReason}
	}
	for i := range run.Jobs {
		if want, have := outcome(run.Jobs[i]), outcome(got.Jobs[i]); !reflect.DeepEqual(have, want) {
			t.Errorf("job %s came back as %+v, want %+v", run.Jobs[i].JobID, have, want)
		}
	}
	if !got.Quarantined {
		t.Error("a run with a quarantined job came back as not quarantined")
	}
	// The listing is what query_test_history reads, and it is the
	// flake it is there to count.
	report := QueryTestHistory(restarted.List(), "TestFlaky")
	if report.Flaked != 1 || report.Failed != 0 {
		t.Errorf("query_test_history over the stored run: %+v", report)
	}
	// And the quarantined failure is still one, counted as such rather
	// than as a failure of the job it did not fail (grain/task-1572).
	report = QueryTestHistory(restarted.List(), "TestFooBar")
	if report.Quarantined != 1 || report.Failed != 0 {
		t.Errorf("query_test_history over the stored quarantined run: %+v", report)
	}
}

// A summary too big to keep entire keeps its tests first, and says how
// many entries it left out.
func TestAStoredFailureSummaryIsBounded(t *testing.T) {
	fs := &FailureSummary{RawErrors: []string{strings.Repeat("panic ", 2000)}}
	for i := 0; i < 400; i++ {
		fs.Tests = append(fs.Tests, TestFailure{
			Name: fmt.Sprintf("TestNumber%03d", i), Message: strings.Repeat("x", 100),
		})
	}
	stored := storedFailureSummary(fs)
	if len(stored) > MaxStoredFailureSummary {
		t.Fatalf("stored %d bytes, over the %d bound", len(stored), MaxStoredFailureSummary)
	}
	got := failureSummaryOf(stored)
	if got == nil || len(got.Tests) == 0 || got.Tests[0].Name != "TestNumber000" {
		t.Fatalf("the bounded summary lost its first tests: %+v", got)
	}
	if len(got.Tests) == len(fs.Tests) {
		t.Fatalf("all %d tests fit, so this test bounds nothing", len(got.Tests))
	}
	last := got.RawErrors[len(got.RawErrors)-1]
	want := fmt.Sprintf("%d more failure(s) were not stored", len(fs.Tests)-len(got.Tests)+1)
	if !strings.Contains(last, want) {
		t.Errorf("the note on what was dropped reads %q, want it to say %q", last, want)
	}

	// And one that fits comes back exactly as it went in.
	small := &FailureSummary{Tests: []TestFailure{{Name: "TestOne", Quarantined: true}},
		Diagnostics: []Diagnostic{{File: "a.go", Line: 3, Message: "undefined: x"}}}
	b, _ := json.Marshal(small)
	if s := storedFailureSummary(small); s != string(b) {
		t.Errorf("a small summary was stored as %s, want %s", s, b)
	}
	if storedFailureSummary(nil) != "" || failureSummaryOf("") != nil {
		t.Error("no summary did not round-trip as no summary")
	}
	// A column this package did not write reads as no summary, rather
	// than failing the read of the run it belongs to.
	for _, column := range []string{"not json", "{}", `{"tests":"TestOne"}`} {
		if got := failureSummaryOf(column); got != nil {
			t.Errorf("a failure_summary column of %q read back as %+v, want none", column, got)
		}
	}
}

// The base a job's verdict was against survives the store, so run
// history can say which base a cached or fresh failure was judged on
// (ci.JobRun.BaseCommit), and Find returns it as Get does.
func TestAStoredRunKeepsTheBaseItsJobsRanAgainst(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().UTC().Truncate(time.Millisecond)
	run := storedTestRun("b45e01", started)
	run.Jobs[0].BaseCommit = "c0ffee1234"
	run.Conclusion = ConclusionFailure
	run.FinishedAt = timePtr(started.Add(time.Minute))
	storedTestStore(t, dir, StoredRunStoreOptions{}).Put(run)

	restarted := storedTestStore(t, dir, StoredRunStoreOptions{})
	got, ok := restarted.Get("b45e01")
	if !ok || len(got.Jobs) == 0 {
		t.Fatalf("the run came back as %+v (%v)", got, ok)
	}
	if got.Jobs[0].BaseCommit != "c0ffee1234" {
		t.Errorf("Get: job base commit came back as %q, want c0ffee1234", got.Jobs[0].BaseCommit)
	}
	page := restarted.Find(RunFilter{}, 10)
	if len(page.Runs) != 1 || len(page.Runs[0].Jobs) == 0 {
		t.Fatalf("Find returned %+v", page)
	}
	if page.Runs[0].Jobs[0].BaseCommit != "c0ffee1234" {
		t.Errorf("Find: job base commit came back as %q, want c0ffee1234", page.Runs[0].Jobs[0].BaseCommit)
	}
}
