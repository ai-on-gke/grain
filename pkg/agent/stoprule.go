package agent

import (
	"regexp"
	"strings"
)

// The stop rule: whether a run that has just ended its turn -- a reply
// with no tool call in it, which is how every framework's CLI ends a run
// -- is allowed to end there, or should be sent back once to finish what
// it started.
//
// It exists because a reply without a tool call ends the run, and agents
// do not always behave as if they knew it. The case that prompted it was
// the Director session (task 831), whose agent repeatedly ended on
// "Checking ... back in a minute" or "I'll keep going": the run stopped,
// nothing more happened, and the person had to send another message to
// get the work they had been promised.
//
// Two places apply it. Claude Code asks it itself, through the Stop hook
// grain installs in each run's settings (agent/claude's stophook.go); for
// every other framework orchestrator.RunDispatch applies it after the CLI
// has exited and resumes the same session once with the same nudge
// (StopRuleFramework says which is which). Either way a run is sent back
// at most once per turn, so the rule cannot loop: what it buys is one
// more turn, and what a wrong answer costs is the same one turn.

// StopDecision is what the stop rule said about one stop.
type StopDecision struct {
	// Block is true when the run should be sent back rather than allowed
	// to end.
	Block bool
	// Reason is the nudge it is sent back with -- what Claude Code's Stop
	// hook answers as its "reason", and the user turn the fallback
	// resumes the session with. Empty when Block is false.
	Reason string
}

// SessionContinueReason is the nudge for a session somebody is sitting
// with (an interactive or console task) whose last message promised more
// work. Plain text is a normal answer there, so only the promise is
// worth sending back; and the honest alternative to keeping going is
// saying so in the conversation and waiting, which is what chat is for.
const SessionContinueReason = "You said you would continue. Do it now, or use `chat` to tell the person and wait for them."

// BackgroundContinueReason is the nudge for a background task that tried
// to end without having made any closing move: nobody reads a background
// run's last message, so ending on one leaves the task with no answer,
// no question and no branch.
const BackgroundContinueReason = "A reply without a tool call ends your run, and this run has not finished: " +
	"nothing has been pushed and nothing has been reported. Before you stop, do one of these: push your " +
	"branch, leave your findings with `comment_on_issue`, ask with `ask_question`, request a credential with " +
	"`request_secret`, park on another task with `wait_for_task`, or say no change is needed with " +
	"`nothing_needed`. If you meant to keep working, keep working now."

// StopRuleFramework is the optional half of Framework that says its CLI
// enforces the stop rule from inside the run -- Claude Code's Stop hook
// -- so the orchestrator must not apply it a second time afterwards. A
// Framework that does not implement it (antigravity, codex, custom) gets
// the fallback: the rule applied to its Result, and one resumed turn.
type StopRuleFramework interface {
	Framework
	EnforcesStopRule() bool
}

// StopBlock is one time the stop rule sent a run back, recorded on the
// Result (Result.StopBlocks) so that the run's transcript and its tool
// census both show it.
type StopBlock struct {
	// Via is how the rule was applied: StopViaHook (the CLI's own hook
	// asked) or StopViaResume (grain resumed the session afterwards).
	Via string `json:"via"`
	// Reason is the nudge the run was sent back with.
	Reason string `json:"reason"`
}

const (
	StopViaHook   = "hook"
	StopViaResume = "resume"
)

// StopCensusTool is the name a run's stop blocks are counted under in
// its tool census (orchestrator.runTelemetry): not a tool the agent can
// call, which is why it is spelled so that no pkg/mcp tool name can
// collide with it.
const StopCensusTool = "grain:stop-blocked"

// StopNote is the line a run's transcript carries for a stop block.
func StopNote(b StopBlock) string {
	how := "grain's stop hook sent the agent back"
	if b.Via == StopViaResume {
		how = "grain resumed the session once"
	}
	return "* the agent ended its turn without finishing; " + how + " with: " + b.Reason
}

// DecideStop applies the stop rule to a run that is about to end:
// interactive says whether somebody is sitting with the task (an
// interactive or console session), finalText is the agent's last
// message, and calls is every tool call the run has made so far.
//
// A session may end on plain text -- that is how it answers -- unless
// that text promises more work (PromisesMoreWork). A background task may
// end only once it has made a closing move (MadeClosingMove).
//
// It knows nothing about how many times it has been asked: keeping the
// rule to once per turn is the caller's job (stop_hook_active for the
// hook, one resume for the fallback).
func DecideStop(interactive bool, finalText string, calls []ToolCall) StopDecision {
	if interactive {
		if PromisesMoreWork(finalText) {
			return StopDecision{Block: true, Reason: SessionContinueReason}
		}
		return StopDecision{}
	}
	if MadeClosingMove(calls) {
		return StopDecision{}
	}
	return StopDecision{Block: true, Reason: BackgroundContinueReason}
}

// closingTools are the tools whose call ends a background run on an
// answer: each one leaves something in the task's conversation, or parks
// the task on something, or (open_pull_request) only works on a branch
// that has been pushed. add_review_comment is how a run reviewing
// another task's change says what it found, and counts for that reason.
var closingTools = map[string]bool{
	"comment_on_issue":   true,
	"ask_question":       true,
	"nothing_needed":     true,
	"request_secret":     true,
	"open_pull_request":  true,
	"add_review_comment": true,
}

// MadeClosingMove reports whether calls include a move a background run
// may end on: one of closingTools, a wait_for_task that parks the task
// (park, or a resume_at time), or a pushed branch -- a run_command that
// ran `git push` and did not fail, the same narrow reading the census's
// own isPush takes.
//
// Errored calls of the closing tools count too. A tool that refused (a
// second ask_question, a malformed attachment) still means the agent was
// trying to close, and a nudge would only send it to try the same thing.
func MadeClosingMove(calls []ToolCall) bool {
	for _, c := range calls {
		if closingTools[c.Name] {
			return true
		}
		switch c.Name {
		case "wait_for_task":
			if park, _ := c.Arguments["park"].(bool); park {
				return true
			}
			if at, _ := c.Arguments["resume_at"].(string); strings.TrimSpace(at) != "" {
				return true
			}
		case "run_command":
			if command, _ := c.Arguments["command"].(string); !c.IsError && strings.Contains(command, "git push") {
				return true
			}
		}
	}
	return false
}

// promisePhrases are the ways an agent says, at the end of a message,
// that it is about to do more -- matched lower-cased, against the message's
// last two sentences only. Deliberately short and specific: a false
// positive costs one extra turn, but a phrase as loose as "I'll" would
// catch "I'll leave that with you" and send every polite answer back.
// "let me " and "let's " match only at the start of a sentence.
var promisePhrases = []string{
	"back in a minute",
	"back in a moment",
	"back shortly",
	"back with results",
	"back to you shortly",
	"i'll keep going",
	"i will keep going",
	"i'll continue",
	"i will continue",
	"i'll carry on",
	"continuing now",
	"checking now",
	"looking now",
	"on it now",
	"working on it now",
	"i'll check",
	"i will check",
	"i'll look into",
	"i'll take a look",
	"i'll report back",
	"i will report back",
	"i'll get back to you",
	"i'll let you know when",
	"i'll update you",
	"stand by",
	"hang tight",
	"one moment",
	"give me a moment",
	"give me a minute",
	"let me ",
	"let's ",
	"now i'll",
	"next i'll",
	"next, i'll",
	"i'll now",
	"i'll fix",
	"i'll run",
	"i'll push",
}

// progressive is a closing sentence that narrates work in progress --
// "Checking the CI logs now.", "Looking into the failing job next" -- an
// -ing verb of the agent's own work at the start, and a word that puts it
// after this message.
var progressive = regexp.MustCompile(`^(checking|looking|investigating|digging|working|running|rerunning|retrying|waiting|reading|fetching|pulling|pushing|fixing|starting|trying|continuing)\b.*\b(now|next|shortly|again)\b`)

// sentenceEnd splits text into sentences, near enough for a check that
// only reads the last two of them.
var sentenceEnd = regexp.MustCompile(`[.!?…]+(\s+|$)|\n+`)

// PromisesMoreWork reports whether an agent's final message promises
// further work it has not done: a future-tense continuation in its last
// two sentences ("back in a minute", "I'll keep going", "checking now",
// "let me look"), or a message that ends on a colon -- the agent
// introducing a step it then never took.
//
// Conservative by construction. It reads only the end of the message,
// matches only promisePhrases and the progressive pattern, skips an
// offer the person can take up ("if you want, I'll fix that too"), and
// ignores what is inside code fences, so an answer that quotes a command
// or says "let me" in its middle is left alone.
func PromisesMoreWork(text string) bool {
	text = strings.TrimSpace(stripCodeFences(text))
	if text == "" {
		return false
	}
	// A colon at the very end, past any markdown emphasis: "Let me check
	// the logs:" and "Next steps:" both introduce something the message
	// then does not contain.
	if strings.HasSuffix(strings.TrimRight(text, " \t\n*_`"), ":") {
		return true
	}
	for _, s := range lastSentences(strings.ToLower(text), 2) {
		s = strings.TrimLeft(s, " \t*_->#`\"'")
		if conditional(s) {
			continue
		}
		for _, p := range promisePhrases {
			if strings.HasPrefix(p, "let") {
				if strings.HasPrefix(s, p) {
					return true
				}
				continue
			}
			if strings.Contains(s, p) {
				return true
			}
		}
		if progressive.MatchString(s) {
			return true
		}
	}
	return false
}

// conditional reports whether a sentence is an offer rather than a
// promise -- "If you want, I'll fix that too", "I'll check the rest if
// you'd like" -- which a session may end on: the person answers it.
func conditional(s string) bool {
	return strings.HasPrefix(s, "if ") || strings.Contains(s, " if ") ||
		strings.Contains(s, "want me to") || strings.Contains(s, "would you like")
}

// lastSentences is the last n non-empty sentences of text, in order.
func lastSentences(text string, n int) []string {
	var out []string
	for _, s := range sentenceEnd.Split(text, -1) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// stripCodeFences drops everything between ``` fences, so a command or a
// log quoted at the end of an answer is not read as the agent's own
// words.
func stripCodeFences(text string) string {
	if !strings.Contains(text, "```") {
		return text
	}
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ContinueResult is the Result of a run the stop rule sent back once and
// whose continuation came back as next: one run's worth of tool calls,
// transcript and blocks, with what the continuation ended as -- its final
// text, its session -- winning wherever both have one. A nil next is a
// continuation that never started, and leaves the first ending standing
// with the block on record.
func ContinueResult(first, next *Result, block StopBlock) *Result {
	merged := *first
	merged.StopBlocks = append(append([]StopBlock(nil), first.StopBlocks...), block)
	merged.Transcript = joinTranscript(first.Transcript, StopNote(block))
	if next == nil {
		return &merged
	}
	merged.FinalText = next.FinalText
	merged.ToolCalls = append(append([]ToolCall(nil), first.ToolCalls...), next.ToolCalls...)
	merged.Transcript = joinTranscript(merged.Transcript, next.Transcript)
	merged.StopBlocks = append(merged.StopBlocks, next.StopBlocks...)
	if next.ResumableSession != "" {
		merged.ResumableSession = next.ResumableSession
	}
	return &merged
}

func joinTranscript(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return strings.TrimRight(a, "\n") + "\n\n" + b
}
