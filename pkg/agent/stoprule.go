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

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent/stoprule"
)

// The stop rule itself -- whether a run that has just ended its turn may
// end there -- lives in pkg/agent/stoprule, which imports nothing of
// grain's so the Claude Code Stop hook can apply it from a binary as small
// as grain-agentd. What is here is the rule as the rest of grain names it,
// plus the parts that need a Result.

// StopDecision is what the stop rule said about one stop.
type StopDecision = stoprule.Decision

// SessionContinueReason and BackgroundContinueReason are the rule's two
// nudges (stoprule's own).
const (
	SessionContinueReason    = stoprule.SessionContinueReason
	BackgroundContinueReason = stoprule.BackgroundContinueReason
)

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
type StopBlock = stoprule.Block

const (
	StopViaHook   = stoprule.ViaHook
	StopViaResume = stoprule.ViaResume
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

// DecideStop applies the stop rule (stoprule.Decide) to a run that is
// about to end: interactive says whether somebody is sitting with the task,
// finalText is the agent's last message, and calls is every tool call the
// run has made so far.
func DecideStop(interactive bool, finalText string, calls []ToolCall) StopDecision {
	return stoprule.Decide(interactive, finalText, stopCalls(calls))
}

// DecideStopForRun applies either QATreatmentPolicy (when requireOpenPR is true)
// or DefaultPolicy to a run that is about to end.
func DecideStopForRun(interactive, requireOpenPR bool, finalText string, calls []ToolCall) StopDecision {
	if requireOpenPR && !interactive {
		return stoprule.DecideWith(stoprule.QATreatmentPolicy(), interactive, finalText, stopCalls(calls))
	}
	return stoprule.Decide(interactive, finalText, stopCalls(calls))
}

// PromisesMoreWork reports whether an agent's final message promises
// further work it has not done (stoprule.PromisesMoreWork).
func PromisesMoreWork(text string) bool { return stoprule.PromisesMoreWork(text) }

func stopCalls(calls []ToolCall) []stoprule.Call {
	out := make([]stoprule.Call, len(calls))
	for i, c := range calls {
		out[i] = stoprule.Call{Name: c.Name, Arguments: c.Arguments, IsError: c.IsError}
	}
	return out
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
	merged.Usage = first.Usage.Add(next.Usage)
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
