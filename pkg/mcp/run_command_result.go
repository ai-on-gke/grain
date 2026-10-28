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

// run_command_result.go is what both transports' run_command turn an exit
// status and two streams into, so that the two answer identically.
//
// Two things are added to the exit=/stdout:/stderr: shape they have
// always returned:
//
//   - a line when the *bound*, not the command, is what ended it. Every
//     run_command is bounded -- defaultRunCommandTimeout applies even
//     when the call passed no "timeout" at all -- and neither transport
//     used to say so: locally the answer was `exit=-1`, which is also
//     what "the command could not be started" looks like, and remotely a
//     bare `exit=124` from the guest's own `timeout`. Neither named the
//     number, and when it is the default it is a number the run never
//     chose and cannot see. The failure mode that costs is re-running the
//     same long command verbatim, twice, before concluding the sandbox is
//     broken.
//   - a cap on how much of the two streams comes back, described in
//     result_size.go.

import (
	"fmt"
	"strings"
	"time"
)

// runCommandBound is the bound one run_command call ran under.
type runCommandBound struct {
	d time.Duration
	// fromCaller distinguishes a bound the call asked for from
	// defaultRunCommandTimeout, which is the one worth explaining: a
	// run that passed no "timeout" has no way to know a bound applied,
	// let alone what it was.
	fromCaller bool
}

// resolveRunCommandBound reads the bound one call runs under: the same
// duration runCommandTimeout has always resolved, plus where it came
// from, which only the notices below need.
func resolveRunCommandBound(args map[string]any) runCommandBound {
	_, fromCaller := argFloat(args, "timeout")
	return runCommandBound{d: runCommandTimeout(args), fromCaller: fromCaller}
}

// seconds is the bound in the whole seconds the guest-side `timeout`
// coreutil takes, never below one: `timeout 0` means "no bound at all" to
// the coreutil, which is the opposite of what any sub-second bound asks
// for. runCommandTimeout already clamps a caller's own value to a second,
// so this only bites a test that has shrunk defaultRunCommandTimeout.
func (b runCommandBound) seconds() int {
	if s := int(b.d.Seconds()); s >= 1 {
		return s
	}
	return 1
}

// human renders the bound for a notice a person or a model reads: whole
// seconds as "300s", anything else as itself.
func (b runCommandBound) human() string {
	if b.d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(b.d/time.Second))
	}
	return b.d.String()
}

// source names the bound the way an agent has to hear it to act: the
// default is described as grain's, and as one this call did not choose,
// because a run that passed no "timeout" would otherwise read the number
// as something its own command asked for.
func (b runCommandBound) source() string {
	if b.fromCaller {
		return "the `timeout` this call passed"
	}
	return "run_command's default bound, which this call did not choose -- it passed no `timeout`"
}

// maxTimeoutMillis is maxRunCommandTimeout in the milliseconds the
// schema's own "timeout" is expressed in, so a notice telling a run to
// ask for more names a number it can actually pass.
func maxTimeoutMillis() int {
	return int(maxRunCommandTimeout / time.Millisecond)
}

// runCommandKilledMarker opens the notice below, and is what
// RunCommandTimedOut recognises it by afterwards -- shared rather than
// repeated, so that rewording the notice cannot silently stop a
// deployment's run_command timeout rate from being measured.
const runCommandKilledMarker = "[grain] Killed after "

// timedOutNotice is the line for a command the bound killed: the local
// ctx deadline firing, or the guest-side `timeout` exiting 124.
func (b runCommandBound) timedOutNotice() string {
	return fmt.Sprintf(
		runCommandKilledMarker+"%s by %s. The command did not finish, so nothing above is "+
			"its verdict -- and re-running it unchanged will be killed at the same point. "+
			"Pass a larger `timeout` (milliseconds, up to %d) or narrow the command.",
		b.human(), b.source(), maxTimeoutMillis())
}

// killedNotice is the line for exit 137 out of the guest, which is what
// the bound's own SIGKILL escalation produces (`timeout --kill-after`,
// see sshRunCommandTool) once a command has ignored the SIGTERM the
// bound sent it first.
//
// It is hedged, because 137 is 128+SIGKILL and the kernel's OOM killer
// sends the same signal: naming both possible causes is the honest
// version, and both are things a run can act on.
func (b runCommandBound) killedNotice() string {
	return fmt.Sprintf(
		"[grain] exit=137 is SIGKILL. %s is %s, and a command still running then is sent "+
			"SIGTERM and then SIGKILL %s later, so a command that ignored the first signal "+
			"ends exactly this way -- as does one the kernel's OOM killer stopped, which is "+
			"worth ruling out if the command is memory-hungry. If it was the bound, pass a "+
			"larger `timeout` (up to %d ms) or narrow the command.",
		capitalizeFirst(b.source()), b.human(), runCommandKillGrace, maxTimeoutMillis())
}

// transportStalledNotice is the line for the one failure the guest-side
// bound cannot cover: the guest command was bounded and the *call* still
// did not come back, so grain cut it off from this side (see
// sshRunCommandTool's own deadline). What the command did is genuinely
// unknown here, which is why this says so rather than guessing.
func (b runCommandBound) transportStalledNotice() string {
	return fmt.Sprintf(
		"[grain] The sandbox guest never answered. grain gave up on this call %s after %s "+
			"(%s) had already told the guest to kill the command, so whether the command "+
			"finished, or is still running in the sandbox, is unknown -- anything above is "+
			"partial. If this repeats, the guest is not answering rather than the command "+
			"being slow, and recreate_sandbox is the escape hatch.",
		sshRunCommandGrace, b.source(), b.human())
}

// formatRunCommandResult renders one run_command answer: the same
// exit=/stdout:/stderr: text both transports have always produced, with
// the two streams capped against one shared budget (splitResultBudget)
// and notice -- "" for a command that ended on its own -- on the end.
//
// The notice goes last, blank-line separated, for the reason
// withDeadlineNotice's does: it reads as grain speaking rather than as
// the last line the command printed, and a client that truncates a long
// result keeps the head, where the exit status is.
func formatRunCommandResult(exitCode int, stdout, stderr, notice string) string {
	outLimit, errLimit := splitResultBudget(len(stdout), len(stderr), int(maxToolResultBytes.Load()))
	stdout = elideCommandOutput(stdout, outLimit)
	stderr = elideCommandOutput(stderr, errLimit)
	text := fmt.Sprintf("exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	if notice == "" {
		return text
	}
	return text + "\n\n" + notice
}

// elideCommandOutput cuts one stream of a command's output to limit.
// When the runner already dropped the stream's middle, its marker is cut
// along with it, so the notice counts those bytes in its place -- less
// the marker's own, which were never the command's.
func elideCommandOutput(out string, limit int) string {
	unseen := 0
	if dropped, markerLen := runnerDropped(out); dropped > 0 {
		head := execOutputLimit / 2
		headEnd := lineBoundaryBefore(out, limit/2)
		tailStart := lineBoundaryAfter(out, len(out)-(limit-limit/2))
		if headEnd <= head && tailStart >= head+markerLen {
			unseen = int(dropped) - markerLen
		}
	}
	return elideMiddleOf(out, limit, elisionAdviceCommandOutput, unseen)
}

// capitalizeFirst upper-cases the first byte of s, for a source() phrase
// that starts a sentence rather than continuing one.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
