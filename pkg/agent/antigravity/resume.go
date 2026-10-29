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

package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// This file is what agy adds to the retry policy every framework shares.
//
// agent/retry.go has the policy itself, and the reasoning behind it: a
// run the provider refused mid-flight is not a run to throw a sandbox
// away for, so Framework.Run backs off and execs agy again rather than
// letting the dispatch fail -- bounded attempts, a doubling backoff,
// only ever for a capacity refusal, and only while the run's own
// deadline has room for another attempt.
//
// What is agy's own is the resume. For the whole of Run the private HOME
// writeAgyHome built is still there, which is where agy keeps its
// conversations (~/.gemini/antigravity-cli/conversations, see
// docs/agy-surface.md), so the second exec can join the first one's
// conversation instead of starting the task again -- which is what keeps
// the model turns already spent, on top of the sandbox that every
// framework's retry keeps.
//
// # What resuming depends on, and what was measured of it
//
// agy takes --conversation <id> and every event it streams carries the
// conversation_id to pass back (transcript.go). What this file rested on
// for a fortnight, and what no measurement in this repository had
// established, was that --conversation composes with --input-format
// stream-json and a fresh user turn on stdin: docs/agy-surface.md records
// the flag's existence, not its behaviour, and every scheduled run of
// .grain/workflows/live-agent.yml had died at "Require a credential"
// without executing a live agy at all.
//
// It has now been measured, and it holds. live_resume_stub_test.go is the
// measurement, docs/hand-runs/2026-09-17-agy-resume.md the run it was
// written from, and the shape is the one agent/claude and agent/codex
// each use: a real agy, the argv Framework.Run builds, the private HOME
// writeAgyHome writes, and the model behind a stub of Gemini's own
// endpoint inside the test process (GOOGLE_GEMINI_BASE_URL, the seam
// live_retry_stub_test.go refuses everything through). No credential, so
// it runs ahead of the gate that has never opened -- and a credential
// would have proved it less well, not better, because what a resumed exec
// *sent* is only readable off the request bodies.
//
// A second exec passing --conversation with the rest of that argv is
// accepted, opens the conversation it was given, reports the same
// conversation_id back, and is sent the whole earlier conversation: the
// first turn, the model's own reply to it, then the new turn. An exec out
// of the same HOME that passes no --conversation is sent none of it,
// which is what makes the first sentence evidence rather than a
// coincidence. Measured on agy 1.1.27 (the release the Dockerfile pins
// and verifies) and on 1.2.5 (what antigravity.google/cli/install.sh
// served on 2026-09-17, and therefore what a freshly built
// Dockerfile.k8s-sandbox carries), with no difference between them.
//
// So resumeRefusedNote and the resumable flag below stay, for a build
// this repository has not measured rather than for the two it has: an
// attempt that passed --conversation and died without agy ever opening a
// session -- no init event, which is the first thing a live agy emits,
// before it so much as authenticates -- is read as the binary refusing
// the argv. Run then drops the flag for good and retries from scratch
// with the original prompt in the same sandbox, which is the cheap
// variant of this whole idea and needs none of the conversation plumbing
// -- the same fallback agent/claude keeps behind its own --resume, and
// agent/codex behind `codex exec resume`. It costs
// one process start of a couple of seconds, it leaves a note in the
// transcript saying it happened, and it means a build that never
// supported the flag degrades to "retry in place" instead of turning a
// provider outage into a hard failure.
//
// # The same conversation, one dispatch later
//
// That the conversation lives in the HOME also makes it last exactly as
// long as the HOME does, and both ends of this file now depend on which
// of the two HOMEs a run was given. A run whose task has a session store
// (WithSessionStore, pkg/agent/sessionstore) works in *that task's own*
// HOME, kept between dispatches, so the conversation its last run ended
// in is still in the directory this run is starting in. That run's first
// exec resumes it (agent.RunConfig.ResumeSession) and the run hands back
// the conversation it ends in (agent.Result.ResumableSession) for the
// next one to do the same. What it recovers is everything the earlier run
// had read, run and worked out before it ran out of wall clock, met the
// account's quota or stopped to ask a person something.
//
// A run without one -- no store configured, no task id, or a second run
// of a task whose store somebody else is holding -- is this file exactly
// as it was: a temp HOME deleted as Run returns, no resume asked for on
// the first exec, and no conversation reported back. That last part is
// not a detail: a conversation id from a deleted directory buys the next
// dispatch a refused exec and shows a person a resume token that resumes
// nothing, so attemptSetup.keepConversation gates the report on the HOME
// outliving the run, and Framework.Run gates the resume on the same fact.
//
// Two things differ from the in-run resume, and both are in the switch in
// runAttempts. The turn sent is the whole prompt behind
// agent.SuspendedPreamble rather than agent.ResumePrompt's nudge, for the
// reason that preamble gives. And a resume that does not take is read
// differently: a conversation from a previous dispatch is one grain's own
// pruning may since have removed, so only the conversation is dropped and
// the run works the task from its prompt, where a conversation agy named
// on this run's own stream and then refused can only be the argv and
// costs the run the flag.
//
// # What a cross-dispatch resume asks of agy, and what it answered
//
// A cross-dispatch resume asks two things of agy that an in-run one does
// not, and no fixture in this repository could answer either. Both were
// put to the binary on 2026-09-17, in the same stub-provider probe as the
// section above (live_resume_stub_test.go, and the hand run beside it),
// and both came back in this file's favour on 1.1.27 and 1.2.5 alike:
//
//   - the conversation was written by a process whose HOME was this same
//     directory but whose *working directory* was a different sandbox.
//     agy puts the cwd in its own init event and builds a workspace out
//     of it, so a conversation named explicitly might not have been found
//     from a new one. It is: an exec resuming from a second sandbox, with
//     the first one deleted outright, opens the conversation and is sent
//     the whole of it. The cwd does not enter into resolving a
//     conversation named by id, which is what codex's own measurement
//     found for `codex exec resume`
//     (docs/hand-runs/2026-09-07-codex-resume.md §3).
//   - the HOME carries a conversation_summaries.db, a
//     jetbox_summaries_proto.pb, a cache/last_conversations.json and a
//     brain/<id>/ tree beside the conversations directory, and what a
//     resume needs out of them was unknown. Nothing: a HOME with all
//     three indexes deleted resumes and replays, and so does a HOME this
//     package has just written from scratch, at a path agy has never
//     seen, carrying one file of the old one --
//     conversations/<id>.db. agy rebuilds the summaries index for itself.
//     The conversation is portable, and sessionstore keeping everything
//     the CLI wrote is more than enough rather than a gamble.
//
// One thing came back against this file's guess, and it is the premise
// the fallback below was keyed on. A conversation agy *cannot* find is
// not refused: it warns on stderr ("warning: conversation \"...\" not
// found"), opens a new conversation with a new id, runs the turn and
// exits 0. So the "no init event" signal never fires for a conversation
// grain has pruned, agent.SuspendedResumeRefusedNote is written for a
// build neither measured agy is, and the only evidence a carried resume
// did not take is that the conversation on the stream is not the one that
// was asked for. runAttempts now reads exactly that, and says so in the
// transcript (suspendedResumeLostNote). Nothing is lost when it happens:
// the turn a resumed dispatch sends is the whole prompt already.
//
// What none of this covers is the one thing a stub cannot: whether
// Google's own side of a resumed conversation survives. On both measured
// builds there is nothing server-side to survive -- agy replays the
// conversation out of its own on-disk database, which is what the request
// bodies show -- and if that ever changes, these probes go on passing
// while a deployment stops resuming. tests/e2e/live_resume_test.go, and
// the credential .grain/workflows/live-agent.yml has never been given,
// remain the only way to see it.
//
// # A refused model turn gets a grain-owned deadline
//
// The other two frameworks retry a refused API call themselves before
// reporting anything: claude ten times over 2m51s, codex thirty times over
// 25 seconds. Both packages left those ladders alone. agy 1.1.27 originally
// made the decision even simpler: one HTTP request over 0.15s, with only
// transport faults retried three times over 2.4s.
//
// That changed in agy 1.2.2. The same live probe, against the same Gemini
// stub and the argv/private HOME a dispatch builds, saw ten requests and
// was still inside agy after five minutes. 1.4.9's ladder ends after nine
// attempts over about 2m33s, which still outlasts the bound below. There
// remains no retry flag, environment variable or settings key to turn
// down. The full schedule and
// the remeasurement are in docs/hand-runs/2026-09-08-agy-retry.md.
//
// turnDeadline therefore enforces the 100-second boundary the live probe
// had already recorded as the point where this package must reconsider.
// It measures a model turn rather than the whole exec: the clock begins
// with the attempt or when its last tool finishes, ends when agy completes
// an agent_response, and pauses for tool calls such as wait_for_checks and
// chat whose grain-owned waits are deliberately much longer. Expiry
// cancels the attempt's process group; the completed diagnostic records
// written by agy before its backoff are then read exactly as on a natural
// exit. A 503 still reaches the capacity classifier and this loop's visible
// 30/60-second retry, and a quota refusal still reaches dispatch's pause.
//
// This is not done by lowering --print-timeout. That flag caps the whole
// multi-turn run and would again kill useful work five minutes in. It is
// also not a guessed generic capacity error: if the diagnostic contains no
// provider refusal, Run reports the model-turn deadline itself and does not
// spend the capacity retry budget.
//
// # What that measurement found instead, which is worse
//
// None of the provider's own words survive into the stream. agy logs
// `calling model: Error 503, Message: The model is overloaded. Please try
// again later., Status: UNAVAILABLE` to its own --log-file and emits
// `"error":"Agent execution terminated due to error."` on stdout -- the
// same sentence every other kind of failure ends with. The error_message
// step carries no text, stderr is empty, and the subprocess error is a
// bare exit status 1.
//
// So capacityFailure returns nil for a real Gemini overload, retryable is
// false, and this whole loop never runs for the case it was built for: a
// refused run fails once, reaches orchestrator.RunDispatch as an ordinary
// failure, and counts against the task's own streak. usageLimitFailure is
// blind in the same way and for the same reason, which is worse again,
// since dispatch then does not pause for a quota refusal.
//
// Each exec now gets a --log-file in the run's private HOME. Grain reads
// only complete records from its bounded tail, redacts them, and gives
// that current exec's excerpt to the quota and capacity classifiers.
// This accepts the deliberately small dependency on agy's undocumented
// log format instead of guessing that every generic exit 1 is a capacity
// refusal. A previous attempt's diagnostic remains transcript evidence,
// but never decides whether a later attempt retries.
//
// # An interrupted attempt is resumed, not reported
//
// Everything above is about a refusal grain can name. What the dispatches
// of 2026-09-15 are full of is the other kind, and the whole of what a
// task's timeline gets to see of them is one sentence each:
//
//	antigravity: model turn exceeded 1m40s; the run made 2 tool call(s)
//	antigravity: run ended in status ERROR: Agent execution terminated
//	due to error. (agy also exited non-zero: exit status 1 (stderr: ))
//
// Neither is a verdict on the task. The first is this package's own
// deadline stopping a turn that had gone quiet; the second is agy's
// all-purpose sentence for a run it could not finish, with no provider
// words anywhere in the diagnostic to classify (the section above). Both
// used to end the dispatch on the spot, and the task came back around
// minutes later in a *fresh* sandbox with an empty conversation -- one
// task that morning spent three attempts that way, the third of which had
// opened a pull request before agy died under it.
//
// So an attempt that ended without a verdict of its own is now resumed,
// on the same terms as a refusal: the same sandbox, the same private
// HOME, the same conversation, this policy's own budget and backoff, and
// the run's own deadline still the outer bound. What makes that safe when
// retrying an unclassified failure would not be is precisely that it is a
// *resume*. The conversation holds what the run had already worked out,
// so the next exec carries on rather than repeating the failure from the
// top, and agent.InterruptedPrompt tells it what happened in the words
// that are true -- a CLI that stopped, not a provider that refused.
//
// What is *not* read this way is agy's other failed status, FAILURE:
// interruptedStatuses below has the argument, which is that FAILURE is
// the status a classified refusal arrives under and so the one that
// might also be a verdict. Where there is no conversation to join -- and
// where agy turns out to refuse the resume argv itself, which no
// measurement in this repository has settled either way -- an
// interruption is reported exactly as it was before. Starting the task
// again from its prompt is the right answer for a refusal, which said
// nothing about the run; it is a guess for a failure nobody could
// classify, and dispatch makes that same guess for us anyway, with a
// clean sandbox behind it.
//
// The bounds are the ones that were already here. Two extra execs per run
// (agent.DefaultTransientRetries), 30 then 60 seconds of backoff, and
// nothing at all once the run's deadline has less than
// agent.MinimumRetryRuntime left. A turn cap that tripped and a quota
// refusal are still terminal, and are checked before any of this: the cap
// is grain ending the run on purpose, and a resume against an exhausted
// quota would spend the budget re-reading the same refusal.

// resumeRefusedNote is what a run says in its transcript when the resume
// flag turned out not to compose with the rest of the argv, and it fell
// back to retrying from scratch. It names the flag because the person
// reading it has to decide whether this package should stop passing it
// -- see this file's header.
const resumeRefusedNote = "! agy refused the invocation resuming this run's conversation " +
	"(--conversation with --input-format stream-json) and opened no session at all: " +
	"grain retried this run from the start in the same sandbox instead. The model turns " +
	"spent before this run was cut off were lost with the conversation."

// suspendedResumeLostNote is what a run says when the conversation a
// previous dispatch of this task left behind was not in agy's store
// anymore: the id agy ran in is not the id it was given.
//
// It is deliberately not agent.SuspendedResumeRefusedNote, which says the
// CLI opened no session at all. That is what codex and claude do with a
// session they do not have, and it is what this package expected until
// agy was asked: agy warns, opens a conversation of its own and carries
// on, so the run is fine and the note's job is only to tell a person
// reading the transcript that the resume token grain held against this
// task had been pruned out from under it.
const suspendedResumeLostNote = "! the conversation a previous run of this task left suspended was " +
	"no longer in agy's store, so agy opened a new one: this run worked the task from its prompt " +
	"rather than from what that run had already worked out. The controller prunes its session " +
	"store on its own bounds (pkg/agent/sessionstore)."

// resumeRefusedInterruptionNote is the same finding for a resume that was
// picking up an interruption rather than a provider refusal, where there
// is no from-scratch fallback to fall back to. It is the whole cost this
// change carries if --conversation turns out not to compose: one exec
// that spends no model turn, and then the ending the run would have had
// anyway.
const resumeRefusedInterruptionNote = "! agy refused the invocation resuming this run's " +
	"conversation (--conversation with --input-format stream-json) and opened no session at " +
	"all, so the run grain meant to pick up where it stopped ends where it stopped instead."

// attemptSetup is everything the attempts of one run share: the private
// HOME writeAgyHome built and the environment pointing at it, the
// directory agy is started in, the four user turns (the task's prompt,
// the two nudges a resumed attempt sends instead -- one for a provider
// refusal, one for an interruption -- and the prompt again behind
// agent.RestartPreamble for an attempt with no conversation to resume),
// the live sink the transcript mirror, turn cap and model-turn deadline
// hang off, and the two watchers themselves.
//
// All of it is built once, in Framework.Run, and none of it is rebuilt
// per attempt -- which is the whole point of retrying here rather than
// letting the dispatch fail. The HOME is where agy keeps the
// conversation being resumed; the sink is opened O_TRUNC, so reopening
// it would throw away everything the run had already streamed.
type attemptSetup struct {
	diagnosticHome    string
	diagnosticSecrets []string
	env               []string
	dir               string
	firstTurn         string
	nextTurn          string
	interruptedTurn   string
	restartTurn       string
	// suspendedTurn is what the first exec of a dispatch resuming a
	// previous one sends: the whole prompt behind
	// agent.SuspendedPreamble, rather than nextTurn's nudge.
	suspendedTurn string
	// resumeConversation is the conversation a *previous dispatch* of
	// this task left in this same HOME (agent.RunConfig.ResumeSession),
	// which the first exec joins rather than starting the task from
	// nothing. Empty for a first dispatch, and for a run whose HOME does
	// not outlive it -- Framework.Run only fills it in when the store
	// gave this run the task's own HOME.
	resumeConversation string
	// deltaSuspended is true when suspendedTurn carries only the delta
	// since the suspended run rather than the full prompt: if agy silently
	// replaces the conversation with a brand-new one instead of joining
	// resumeConversation, the loop must re-run from firstTurn so the new
	// conversation receives the full prompt.
	deltaSuspended bool
	// keepConversation is whether the conversation this run ends in may
	// be handed to the task's next dispatch: true only where the HOME
	// holding it outlives the run.
	keepConversation bool
	live             io.Writer
	// onConversation is agent.RunConfig.OnResumableSession: told the
	// conversation an attempt is in as soon as agy names it, under the
	// same two conditions attemptsResult.conversation is reported on.
	onConversation func(string)
	cap            *turnCap
	deadline       *turnDeadline
	// refresh puts this run's configuration back into that HOME before
	// an attempt after the first, agy having rewritten part of it as it
	// started (refreshAgyHome).
	refresh func() error
}

// attemptsResult is what a run's attempts amount to: every attempt's
// stream-json in order; the last one's subprocess error, redacted
// diagnostic and parsed stream; and what the loop has to say about
// itself for the transcript.
type attemptsResult struct {
	capture string
	runErr  error
	// events is the *last* attempt's own events, and it is what the run
	// is classified from -- not a parse of the whole capture, which
	// holds one attempt's stream per exec.
	//
	// The difference is only visible when the last attempt ended without
	// a terminal result event of its own, which is the ordinary shape of
	// the interruptions this file resumes: agy dying mid-run, a stream
	// that stopped. A whole-capture parse then still carries the
	// *previous* attempt's terminal event -- parseEvents clears that
	// state on a result event and nowhere else -- so a run refused for
	// capacity, resumed, and then cut off twice for some unrelated
	// reason was handed back as a capacity refusal, and recorded by
	// orchestrator.RunDispatch as model.CapacityOutcome, which no
	// failure streak counts. The refusal belonged to the attempt that
	// met it; what the run ended as is what its last attempt said, which
	// is what this loop has always judged *its own* decisions on (it
	// parses each attempt separately) and what agent/codex's parser
	// already gave its caller.
	//
	// Never nil once runAttempts has returned: the first exec always
	// sets it, and only an exec that opened no session at all (the
	// refused-resume fallback below) leaves the attempt before it
	// standing.
	events       *parsedEvents
	diagnostic   string
	turnTimedOut bool
	turnVerdict  turnVerdict
	notes        []string
	// conversation is what a later dispatch of this task may be resumed
	// into (agent.Result.ResumableSession): whichever conversation this
	// run is actually in at the end of it, and "" where handing it on
	// would buy the next dispatch nothing -- a HOME that does not
	// outlive this run, or a build that has already refused to reopen a
	// conversation it was given.
	conversation string
}

// runAttempts execs agy for one grain run: once in the ordinary case,
// and again -- in the same sandbox, the same HOME and the same
// conversation -- when the provider refused it mid-flight, or when the
// attempt was cut off without reaching a verdict of its own. See
// agent/retry.go for what counts as a transient refusal and what does
// not, and this file's header for the interruptions.
func (f *Framework) runAttempts(ctx context.Context, cfg agent.RunConfig, s attemptSetup) attemptsResult {
	var (
		capture strings.Builder
		out     attemptsResult
		// The session to resume, as agy last named it on its own stream
		// -- or, before the first exec, the one a previous dispatch of
		// this task left in this same HOME.
		conversation = s.resumeConversation
		// Whether that conversation is the one carried in from a
		// previous dispatch rather than one this run's own execs opened.
		// It decides which turn a resume sends and how a refused resume
		// is read, and is false from the first exec that got past the
		// fallback below.
		carried = s.resumeConversation != ""
		// True until an exec that passed --conversation was refused
		// outright, after which this run stops asking for one.
		resumable = true
		// Why the attempt before this one was cut off, when it was cut
		// off rather than refused: it decides which turn the resumed
		// attempt sends.
		interrupted = ""
		// How many of this run's retries have been spent.
		spent   = 0
		attempt = 0
	)
	for execs := 0; ; execs++ {
		attempt++
		if execs > 0 {
			// agy rewrites part of the HOME it starts in, so the next
			// launch is given this run's own configuration back rather
			// than what the last one left. A HOME this process can no
			// longer write is not something to retry through: stop
			// here, with the refusal that prompted the retry still the
			// error this run ends on.
			if err := s.refresh(); err != nil {
				out.notes = append(out.notes, "! "+err.Error())
				break
			}
		}
		resuming := resumable && conversation != "" && (carried || attempt > 1)
		resumeID := ""
		stdin := s.firstTurn
		switch {
		case resuming && carried:
			// A conversation from a previous dispatch of this task. It
			// holds what that run worked out and nothing since, and the
			// sandbox it worked in is gone -- so it gets the task in
			// full, behind the paragraph that says both
			// (agent.SuspendedPreamble).
			resumeID, stdin = conversation, s.suspendedTurn
		case resuming:
			// The conversation already has the prompt and everything the
			// last attempt did with it; what it cannot have is why it
			// stopped. agent.ResumePrompt says a provider refused the
			// turn, agent.InterruptedPrompt that the CLI itself stopped
			// -- and the second must not be told as the first, since
			// nothing measured here knows a provider was involved at all.
			resumeID, stdin = conversation, s.nextTurn
			if interrupted != "" {
				stdin = s.interruptedTurn
			}
		case attempt > 1:
			// The task from the top, but into a sandbox holding an
			// earlier attempt's work: agent.RestartPreamble is what says
			// so, and what keeps the model from reading its own half
			// finished work as somebody else's.
			stdin = s.restartTurn
		}

		// Each attempt gets a cancel of its own, and the cap is pointed
		// at it: the turn cap works by cancelling the run it is
		// watching, and a cap still holding the first attempt's cancel
		// would stop nothing on the second.
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		s.cap.retarget(cancelAttempt)
		args := f.agyArgs(attemptCtx, cfg, resumeID)
		logPath, diagnostic := prepareDiagnosticLog(ctx, f.host, s.diagnosticHome)
		if logPath != "" {
			args = append(args, "--log-file", logPath)
		}
		// The deadline reads the same log while a turn is open, which is
		// how it tells agy retrying a provider error from a slow model.
		s.deadline.retarget(cancelAttempt, logPath)
		// The conversation goes on record from the stream itself, the
		// moment agy names it, so a run the daemon is stopped in the
		// middle of can still be resumed out of this HOME -- which
		// outlives the run however it ends (sessionstore).
		var report func(string)
		if s.keepConversation && resumable {
			report = s.onConversation
		}
		live := agent.NewSessionWatcher(s.live, conversationFromLine, report)
		stdout, runErr := f.run.Run(attemptCtx, args, stdin, s.env, s.dir, live)
		s.deadline.stop()
		cancelAttempt()
		if errors.Is(runErr, agent.ErrDetached) {
			// Still running, in grain hostd, which this daemon has let go
			// of as it stops: not an attempt that ended, so nothing here
			// may read it as one or retry it. The next daemon reads its
			// stream (Framework.ParseResult).
			out.runErr = runErr
			break
		}

		events := parseEvents(stdout)
		appendCapture(&capture, stdout)
		if runErr != nil || events.resultErr != nil || !events.sawResult {
			if logPath != "" {
				diagnostic = readDiagnosticLog(ctx, f.host, logPath, s.diagnosticSecrets)
			}
			appendDiagnostic(&capture, s.live, execs+1, diagnostic)
		}
		out.runErr = runErr
		out.diagnostic = diagnostic
		out.turnVerdict, out.turnTimedOut = s.deadline.trippedVerdict()
		if id := events.conversationID; id != "" {
			conversation = id
		}

		// A conversation carried in from a previous dispatch that agy did
		// not join. Neither measured build refuses this invocation for a
		// conversation it cannot find -- it warns on stderr, opens a new
		// one and runs the turn (this file's header, and
		// TestLiveAgyOpensANewConversationForOneItCannotFind) -- so the
		// id on the stream is the only evidence that grain's store no
		// longer had what it handed over. The run itself is unharmed: the
		// turn this exec sent was the whole prompt behind
		// agent.SuspendedPreamble, and the conversation it opened instead
		// is the one a later dispatch will be given.
		if resuming && carried && events.sawInit && events.conversationID != "" &&
			events.conversationID != resumeID {
			out.notes = append(out.notes, suspendedResumeLostNote)
			if s.deltaSuspended {
				carried, conversation = false, ""
				attempt--
				continue
			}
		}

		// A resumed exec agy never opened a session for. No model turn
		// was spent, so no retry is either.
		//
		// What it means depends on where the conversation came from,
		// and the two readings are worth keeping apart:
		//
		//   - a conversation agy itself named minutes ago, on this run's
		//     own stream, is certainly in this HOME. So the refusal is
		//     this repository asking for an argv the binary does not
		//     support: the flag is dropped for the rest of the run, and
		//     with it any claim that this run left a conversation a
		//     later dispatch could join.
		//   - a conversation a previous dispatch left behind was last
		//     written hours or days ago, into a store grain prunes on
		//     its own bounds (sessionstore). The likelier reading is
		//     that it is not there anymore, which says nothing about the
		//     argv: only the conversation is dropped, the run starts the
		//     task from its prompt, and a refusal later in it may still
		//     resume the conversation agy is about to open.
		if resuming && runErr != nil && !events.sawInit {
			if carried {
				carried, conversation = false, ""
				out.notes = append(out.notes, agent.SuspendedResumeRefusedNote)
				attempt--
				continue
			}
			resumable = false
			if interrupted != "" {
				// This resume was picking an interruption back up rather
				// than a refusal, and there is no cheap variant for one
				// of those: starting the task from the top is the guess
				// this file's header declines to make. The run ends on
				// the failure that sent it here, one instant exec later.
				out.notes = append(out.notes, resumeRefusedInterruptionNote)
				break
			}
			out.notes = append(out.notes, resumeRefusedNote)
			attempt--
			continue
		}
		// Past the fallback above, so an exec agy opened no session for
		// -- which spent no model turn and says nothing about this run --
		// does not become what the run is classified from. And whatever
		// conversation this run is in from here is one its own execs
		// opened rather than the carried one.
		out.events = events
		carried = false

		// Two ways an attempt earns another exec, and they differ in what
		// the next one is allowed to be. A refusal says nothing about
		// this run, so it may be retried from the prompt where there is
		// no conversation to join; an interruption is only ever resumed.
		refused := f.retryable(events, runErr, diagnostic, s.cap)
		interrupted = ""
		if !refused {
			interrupted = f.interruption(events, runErr, diagnostic, s)
		}
		canResume := resumable && conversation != ""
		if interrupted != "" && !canResume {
			break
		}
		if !refused && interrupted == "" {
			break
		}
		if !f.retry.WaitBeforeRetry(ctx, spent+1) {
			break
		}
		spent++
		if interrupted != "" {
			out.notes = append(out.notes, interruptedNote(attempt+1, interrupted))
		} else {
			out.notes = append(out.notes, agent.RetryNote(attempt+1, canResume))
		}
	}
	out.capture = capture.String()
	// What a later dispatch of this task may be resumed into -- but only
	// where the HOME holding it outlives this run, and only where this
	// build has not already refused to reopen a conversation it was
	// handed. A token grain shows a person and gives the next dispatch
	// has to be one that can actually be resumed.
	if s.keepConversation && resumable {
		out.conversation = conversation
	}
	if out.events == nil {
		// Only reachable if the loop never got an exec past the fallback
		// above, which needs a first attempt that resumed something --
		// and a first attempt has nothing to resume. An empty parse
		// reads as "this run said nothing", which is what happened.
		out.events = parseEvents("")
	}
	return out
}

// retryable reports whether the attempt just finished is one worth
// running again: a capacity refusal and nothing else. A cap that
// tripped is grain stopping the run on purpose; a quota refusal is
// hours away from clearing and belongs to dispatch's pause rather than
// to a backoff; an ordinary failure -- and a run that simply finished --
// would only be repeated.
func (f *Framework) retryable(events *parsedEvents, runErr error, diagnostic string, cap *turnCap) bool {
	if cap.tripped() {
		return false
	}
	if usageLimitFailure(events.resultText+"\n"+diagnostic, runErr) != nil {
		return false
	}
	return capacityFailure(events.resultText+"\n"+diagnostic, runErr) != nil
}

// interruptedStatuses are the terminal statuses that say agy did not
// finish the run, as opposed to finishing it badly. ERROR is the one the
// failing dispatches are full of -- agy's own word for its internal
// error path, and the status carrying "Agent execution terminated due to
// error". TIMEOUT is agy's --print-timeout expiring, CANCELLED something
// stopping the process; neither is a sentence about the task either.
//
// FAILURE is deliberately absent, and it is the interesting omission. It
// is the status a real provider refusal arrives under (capacity.go's own
// live probe), so its text is what the quota and capacity classifiers
// read, and a FAILURE they could not classify is a failure nothing in
// this package can tell from a run that ended on its own terms. Leaving
// it terminal keeps this loop off the one status that might be a verdict.
// Any status agy grows later is terminal for the same reason, exactly as
// succeeded() fails closed in the other direction.
var interruptedStatuses = map[string]bool{
	"ERROR":     true,
	"TIMEOUT":   true,
	"CANCELLED": true,
	"CANCELED":  true,
}

// interruption names what cut an attempt short, for the kind of ending
// this package can pick back up rather than report: agy stopping on one
// of the statuses above, a stream that ended with no terminal event at
// all, or the model-turn deadline grain enforced itself. "" is every
// other ending, and a run that ends on one of those ends where it did.
//
// The order matters. A successful terminal result is final whatever the
// process did afterwards: agy has already given the run its answer, and
// resuming would ask a finished conversation to finish again. A turn cap
// that tripped is grain ending the run deliberately. A quota refusal is
// hours from clearing and belongs to dispatch's pause. Only what is left
// over is read as an interruption, and the phrase returned is what the
// transcript note says the run was cut off by.
func (f *Framework) interruption(events *parsedEvents, runErr error, diagnostic string, s attemptSetup) string {
	switch {
	case events.sawResult && events.resultErr == nil:
		return ""
	case s.cap.tripped():
		return ""
	case usageLimitFailure(events.resultText+"\n"+diagnostic, runErr) != nil:
		return ""
	case s.deadline.tripped():
		v, _ := s.deadline.trippedVerdict()
		return "a model turn that went quiet for " + v.limit.String() + " (" + v.tier + "), which grain cancelled"
	case events.sawResult:
		if !interruptedStatuses[events.resultStatus] {
			return ""
		}
		// agy's own words rather than this package's rendering of them:
		// "Agent execution terminated due to error" is what one of these
		// looks like in a transcript, and it is all that distinguishes
		// one from the next.
		return "agy ending the run in status " + events.resultStatus + ` ("` + firstLine(events.resultText) + `")`
	case runErr != nil:
		// No terminal event and a process that failed: agy died with the
		// run still open, which is the same interruption as ERROR with
		// one less thing said about it.
		return "agy exiting without finishing the run"
	default:
		// No terminal event and no error either -- a capture that stops
		// mid-stream. Run's own parseErr branch is what reports it today.
		return "agy's stream ending without a terminal result event"
	}
}

// firstLine keeps a transcript note to one line of whatever agy said,
// the whole of it being in the transcript a few lines above anyway.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// interruptedNote is what a run says in its transcript for an attempt it
// resumed after the last one was cut off. It is deliberately not
// agent.RetryNote's sentence: that one says the provider refused the run,
// which is the one thing nobody knows about these.
func interruptedNote(attempt int, why string) string {
	return fmt.Sprintf("* this run was cut off by %s; attempt %d resumed the same conversation "+
		"in the same sandbox", why, attempt)
}

// appendCapture adds one attempt's stdout to the run's whole capture,
// terminating it if the attempt did not. A killed agy can stop
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
