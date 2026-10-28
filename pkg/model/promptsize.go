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

package model

// What a dispatch's prompt costs the run it is handed to, as a number.
//
// Two of the three things a prompt is built from were already bounded
// and neither of them was bounded by measurement first. A tool result is
// capped at mcp.maxToolResultBytes and says so where it bites; the
// attempt history is capped at orchestrator.maxPreviousAttempts and says
// how many attempts it left out. The conversation was carried whole --
// every message a task had ever collected, replayed into every attempt's
// prompt -- so a task open for a week grew a prompt nobody could see the
// size of, monotonically, until it crowded out the instructions it was
// wrapped around (grain/task-268).
//
// The ceiling is not disk. It is the model's context window, and a
// prompt approaching it does not fail loudly: the run behaves worse than
// the same run did last week, on a task that has been open longer, and
// nothing in the record names the reason. So the size is measured on
// every dispatch, reported per attempt where a reader already looks
// (pkg/ui's prompt pane, read_grain_task's attempt list), and warned
// about in the daemon's own log when it crosses a fraction of the
// window -- the same shape the sandbox budget and the disk floor have,
// both of which exist because an invisible limit is worse than a low
// one.
//
// Nothing here bounds anything. The bound is one layer up, in
// orchestrator's conversation.go, which is where the text that grows is
// assembled; this is the instrument, and an instrument that could fail a
// dispatch would be worse than the thing it measures.

import "fmt"

// PromptContextTokens is the context window a prompt is measured
// against: 200,000 tokens.
//
// It is a stated assumption rather than a fact grain holds. Which window
// a run actually gets is the framework's and the provider's business --
// agent.Framework names a CLI, the CLI names a model, the model's window
// is whatever the provider says it is this month -- and none of that
// reaches a dispatch. 200,000 is the smallest window among the models
// this deployment's frameworks dispatch into today, so a prompt measured
// against it is measured against the tightest case, and a share reported
// here is an upper bound on the share the run really pays.
//
// A deployment running a model with a smaller window than this would be
// told its prompts are fine when they are not. That is the one way this
// number can lie, it is why the share is always reported *with* the
// window it was taken against (PromptSize.String), and it is the reason
// this is a named constant rather than a literal buried in a ratio: when
// it needs to become a per-framework or per-deployment setting, this is
// the thing to move.
//
// It was looked at again in grain/task-286, beside the conversation
// bound it is the denominator of, and left as a constant -- with the
// trigger for moving it and the price of doing so written down here
// rather than rediscovered:
//
// The trigger is a framework dispatching into a window under 200,000
// tokens, which for this deployment means a custom framework rather than
// one of the named ones. Nothing detects that, and nothing can: the
// window is the provider's business and never reaches a dispatch. So
// this stays a stated assumption until somebody states a different one.
//
// The price is small, deliberately. Everything downstream already reads
// the window off the measurement rather than off this constant --
// PromptSize carries ContextTokens per sample, Large scales its quarter
// to whatever window the sample was taken against, and every renderer
// prints the window beside the share -- so moving this becomes a
// question of where the value is read from (a model.Config field, a
// per-framework table) and not a refactor of the readers. What it does
// cost is a setting in grain/config as well as the field here, since a
// deployment's own configuration is exported there.
//
// And what would have to move with it is orchestrator's
// maxConversationBytes. The whole of what grain bounds comes to about
// 21,000 tokens at once (that constant's own note), which is 11% of this
// window and would be two thirds of a 32,000-token one: a narrower
// window does not just re-scale this report, it makes the conversation
// bound wrong. The two numbers are one decision.
const PromptContextTokens = 200_000

// PromptBytesPerToken is how many bytes of prompt one token is taken to
// be. Four, which is the usual rule of thumb for English prose, and
// prose with paths and code in it is a little denser than that -- so the
// estimate reads slightly low for a prompt full of diffs and slightly
// high for one that is all conversation.
//
// It is an estimate and every reader of it is told so (the "~" in
// PromptSize.String). Tokenizing properly would mean shipping a
// tokenizer per model to answer a question whose whole use is "is this
// prompt in the region where it matters?" -- a question a factor of 1.3
// does not change the answer to.
const PromptBytesPerToken = 4

// PromptWarnTokens is where a prompt stops being ordinary: a quarter of
// the window, 50,000 tokens.
//
// A quarter rather than something closer to the top because the prompt
// is turn one. Everything the run does afterwards -- every tool result,
// every file it reads, everything it writes -- shares the same window
// with it, and a run whose prompt is half the window has lost half its
// working room before its first command. A quarter is generous for a
// prompt (grain's own standing sections come to a few thousand tokens)
// and still leaves the run the great majority of the window, so a prompt
// that crosses it is one where a task's own accumulated material, not
// grain's boilerplate, is the thing that grew.
//
// Crossing it is a warning and nothing else: no dispatch is refused, no
// run is held back. The number exists so that somebody is told before a
// prompt is a problem rather than after.
const PromptWarnTokens = PromptContextTokens / 4

// PromptSize is one assembled prompt measured: how many bytes it is, how
// many tokens that is estimated to be, and the window that estimate is
// judged against.
//
// The window travels with the measurement rather than being looked up by
// each reader, so that a size recorded today still reads honestly if
// PromptContextTokens moves: a share quoted without the window it was
// taken against is a number nobody can check.
type PromptSize struct {
	Bytes           int
	EstimatedTokens int
	ContextTokens   int
}

// MeasurePrompt measures one assembled prompt.
func MeasurePrompt(prompt string) PromptSize {
	return MeasurePromptBytes(len(prompt))
}

// MeasurePromptBytes is MeasurePrompt for a caller that has the size
// without the text -- a reader asking the store for `length(prompt)`
// rather than fetching every attempt's prompt to measure it in Go.
func MeasurePromptBytes(n int) PromptSize {
	if n < 0 {
		n = 0
	}
	return PromptSize{
		Bytes:           n,
		EstimatedTokens: n / PromptBytesPerToken,
		ContextTokens:   PromptContextTokens,
	}
}

// Zero reports whether nothing was measured at all -- an attempt that
// never reached its agent, or one that ran before any of this existed.
// Such a run is not a prompt of nought bytes and must not be reported as
// one.
func (p PromptSize) Zero() bool { return p.Bytes == 0 }

// Share is the fraction of the window this prompt is estimated to
// occupy, in [0,1] for everything short of a prompt that exceeds the
// window outright. Zero when no window was recorded, which is every
// PromptSize that was never measured.
func (p PromptSize) Share() float64 {
	if p.ContextTokens <= 0 {
		return 0
	}
	return float64(p.EstimatedTokens) / float64(p.ContextTokens)
}

// Large reports whether this prompt has crossed PromptWarnTokens --
// scaled to whatever window the measurement was taken against, so the
// test is the same quarter whichever window that was.
func (p PromptSize) Large() bool {
	if p.ContextTokens <= 0 {
		return false
	}
	return p.EstimatedTokens*4 >= p.ContextTokens
}

// String is the measurement as a reader wants it: the size, the token
// estimate marked as an estimate, and the share of the window it is a
// share of -- never a bare percentage, since the window is the half of
// that number a reader cannot otherwise check.
func (p PromptSize) String() string {
	if p.Zero() {
		return "no prompt recorded"
	}
	return fmt.Sprintf("%s, ~%d tokens -- %.0f%% of the %d-token window grain measures against",
		PromptBytes(p.Bytes), p.EstimatedTokens, p.Share()*100, p.ContextTokens)
}

// PromptThreadCutNotice and PromptMessageCutNotice are how the two
// notices open that grain writes into a prompt where it has cut a task's
// conversation down to fit (orchestrator's commentThreadSection and
// elideMessageMiddle, which write the rest of each line after them).
//
// They are constants here, rather than words inside the two format
// strings that use them, because a recorded prompt is the only channel
// between a cut and a measurement of it -- exactly the position
// mcp.ResultElision is in, reading a tool's own answer back for the same
// reason. Every prompt recorded since the bound landed is at most
// maxConversationBytes of thread plus grain's own sections, so a
// distribution of prompt sizes is censored at the bound and cannot say
// which prompts were cut down to it; counting the prompts carrying one
// of these notices is the measurement that survives that
// (Store.RunPrompts, metrics.Prompts.ThreadCut).
//
// Each opens a line of its own in the prompt, and both readers require
// that -- a prompt that merely *quotes* one, because somebody pasted a
// prompt into a comment and the comment came back in a later prompt,
// does not begin a line with it unless the quote was itself unindented.
// That is the same trade mcp.ResultElision makes and states: over
// thousands of dispatches an occasional false positive costs less than
// only counting the cuts grain can prove.
const (
	PromptThreadCutNotice  = "[grain] the middle of this conversation was cut: "
	PromptMessageCutNotice = "[grain] one message in this conversation was cut: "
)

// PromptBytes renders a size the way somebody judging whether it is
// large reads it: two significant figures, no more. The twin of
// mcp.humanBytes, which does the same for a tool result's elision
// notice; this one is in model because a prompt's size is reported by
// three packages that have no business importing pkg/mcp.
func PromptBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
