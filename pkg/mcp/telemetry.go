package mcp

// Reading a tool's own answer back, for the run census.
//
// orchestrator records what each run did with its tools
// (model.RunTelemetry), and three of the facts worth recording are only
// visible in the text a tool returned: whether a run_command was ended by
// its bound rather than by the command, which of its four verdicts a
// wait_for_checks call came back with, and whether grain shortened the
// answer to fit the result cap. None of them is in the tool's name, its
// arguments or its error flag -- a bounded-out command is an error like
// any other, every wait_for_checks answer is a success, and a cut answer
// is recorded at the size it was cut to.
//
// The readers live here, beside the writers, because that is the only
// arrangement in which they cannot drift apart. Each matches a marker
// that the sentence it is reading is *built from*, so a reworded report
// changes the marker and both halves move together; result_size.go's cap
// and run_deadline.go's notice may be appended to the same text without
// disturbing either. What a reader cannot recognise it reports as
// nothing at all rather than as a guess -- the same rule pkg/metrics
// holds to for a moment that was never recorded.

import (
	"strings"
	"time"
)

// The four verdicts a wait_for_checks call can end in, as the words
// task_run_check_wait.verdict stores. They are this package's vocabulary
// rather than the store's: what a wait can conclude is a fact about this
// tool, and a fifth verdict would be added here first.
const (
	// WaitVerdictFailed: at least one check failed, and the wait
	// returned without waiting for the rest.
	WaitVerdictFailed = "failed"
	// WaitVerdictPassed: every check finished and none failed. The one
	// green light.
	WaitVerdictPassed = "passed"
	// WaitVerdictTimedOut: checks were still running when the wait's own
	// clock ran out. A deployment where most waits end this way has
	// DefaultWaitForChecksTimeout set wrong for its CI.
	WaitVerdictTimedOut = "timed_out"
	// WaitVerdictNoChecks: GitHub reported no checks at all against the
	// commit -- either this repo has no CI, or nothing had registered any
	// yet.
	WaitVerdictNoChecks = "no_checks"
)

// CheckWait is one wait_for_checks answer, read back off its own text.
//
// Waited is how long the call blocked; WaitedKnown is false when the
// header it comes from could not be parsed, which is the difference
// between "this wait took no time" and "nothing here says how long it
// took". A caller that stores it anyway (orchestrator does, as zero) is
// safe: pkg/metrics drops a non-positive duration rather than counting it
// as a zero-second wait.
type CheckWait struct {
	Verdict     string
	Waited      time.Duration
	WaitedKnown bool
}

// ReadCheckWait reads a wait_for_checks result back as the verdict it
// reported, and reports whether the text was a verdict at all.
//
// Not every answer is one: a wait against a branch that has not been
// pushed yet, and a wait that could not read CI at all, are both real
// answers to the agent and neither is a verdict about a build. Those
// return false and are counted nowhere, since folding them into any of
// the four would misreport a run that had not pushed as a run whose CI
// said something.
//
// The order the markers are tried in matters for exactly one of them:
// only the failed report carries text grain did not write (each failing
// job's own log excerpt, failing_job_logs.go), so it is matched first and
// a log that happens to quote another verdict's wording cannot displace
// it.
func ReadCheckWait(text string) (CheckWait, bool) {
	var verdict string
	switch {
	case strings.Contains(text, waitFailedMarker):
		verdict = WaitVerdictFailed
	case strings.Contains(text, waitNoChecksMarker):
		verdict = WaitVerdictNoChecks
	case strings.Contains(text, waitTimedOutMarker):
		verdict = WaitVerdictTimedOut
	case strings.Contains(text, waitPassedMarker):
		verdict = WaitVerdictPassed
	default:
		return CheckWait{}, false
	}
	waited, known := waitedFrom(text)
	return CheckWait{Verdict: verdict, Waited: waited, WaitedKnown: known}, true
}

// waitedFrom reads the "Waited 4m12s for CI on ..." header every report
// opens with. The clamp note waitForChecksTimeout can put in front of it
// is why this searches rather than reading a prefix.
func waitedFrom(text string) (time.Duration, bool) {
	start := strings.Index(text, waitHeaderPrefix)
	if start < 0 {
		return 0, false
	}
	rest := text[start+len(waitHeaderPrefix):]
	end := strings.Index(rest, waitHeaderSuffix)
	if end < 0 {
		return 0, false
	}
	waited, err := time.ParseDuration(rest[:end])
	if err != nil || waited < 0 {
		return 0, false
	}
	return waited, true
}

// RunCommandTimedOut reports whether a run_command answer is one the
// bound ended rather than the command -- runCommandBound.timedOutNotice,
// which both transports append (a local context deadline, or the guest's
// own `timeout` exiting 124).
//
// The other two notices a bound can produce are deliberately not counted
// here. killedNotice's exit 137 is hedged on purpose -- 128+SIGKILL is
// also what the kernel's OOM killer leaves behind -- and
// transportStalledNotice is grain giving up on a guest that never
// answered, which is a sandbox that stopped talking rather than a command
// that ran long. Counting either as a timeout would put a number on the
// timeout rate that is not one.
//
// Only the trailing paragraphs are searched, because that is where grain
// appends its own notices: a command whose *output* happens to contain
// this sentence -- a run reading back an earlier result, a grep over this
// file -- is not a command that timed out.
func RunCommandTimedOut(text string) bool {
	for _, para := range trailingParagraphs(text, 3) {
		// Trimmed because a stream that ended in its own newline leaves
		// the notice's paragraph starting with one.
		if strings.HasPrefix(strings.TrimSpace(para), runCommandKilledMarker) {
			return true
		}
	}
	return false
}

// ResultElision reports whether grain cut the middle out of this answer,
// and how many bytes it dropped doing so -- summed over every cut in the
// answer, since one result can carry several (run_command's two streams,
// a CI report's several job logs) and the question the census asks is how
// much of *this answer* the run never saw.
//
// It exists because the caps result_size.go applies censor the very
// distribution that sized them. Every census row records how big a tool's
// answers were, and after the cap every one of those answers is at or
// under it: "p95 <= 16383" stopped telling a tool whose answers fit apart
// from one whose answers are all being cut. Counting the cuts is the
// measurement that survives capping, and the next person to move
// maxToolResultBytes needs it rather than a distribution that stops at the
// cap.
//
// Read off the text for the reason RunCommandTimedOut is: the census is
// built from agent.Result, which each framework's transcript parser fills
// in from what the CLI recorded of the answer, so the answer itself is the
// only channel between a cut here and a row in pkg/metrics.
//
// Only a notice starting a line of its own counts, which is what keeps an
// answer that *quotes* a cut -- a run reading an earlier result back out
// of a file, a grep over this package -- from being counted as one. A
// command that prints an unindented copy at the start of a line would
// still be miscounted, and that is the trade: a measurement over thousands
// of calls can afford the odd false positive far better than it can afford
// only counting the cuts grain can prove.
//
// A notice whose size cannot be read back (a build whose humanBytes says
// something parseHumanBytes does not know) still counts as a cut with
// nothing added to the bytes -- the same "report what was seen, never a
// guess" rule the readers above hold to, and why pkg/metrics reports cut
// calls next to the bytes rather than deriving one from the other.
func ResultElision(text string) (dropped int64, cut bool) {
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(line, elisionNoticePrefix)
		if !ok || !strings.Contains(rest, elisionMarker) {
			continue
		}
		cut = true
		size, _, ok := strings.Cut(rest, " of ")
		if !ok {
			continue
		}
		if n, ok := parseHumanBytes(size); ok {
			dropped += n
		}
	}
	return dropped, cut
}

// elisionNoticePrefix is how every notice grain writes into an answer
// opens, elisionNotice's included.
const elisionNoticePrefix = "[grain] "

// trailingParagraphs is the last n blank-line-separated blocks of text.
// Three is enough for every answer grain builds: the bound's own notice,
// the run-deadline notice that may follow it (withDeadlineNotice), and
// the addenda notice that may follow that one (addenda.go). Each of the
// two notices is a single paragraph by construction -- addendaNotice
// prefixes every line of a person's comment, blank lines included,
// precisely so that no amount of typing can push a command's own verdict
// out of this window.
func trailingParagraphs(text string, n int) []string {
	paras := strings.Split(text, "\n\n")
	if len(paras) > n {
		paras = paras[len(paras)-n:]
	}
	return paras
}
