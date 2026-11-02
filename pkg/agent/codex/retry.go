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

package codex

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// This file is what codex adds to the retry policy every framework
// shares.
//
// agent/retry.go has the policy itself and the reasoning behind it: a
// run the provider refused mid-flight -- a 503 from OpenAI's own edge, a
// gateway in front of it that gave up -- costs a dispatch, a granule VM
// boot, a fresh clone and everything the agent had written but not
// pushed, and none of that has to be spent at the moment the refusal
// arrives, because Framework.Run is still holding the sandbox. So it
// backs off and execs codex again against that same sandbox, with the
// same private CODEX_HOME, inside the runtime budget the run already
// had.
//
// # Resuming the thread, rather than starting the task again
//
// What codex adds on top is the resume. For the whole of Run the private
// CODEX_HOME writeConfig built is still there, and that directory is
// where codex records its threads, so the second exec can rejoin the
// first one's thread instead of starting the task from its prompt --
// which is what keeps the model turns already spent, on top of the
// sandbox that every framework's retry keeps.
//
// # The same thread, one dispatch later
//
// That the thread lives in the home also makes it last exactly as long
// as the home does, and both ends of this file now depend on which of
// the two homes a run was given. A run whose task has a session store
// (WithSessionStore, pkg/agent/sessionstore) works in *that task's own*
// CODEX_HOME, kept between dispatches, so the thread its last run ended
// in is still in the directory this run is starting in. That run's first
// exec resumes it (agent.RunConfig.ResumeSession) and the run hands back
// the thread it ends in (agent.Result.ResumableSession) for the next one
// to do the same. What it recovers is everything the earlier run had
// read, run and worked out before it ran out of wall clock, met the
// account's usage limit or stopped to ask a person something.
//
// A run without one -- no store configured, no task id, or a second run
// of a task whose store somebody else is holding -- is this file exactly
// as it was: a temp home deleted as Run returns, no resume asked for on
// the first exec, and no thread reported back. That last part is not a
// detail. A thread id from a deleted directory buys the next dispatch a
// refused exec and shows a person a resume token that resumes nothing,
// so attemptSetup.keepThread gates the report on the home outliving the
// run, and Framework.Run gates the resume on the same fact.
//
// Two things differ from the in-run resume, and both are in the switch
// in runAttempts. The turn sent is the whole prompt behind
// agent.SuspendedPreamble rather than agent.ResumePrompt's nudge: the
// task may have been edited, a human may have answered the question the
// run stopped on, and the sandbox is a new one with a fresh clone, none
// of which the conversation can know. And a refused resume is read
// differently, since a thread from a previous dispatch is one grain's
// own pruning may since have removed -- see the fallback below, which
// drops the thread without dropping the invocation.
//
// This is the half agent/antigravity's resume.go has had since task 200
// and this package did not: task 220 gave codex the retry with the
// *cheap* variant of the second attempt -- the task's prompt again with
// agent.RestartPreamble in front of it -- because nothing here had
// measured codex's resume, and a resume built on a guess about the argv
// would have been first exercised in the middle of a provider outage.
//
// # What the measurement said
//
// It has now been taken, by hand, against the codex the image actually
// carries (codex-cli 0.153.0, the Dockerfile's CODEX_CLI_VERSION pin).
// the 2026-09-07 codex-resume hand run is the transcript; the five
// facts this file rests on are:
//
//   - `codex exec resume [OPTIONS] [SESSION_ID] [PROMPT]` is a real
//     subcommand, and it takes every flag this package passes: --json,
//     --skip-git-repo-check, --model, and the bare "-" that means "read
//     the prompt from stdin". Flags may precede the positional id.
//   - it resolves the thread out of $CODEX_HOME and nothing else, so a
//     home built for one run -- which is all this package ever builds --
//     is exactly the right place to resume from. The working directory
//     the resumed exec is started in does not enter into it.
//   - `thread.started` carries the thread_id, and is the first line of
//     every exec's stream, emitted before codex authenticates. So the id
//     to resume is one this package already parses out of a stream it
//     already reads (transcript.go).
//   - a resumed exec replays the whole earlier conversation to the model
//     -- the first attempt's prompt, its assistant turns, its tool calls
//     -- and appends the new prompt after them. Including when the
//     earlier turn *failed*: a thread whose only turn the provider
//     refused still carries that turn's prompt on resume, which is the
//     exact case this retry loop exists for.
//   - a resume codex will not perform writes nothing to stdout at all.
//     An argv it does not recognize is a clap error on stderr and exit
//     2; a thread id it has no rollout for is "no rollout found for
//     thread id ..." and exit 1. Neither emits `thread.started`, which
//     is what makes "no thread event" a sound test for "codex refused
//     this invocation" below.
//
// The first four are pinned by TestLiveCodexResumesAThreadAcrossExecs
// and the last by TestLiveCodexRefusesAThreadItDoesNotHave, both nightly
// and both against the installed binary rather than a fixture, so a
// codex release that takes one of them back says so on the first night
// instead of during an outage.
//
// # The fallback, and why it stays
//
// The last of those is what resumeRefusedNote and the resumable flag are
// built on, and they stay in spite of the measurement rather than
// because it was inconclusive. What was measured is one pinned build on
// one machine; the binary a deployment runs is whatever its image
// carries, and codex has already changed its event vocabulary once
// underneath this package. So an attempt that asked to resume and died
// without codex opening a thread at all is read as the binary refusing
// the argv rather than as anything about this run: Run drops the flag
// for good, retries from scratch with the original prompt in the same
// sandbox, and leaves a note in the transcript saying it happened. It
// costs one process start of a couple of seconds, and it means a codex
// that stops supporting this degrades to "retry in place" -- the variant
// this package ran on until now -- instead of turning a provider outage
// into a hard failure.
//
// # What proves the loop, and not just its parts
//
// Both halves above are measured against a real codex rather than
// asserted, and so is the loop that chooses between them.
// TestLiveCodexResumesAThreadAcrossExecs execs codex by hand and settles
// the argv; retry_test.go settles what runAttempts decides, against a
// scripted runner that speaks whichever vocabulary this repository wrote
// down. live_retry_loop_test.go is the join: a stub of the responses API
// inside the test process refuses a turn on demand, so Framework.Run
// takes its real capacity-refusal path against a real binary -- and the
// fallback is driven the same way, with an invocation this codex will
// not perform in place of the resume. Neither costs a credential.
//
// # A refusal reaches this loop late, and grain leaves it that way
//
// codex retries a refused API call itself, inside the exec, before it
// reports anything. Measured on 0.153.0 with the argv above and the
// config writeConfig writes, against a stub answering every request with
// OpenAI's own 503: thirty requests over 25 seconds before the
// `turn.failed` event capacity.go matches even exists. Thirty is two
// ladders multiplied -- five requests per attempt (request_max_retries,
// default 4, backing off 0.2s, 0.4s, 0.8s, 1.6s) times six attempts
// (stream_max_retries, default 5, the "Reconnecting... n/5" events this
// package's own stream carries), each attempt starting a little later
// than the last. So a run refused twice spends about 2m45s inside
// Framework.Run, of which only 75 seconds is codex's: agent.RetryPolicy's
// own 30 and 60 are the larger half, and unlike codex's they are
// narrated in the transcript.
//
// That is the vendor's default rather than anything grain chose, so task
// 418 asked whether a run should bound it -- as task 410 asked for
// claude, which retries the same refusal ten times over 2m51s. The
// answer is the same, no, and for a stronger reason: for codex there is
// no cheap way to do it.
//
//   - There is no environment variable. codex reads request_max_retries
//     and stream_max_retries out of a [model_providers.<id>] table and
//     nowhere else; both were measured to take 0, on the table and on
//     the command line (-c model_providers.<id>.request_max_retries=0),
//     and both bound exactly the ladder they name.
//   - But the provider a grain run uses is codex's own built-in
//     "openai", since configTOML sets no model_provider, and codex
//     0.153 refuses to override a single key on a built-in one: "Error
//     loading config.toml: model_providers contains reserved built-in
//     provider IDs: `openai`. Built-in providers cannot be overridden."
//     Bounding the retry therefore means this package declaring a whole
//     provider of its own -- a base URL, a wire API, an auth key --
//     which is to say owning OpenAI's endpoint and the auth wiring
//     around it, freezing a URL the vendor moves and breaking every
//     deployment whose codex authenticates as anything but an
//     OPENAI_API_KEY. That is a great deal to take on to save 25
//     seconds.
//   - And 25 seconds is not worth saving. It is the cheapest backoff in
//     this stack -- it holds the sandbox and spends nothing else: no
//     dispatch, no granule VM boot, no clone, and no model turn, since a
//     refused request produces no assistant turn -- and it is shorter
//     than agent.BaseTransientBackoff's own first wait. Every outage it
//     absorbs is one that never reaches grain's budget of two.
//
// Where codex differs from claude is the informedness rather than the
// answer: claude waits the retry_delay_ms Anthropic asks for, while
// codex's ladder is a fixed exponential that ignores Retry-After
// entirely (measured: a 503 carrying `Retry-After: 7` produced the same
// thirty requests on the same gaps). At this length that costs nothing
// worth writing a provider stanza over.
//
// The claude decision's real complaint -- that three invisible minutes
// read as a hung run -- barely applies here, because 25 seconds does
// not. The numbers are written down anyway, in agent/retry.go beside the
// retry budget they sit under and in
// the 2026-09-08 codex-retry hand run, which is also where the one
// thing that measurement found that *was* worth acting on lives: codex
// 0.153 collapses every 429 into "exceeded retry limit, last status: 429
// Too Many Requests", retrying it not at all and dropping the body, so
// neither capacity.go nor usagelimit.go recognised a rate limit that
// arrived as one. It reached orchestrator.RunDispatch as an ordinary
// failed run and spent one of the five failures that retire a task.
// capacity.go now matches it, as capacity rather than as a usage limit
// -- its header has why -- which makes it the one refusal where this
// loop's own retry is the *only* retry there is: codex's ladder above
// does not run for a 429.
//
// TestRunLeavesCodexsOwnAPIRetryAlone is what keeps the decision honest
// from below (no provider stanza, no retry keys in the config this
// package writes), and TestLiveCodexRetriesARefusedTurnItself is the
// measurement itself, run nightly: it fails if codex stops retrying, if
// its ladder grows into claude's minutes, or if what it finally emits
// stops reading as a capacity refusal.
// TestLiveCodexReportsA429GrainCanClassify is its twin for the status
// codex will not retry, and watches the same wording from the other
// side: that the sentence still names the 429 at all.

// resumeRefusedNote is what a run says in its transcript when the resume
// invocation turned out not to be one this codex would perform, and it
// fell back to starting the task again. It names the argv because the
// person reading it has to decide whether this package should stop
// asking for it -- see this file's header.
const resumeRefusedNote = "! codex refused the invocation resuming this run's thread " +
	"(`codex exec resume <thread> -`) and opened no thread at all: grain restarted this " +
	"run from the top in the same sandbox instead. The model turns spent before the " +
	"provider refusal were lost with the thread."

// attemptSetup is everything the attempts of one run share, built once
// in Framework.Run and never rebuilt: how to build the argv for an
// attempt (with or without a thread to resume), the environment carrying
// this run's private CODEX_HOME and its credential, the directory codex
// is started in, the three prompts, the live sink the transcript mirror
// and the turn cap hang off, and the cap itself.
//
// None of it is rebuilt per attempt, which is the whole point of
// retrying here rather than letting the dispatch fail. The CODEX_HOME in
// the environment is where codex keeps the thread being resumed; the
// sink is opened O_TRUNC once, so reopening it for a retry would throw
// away everything the run had already streamed.
type attemptSetup struct {
	// args builds one attempt's argv. An empty thread means a fresh
	// `codex exec`; anything else is `codex exec resume <thread>`.
	args   func(thread string) []string
	env    []string
	dir    string
	prompt string
	// resumePrompt is the user turn a resumed attempt sends instead of
	// the task's prompt: the thread it is rejoining already has that
	// (agent.ResumePrompt).
	resumePrompt string
	// restartPrompt is the task's prompt again behind
	// agent.RestartPreamble, for an attempt with no thread to resume.
	restartPrompt string
	// resumeThread is the thread a *previous dispatch* of this task left
	// behind in this same CODEX_HOME (agent.RunConfig.ResumeSession),
	// which the first exec joins rather than starting the task from
	// nothing. Empty for a first dispatch, and for a run whose home does
	// not outlive it -- Framework.Run only fills it in when the store
	// gave this run the task's own home.
	resumeThread string
	// suspendedPrompt is what that first exec sends: the whole prompt
	// behind agent.SuspendedPreamble, not resumePrompt's nudge, for the
	// reason that preamble gives -- the task may have been edited, a
	// person may have answered the question the last run stopped on, and
	// the sandbox is a new one.
	suspendedPrompt string
	// keepThread is whether the thread this run ends in may be handed to
	// the task's next dispatch: true only where the home holding it
	// outlives the run.
	keepThread bool
	live       io.Writer
	// onThread is agent.RunConfig.OnResumableSession: told the thread
	// an attempt is in as soon as codex names it, under the same two
	// conditions attemptsResult.thread is reported on at the end.
	onThread func(string)
	cap      *turnCap
}

// attemptsResult is what a run's attempts amount to: every attempt's
// event stream in order, the last one's own subprocess error, and what
// the loop has to say about itself for the transcript.
type attemptsResult struct {
	capture string
	runErr  error
	notes   []string
	// thread is the conversation a later dispatch of this task may be
	// resumed into (agent.Result.ResumableSession). Not the last
	// attempt's own id but whichever one this run is actually in at the
	// end of it, and "" where handing it on would buy the next dispatch
	// nothing: a home that does not outlive this run, or a build that
	// has already refused to reopen a thread it was given.
	thread string
}

// runAttempts execs codex for one grain run: once in the ordinary case,
// and again -- in the same sandbox, the same CODEX_HOME and the same
// thread -- when the provider refused it mid-flight.
func (f *Framework) runAttempts(ctx context.Context, s attemptSetup) attemptsResult {
	var (
		capture strings.Builder
		out     attemptsResult
		// The thread to resume, as codex last named it on its own
		// stream -- or, before the first exec, the one a previous
		// dispatch of this task left in this same CODEX_HOME.
		thread = s.resumeThread
		// Whether that thread is the one carried in from a previous
		// dispatch rather than one this run's own execs opened. It
		// decides which turn a resume sends and how a refused resume is
		// read, and is false from the first exec that got past the
		// fallback below.
		carried = s.resumeThread != ""
		// True until an exec that asked to resume was refused outright,
		// after which this run stops asking.
		resumable = true
		// How many of this run's retries have been spent.
		spent   = 0
		attempt = 0
	)
	for {
		attempt++
		resuming := resumable && thread != "" && (carried || attempt > 1)
		resumeThread := ""
		stdin := s.prompt
		switch {
		case resuming && carried:
			// A thread from a previous dispatch of this task. It holds
			// what that run worked out and nothing since, and the
			// sandbox it worked in is gone -- so it gets the task in
			// full, behind the paragraph that says both
			// (agent.SuspendedPreamble).
			resumeThread, stdin = thread, s.suspendedPrompt
		case resuming:
			// The thread already has the prompt, and everything the
			// attempt did before it was cut off; what it does not have
			// is the fact that the interruption came from outside.
			resumeThread, stdin = thread, s.resumePrompt
		case attempt > 1:
			// The task from the top, but into a sandbox holding an
			// earlier attempt's work: agent.RestartPreamble is what says
			// so, and what keeps the model from reading its own half
			// finished work as somebody else's.
			stdin = s.restartPrompt
		}

		// Each attempt gets a cancel of its own, and the cap is pointed
		// at it: the turn cap works by cancelling the run it is
		// watching, and a cap still holding the first attempt's cancel
		// would stop nothing on the second.
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		s.cap.retarget(cancelAttempt)
		// The thread goes on record from the stream itself, the moment
		// codex names it, so a run the daemon is stopped in the middle
		// of can still be resumed out of this CODEX_HOME -- which
		// outlives the run however it ends (sessionstore).
		var report func(string)
		if s.keepThread && resumable {
			report = s.onThread
		}
		live := agent.NewSessionWatcher(s.live, threadFromLine, report)
		stdout, runErr := f.run.Run(attemptCtx, s.args(resumeThread), stdin, s.env, s.dir, live)
		cancelAttempt()
		if errors.Is(runErr, agent.ErrDetached) {
			// Still running, in grain hostd, which this daemon has let go
			// of: not an attempt that ended, so nothing here may read it
			// as one or retry it. The next daemon reads its stream
			// (ParseResult).
			out.runErr = runErr
			break
		}

		events := parseEvents(stdout)
		appendCapture(&capture, stdout)
		out.runErr = runErr
		if id := events.threadID; id != "" {
			thread = id
		}

		// A resumed exec codex never opened a thread for. No model turn
		// was spent, so neither a retry nor any of this run's budget is
		// spent on the attempt that replaces it -- it is run again under
		// the same number.
		//
		// What it means depends on where the thread came from, and the
		// two readings are worth keeping apart:
		//
		//   - a thread codex itself named minutes ago, on this run's own
		//     stream, is certainly in this CODEX_HOME. So the refusal is
		//     about the invocation -- this repository asking for
		//     something the binary will not perform -- and it is dropped
		//     for the rest of the run, and with it any claim that this
		//     run left a thread a later dispatch could join.
		//   - a thread a previous dispatch left behind was last written
		//     hours or days ago, into a store grain prunes on its own
		//     bounds (sessionstore). The likelier reading is that it is
		//     not there anymore, which says nothing about the
		//     invocation. So only the thread is dropped: this run starts
		//     the task from its prompt, and a provider refusal later in
		//     it may still resume the thread codex is about to open.
		if resuming && runErr != nil && !events.sawThread {
			if carried {
				carried, thread = false, ""
				out.notes = append(out.notes, agent.SuspendedResumeRefusedNote)
			} else {
				resumable = false
				out.notes = append(out.notes, resumeRefusedNote)
			}
			attempt--
			continue
		}
		// Past the fallback, which is where this belongs: whatever
		// thread this run is in from here is one its own execs opened
		// rather than the carried one, and an exec that opened no thread
		// at all -- which spent no model turn -- does not get to say so.
		carried = false

		if !retryable(stdout, runErr, s.cap) {
			break
		}
		if !f.retry.WaitBeforeRetry(ctx, spent+1) {
			break
		}
		spent++
		out.notes = append(out.notes, agent.RetryNote(attempt+1, resumable && thread != ""))
	}
	out.capture = capture.String()
	// What a later dispatch of this task may be resumed into -- but only
	// where the CODEX_HOME holding it outlives this run, and only where
	// this build has not already refused to reopen a thread it was
	// handed. A token grain shows a person and gives the next dispatch
	// has to be one that can actually be resumed.
	if s.keepThread && resumable {
		out.thread = thread
	}
	return out
}

// retryable reports whether the attempt just finished is one worth
// running again: a capacity refusal and nothing else.
//
// The failure test comes first and is not a formality. capacity.go
// matches phrases against text this run produced, and a run that
// *finished* has spent an hour quoting CI logs and HTTP errors into that
// same stream -- codex's own "Reconnecting... 1/5 (unexpected status 503
// Service Unavailable)" among them, which is an event a successful run
// emits on its way to succeeding. A run that ended in codex's own
// account of a failure (resultErr) or died under it (runErr) is the only
// kind this reads at all.
//
// A cap that tripped is grain stopping the run on purpose; a quota
// refusal is hours away from clearing and belongs to dispatch's pause
// rather than to a 30-second backoff; an ordinary failure would only be
// repeated.
func retryable(stdout string, runErr error, cap *turnCap) bool {
	if cap.tripped() {
		return false
	}
	p := parseEvents(stdout)
	if !failed(p, runErr) {
		return false
	}
	if usageLimitFailure(p.failureText(), runErr) != nil {
		return false
	}
	return capacityFailure(p.failureText(), runErr) != nil
}

// failed reports whether an attempt ended badly at all -- what makes a
// marker match evidence rather than a coincidence, for both classifiers
// above and for Framework.Run's own reading of the same stream. A
// terminal failure codex reported, or a subprocess that died: nothing
// else is a failed run, however many provider errors the stream carries
// on its way to finishing.
func failed(p *parsedEvents, runErr error) bool {
	return runErr != nil || p.resultErr != nil
}

// appendCapture adds one attempt's stdout to the run's whole capture,
// terminating it if the attempt did not. A killed codex can stop
// mid-line, and gluing that half line to the next attempt's first one
// would leave both unparseable.
func appendCapture(b *strings.Builder, stdout string) {
	if stdout == "" {
		return
	}
	b.WriteString(stdout)
	if !strings.HasSuffix(stdout, "\n") {
		b.WriteString("\n")
	}
}

// appendNote puts one of the retry loop's own lines at the end of a
// run's narrative, so a transcript that appears to restart half way
// through says why it did.
func appendNote(transcript, note string) string {
	switch {
	case note == "":
		return transcript
	case transcript == "":
		return note
	}
	return transcript + "\n\n" + note
}
