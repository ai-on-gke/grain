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

import (
	"fmt"
	"path"
	"strings"
)

// A run's agent framework opens it with a preamble grain does not write,
// describing the process that framework is running in: Claude Code's
// says "Primary working directory: /" and "Is a git repository: false",
// both true of grain's controller (which is where the agent CLI runs,
// and must -- see pkg/agent/claude's package comment) and neither true
// of the sandbox these tools reach. What a run does with it is invent an
// absolute path out of the relative one its prompt gave it:
// "/work/pkg/model/task.go", for a checkout that is "work" under the
// sandbox's own workspace. Measured over the 185 runs this deployment
// had recorded when this was written, 105 read_file and edit_file calls
// failed on such a path, across 84 distinct runs -- and five of those
// went on to try "/work/work/..." next, trying to reconcile the absolute
// guess with the relative form they were given.
//
// orchestrator.sandboxDirsSection is the half of the fix that stops the
// guess being made, by naming both directories in the prompt. This is
// the half that catches the run that makes it anyway: an absolute path
// that is not there is tried once more under the workspace, and -- the
// part that matters as much as the retry itself -- the answer says so.
// A tool that corrects silently leaves the next call in the same run
// wrong in exactly the same way, which is a turn saved once and then
// spent again.
//
// Both transports share this: NewSandboxTools resolves a path against a
// local directory and NewSSHSandboxTools against a guest's filesystem,
// but the guess a run makes is the same one either way. Paths here are
// POSIX either way too -- a sandbox is a Linux directory or a Linux
// guest -- which is why this uses path rather than path/filepath.

// workspaceRetry returns the one other path an absolute file_path could
// plausibly have meant -- the same path taken as if it were relative to
// the sandbox's workspace, which is where a "/work/..." guess was aiming
// -- and whether there is such a path worth trying at all.
//
// It is deliberately narrow. A relative path already lands under the
// workspace, so there is nothing else for it to mean; a path already
// inside the workspace is simply not there, and answering a mistyped
// filename with a second guess helps nobody; and a workspace of "/"
// makes every path its own retry. Anything wider would turn a wrong path
// into a different wrong path, which is worse than the error it
// replaced.
func workspaceRetry(workspace, p string) (string, bool) {
	if workspace == "" || !path.IsAbs(workspace) || !path.IsAbs(p) {
		return "", false
	}
	ws, clean := path.Clean(workspace), path.Clean(p)
	if ws == "/" || clean == ws || strings.HasPrefix(clean, ws+"/") {
		return "", false
	}
	return path.Join(ws, clean), true
}

// workspaceRetryNotice is what the tool says on the result when the
// retry worked: which path was asked for, which one was actually used,
// and the fact behind the difference. Said in full rather than as a
// terse "(corrected)" because the run is holding a wrong model of where
// it is -- one it was handed by its own framework, and will keep acting
// on for the rest of the run unless something contradicts it.
func workspaceRetryNotice(asked, used, workspace string) string {
	return fmt.Sprintf(
		"[grain] %s is not a path in this sandbox, so %s was used instead. These tools "+
			"run in the sandbox, whose workspace is %s -- what your framework told you "+
			"about a working directory describes the machine it runs on, not this one. "+
			"Name files by their path in the sandbox from here on.",
		asked, used, workspace)
}

// wroteName is the path a write reports having written: the one the
// caller asked for, spelled as it asked for it, unless the retry sent
// the write somewhere else -- "Wrote /work/x" underneath a notice
// explaining that /work/x is not a path in this sandbox would contradict
// itself in the same breath.
func wroteName(asked, used, notice string) string {
	if notice == "" {
		return asked
	}
	return used
}

// noticed prefixes a tool's own answer with a notice, or returns it
// unchanged when there is nothing to say -- the shape every retrying
// handler here returns, so the notice always arrives ahead of the
// content it qualifies rather than after it.
func noticed(notice, text string) string {
	if notice == "" {
		return text
	}
	return notice + "\n\n" + text
}
