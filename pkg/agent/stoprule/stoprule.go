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

// Package stoprule is grain's stop rule on its own: whether a run that has
// just ended its turn may end there. It imports only hookpolicy, so the
// Claude Code Stop hook (pkg/agent/claude/stophook) can evaluate a staged
// policy file or fall back to DefaultPolicy from a binary as small as
// grain-agentd; pkg/agent re-exports it (agent.DecideStop) for the
// orchestrator's fallback.
package stoprule

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
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
// grain installs in each run's settings (pkg/agent/claude/stophook); for
// every other framework orchestrator.RunDispatch applies it after the CLI
// has exited and resumes the same session once with the same nudge
// (agent.StopRuleFramework says which is which). Either way a run is sent back
// at most once per turn, so the rule cannot loop: what it buys is one
// more turn, and what a wrong answer costs is the same one turn.

// Call is the part of one tool call the rule reads: agent.ToolCall's Name,
// Arguments and IsError. Name is the tool's bare name, as pkg/mcp
// registers it ("ask_question"), never a CLI's namespaced spelling.
type Call struct {
	Name      string
	Arguments map[string]any
	IsError   bool
}

// Decision is what the stop rule said about one stop.
type Decision struct {
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
	"`request_secret`, ask for more access with `request_access`, park on another task with `wait_for_task`, " +
	"suspend with `resume_later`, or say no change is needed with `nothing_needed`. If you meant to keep working, keep working now."

// Block is one time the stop rule sent a run back, recorded on the
// run's agent.Result (Result.StopBlocks) so that the run's transcript and
// its tool census both show it.
type Block struct {
	// Via is how the rule was applied: ViaHook (the CLI's own hook
	// asked) or ViaResume (grain resumed the session afterwards).
	Via string `json:"via"`
	// Reason is the nudge the run was sent back with.
	Reason string `json:"reason"`
}

const (
	ViaHook   = "hook"
	ViaResume = "resume"
)

// defaultClosingTools are the tools whose call ends a background run on an
// answer: each one leaves something in the task's conversation, or parks
// the task on something, or (open_pull_request) only works on a branch
// that has been pushed. add_review_comment is how a run reviewing
// another task's change says what it found, and counts for that reason.
var defaultClosingTools = []string{
	"comment_on_issue",
	"ask_question",
	"nothing_needed",
	"request_secret",
	"request_access",
	"propose_config_change",
	"open_pull_request",
	"add_review_comment",
}

// defaultClosingCalls are the conditional tool-call rules that close a
// background run: resume_later on success, wait_for_task when it parks,
// and run_command when it ran git push without failing.
var defaultClosingCalls = []hookpolicy.ClosingCallRule{
	{Tool: "resume_later", RequireSuccess: true},
	{Tool: "wait_for_task", ArgBoolTrue: "park"},
	{Tool: "wait_for_task", ArgNonEmpty: "resume_at"},
	{Tool: "run_command", RequireSuccess: true, Arg: "command", Substring: "git push"},
}

// promisePhrases are the ways an agent says, at the end of a message,
// that it is about to do more -- matched lower-cased, against the message's
// last two sentences only. Deliberately short and specific: a false
// positive costs one extra turn, but a phrase as loose as "I'll" would
// catch "I'll leave that with you" and send every polite answer back.
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
	"now i'll",
	"next i'll",
	"next, i'll",
	"i'll now",
	"i'll fix",
	"i'll run",
	"i'll push",
}

// promisePrefixes match only at the start of a sentence.
var promisePrefixes = []string{
	"let me ",
	"let's ",
}

const progressivePattern = `^(checking|looking|investigating|digging|working|running|rerunning|retrying|waiting|reading|fetching|pulling|pushing|fixing|starting|trying|continuing)\b.*\b(now|next|shortly|again)\b`

// progressive is a closing sentence that narrates work in progress --
// "Checking the CI logs now.", "Looking into the failing job next" -- an
// -ing verb of the agent's own work at the start, and a word that puts it
// after this message.
var progressive = regexp.MustCompile(progressivePattern)

// sentenceEnd splits text into sentences, near enough for a check that
// only reads the last two of them.
var sentenceEnd = regexp.MustCompile(`[.!?…]+(\s+|$)|\n+`)

// DefaultToolPrefixes are the prefixes Claude Code puts in front of grain's
// MCP tool names in a session transcript.
var DefaultToolPrefixes = []string{"mcp__grain-sandbox__", "mcp_grain-sandbox_"}

// DefaultPolicy returns this build's stop rule as data for staging in a
// per-run hook policy file (hookpolicy.StopPolicy), and is also the fallback
// claude-stop-hook evaluates when no policy file was staged.
func DefaultPolicy() hookpolicy.StopPolicy {
	return hookpolicy.StopPolicy{
		ToolPrefixes:             slices.Clone(DefaultToolPrefixes),
		SessionContinueReason:    SessionContinueReason,
		BackgroundContinueReason: BackgroundContinueReason,
		ClosingTools:             slices.Clone(defaultClosingTools),
		ClosingCalls:             slices.Clone(defaultClosingCalls),
		TrailingColon:            true,
		LastSentences:            2,
		PromisePhrases:           slices.Clone(promisePhrases),
		PromisePrefixes:          slices.Clone(promisePrefixes),
		ProgressivePattern:       progressivePattern,
		ConditionalPrefixes:      []string{"if "},
		ConditionalPhrases:       []string{" if ", "want me to", "would you like"},
	}
}

// Decide applies this build's stop rule (DefaultPolicy) to a run that is
// about to end: interactive says whether somebody is sitting with the task,
// finalText is the agent's last message, and calls is every tool call the
// run has made so far.
//
// It knows nothing about how many times it has been asked: keeping the
// rule to once per turn is the caller's job (stop_hook_active for the
// hook, one resume for the fallback).
func Decide(interactive bool, finalText string, calls []Call) Decision {
	return DecideWith(DefaultPolicy(), interactive, finalText, calls)
}

// DecideWith evaluates policy directly, consulting none of this binary's
// compiled-in lists or nudges unless policy leaves a required reason empty.
func DecideWith(policy hookpolicy.StopPolicy, interactive bool, finalText string, calls []Call) Decision {
	if policy.Interactive != nil {
		interactive = *policy.Interactive
	}
	if interactive {
		if PromisesMoreWorkWith(policy, finalText) {
			reason := policy.SessionContinueReason
			if reason == "" {
				reason = SessionContinueReason
			}
			return Decision{Block: true, Reason: reason}
		}
		return Decision{}
	}
	if MadeClosingMoveWith(policy, calls) {
		return Decision{}
	}
	reason := policy.BackgroundContinueReason
	if reason == "" {
		reason = BackgroundContinueReason
	}
	return Decision{Block: true, Reason: reason}
}

// MadeClosingMove reports whether calls include a closing move under
// DefaultPolicy.
func MadeClosingMove(calls []Call) bool {
	return MadeClosingMoveWith(DefaultPolicy(), calls)
}

// MadeClosingMoveWith reports whether calls include a move policy counts as
// closing a background run: any tool matched by ClosingTools (including
// errored calls, since a refused closing call still means the agent was
// trying to close), or any call matching a rule in ClosingCalls.
func MadeClosingMoveWith(policy hookpolicy.StopPolicy, calls []Call) bool {
	for _, c := range calls {
		for _, tool := range policy.ClosingTools {
			if hookpolicy.MatchWildcard(tool, c.Name) {
				return true
			}
		}
		for _, rule := range policy.ClosingCalls {
			if matchesClosingCall(rule, c) {
				return true
			}
		}
	}
	return false
}

func matchesClosingCall(r hookpolicy.ClosingCallRule, c Call) bool {
	if r.Tool == "" || !hookpolicy.MatchWildcard(r.Tool, c.Name) {
		return false
	}
	if r.RequireSuccess && c.IsError {
		return false
	}
	if r.ArgBoolTrue != "" {
		if v, _ := c.Arguments[r.ArgBoolTrue].(bool); !v {
			return false
		}
	}
	if r.ArgNonEmpty != "" {
		if v, _ := c.Arguments[r.ArgNonEmpty].(string); strings.TrimSpace(v) == "" {
			return false
		}
	}
	if r.Arg != "" {
		v, _ := c.Arguments[r.Arg].(string)
		if !strings.Contains(v, r.Substring) {
			return false
		}
	}
	return true
}

// PromisesMoreWork reports whether an agent's final message promises
// further work it has not done under DefaultPolicy.
func PromisesMoreWork(text string) bool {
	return PromisesMoreWorkWith(DefaultPolicy(), text)
}

// PromisesMoreWorkWith reports whether text promises further work according
// to policy alone.
func PromisesMoreWorkWith(policy hookpolicy.StopPolicy, text string) bool {
	text = strings.TrimSpace(stripCodeFences(text))
	if text == "" {
		return false
	}
	// A colon at the very end, past any markdown emphasis: "Let me check
	// the logs:" and "Next steps:" both introduce something the message
	// then does not contain.
	if policy.TrailingColon && strings.HasSuffix(strings.TrimRight(text, " \t\n*_`"), ":") {
		return true
	}
	if len(policy.PromisePhrases) == 0 && len(policy.PromisePrefixes) == 0 && policy.ProgressivePattern == "" {
		return false
	}
	n := policy.LastSentences
	if n <= 0 {
		n = 2
	}
	var prog *regexp.Regexp
	switch policy.ProgressivePattern {
	case "":
	case progressivePattern:
		prog = progressive
	default:
		prog, _ = regexp.Compile(policy.ProgressivePattern)
	}
	for _, s := range lastSentences(strings.ToLower(text), n) {
		s = strings.TrimLeft(s, " \t*_->#`\"'")
		if isConditional(policy, s) {
			continue
		}
		for _, pref := range policy.PromisePrefixes {
			if strings.HasPrefix(s, pref) {
				return true
			}
		}
		for _, p := range policy.PromisePhrases {
			if strings.Contains(s, p) {
				return true
			}
		}
		if prog != nil && prog.MatchString(s) {
			return true
		}
	}
	return false
}

// isConditional reports whether a sentence is an offer rather than a
// promise -- "If you want, I'll fix that too", "I'll check the rest if
// you'd like" -- which a session may end on: the person answers it.
func isConditional(policy hookpolicy.StopPolicy, s string) bool {
	for _, pref := range policy.ConditionalPrefixes {
		if strings.HasPrefix(s, pref) {
			return true
		}
	}
	for _, phrase := range policy.ConditionalPhrases {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
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
