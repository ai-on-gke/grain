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
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/sandbox"
)

// runCommandKillGrace is how long the guest-side `timeout` waits between
// the SIGTERM it sends at the bound and the SIGKILL that follows
// (`--kill-after`). Without it the "bound" is not one: measured on a real
// grain guest, `timeout 2 ./ignores-sigterm.sh` against a command
// trapping TERM waits for that command to finish of its own accord --
// sixty seconds, for a sixty-second sleep -- and only then reports 124,
// so a run_command whose command ignores SIGTERM held the tool call open
// for as long as it liked with nothing to stop it.
//
// Five seconds is room for a well-behaved process to flush and exit on
// the signal it was sent, and short enough that a badly-behaved one
// cannot spend a meaningful part of a run's budget refusing to.
//
// This is *not* accompanied by `--foreground`, which the fix for this was
// first written as. `--foreground` means what its own documentation says:
// "children of COMMAND will not be timed out". Plain `timeout` runs the
// command in its own process group and signals that whole group, so it
// already reaches everything the command forked -- confirmed on a real
// guest, where `timeout 2 bash -c './gc.sh & sleep 10'` leaves no
// surviving gc.sh -- and it is procgroup.Prepare's guarantee for the
// local transport arriving by a different route. Adding `--foreground`
// would have removed the only process-group discipline this path has.
const runCommandKillGrace = 5 * time.Second

// sshRunCommandGrace is how long past the guest-side bound this side
// waits for the guest to answer at all before abandoning the call. It
// covers the SIGTERM-to-SIGKILL escalation (runCommandKillGrace) plus a
// slow `docker exec`/ssh teardown, and it exists for what the guest-side
// bound structurally cannot cover: the command being bounded says nothing
// about the *call* returning.
//
// The one shape suspected of hitting it -- a command that backgrounds
// something inheriting the channel's stdout, which is enough to hang an
// OpenSSH client waiting for EOF -- turns out not to, measured on a real
// guest: `./bgtest.sh &` returns in about two milliseconds, because the
// Go ssh client "kontur exec" uses returns on the exit-status message
// rather than waiting for the streams to close. So this is a backstop for
// a transport that has stopped answering, not the everyday case.
const sshRunCommandGrace = 30 * time.Second

// remoteRunner is the interface NewSSHSandboxTools' handlers need out of
// their transport -- kept separate from the concrete type that supplies it
// (DockerExecRunner) so a test can supply an in-process double instead of
// exec'ing into a real container (ssh_tools_test.go's localExecRunner);
// docker_exec_runner_test.go is what actually exercises DockerExecRunner's
// own command construction.
type remoteRunner interface {
	Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int)
}

// NewSSHSandboxTools returns the same four tools NewSandboxTools does --
// run_command, read_file, edit_file, write_file, same names, same input
// schemas, same output shapes -- but each one runs its work on runner's
// remote host instead of a local directory, which is what a kontur-managed
// sandbox VM needs (bwsalmon/agents#256): unlike NewSandboxTools' root,
// there is no local filesystem to call os.ReadFile/os.WriteFile against,
// so every read, write and edit here is itself a command sent over runner
// -- ported field for field from run_command/read_file/edit_file/
// write_file in grain/automation/mcp_server.py, which does the same thing
// against v1's SshRunner.
func NewSSHSandboxTools(runner remoteRunner, workspace string) []Tool {
	return []Tool{
		sshRunCommandTool(runner, workspace),
		sshReadFileTool(runner, workspace),
		sshEditFileTool(runner, workspace),
		sshWriteFileTool(runner, workspace),
	}
}

// sandboxBinDir is where a capability grant installs sandbox commands
// (protocol.SandboxBinDir, which this package cannot import).
const sandboxBinDir = "/home/debian/.grain/bin"

func sshRunCommandTool(runner remoteRunner, workspace string) Tool {
	return Tool{
		Name:        "run_command",
		Description: "Run a shell command in your assigned sandbox workspace.",
		InputSchema: runCommandInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			command, ok := argString(args, "command")
			if !ok || command == "" {
				return Result{Text: "command is required", IsError: true}
			}

			// timeout (milliseconds, matching native Bash's own unit) is
			// applied with the `timeout` coreutil on the remote side,
			// exactly as mcp_server.py's run_command does, rather than
			// bounding the local ssh subprocess: SSH has no per-command
			// timeout of its own to hook, and a remote `timeout` bounds
			// the thing that's actually slow. Applied unconditionally,
			// not only when the caller passes its own "timeout", so an
			// omitted one still gets runCommandTimeout's own default
			// rather than running with no server-side bound at all
			// (bwsalmon/agents#575).
			bound := resolveRunCommandBound(args)
			// Sandbox commands a capability grant installs (the
			// host-script capability's) are on PATH ahead of the image's.
			shell := fmt.Sprintf("PATH=%s:$PATH; export PATH; cd %s && %s", sandboxBinDir, shellQuote(workspace), command)
			// The trailing `exit $?` is not decoration. bash replaces
			// itself with the last simple command in a -c string, so
			// without it the process the transport is watching *is*
			// `timeout` -- and since `timeout` signals the process group
			// it made itself the leader of, the SIGKILL escalation here
			// kills the very process whose status is the answer. Death
			// by signal is not an exit status either os/exec or an SSH
			// exit-status message can carry (it arrives as -1), so the
			// 137 that says the bound ended this would be lost exactly
			// when it matters. Leaving a shell alive to report brings it
			// back as a status.
			shell = fmt.Sprintf("timeout --kill-after=%ds %d bash -c %s; exit $?",
				int(runCommandKillGrace.Seconds()), bound.seconds(), shellQuote(shell))

			// ...and bounded again from this side, well after the guest's
			// own bound should have ended it. The remote `timeout` bounds
			// the command, not the call: nothing here can make the guest
			// answer, so without this a wedged transport (an ssh channel
			// that never closes, a guest that stops responding) holds the
			// tool call open until the run's own wall clock, and a run
			// only advances a turn when its current call returns.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, bound.d+sshRunCommandGrace)
			defer cancel()
			started := time.Now()

			stdout, stderr, exitCode := runner.Run(ctx, []string{"bash", "-c", shell}, "")

			// `timeout`'s own reserved statuses, which are how the guest
			// reports that the bound, not the command, ended this: 124
			// for the command stopping on the SIGTERM it was sent, 137
			// (128+SIGKILL) for it having to be killed --kill-after
			// later. Both used to arrive as a bare number.
			//
			// Only once the bound has actually elapsed, though: both are
			// statuses a command produces on its own too -- its own
			// `timeout 4 curl ...` exits 124, an OOM-killed process 137
			// -- and telling an agent a 4-second probe was "killed after
			// 300s" and to pass a larger timeout sends it the wrong way.
			notice := ""
			elapsed := time.Since(started)
			switch {
			case errors.Is(ctx.Err(), context.DeadlineExceeded) && elapsed >= bound.d:
				notice = bound.transportStalledNotice()
			case exitCode == 124 && elapsed >= bound.d:
				notice = bound.timedOutNotice()
			case exitCode == 137 && elapsed >= bound.d:
				notice = bound.killedNotice()
			case execTransportFailed(exitCode, stderr):
				notice = execTransportNotice
			}
			text := formatRunCommandResult(exitCode, stdout, stderr, notice)
			return Result{Text: text, IsError: exitCode != 0}
		},
	}
}

// sshReadRemote reads path off runner's remote host, the stand-in for a
// remote os.ReadFile mcp_server.py's own _read_remote uses `cat` for.
//
// Bounded three ways, because what it is asked to read is whatever a run
// names: a path that exists and is not a regular file (/dev/zero, a FIFO,
// a socket) is refused before anything is read; at most maxReadFileBytes+1
// bytes are read (`head -c`, so one byte past the cap is how a file that is
// too big is told from one that is exactly big enough); and the whole read
// has sshReadTimeout to finish in. Without them a read of /dev/zero
// streamed into this process's heap until it died (task 2801).
func sshReadRemote(ctx context.Context, runner remoteRunner, path string) (string, *Result) {
	ctx, cancel := context.WithTimeout(ctx, sshReadTimeout)
	defer cancel()
	stdout, stderr, exitCode := runner.Run(ctx, sshReadArgv(path), "")
	if exitCode != 0 {
		msg := strings.TrimSpace(stderr)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = fmt.Sprintf("the read did not finish within %s", sshReadTimeout)
		}
		text := withExecTransportNotice(fmt.Sprintf("Error reading %s: %s", path, msg), exitCode, stderr)
		return "", &Result{Text: text, IsError: true}
	}
	if len(stdout) > maxReadFileBytes {
		return "", &Result{Text: fmt.Sprintf("Error reading %s: %s", path, errFileTooLarge), IsError: true}
	}
	return stdout, nil
}

// sshReadTimeout bounds one remote read, which used to have only the
// run's own wall clock: hours, for a FIFO nobody ever writes to.
const sshReadTimeout = 2 * time.Minute

// sshReadArgv is the remote read sshReadRemote runs. path travels as $1
// rather than being spliced into the script, so nothing in it is ever
// shell syntax. -e before -f so a missing path still fails in head's own
// words ("No such file or directory"), which is what the workspace retry
// and a run both expect to read.
func sshReadArgv(path string) []string {
	return sshReadArgvUpTo(path, maxReadFileBytes)
}

// sshReadArgvUpTo is sshReadArgv reading at most limit+1 bytes.
func sshReadArgvUpTo(path string, limit int) []string {
	script := fmt.Sprintf(`if [ -e "$1" ] && [ ! -f "$1" ]; then echo "$1: not a regular file" >&2; exit 1; fi; exec head -c %d -- "$1"`, limit+1)
	return []string{"sh", "-c", script, "sh", path}
}

// sshWriteRemote writes content to path on runner's remote host with `dd`,
// piped over stdin rather than embedded in the command line -- content is
// arbitrary model-generated text, and a command-line argument has no safe
// way to carry it whole (length limits, shell interpretation) the way
// stdin does.
func sshWriteRemote(ctx context.Context, runner remoteRunner, path, content string) Result {
	_, stderr, exitCode := runner.Run(ctx, []string{"dd", "of=" + path, "status=none"}, content)
	if exitCode != 0 {
		text := fmt.Sprintf("Error writing %s: %s", path, strings.TrimSpace(stderr))
		return Result{Text: withExecTransportNotice(text, exitCode, stderr), IsError: true}
	}
	return Result{Text: fmt.Sprintf("Wrote %s", path)}
}

// sshReadForTool reads fp off the remote host and, when that fails for
// an absolute path, reads the one other path it could have meant --
// workspaceRetry's, the same path taken under the sandbox's workspace.
// It returns the content, the notice naming the path actually read (""
// when none was needed), and the path itself, which is where an edit
// must write its result back to.
//
// Failing first and retrying second, rather than checking the path
// exists up front: the ordinary call is one that works, and it must stay
// one round trip. The retry costs a second `cat` only on a call that was
// already going to come back as an error.
func sshReadForTool(ctx context.Context, runner remoteRunner, workspace, fp string) (content, notice, read string, errResult *Result) {
	content, errResult = sshReadRemote(ctx, runner, fp)
	if errResult == nil {
		return content, "", fp, nil
	}
	retry, ok := workspaceRetry(workspace, fp)
	if !ok {
		return "", "", "", errResult
	}
	retried, retryErr := sshReadRemote(ctx, runner, retry)
	if retryErr != nil {
		// The original error, not this one: the run asked about fp, and
		// a message about a path it never named is a second puzzle on
		// top of the first.
		return "", "", "", errResult
	}
	return retried, workspaceRetryNotice(fp, retry, workspace), retry, nil
}

func sshReadFileTool(runner remoteRunner, workspace string) Tool {
	return Tool{
		Name:        "read_file",
		Description: "Read a file from your assigned sandbox workspace.",
		InputSchema: readFileInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			fp, _ := argString(args, "file_path")
			if fp == "" {
				return Result{Text: "file_path is required", IsError: true}
			}
			content, notice, _, errResult := sshReadForTool(ctx, runner, workspace, fp)
			if errResult != nil {
				return *errResult
			}
			return Result{Text: noticed(notice, numberedRange(linesFromContent(content), args))}
		},
		ReadBytes: sshReadBytes(runner, workspace),
	}
}

func sshWriteFileTool(runner remoteRunner, workspace string) Tool {
	return Tool{
		Name:        "write_file",
		Description: "Write (create or overwrite) a file in your assigned sandbox workspace.",
		InputSchema: writeFileInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			fp, _ := argString(args, "file_path")
			content, ok := argString(args, "content")
			if !ok {
				return Result{Text: "content is required", IsError: true}
			}
			if fp == "" {
				return Result{Text: "file_path is required", IsError: true}
			}

			// A file being written need not exist yet, so there is no
			// failed read to trigger the retry off the way read_file and
			// edit_file have. The question asked instead is whether the
			// directory being written into exists: it does for the
			// ordinary call, and for the /work/... guess it is the
			// directory under the workspace that does. Both `test -d`s
			// are skipped entirely unless the path is an absolute one
			// pointing outside the sandbox's workspace, which is the
			// only shape the guess ever takes.
			//
			// Deliberately not "mkdir -p and see": a guest whose account
			// can create /work would answer that by silently building a
			// second tree beside the checkout, and the run would not
			// find its own file again from a shell.
			target, notice := fp, ""
			if retry, ok := workspaceRetry(workspace, fp); ok &&
				!sshIsDir(ctx, runner, path.Dir(fp)) && sshIsDir(ctx, runner, path.Dir(retry)) {
				target, notice = retry, workspaceRetryNotice(fp, retry, workspace)
			}

			if bp, ok := runner.(batchFilePlacer); ok && strings.HasPrefix(target, "/") {
				if err := bp.PutFiles(ctx, []sandbox.FileUpload{{
					Path:    target,
					Content: []byte(content),
					Mode:    0o644,
				}}); err != nil {
					text := fmt.Sprintf("Error writing %s: %v", target, err)
					return Result{Text: text, IsError: true}
				}
				return Result{Text: noticed(notice, fmt.Sprintf("Wrote %s", target))}
			}

			parent := path.Dir(target)
			_, stderr, exitCode := runner.Run(ctx, []string{"mkdir", "-p", parent}, "")
			if exitCode != 0 {
				text := fmt.Sprintf("Error creating %s: %s", parent, strings.TrimSpace(stderr))
				return Result{Text: withExecTransportNotice(text, exitCode, stderr), IsError: true}
			}
			written := sshWriteRemote(ctx, runner, target, content)
			if written.IsError {
				return written
			}
			return Result{Text: noticed(notice, written.Text)}
		},
	}
}

// sshIsDir reports whether dir is a directory on the remote host --
// `test -d`, the cheapest question this transport can ask about a path.
func sshIsDir(ctx context.Context, runner remoteRunner, dir string) bool {
	_, _, exitCode := runner.Run(ctx, []string{"test", "-d", dir}, "")
	return exitCode == 0
}

func sshEditFileTool(runner remoteRunner, workspace string) Tool {
	return Tool{
		Name:        "edit_file",
		Description: "Replace an exact string in a file in your assigned sandbox workspace.",
		InputSchema: editFileInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			fp, _ := argString(args, "file_path")
			oldStr, hasOld := argString(args, "old_string")
			newStr, hasNew := argString(args, "new_string")
			if !hasOld || !hasNew {
				return Result{Text: "old_string and new_string are required", IsError: true}
			}
			// An empty old_string matches between every character in
			// the file: with replace_all it interleaves new_string
			// through the whole file ("abc" becomes "XaXbXcX"), and
			// without it the refusal counts those phantom matches and
			// reads as nonsense ("String appears 19 times in the
			// file"). Neither is what a caller that meant "create this
			// file" or "add this to it" was asking for -- found by hand
			// driving this server the way a framework does, task 244.
			if oldStr == "" {
				return Result{
					Text: "old_string must not be empty: it matches between every character in the file. " +
						"Use write_file to create or replace a file, or name the exact text this edit replaces.",
					IsError: true,
				}
			}
			replaceAll, _ := argBool(args, "replace_all")

			content, notice, read, errResult := sshReadForTool(ctx, runner, workspace, fp)
			if errResult != nil {
				return *errResult
			}
			count := strings.Count(content, oldStr)
			if count == 0 {
				return Result{Text: fmt.Sprintf("String not found in file: %q", echoedArgument(oldStr)), IsError: true}
			}
			if count > 1 && !replaceAll {
				return Result{
					Text: fmt.Sprintf(
						"String appears %d times in the file. Use replace_all: true to replace "+
							"every occurrence, or provide more surrounding context to uniquely "+
							"identify the instance you mean.", count),
					IsError: true,
				}
			}
			n := 1
			if replaceAll {
				n = -1
			}
			newContent := strings.Replace(content, oldStr, newStr, n)
			// Written back to the path the content was read from, which
			// is the retried one when the path asked for was not there:
			// an edit that read one file and wrote another would be the
			// worst answer available here.
			written := sshWriteRemote(ctx, runner, read, newContent)
			if written.IsError {
				return written
			}
			return Result{Text: noticed(notice, written.Text)}
		},
	}
}
