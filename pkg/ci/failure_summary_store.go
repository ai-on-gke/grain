package ci

import (
	"encoding/json"
	"fmt"
)

// MaxStoredFailureSummary is how much of a job's FailureSummary a
// StoredRunStore keeps, as the JSON it is stored as.
//
// The summary is the structured half of what a red step printed -- the
// failing tests by name, the compiler's file:line diagnostics -- and it
// is what ci.QueryTestHistory counts failures and flakes from once a run
// has been read back out of the store. It is kept with the run's record
// rather than with its output, so it outlives the output's sweep and is
// what still answers "did TestFoo fail last week" after the log is gone.
// That is why it is bounded far tighter than a step's output
// (MaxStoredStepOutput): it is paid for on every run for as long as the
// record lives (DefaultStoredRunHistory), not for a fortnight.
//
// 16KiB is a hundred-odd failing tests with their messages, which is
// already more than anybody reads of one job.
const MaxStoredFailureSummary = 16 << 10

// maxStoredFailureText bounds any one message, diff or raw error inside a
// stored summary, so a single enormous panic or diff cannot spend the
// whole of MaxStoredFailureSummary and push every test name out of it.
const maxStoredFailureText = 1 << 10

// storedFailureSummary is fs as the JSON a model.CIJob keeps, bounded to
// MaxStoredFailureSummary. What does not fit is dropped whole, entry by
// entry, in the order a reader can best spare it: raw errors first, then
// diagnostics, and failing tests last, since a test's name is what the
// history of it is looked up by. The drop is said in the summary itself,
// as one more raw error, rather than left for a reader to wonder about.
func storedFailureSummary(fs *FailureSummary) string {
	if fs.Empty() {
		return ""
	}
	bounded := FailureSummary{}
	for _, t := range fs.Tests {
		t.Message = TruncateOutputTo(t.Message, maxStoredFailureText)
		t.Diff = TruncateOutputTo(t.Diff, maxStoredFailureText)
		bounded.Tests = append(bounded.Tests, t)
	}
	for _, d := range fs.Diagnostics {
		d.Message = TruncateOutputTo(d.Message, maxStoredFailureText)
		bounded.Diagnostics = append(bounded.Diagnostics, d)
	}
	for _, r := range fs.RawErrors {
		bounded.RawErrors = append(bounded.RawErrors, TruncateOutputTo(r, maxStoredFailureText))
	}
	if b, err := json.Marshal(bounded); err == nil && len(b) <= MaxStoredFailureSummary {
		return string(b)
	}

	// Over the bound: keep entries, tests first, while each one's own
	// JSON (and the comma before it) still fits, leaving room for the
	// note that says how many were left out.
	const reserve = 256
	budget := MaxStoredFailureSummary - reserve
	size := func(v any) int {
		b, _ := json.Marshal(v)
		return len(b) + 1
	}
	kept := FailureSummary{}
	dropped := 0
	for _, t := range bounded.Tests {
		if n := size(t); n <= budget {
			kept.Tests = append(kept.Tests, t)
			budget -= n
		} else {
			dropped++
		}
	}
	for _, d := range bounded.Diagnostics {
		if n := size(d); n <= budget {
			kept.Diagnostics = append(kept.Diagnostics, d)
			budget -= n
		} else {
			dropped++
		}
	}
	for _, r := range bounded.RawErrors {
		if n := size(r); n <= budget {
			kept.RawErrors = append(kept.RawErrors, r)
			budget -= n
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		kept.RawErrors = append(kept.RawErrors, fmt.Sprintf(
			"[grain ci] %d more failure(s) were not stored: a job's stored failure summary is bounded to %s",
			dropped, humanBytes(MaxStoredFailureSummary)))
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return ""
	}
	return string(b)
}

// failureSummaryOf reads storedFailureSummary back. A column that does
// not parse -- nothing this package wrote -- reads as no summary rather
// than failing the read of the run around it.
func failureSummaryOf(stored string) *FailureSummary {
	if stored == "" {
		return nil
	}
	var fs FailureSummary
	if err := json.Unmarshal([]byte(stored), &fs); err != nil || fs.Empty() {
		return nil
	}
	return &fs
}

// prepareJobAttemptsForStore prepares a job's attempts for storage, cutting
// step outputs and GoTestLog to MaxStoredStepOutput and omitting duplicate
// step slices for the final attempt (whose steps are stored in ci_step).
// Returns nil for a single-attempt job that carries no GoTestLog so single-
// attempt runs cost zero extra bytes in ci_job.attempts.
func prepareJobAttemptsForStore(job JobRun) []JobAttempt {
	hasGoTestLog := job.GoTestLog != ""
	for _, a := range job.Attempts {
		if a.GoTestLog != "" {
			hasGoTestLog = true
			break
		}
	}
	if len(job.Attempts) <= 1 && !job.Retried && job.RetryCount == 0 && !hasGoTestLog {
		return nil
	}
	attempts := job.EffectiveAttempts()
	if len(attempts) == 0 {
		return nil
	}
	expectedFinal := 1 + job.RetryCount
	if job.Retried && expectedFinal < 2 {
		expectedFinal = 2
	}
	out := make([]JobAttempt, len(attempts))
	for i, a := range attempts {
		a.FailureSummary = failureSummaryOf(storedFailureSummary(a.FailureSummary))
		if a.GoTestLog != "" {
			a.GoTestLog = TruncateOutputTo(a.GoTestLog, MaxStoredStepOutput)
		}
		isFinal := (i == len(attempts)-1) && (a.Attempt == expectedFinal) && (job.Conclusion.Done() || !job.Retried)
		if isFinal && len(job.Steps) > 0 {
			// The final attempt's steps are stored in ci_step and restored
			// on read by runOf, avoiding duplicate storage in ci_job.attempts.
			a.Steps = nil
		} else if len(a.Steps) > 0 {
			steps := make([]StepRun, len(a.Steps))
			for j, s := range a.Steps {
				s.Output = TruncateOutputTo(s.Output, MaxStoredStepOutput)
				steps[j] = s
			}
			a.Steps = steps
		}
		out[i] = a
	}
	return out
}

func encodeStoredAttempts(attempts []JobAttempt) string {
	if len(attempts) == 0 {
		return ""
	}
	b, err := json.Marshal(attempts)
	if err != nil {
		return ""
	}
	return string(b)
}

func jobAttemptsOf(stored string) []JobAttempt {
	if stored == "" {
		return nil
	}
	var attempts []JobAttempt
	if err := json.Unmarshal([]byte(stored), &attempts); err != nil || len(attempts) == 0 {
		return nil
	}
	return attempts
}
