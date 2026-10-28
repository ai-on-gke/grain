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

// This file is the policy behind a Framework exec'ing its CLI more than
// once for a single run, shared by every implementation that does it --
// agent/antigravity's resume.go first, and agent/claude and agent/codex
// after it. capacity.go is the other half of the same idea: it decides
// that a failure was the provider rather than the task, and this decides
// what a Framework does about it while it still has the sandbox in its
// hands.
//
// # What a transient refusal used to cost
//
// A run a provider refuses for want of capacity is classified by
// capacity.go and reported as a CapacityError, which keeps the task's
// own retry streak intact (orchestrator.RunDispatch records
// model.CapacityOutcome rather than a fifth strike). What it does not
// keep is anything else. The dispatch is over, so the sandbox is
// released (orchestrator's own deferred release in cycle.go), and the
// next attempt pays for a fresh sandbox -- a granule VM boot and a clone
// -- and starts the task from nothing. Everything the agent had already
// done and not pushed goes with the sandbox, and so does every model
// turn it spent getting there.
//
// None of that is necessary at the moment the failure happens. For the
// whole of Run the sandbox is still alive, still holding the checkout
// and whatever the agent wrote in it, and so is the private
// configuration directory the framework built for the CLI. So a Run
// backs off briefly and execs the CLI again, against that same sandbox,
// inside the runtime budget the run already had.
//
// # What is retried, and what is not
//
// Only a capacity refusal (capacity.go). That classifier is the whole
// answer to "was this the provider rather than the task", and re-running
// an agent on a failure that was really about the task would spend the
// run's budget repeating it. A quota refusal (usagelimit.go) is
// deliberately not retried either: waiting out a quota window is a
// matter of hours, which is dispatch's business and not a backoff's, and
// the pause it triggers is what keeps the rest of the queue off the same
// wall. A turn cap tripping is not a failure at all -- it is grain
// stopping the run on purpose.
//
// A framework whose CLI can be asked to pick its own conversation back up
// may go one step further, and agent/antigravity does: an attempt that
// died without reaching a verdict of its own -- agy exiting on its
// generic "Agent execution terminated due to error", a stream that
// stopped, a model turn grain cancelled for running past its deadline --
// is *resumed*, and only resumed. That is a narrower thing than retrying
// an unclassified failure: the conversation carries what the run had
// already worked out, so the next exec continues the task rather than
// repeating it, and where there is no conversation to join the run ends
// exactly as it did before. InterruptedPrompt is the turn such an
// attempt sends, and agent/antigravity's resume.go carries the argument
// for why agy needs this and the other two CLIs, so far, do not.
//
// A refusal is only ever read off an attempt that *failed*. Every marker
// list here and in capacity.go is matched against text a run produced,
// and a run that finished successfully may have spent an hour quoting CI
// logs, HTTP errors and a provider's own words back at itself. "The run
// failed" is what makes a substring match evidence rather than a
// coincidence, and it is what keeps a finished run from being run again.
//
// # One attempt here is usually several at the provider
//
// This is not the first backoff a refusal meets. A CLI retries its own
// API calls before it reports a failure at all, so an attempt from this
// policy's point of view can be ten of them from the provider's, and the
// wall clock a run really spends is the CLI's ladder plus this one's.
//
// Measured for all three, each on the argv a grain dispatch builds, and
// they are two orders of magnitude apart:
//
//   - claude 2.1.197 retries a persistent 529 ten times over 2m51s, so a
//     run refused twice sits for around six minutes inside Framework.Run
//     with nothing on its transcript
//     (docs/hand-runs/2026-09-08-claude-resume.md §5).
//   - codex 0.153.0 retries a persistent 503 thirty times over 25
//     seconds -- five requests per stream attempt, six stream attempts
//     -- so a run refused twice spends about 2m45s, of which this
//     policy's own 30 and 60 second waits are the larger half
//     (docs/hand-runs/2026-09-08-codex-retry.md).
//   - agy 1.2.2 was still retrying a persistent 503 after ten requests
//     and five minutes. Version 1.1.27 had made one request in 0.15s; the
//     change is why agent/antigravity now cancels one model turn after
//     100 seconds (docs/hand-runs/2026-09-08-agy-retry.md).
//
// Claude and codex leave their internal ladders alone; their own files
// carry the decisions on CLAUDE_CODE_MAX_RETRIES and
// request_max_retries/stream_max_retries. The shared argument is that a
// CLI's backoff is the cheaper one -- it holds only the sandbox, spends no
// dispatch and no model turn -- so absorbing short outages there keeps the
// budget below for real ones.
//
// Antigravity is now the exception. It still exposes no retry knob, and a
// five-minute silent turn multiplied by this policy's attempts is not a
// short outage anymore. Its package enforces the former probe ceiling as
// a semantic model-turn deadline, paused while a grain tool is active.
// Cancelling there does not hide the refusal: antigravity reads agy's
// completed private diagnostic, classifies it, and only then reaches this
// policy's visible 30/60-second waits.
//
// # What each framework still owns
//
// How its CLI is asked to pick a conversation back up, if it can be at
// all. agent/antigravity resumes agy's conversation by handing back the
// conversation_id agy puts on its own stream, agent/claude resumes
// claude's session by handing back the session_id claude puts on its
// own, and agent/codex resumes codex's thread by handing back the
// thread_id codex puts on its own -- which is what keeps the model turns
// already spent as well as the sandbox. None of the three rests on a
// guess: each has a live probe that measures the flag or subcommand
// against the real binary (agent/codex's is
// docs/hand-runs/2026-09-07-codex-resume.md, written down as a test that
// needs the binary and no credential), and each keeps the cheap variant
// as its fallback for a build that will not take it: see those packages'
// resume.go and retry.go.
//
// That fallback is the same in all three, and it is what every framework
// would have had on its own: the same sandbox, the prompt again,
// RestartPreamble in front of it. It needs no conversation plumbing and
// is most of the saving anyway, which is why it was worth having before
// any of the three resumes were measured, and why it is still worth
// keeping behind them.

import (
	"context"
	"fmt"
	"time"
)

const (
	// DefaultTransientRetries is how many extra execs of a CLI one run
	// may spend on transient refusals. Two, so the run survives a
	// provider blip and a second one right behind it, and stops there:
	// past that the outage is not a blip, and the task is better off
	// back in the queue where dispatch's own backoff -- minutes, not
	// seconds -- can wait it out without holding a sandbox open the
	// whole time. Two of *these*: each one sits on top of whatever the
	// CLI already retried on its own before reporting anything, which
	// for claude is ten more attempts over nearly three minutes and for
	// codex thirty over 25 seconds (the section above).
	DefaultTransientRetries = 2

	// BaseTransientBackoff is the wait before the first retry, doubling
	// for each one after it (30s, then 60s). Long enough that a provider
	// that just said "try again later" has been given a moment to mean
	// it, short enough to be noise beside the several minutes rebuilding
	// a sandbox and recloning would cost.
	BaseTransientBackoff = 30 * time.Second

	// MinimumRetryRuntime is how much of the run's own deadline has to
	// be left, over and above the backoff, for another attempt to be
	// worth starting. A retry that is cancelled a minute after it starts
	// has spent the backoff and a model call to end up exactly where it
	// began.
	MinimumRetryRuntime = 2 * time.Minute
)

// RetryPolicy is how many extra attempts one run may spend on transient
// provider refusals, and how it waits between them. The zero value
// retries nothing, which is what a Framework that has not opted in
// should get; New in each framework fills in DefaultTransientRetries.
type RetryPolicy struct {
	// Retries is the number of *extra* execs allowed, over and above the
	// first attempt.
	Retries int
	// Sleep waits out one backoff, or reports ctx's error if the run is
	// cancelled while it waits. Nil means SleepBeforeRetry; a test
	// overrides it so driving the retry loop costs the suite no wall
	// clock.
	Sleep func(context.Context, time.Duration) error
}

// WaitBeforeRetry reports whether a run whose attempt was refused for
// want of capacity should make retry number `retry` (counting from 1),
// having waited out that retry's own backoff before saying yes.
//
// Three ways it says no, and they are the three bounds this policy is:
// the retry budget is spent, the backoff plus a useful attempt would not
// fit in what is left of the run's deadline, or the run was cancelled
// during the wait.
func (p RetryPolicy) WaitBeforeRetry(ctx context.Context, retry int) bool {
	if retry < 1 || retry > p.Retries {
		return false
	}
	backoff := TransientBackoff(retry)
	if !EnoughTimeToRetry(ctx, backoff) {
		return false
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = SleepBeforeRetry
	}
	return sleep(ctx, backoff) == nil
}

// TransientBackoff is the wait before the nth retry, counting from 1.
func TransientBackoff(retry int) time.Duration {
	d := BaseTransientBackoff
	for i := 1; i < retry && d < time.Hour; i++ {
		d *= 2
	}
	return d
}

// EnoughTimeToRetry reports whether the run's own deadline leaves room
// for the backoff and an attempt after it. A context with no deadline --
// a test, or a caller that means a run to take as long as it takes --
// always has room.
func EnoughTimeToRetry(ctx context.Context, backoff time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) > backoff+MinimumRetryRuntime
}

// SleepBeforeRetry waits out the backoff, or returns ctx's error if the
// run is cancelled while it waits.
func SleepBeforeRetry(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ResumePrompt is the user turn a resumed attempt sends -- for a
// framework whose CLI can be asked to pick its own conversation back up.
// It is not the task's own prompt: the conversation it is joining
// already has that, along with everything the agent did before it was
// cut off, and repeating the prompt would read as "start again" to a
// model that had already got half way. What it needs instead is the one
// fact the conversation cannot contain -- that it was interrupted from
// outside, by something that was nothing to do with the work.
const ResumePrompt = "Your previous turn was cut short: the model provider refused the " +
	"request while you were working, and grain has restarted you on the same conversation, " +
	"in the same sandbox, with everything you had already done still in place.\n\n" +
	"Carry on from where you stopped rather than starting over. Check the state of the " +
	"sandbox first -- what you have already written, committed or pushed -- and continue " +
	"the task from there."

// InterruptedPrompt is the user turn a resumed attempt sends when the
// attempt before it was not refused by the provider but cut off some
// other way: a CLI that died mid-run, a stream that stopped without a
// verdict, a model turn grain itself cancelled for running past its
// deadline. agent/antigravity resumes all three (its resume.go says why
// agy needs it and the other two frameworks so far do not).
//
// It says what ResumePrompt says, minus the one claim that would be a
// guess. A refusal is something the provider said; an interruption is
// only ever something grain watched happen, and telling a model the
// provider refused it when nobody knows that invites it to wait, or to
// report a provider outage it has no evidence for. What it does add is
// the one instruction a stalled or crashed turn earns: make the next
// steps smaller, and get what already works pushed.
const InterruptedPrompt = "Your previous turn was cut short: the agent CLI you run in stopped " +
	"mid-run -- it failed, or it stalled long enough that grain stopped it -- and grain has " +
	"restarted you on the same conversation, in the same sandbox, with everything you had " +
	"already done still in place. Nothing about the task itself failed, and nothing you had " +
	"done was undone.\n\n" +
	"Carry on from where you stopped rather than starting over. Check the state of the sandbox " +
	"first -- what you have already written, committed or pushed -- and continue the task from " +
	"there. Work in smaller steps than the turn that was cut off: commit and push what already " +
	"works before you start anything new, so another interruption costs less than this one did."

// RestartPreamble goes in front of the task's own prompt when an attempt
// after the first has no conversation to resume: the model is being
// given the task from the top, in a sandbox that is *not* in the state a
// first attempt would find it in.
//
// Saying so is the whole of what the cheap variant needs. Without it the
// agent meets a checkout with edits, commits, maybe a pushed branch that
// it has no memory of making, and its most likely reading of that is
// somebody else's work in progress -- so it either starts again beside
// it or spends turns working out what happened. One paragraph turns that
// into the first thing it knows.
const RestartPreamble = "Before the task below: an earlier attempt at this same task was cut " +
	"short -- the model provider refused the request part-way through -- and grain has started " +
	"you again in the same sandbox, so anything that attempt had already written, committed or " +
	"pushed is still there. The conversation itself was lost, which is why you are being given " +
	"the task from the top rather than being asked to carry on. Check the state of the sandbox " +
	"before you begin -- the working tree, the branch, what has been pushed -- and continue from " +
	"what is already there instead of redoing it.\n\n"

// SuspendedPreamble goes in front of the task's own prompt when a *new
// dispatch* resumes the conversation a previous run of the same task
// left suspended (RunConfig.ResumeSession, model.SuspendedSession).
//
// It is not ResumePrompt and must not be confused with it. Those two
// send a short nudge *instead of* the prompt, because the conversation
// they rejoin is minutes old, in the same sandbox, and already holds a
// prompt that is still accurate. This one rejoins a conversation from a
// previous dispatch, which is neither: the task may have been edited, a
// human may have answered the very question the run stopped to ask, the
// attempt history has grown, and the merge queue may be asking for a
// repair. So the whole prompt is sent as well, and what this paragraph
// adds is the two things the prompt cannot say -- that the conversation
// above it is the model's own earlier work rather than someone else's,
// and that the sandbox underneath it is *not* the one that work was
// done in.
//
// The second half is the one that would otherwise cost turns. A dispatch
// builds a fresh sandbox and clones the repository again, so an agent
// resumed into its own conversation is looking at a checkout with none
// of the uncommitted edits it remembers making. Told that, it starts by
// pushing on from what is on the branch; not told, its most likely
// reading is that something ate its work.
const SuspendedPreamble = "Before the task below: you have worked on this task before. The " +
	"conversation above is your own -- grain suspended it when your previous run ended (it ran " +
	"out of time, the model provider stopped it, or it was parked waiting for a person) and has " +
	"now resumed you in it rather than starting you from nothing.\n\n" +
	"The sandbox is not the same one. This run has a new sandbox with a fresh clone, so only what " +
	"you actually committed and pushed to your branch still exists -- anything you had written " +
	"but not pushed is gone, and so is anything you installed. Start by looking at the branch and " +
	"the working tree as they are now, not as you remember leaving them.\n\n" +
	"The task is restated in full below, including anything said or decided since your last run, " +
	"and it is what is true now. Read it rather than assuming it is the brief you already have: " +
	"where it differs from what you remember, it wins. Then carry on with what is left rather " +
	"than repeating work you have already pushed.\n\n"

// SuspendedResumeRefusedNote is what a run says in its transcript when
// the session a previous run left suspended turned out not to be
// resumable after all -- the CLI refused the invocation, or no longer
// has that conversation -- and the dispatch started the task from its
// prompt instead.
//
// Worth a line rather than silence: the fallback is invisible in the
// result (the run simply works the task), and somebody reading a
// transcript wants to know whether the conversation grain recorded
// against this task is still there or has been pruned out from under it.
const SuspendedResumeRefusedNote = "! the session a previous run of this task left suspended " +
	"could not be resumed -- the CLI opened no session for it, so it has most likely been pruned " +
	"from the controller's session store -- and grain ran this task from its prompt instead."

// RetryNote is what a run says in its transcript for each attempt it
// spent, so a reader of a transcript that appears to restart half way
// through knows why it did.
func RetryNote(attempt int, resumed bool) string {
	how := "restarted it from the prompt in the same sandbox"
	if resumed {
		how = "resumed the same conversation in the same sandbox"
	}
	return fmt.Sprintf("* the provider refused this run mid-flight; attempt %d %s", attempt, how)
}
