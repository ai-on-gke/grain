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

package agent

// The instrument for the two files either side of this one.
//
// usagelimit.go and capacity.go both work by matching phrases in text a
// provider wrote and grain does not control: "overloaded_error", "api
// error: 529", "503 service unavailable", "Claude AI usage limit
// reached". Both lists are deliberately tight, for the reason each says
// -- a marker loose enough to appear in a repository's own files would
// mark ordinary failures as the provider's doing -- and both are matched
// against somebody else's product, which is reworded without telling
// anyone.
//
// The failure that follows a rewording is silent by construction. No
// marker matches, the run is recorded as an ordinary failure, and
// nothing anywhere says that a phrase grain relies on has stopped
// existing: every test still passes, because every test asserts against
// the wording as it was on the day it was written. What an operator sees
// is tasks failing normally, and five normal failures in a row retire a
// task that was never wrong (model.MaxConsecutiveFailures).
//
// So the answer cannot be another fixture. It has to be a record of what
// grain could not classify, kept where a week of it can be read at once:
// orchestrator.RunDispatch stores the tail of every failure that matched
// neither list (model.UnmatchedFailure), and the two functions here are
// what it stores -- how much of the text to keep, and whether the text
// looks like a provider refusing a run rather than a run failing.
//
// The looseness here is the opposite of the looseness next door, on
// purpose. A false positive in capacity.go changes a run's outcome and
// spares a task a strike it may have earned; a false positive here adds
// a line to a log an operator reads deliberately, and a hedged sentence
// to a run that has already failed. Nothing branches on it. That is what
// lets ResemblesRefusal match the words a rewording would most likely
// still contain, where mentionsCapacity may not.

import "strings"

// MaxUnmatchedFailureText is the ceiling on the text kept for one
// unmatched failure -- a bound stated here rather than at the database,
// so the cap is a property of the record and not of the column it lands
// in.
//
// This is model output, and model output has no length: a failed run's
// error can be a sentence, or a JSON body, or a wall of a provider's
// prose. Bigger than TrimLimitMessage's own 240, because that bound is
// sized for a column `grain get` prints inline next to a task, and this
// one is sized for the question "what did the provider actually say?" --
// which a sentence cut in half does not answer. Small enough that a
// week of a busy deployment's failures is still a report rather than a
// download.
const MaxUnmatchedFailureText = 1000

// UnmatchedFailureTail is the part of a failed run's own account worth
// keeping: the last MaxUnmatchedFailureText bytes of it, on one line,
// with a leading ellipsis when anything was dropped so a reader can see
// that they are holding the end of something longer.
//
// The tail rather than the head because the end is where a refusal
// lands. What this is given is a Framework's error for a run that failed
// (agent.RunFailure's own composition: what the stream reported, then
// the exit status behind it), and a provider that refuses mid-run says
// so last -- the beginning of a long error is the CLI's own preamble and
// whatever the run was doing when it was refused.
//
// The bound is in bytes, since that is what a column and a report body
// are measured in, but a cut taken at a byte offset would leave half a
// multi-byte character at the front of what is kept. The loop below
// steps forward to the next rune boundary instead, so what is stored is
// always valid UTF-8 and renders anywhere.
func UnmatchedFailureTail(text string) string {
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	if len(text) <= MaxUnmatchedFailureText {
		return text
	}
	cut := text[len(text)-MaxUnmatchedFailureText:]
	for len(cut) > 0 && !isRuneStart(cut[0]) {
		cut = cut[1:]
	}
	return "..." + cut
}

// isRuneStart reports whether b begins a UTF-8 encoding rather than
// continuing one -- a continuation byte is 10xxxxxx.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// headAtRune is s cut to at most n bytes, moved back to a rune boundary
// so the cut never leaves half a multi-byte character at the end -- the
// head-keeping counterpart of UnmatchedFailureTail's own loop.
func headAtRune(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// refusalHints are the words a provider uses when it is the provider's
// fault, matched against a lowercased haystack.
//
// Every one of them would be reckless in capacity.go and is safe here,
// and the difference is not the words but the haystack. mentionsCapacity
// reads a whole run's stream -- an hour of a repository's files, diffs
// and test output, where "unavailable" and "503" are things a source
// file says -- and its verdict spares the task a failure. This reads one
// failure's own error text, which is at most MaxUnmatchedFailureText
// bytes written by the CLI about why it stopped, and its verdict only
// decides whether an operator's eye is drawn to a line in a log.
//
// Bare status codes are in the list for that reason. "429" alone is the
// thing usagelimit.go refuses to match, and matching it here is not a
// disagreement with that: there it would pause a whole deployment, and
// here it flags one already-failed run as worth a look.
var refusalHints = []string{
	"overload", "capacity", "unavailable", "quota", "rate limit", "rate-limit",
	"too many requests", "try again", "retry", "temporarily", "upstream", "gateway",
	"server error", "api error", "429", "500", "502", "503", "504", "529",
}

// ResemblesRefusal reports whether text reads like a provider refusing a
// run rather than a run failing on its own terms -- the loose question,
// asked of a failure that has already been put to the strict ones in
// usagelimit.go and capacity.go and matched neither.
//
// A true answer is not a claim that the provider refused anything. It
// means only "somebody reading a week of these should start here": the
// most likely shape of a marker that has stopped matching is a refusal
// that still says "overloaded" or still carries a 5xx, worded just
// differently enough that a tight marker misses it.
func ResemblesRefusal(text string) bool {
	lower := strings.ToLower(text)
	for _, hint := range refusalHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}
