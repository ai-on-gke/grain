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

// result_size.go bounds how much text one tool answer may carry back.
//
// Nothing used to. run_command returned the whole of stdout and stderr,
// read_file with no "limit" returned the whole file line-numbered, and
// one `go test ./...` on a failing suite, one `git log -p`, one `grep -r`
// that matched more than expected or one 250 KB README spent a large
// fraction of a run's context in a single turn -- context the run cannot
// get back, on output it did not ask for and mostly will not read.
//
// Past that point the behaviour was whichever CLI happens to be driving
// the run: claude caps an MCP tool's output itself and rejects a result
// that exceeds the cap, so an oversized answer costs the run the wall
// clock the command took and buys it nothing at all; antigravity is a
// different CLI with its own answer. So the size at which a tool result
// stopped working was a per-framework default grain had not chosen, did
// not know, and could not explain to an agent when it was hit. Capping
// here fixes it once for every framework, and the server is the only
// place that can produce the thing that makes a cut answer usable: a
// statement of what was dropped and how to get it back.
//
// Head and tail are kept, never just the head. A command's verdict is
// usually in the last lines (the failure summary, the final error) and
// its invocation in the first, and the middle is what nobody needed.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
)

// maxToolResultBytes is the most output text a single run_command or
// read_file answer carries. 16 KB, which is two things at once: what the
// window below says keeps nearly every answer whole, and antigravity's
// own per-result default -- and agy is what grain dispatches with unless
// a deployment says otherwise (model.AgentFrameworkAntigravity is
// -agent-framework's default). Past a framework's own limit a result is
// not a bigger answer, it is one the CLI truncates or refuses on its own
// terms, with none of the notice below saying what went and how to get
// it back, so there is nothing to be gained by capping above it.
//
// It replaces 64 KB, which this file called "a starting guess, not a
// measurement" and which turned out to be too high to ever fire: over
// the 90 days to 2026-09-04, across the 23 runs of this deployment that
// recorded a census, no run_command answer reached 43 KB and no
// read_file answer 45 KB, so the cap cut nothing in 1,254 calls. The
// same window is what sizes the replacement (`grain metrics -window
// 90d`, quoted in full under "No single answer may eat the run's
// context" in grain's former implementation notes):
//
//	tool          calls  mean bytes    p50      p95      p99    max
//	run_command    1162        2163  <=1023   <=8191  <=32767  43460
//	read_file        92        6736  <=4095  <=32767  <=65535  44860
//
// The percentiles are base-2 bucket bounds, not measured bytes
// (metrics.Sizes says why). Read against 16 KB: at least 95% of
// run_command answers pass whole and half of them are under 1 KB;
// read_file is the heavier of the two distributions -- its p95 sits in
// the 16-32 KB octave, so a few in twenty are cut -- and it is also the
// tool whose elision notice recovers exactly, since the line numbers
// either side of the cut are the offset and limit that fetch the missing
// range. One number rather than one per tool despite that difference:
// the framework limit this now matches is per result, so a 32 KB
// read_file answer grain let through is one agy would cut instead,
// without saying so.
//
// It bounds the command's own output, not the whole result: the exit
// line, grain's own notices and the deadline notice are added on top and
// are a few hundred bytes between them.
//
// A var, not a const. A test shrinks it rather than generating 16 KB of
// output to prove the cap works, and one deployment's distribution is
// not yet a reason to make it a stored per-deployment setting -- when a
// second deployment's numbers disagree with these, or runs a framework
// whose own limit is higher, that is the moment for a setting rather
// than now.
//
// Whoever moves it next will not have the table above to move it by: the
// sizes it is drawn from are the sizes runs received, so a cap in force
// truncates that distribution at itself, and every percentile under 16 KB
// now describes answers that fit and answers cut down to fit alike. What
// to read instead is the count of cuts the census keeps beside those
// buckets -- how often this fired, per tool, and how much the median cut
// dropped (ResultElision, and metrics.ToolUse.Cut).
//
// That count starts on 2026-09-17, which is a date rather than a
// footnote: a run that finished before it counts no cuts because it
// could not count them, and it is still in the denominator of every rate
// reported over a window containing it. So a window read across that
// date understates how often this fires, and the first honest read of it
// is a window beginning after it. 16 KB stands until there is one -- the
// first attempt at re-sizing it from cuts (grain/task-190, 2026-09-18)
// had a day and a half of them and moved nothing.
var maxToolResultBytes atomic.Int64

func init() {
	maxToolResultBytes.Store(16 << 10)
}

// maxResultTextBytes is the backstop: the most text any tool answer may
// carry back, whatever tool produced it and whether or not that tool
// bounds itself.
//
// The cap above is per-tool, and it was applied tool by tool -- both of
// run_command's streams, read_file's line range, a task record, a task's
// prompt, a transcript. Every other tool answered with whatever it
// happened to be holding, which for several of them is a great deal:
// pull_request_status and base_branch_status quote up to four failing
// jobs' logs at github.JobLogTailBytes each; wait_for_checks and
// list_ci_runs render a CI run's jobs with each one's error text under it,
// and a kontur guest that never came up puts a whole guest dump in one of
// those; list_ci_runs then does that for every run this deployment still
// remembers; read_memory_document returns a note that may be four times
// this cap (model.MaxMemoryDocumentLength); chat returns whatever a person
// typed; and a tool proxied from another MCP server (managed_http.go)
// returns whatever that server said, which grain does not write and cannot
// predict. None of those is unbounded by design. They are unbounded
// because nothing had asked them to be bounded, and each of them is one
// bad day away from spending a run's whole context in a single turn --
// the failure result_size.go was written for, arriving through a tool it
// had not reached yet.
//
// So the bound is also applied once, in the one place every tool call of
// every transport passes through (Registry.handleCall). A tool that caps
// its own output is untouched by it: this sits above maxToolResultBytes
// by a quarter, which is room for what a tool writes *around* output it
// has already capped -- run_command's exit line and its two elision
// notices, read_file's workspace-retry notice, the check list and the
// grain CI paragraph above a CI report's logs. What reaches it is an
// answer nobody had bounded at all, and for that answer a head-and-tail
// cut with a notice is strictly better than whichever CLI's own limit
// would otherwise have cut it silently.
//
// A function rather than a constant for maxAddendaNoticeBytes's reason:
// maxToolResultBytes is a var a test shrinks, and a backstop computed
// once at init would stay at its production value while the cap under it
// moved.
func maxResultTextBytes() int {
	limit := int(maxToolResultBytes.Load())
	return limit + limit/4
}

// elisionAdviceCommandOutput and elisionAdviceFileLines are the "how to
// get the rest" half of an elision notice, which is the half that decides
// whether a cut answer is usable. Each names the specific next call for
// its tool rather than saying "output was truncated" and leaving the run
// to invent one -- re-running the same command verbatim to see the middle
// is exactly the move the notice exists to prevent.
const elisionAdviceCommandOutput = "re-run it narrowed to what you need " +
	"(grep, head, tail, a quieter flag), or redirect the output to a file and " +
	"read that back with read_file's \"offset\" and \"limit\""

const elisionAdviceFileLines = "read the missing range with read_file's \"offset\" " +
	"and \"limit\" -- the line numbers either side of this notice are where it starts and ends"

// elisionAdviceGeneric is what the backstop says for a tool that named no
// advice of its own (Tool.ElisionAdvice) -- including a tool grain did not
// write, proxied in from another MCP server, whose arguments this package
// has never seen.
//
// It cannot name the next call, so it does the two things it still can:
// point at the arguments a tool of this kind usually has, and tell a run
// what to do when there are none. Saying nothing would leave the run
// re-calling the same tool for the same answer, and a run that quietly
// treats a cut answer as the whole of one is how a wrong report gets
// written from a right tool.
const elisionAdviceGeneric = "call it again for less -- a tool that can answer " +
	"this much usually takes an argument that narrows what comes back (a name, a " +
	"limit, an offset, a range) -- and if this one takes none, say in what you " +
	"report that you saw the head and the tail of this answer and not its middle"

// elideMiddle returns text unchanged when it fits in limit bytes, and
// otherwise its head and its tail with one notice between them saying how
// much was dropped and how to get it.
//
// The cut snaps to line boundaries where there are any, so neither half
// ends mid-line: a half-line of JSON or a half-written line number is
// worse than no line at all, and read_file's numbering only survives as a
// way of naming the gap if the numbers at the cut are whole.
//
// limit <= 0 disables the cap, which is what a caller with nothing left
// in its budget for this stream means -- splitResultBudget never hands
// one out, but a test shrinking maxToolResultBytes to zero would.
func elideMiddle(text string, limit int, advice string) string {
	return elideMiddleOf(text, limit, advice, 0)
}

// elideMiddleOf is elideMiddle for text that already lost unseen bytes
// in its middle before it got here: what a runner's headTailBuffer
// dropped, which the notice counts as elided along with the rest, so a
// command that printed 200 MB is not reported as having printed 64.
func elideMiddleOf(text string, limit int, advice string, unseen int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	headEnd := lineBoundaryBefore(text, limit/2)
	tailStart := lineBoundaryAfter(text, len(text)-(limit-limit/2))
	if tailStart <= headEnd {
		// One line longer than the whole budget (minified JSON, a
		// base64 blob): there is no boundary to snap the tail to
		// above the head, so keep the head and say the rest went.
		tailStart = len(text)
	}

	var b strings.Builder
	b.WriteString(text[:headEnd])
	if headEnd > 0 && !strings.HasSuffix(text[:headEnd], "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(elisionNotice(tailStart-headEnd+unseen, len(text)+unseen, advice))
	b.WriteString(text[tailStart:])
	return b.String()
}

// elisionMarker is the half of the notice below that never varies,
// whichever tool was cut and whatever advice it gave -- and the thing
// ResultElision matches an answer on when the census asks how often these
// caps actually fire (telemetry.go says why that has to be read back out
// of the text). Reworded here, both halves move together.
const elisionMarker = "elided here by grain, not by the command"

// elisionNotice is what goes where the middle was: how much grain
// dropped, how much there was, that grain dropped it rather than the tool,
// and the next call that gets it back.
//
// It always begins a line of its own (elideMiddle's caller ensures the
// newline before it), which is what lets the reader tell grain's own
// notice from an answer that merely quotes one -- a `cat` of a transcript,
// a grep over this file.
func elisionNotice(dropped, total int, advice string) string {
	return fmt.Sprintf("[grain] %s of %s %s -- a tool result is capped so one "+
		"answer cannot eat the run's context. To see it: %s.\n",
		humanBytes(dropped), humanBytes(total), elisionMarker, advice)
}

// maxEchoedArgumentBytes bounds text an error quotes back to the caller
// out of its own call. Generous for what it is for -- naming which edit
// failed -- and far below the cap, because this is the one kind of answer
// whose content the run demonstrably already has.
const maxEchoedArgumentBytes = 500

// echoedArgument is how an error quotes back text the call itself sent:
// edit_file's old_string, which is the one argument in this package that
// can be as long as a file, and which an agent generating an edit from a
// large block sometimes makes very long indeed.
//
// The head is what identifies the failed edit, and repeating the rest
// spends a run's context on text it wrote in the turn before. The notice
// says so rather than leaving a cut looking like a shorter string than
// the one that was actually searched for -- which would send the run
// hunting a whitespace difference that is not there.
func echoedArgument(s string) string {
	if len(s) <= maxEchoedArgumentBytes {
		return s
	}
	head := s[:lineBoundaryBefore(s, maxEchoedArgumentBytes)]
	return fmt.Sprintf("%s\n[grain] ...and %s more of the string you sent, which is not "+
		"repeated back to you -- you have it already. What was searched for is the whole "+
		"of it, not this head.", head, humanBytes(len(s)-len(head)))
}

// lineBoundaryBefore returns the offset just past the last newline at or
// before pos, or pos itself when there is no newline to snap to.
func lineBoundaryBefore(text string, pos int) int {
	if i := strings.LastIndexByte(text[:pos], '\n'); i >= 0 {
		return i + 1
	}
	return pos
}

// lineBoundaryAfter returns the offset just past the first newline at or
// after pos, or pos itself when there is no newline to snap to.
func lineBoundaryAfter(text string, pos int) int {
	if pos < 0 {
		pos = 0
	}
	if i := strings.IndexByte(text[pos:], '\n'); i >= 0 {
		return pos + i + 1
	}
	return pos
}

// splitResultBudget divides budget between two streams that share one
// answer -- run_command's stdout and stderr -- so that the pair of them
// is bounded rather than each being bounded separately and the answer
// being twice the cap.
//
// A stream that fits in its half keeps all of it and lends the remainder
// to the other, which is the common shape by far: a build that fails
// prints kilobytes of stderr and megabytes of stdout, or the reverse, and
// halving the big one while the small one leaves half the budget unspent
// would throw away output there was room for.
func splitResultBudget(a, b, budget int) (int, int) {
	if budget <= 0 || a+b <= budget {
		return a, b
	}
	half := budget / 2
	switch {
	case a <= half:
		return a, budget - a
	case b <= half:
		return budget - b, b
	}
	return half, budget - half
}

// humanBytes renders a size the way somebody deciding whether they need
// the missing part reads it: two significant figures, no more.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// parseHumanBytes reads humanBytes' output back, and sits here rather
// than beside its caller so that the pair cannot drift: a unit added
// above is one this switch fails on immediately, in the same file.
//
// It recovers what the sentence says rather than what was measured --
// humanBytes rounded to two figures on the way out and nothing brings
// those digits back. That is deliberate and is all the census wants: the
// dropped bytes it feeds are kept in base-2 buckets (model.SizeHistogram),
// an octave wide, and rounding to a kilobyte cannot move a sample out of
// an octave that is at least that wide. The one place it can is the MB
// form, which rounds up as well as down, so a cut a hair under a power of
// two can be counted in the bucket above it.
func parseHumanBytes(s string) (int64, bool) {
	number, unit, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseFloat(number, 64)
	if err != nil || n < 0 || math.IsInf(n, 0) {
		return 0, false
	}
	var scale float64
	switch unit {
	case "B":
		scale = 1
	case "KB":
		scale = 1 << 10
	case "MB":
		scale = 1 << 20
	default:
		return 0, false
	}
	return int64(n * scale), true
}
