package claude

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/bwsalmon/grain/pkg/agent"
)

// This file is what claude adds to the retry policy every framework
// shares.
//
// agent/retry.go has the policy itself and the reasoning behind it: a
// run the provider refused mid-flight -- an Anthropic 529
// overloaded_error, a 503 from a gateway in front of it -- costs a
// dispatch, a granule VM boot, a fresh clone and everything the agent had
// written but not pushed, and none of that has to be spent at the moment
// the refusal arrives, because Framework.Run is still holding the
// sandbox. So it backs off and execs claude again against that same
// sandbox, with the same --mcp-config, inside the runtime budget the run
// already had.
//
// # Resuming the session, rather than starting the task again
//
// The sandbox is not the only thing a refusal used to cost. Restarting
// the task in it -- the cheap variant task 220 shipped, and the one this
// file still falls back to -- throws away every model turn the attempt
// had already spent: the repository it had read, the tests it had run,
// the half-written change it was in the middle of explaining to itself.
// claude can be asked to keep those too. Every event on a
// --output-format stream-json run carries a session_id, and --resume
// <session-id> starts the next exec on that session, so the retry sends
// a short "you were interrupted, carry on" turn (agent.ResumePrompt)
// instead of the prompt again -- the conversation it is joining already
// has the prompt, and repeating it reads as "start over" to a model that
// had got half way.
//
// Where the session lives matters and is worth stating, because it is
// the reason this works at all. Unlike agy, claude is not given a
// private HOME per run: it is exec'd with the controller's own
// environment and working directory, so its session store (~/.claude,
// keyed by that directory) is the same one on every attempt of a run and
// is still there when the retry starts. Nothing here creates that store
// or cleans it up -- claude has been writing a session per run into it
// since long before this file resumed anything.
//
// # The same session, one dispatch later
//
// That session store outliving the exec is also what makes it outlive
// the *run*, and this file now does both. A run whose session grain kept
// (agent.Result.ResumableSession, stored against the task as
// model.SuspendedSession) hands it back on the task's next dispatch as
// agent.RunConfig.ResumeSession, and the first exec of that dispatch
// resumes it instead of starting the task's conversation from nothing.
// What that recovers is everything the earlier run had read, run and
// worked out before it ran out of wall clock, met the account's usage
// limit or stopped to ask a person something -- which is the larger
// share of a long run, and is otherwise re-derived from the repository
// on every attempt.
//
// claude was the only one of the three frameworks that could do this,
// and the reason is the one already stated above: its session store is
// the controller's own ~/.claude, keyed by HOME and working directory,
// and both are the same on every dispatch this deployment makes.
// agent/codex and agent/antigravity each built a *private* home per run
// and deleted it when Run returned, so their threads and conversations
// did not exist by the time a second dispatch could ask for one. Both
// now take a per-task home that outlives the run from
// pkg/agent/sessionstore instead, which is the same idea arrived at from
// the other end: what claude gets from a session store it does not own,
// those two get from one grain builds and prunes. Nothing here changed
// for it, and claude still takes no store -- ~/.claude is already
// exactly that.
//
// Two things differ from the in-run resume, and both are in the switch
// in runAttempts. The turn sent is the whole prompt behind
// agent.SuspendedPreamble rather than agent.ResumePrompt's nudge: the
// task may have been edited, a human may have answered the question the
// run stopped on, and the sandbox is a new one with a fresh clone, none
// of which the conversation can know. And a refused resume is read
// differently, since a session from a previous dispatch is one the
// controller's own store may since have pruned -- see the fallback
// below, which drops the session without dropping the flag.
//
// # What this rests on, and what happens when it is not there
//
// That --resume composes with the argv a grain run actually uses --
// `claude -p --tools '' --mcp-config <file> --strict-mcp-config
// --allowedTools ... --output-format stream-json --verbose`, with the
// prompt on stdin -- is a fact about somebody else's binary, and no stub
// runner can establish it: a fake answers whatever the test told it to.
//
// It has been measured, against claude-code 2.1.197, and the transcript
// is docs/hand-runs/2026-09-08-claude-resume.md. What it says:
//
//   - --resume takes that whole argv, and the resumed exec still reads
//     its new turn from stdin.
//   - the model is sent the earlier conversation -- the first exec's
//     prompt, the assistant turn it produced, then the new turn --
//     replayed by claude from $HOME/.claude/projects/<cwd>/<session>.jsonl.
//     Which is also why the session store's location matters above: it
//     is keyed on HOME and the working directory and on nothing else.
//   - a session whose only turn the *provider refused* is written to that
//     store anyway, is resumable, and still carries the refused turn's
//     prompt. That is the only case this loop ever resumes after, and it
//     is not implied by the other two.
//   - claude asked for a session it does not have exits 1 with no
//     "system"/"init" event and no API call at all, which is what makes
//     the fallback below a sound test rather than a guess.
//
// live_resume_stub_test.go is that measurement written down, four probes
// that need a claude binary and no credential (they stub /v1/messages in
// the test process), one of which drives this very loop through a refusal
// and reads what the retry sent. .github/workflows/live-frameworks.yml
// runs them nightly ahead of its credential gate.
//
// What is still unmeasured is the part a stub cannot reach:
// TestLiveClaudeResumesASessionAcrossExecs puts a real Anthropic behind
// the resumed session, and as of 2026-09-08 the live-claude job has no
// CLAUDE_CODE_OAUTH_TOKEN -- the environment secret it names has never
// been created -- so it has never run. On this build that covers nothing
// the probes above do not, since the conversation is replayed locally;
// it is what would notice a claude that moved the session server-side.
//
// resumeRefusedNote and the resumable flag are that fallback: an attempt
// that passed --resume and died without claude ever opening a session --
// no "system"/"init" event, which is the first line of every stream-json
// run, written before it authenticates -- is read as the binary refusing
// the argv rather than as anything about this run. The loop drops the
// flag for good, notes it in the transcript, and retries from scratch
// with agent.RestartPreamble in front of the prompt: the cheap variant,
// which needs no session plumbing and is most of the saving on its own.
// It costs one process start of a couple of seconds, and it means a
// build that never supported the flag degrades to "retry in place"
// instead of turning a provider outage into a hard failure.
//
// # A refusal reaches this loop late, and grain leaves it that way
//
// claude retries a refused API call itself, inside the exec, before it
// reports anything. Measured on 2.1.197 with the argv above, a
// persistent 529 costs ten internal attempts and 2m51s of wall clock
// before the terminal "API Error: 529 Overloaded" event capacity.go
// matches even exists -- and agent.RetryPolicy's own 30 seconds come on
// top of that. So a run refused twice can spend around six minutes
// inside Framework.Run with nothing on its transcript, and
// agent.DefaultTransientRetries is really two grain retries layered over
// ten claude ones apiece. (Ten is not a constant of the binary: the same
// stub answering a bare `claude -p` was given up on after two. It is
// what this argv and this build do, which is what a grain run gets.)
//
// That is the vendor's default rather than anything grain chose, so task
// 410 asked whether a run should bound it -- CLAUDE_CODE_MAX_RETRIES,
// which live_resume_stub_test.go already sets to 0 so that a probe about
// sessions does not sit through somebody else's backoff, and which
// measurably takes 0 (no internal retry at all) and 1 (one). The answer
// is no, grain does not set it, and the reasoning belongs here rather
// than in a closed task:
//
//   - The internal retry is the cheapest backoff anyone in this stack
//     has. It holds the sandbox open and spends nothing else: no
//     dispatch, no granule VM boot, no clone, and no model turn either --
//     a refused request produces no assistant turn, so the conversation
//     and --max-turns are untouched by it. A grain retry costs a process
//     start, a resumed conversation and one of a budget of two.
//   - It is also the better-informed backoff. Anthropic answers a 529
//     with its own retry_delay_ms and claude waits exactly that (534ms,
//     then 1245ms, out to 32595ms); agent.RetryPolicy waits a flat 30s
//     because at that point there is nothing left to read a hint off.
//   - Bounding it moves ordinary short outages onto that budget of two.
//     grain's whole ladder is 90 seconds of backoff against claude's
//     three minutes, so with CLAUDE_CODE_MAX_RETRIES=0 an overload
//     lasting a couple of minutes -- the common kind, the kind claude
//     currently absorbs without grain ever hearing about it -- would
//     spend both retries and put the task back in the queue, paying the
//     dispatch and the fresh clone this whole file exists to avoid.
//     Those three minutes are part of why a refusal reaching this loop
//     is rare enough that two retries are enough for it.
//
// What is actually wrong with the default is that it is invisible, and
// that is a reporting problem rather than a policy one: six quiet
// minutes inside Framework.Run read as a hung run to whoever is watching
// the row. So the number is written down here, in agent/retry.go beside
// the retry budget it sits under, and in
// docs/hand-runs/2026-09-08-claude-resume.md §5 -- the three places
// somebody working out where a slow run's time went would look.
//
// A deployment that disagrees does not need a change here. Nothing in
// this package sets CLAUDE_CODE_MAX_RETRIES of its own, and an operator
// who sets it on the controller has it reach claude untouched -- inside
// os.Environ() under agent.LocalHost, and named in claude.go's
// operatorVars for a run host, which inherits nothing else of the
// daemon's environment: 0 to leave grain's backoff, budget and
// transcript notes as the only retry policy in force, 1 to keep a single
// cheap attempt and hear about the rest quickly. claude_test.go's
// TestRunLeavesClaudesOwnAPIRetryAlone and
// TestRunForwardsAnOperatorsOwnSettings are what keep that hatch open.
//
// The other two have since been measured and decided the same way, on
// materially smaller numbers. codex makes thirty internal requests over
// 25 seconds rather than ten over 2m51s, with no environment variable to
// leave alone, since it takes the bound per model provider and refuses to
// override a key on the built-in one (agent/codex/retry.go,
// docs/hand-runs/2026-09-08-codex-retry.md). agy does not retry an HTTP
// refusal at all -- one request, 0.15 seconds -- and has no flag,
// variable or setting that would change it, so there was nothing to
// decide about beyond writing the number down
// (agent/antigravity/resume.go, docs/hand-runs/2026-09-08-agy-retry.md).
//
// So claude's three minutes are the outlier of the three, in both
// directions: the most outage absorbed before grain hears anything, and
// the only one long enough to make a refused run look hung.
//
// # The turn budget is the run's, not each exec's
//
// claude enforces RunConfig.MaxTurns itself, through --max-turns, which
// means an unadjusted retry would hand the run a whole second budget. So
// each attempt is passed what is left of the run's own: the turns
// earlier attempts spent, counted off their streams, subtracted from the
// cap. A run with no turns left is not retried at all -- there would be
// nothing for the attempt to do.

// resumeRefusedNote is what a run says in its transcript when the resume
// flag turned out not to compose with the rest of the argv, and it fell
// back to retrying from scratch. It names the flag because the person
// reading it has to decide whether this package should stop passing it
// -- see this file's header.
const resumeRefusedNote = "! claude refused the invocation resuming this run's session " +
	"(--resume with -p and the prompt on stdin) and opened no session at all: grain retried " +
	"this run from the start in the same sandbox instead. The model turns spent before the " +
	"provider refusal were lost with the session."

// attemptSetup is everything the attempts of one run share, built once
// in Framework.Run and never rebuilt: the argv without its --max-turns
// and --resume (the two flags that differ per attempt), the environment
// carrying this run's credential, the three prompts (the task's own, the
// nudge a resumed attempt sends instead, and the prompt again behind
// agent.RestartPreamble for an attempt with no session to resume), and
// the live transcript sink.
//
// The sink is opened O_TRUNC once, so reopening it for a retry would
// throw away everything the run had already streamed; the same writer is
// handed to every attempt instead.
type attemptSetup struct {
	args          []string
	env           []string
	prompt        string
	resumePrompt  string
	restartPrompt string
	// resumeSession is the session a previous *dispatch* of this task
	// left suspended (agent.RunConfig.ResumeSession), which the first
	// attempt joins rather than starting the conversation over, and ""
	// for every run that has no such session. suspendedPrompt is the
	// turn it sends when it does -- the whole prompt behind
	// agent.SuspendedPreamble, not resumePrompt's nudge, for the reason
	// that preamble's own doc comment gives.
	resumeSession   string
	suspendedPrompt string
	tee             io.Writer
	maxTurns        int
	// onSession is agent.RunConfig.OnResumableSession: told the session
	// an attempt is in as soon as its stream names it, for as long as
	// this run is still willing to hand one on (attemptsResult.session).
	onSession func(string)
}

// attemptsResult is what a run's attempts amount to: every attempt's
// stream-json in order, the last one's own subprocess error and its own
// parsed stream, and what the loop has to say about itself for the
// transcript.
type attemptsResult struct {
	capture string
	runErr  error
	// events is the *last* attempt's own events, and it is what the run
	// is classified from -- not a parse of the whole capture, which
	// holds one attempt's stream per exec.
	//
	// The difference is only visible when the last attempt ended without
	// a terminal result event of its own: a claude killed mid-stream, or
	// one that died before it wrote one. A whole-capture parse then
	// still carries the *previous* attempt's terminal event, so a run
	// refused for capacity and then killed twice for some unrelated
	// reason was reported to the caller as a capacity refusal -- and
	// recorded by orchestrator.RunDispatch as model.CapacityOutcome,
	// which no failure streak counts. The refusal belonged to the
	// attempt that met it; what the run ended as is what its last
	// attempt said, exactly as agent/codex's own parser already had it
	// (its parseEvents starts a fresh reading at each thread.started).
	//
	// Never nil once runAttempts has returned: the first exec always
	// sets it, and only an exec that opened no session at all (the
	// refused-resume fallback below) leaves the attempt before it
	// standing.
	events *parsedEvents
	notes  []string
	// session is the session claude last named on any of this run's
	// attempts -- what a later dispatch of the same task can be resumed
	// into (agent.Result.ResumableSession). Not the last attempt's own
	// parse, unlike events above: an exec that died before opening a
	// session says nothing about which session this run's work is in,
	// and the one the attempt before it named is still the right answer.
	//
	// Empty when claude never named one, and deliberately empty once a
	// resume has been refused outright: a session this build will not
	// reopen is not one to hand the next dispatch.
	session string
}

// runAttempts execs claude for one grain run: once in the ordinary case,
// and again -- in the same sandbox, on the same session -- when the
// provider refused it mid-flight.
func (f *Framework) runAttempts(ctx context.Context, s attemptSetup) attemptsResult {
	var (
		capture strings.Builder
		out     attemptsResult
		// The session to resume: a previous dispatch's, if this task
		// left one suspended, and after the first exec whatever claude
		// last named on its own stream.
		session = s.resumeSession
		// True while that session is the suspended one rather than one
		// this run opened -- which is the whole difference between the
		// first exec joining a conversation from another dispatch and a
		// retry joining this run's own (see the switch below, and the
		// two ways a refused resume is read).
		carried = session != ""
		// True until an exec that passed --resume was refused outright,
		// after which this run stops asking for one.
		resumable = true
		// Turns earlier attempts spent, which come off this attempt's
		// own --max-turns; see this file's header.
		spentTurns int
		// How many of this run's retries have been spent.
		spentRetries int
		attempt      = 0
	)
	for {
		attempt++
		remaining := s.maxTurns - spentTurns
		if s.maxTurns > 0 && remaining < 1 {
			// Nothing left to give the attempt. The cap that ran out is
			// what this run ends on, reported by the capture's own
			// terminal event exactly as it would be without a retry.
			break
		}

		resuming := resumable && session != "" && (carried || attempt > 1)
		resumeID := ""
		stdin := s.prompt
		switch {
		case resuming && carried:
			// A conversation from a previous dispatch of this task. It
			// holds what that run worked out and nothing since, and the
			// sandbox it worked in is gone -- so it gets the task in
			// full, behind the paragraph that says both
			// (agent.SuspendedPreamble).
			resumeID, stdin = session, s.suspendedPrompt
		case resuming:
			// The conversation already has the prompt, and everything
			// the attempt did before it was cut off; what it does not
			// have is the fact that the interruption came from outside.
			resumeID, stdin = session, s.resumePrompt
		case attempt > 1:
			// The task from the top, but into a sandbox holding an
			// earlier attempt's work: agent.RestartPreamble is what says
			// so, and what keeps the model from reading its own half
			// finished work as somebody else's.
			stdin = s.restartPrompt
		}

		// A cancel of its own for each attempt, so nothing a finished
		// attempt left behind can reach the next one.
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		// The session goes on record from the stream itself, the moment
		// claude names it, so a run the daemon is stopped in the middle
		// of still leaves a conversation behind (sessionFromLine). Only
		// while this run would hand a session on at all: once a resume
		// has been refused outright, the answer at the end is "" and
		// nothing earlier may say otherwise.
		var report func(string)
		if resumable {
			report = s.onSession
		}
		tee := agent.NewSessionWatcher(s.tee, sessionFromLine, report)
		stdout, runErr := f.run.Run(attemptCtx, attemptArgs(s, remaining, resumeID), stdin, s.env, tee)
		cancelAttempt()
		if errors.Is(runErr, agent.ErrDetached) {
			// Still running, in a run host this daemon has let go of: not
			// an attempt that ended, so nothing here may read it as one or
			// retry it. The next daemon reads its stream (ParseResult).
			out.runErr = runErr
			out.session = session
			break
		}

		appendCapture(&capture, stdout)
		out.runErr = runErr
		p := parseEvents(stdout)
		if p.sessionID != "" {
			session = p.sessionID
		}

		// A resumed exec claude never opened a session for: it died with
		// no "system"/"init" event, which is the first line of every
		// stream-json run and is written before it authenticates. No
		// model turn was spent, so neither a retry nor any of the turn
		// budget is spent on the attempt that replaces it.
		//
		// What that means depends on where the session came from, and
		// the two readings are worth keeping apart:
		//
		//   - a session claude itself named minutes ago, on this run's
		//     own stream, certainly exists. So the refusal is about the
		//     argv -- this repository asking for a flag combination the
		//     binary does not support -- and the flag is dropped for the
		//     rest of the run, and with it any claim that this run left
		//     a session a later dispatch could join.
		//   - a session a previous dispatch left suspended was last
		//     written hours or days ago, into a store on the
		//     controller's disk that grain neither owns nor prunes. The
		//     likelier reading is that it is simply not there anymore,
		//     which says nothing about the argv. So only the session is
		//     dropped: this run starts the task from its prompt, and a
		//     provider refusal later in it may still resume the session
		//     claude is about to open.
		if resuming && runErr != nil && !p.sawInit {
			if carried {
				carried, session = false, ""
				out.notes = append(out.notes, agent.SuspendedResumeRefusedNote)
			} else {
				resumable = false
				out.notes = append(out.notes, resumeRefusedNote)
			}
			attempt--
			continue
		}
		// Past the fallback, which is where both of these belong:
		// whatever session this run is in from here is one its own
		// execs opened rather than the suspended one, and an exec that
		// opened no session at all -- which spent no model turn and says
		// nothing about this run -- does not become what the run is
		// classified from.
		carried = false
		out.events = p
		spentTurns += attemptTurns(p)

		if !retryable(p, runErr) {
			break
		}
		if !f.retry.WaitBeforeRetry(ctx, spentRetries+1) {
			break
		}
		spentRetries++
		out.notes = append(out.notes, agent.RetryNote(attempt+1, resumable && session != ""))
	}
	out.capture = capture.String()
	// What a later dispatch of this task may be resumed into -- but only
	// where this build has not already refused to reopen a session it
	// was handed. A token grain shows a person and hands to the next
	// dispatch has to be one that can actually be resumed.
	if resumable {
		out.session = session
	}
	if out.events == nil {
		// Only reachable if the loop never ran an attempt at all, which
		// the turn-budget guard at the top can do. An empty parse reads
		// as "this run said nothing", which is what happened.
		out.events = parseEvents("")
	}
	return out
}

// attemptArgs is one attempt's argv: the run's own, plus the turn budget
// this attempt is allowed and the session it is picking up, if any.
//
// remaining is only meaningful when a cap is configured at all -- with
// none, the flag is omitted entirely, which is what claude reads as
// "unlimited" (see Framework.Run's own note on why a large number is not
// the same thing). resumeID is empty for a first attempt with no
// suspended session to join, for a run whose stream never named a
// session, and for every attempt after claude refused the flag.
//
// --max-turns is undocumented and live: docs/claude-surface.md's flag
// listing, taken off `claude --help`, does not have it, and the probe
// table in the same capture has the binary accepting it in this argv.
// That is the argument for probing a flag rather than reading for it --
// and the reason to watch this one, since a flag nobody documents is a
// flag nobody promised to keep.
func attemptArgs(s attemptSetup, remaining int, resumeID string) []string {
	args := append([]string(nil), s.args...)
	if s.maxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(remaining))
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	return args
}

// retryable reports whether the attempt just finished is one worth
// running again: a capacity refusal and nothing else.
//
// The failure test comes first and is not a formality. capacity.go
// matches phrases against text this run produced, and a run that
// *finished* has spent an hour quoting CI logs and HTTP errors into that
// same stream -- so a successful run whose final answer mentions a 503
// somebody else got must not be run a second time. A run that has
// already ended in claude's own words (resultErr) or died under it
// (runErr) is the only kind this reads at all.
//
// A cap that ran out is grain stopping the run on purpose and not a
// refusal; a quota refusal is hours away from clearing and belongs to
// dispatch's pause rather than to a 30-second backoff; an ordinary
// failure would only be repeated.
func retryable(p *parsedEvents, runErr error) bool {
	if runErr == nil && p.resultErr == nil {
		return false
	}
	if p.resultSubtype == maxTurnsSubtype {
		return false
	}
	if usageLimitFromFailure(p.resultText, runErr) != nil {
		return false
	}
	return capacityFailure(p.resultText, runErr) != nil
}

// attemptTurns is how much of the run's --max-turns budget one attempt
// spent: claude's own count from the terminal result event when it wrote
// one, and a count of the assistant messages in the stream when it did
// not -- an attempt the provider cut off mid-flight never reports a
// total, and reading that as zero would hand the retry the whole budget
// back.
func attemptTurns(p *parsedEvents) int {
	if p.numTurns > 0 {
		return p.numTurns
	}
	return p.assistantMessages
}

// appendCapture adds one attempt's stdout to the run's whole capture,
// terminating it if the attempt did not. A killed claude can stop
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
