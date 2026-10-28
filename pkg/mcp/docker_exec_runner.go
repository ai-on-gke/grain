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
	"io"
	"os/exec"
	"strings"
	"time"
)

// DockerExecRunner runs argv inside a granule-managed VM's guest through
// `docker exec <container> granule exec` -- the remoteRunner contract
// NewSSHSandboxTools and ConfigureGitCredentialsOverSSH take, reaching the
// guest from *inside* its own VM container rather than over a TCP
// connection to netshim's externally forwarded port.
//
// bwsalmon/granule already ships the guest-side half of this: its
// guestexec ("granule exec", one of the modes cmd/granule-shim
// dispatches). That address is reachable without any address
// translation at all, since the container shares the network namespace
// netshim set the tap up in.
//
// The point of routing through it is that nothing outside the VM's own
// container has to be able to reach the guest, so none of the machinery
// that makes such a connection possible has to exist or be discovered.
type DockerExecRunner struct {
	// Container is the docker container to exec into: a VM's own
	// container, not the "-netns" holder that merely owns the network
	// namespace -- granule.PodName(vmName), the name dockervm gives it.
	Container string

	// User is the guest account to log in as, passed through as
	// GRANULE_EXEC_USER. Empty leaves guestexec's own default ("root",
	// the only account granule's reference guest image creates) in
	// place -- which is not what a grain guest image wants, since
	// the repository's Granulefile creates "debian" for its sandbox
	// account instead.
	User string

	// KeyPath is the private key "granule exec" authenticates with,
	// passed through as GRANULE_EXEC_KEY.
	KeyPath string

	// ConnectTimeout bounds how long "granule exec" retries its initial
	// connection to the guest before giving up, passed through as
	// GRANULE_EXEC_CONNECT_TIMEOUT. Zero leaves guestexec's own default
	// (30s) in place.
	ConnectTimeout time.Duration
}

// Run executes argv inside the guest, piping stdin to it verbatim if
// non-empty, and is what satisfies the remoteRunner contract.
//
// Unlike an `ssh host <command>` call, argv is passed through as a real
// argv rather than shell-quoted into one string first: "granule exec --" takes its trailing
// arguments as a slice and does that join itself. Quoting here as well would double-quote every argument.
//
// exitCode follows remoteRunner's convention: the guest command's own
// status when it ran, and -1 when it never did. `docker exec` propagates the
// exit status of the process it started, and "granule exec" exits with the
// guest command's own (cmd/granule-shim's runGuestSession ends in os.Exit(code)),
// so a real remote status arrives here intact. A failure *before* the
// guest command ran -- no such container, a docker daemon that is not
// answering, an unreachable or unauthenticated guest -- surfaces as
// exit 1 either way, which is why execFailedBeforeGuest below has to tell
// the two apart by what was written to stderr rather than by status alone.
func (r *DockerExecRunner) Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	return r.RunStream(ctx, argv, stdin, nil)
}

// RunStream is Run with somebody watching: everything either stream
// produces is written to out as it arrives, as well as being collected
// into the strings Run returns.
//
// What reaches out is the two streams interleaved in the order the guest
// produced them, which is what a log is; the returned pair keeps them
// apart, because the exit classification below reads stderr on its own.
// A caller that wants one transcript should use what it was given here
// rather than joining the pair afterwards, so that the log it followed and
// the log it keeps are the same text (orchestrator.granuleSandbox.RunShell
// does exactly that).
//
// A nil out is Run, with nothing extra done.
func (r *DockerExecRunner) RunStream(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int) {
	args := r.execArgs(argv, stdin != "")

	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	outBuf, errBuf := newHeadTailBuffer(), newHeadTailBuffer()
	live := &liveStream{to: out}
	cmd.Stdout = live.writerFor(outBuf)
	cmd.Stderr = live.writerFor(errBuf)
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			if code == 1 && execFailedBeforeGuest(errBuf.String()) {
				return outBuf.String(), errBuf.String(), -1
			}
			return outBuf.String(), errBuf.String(), code
		}
		return outBuf.String(), err.Error(), -1
	}
	return outBuf.String(), errBuf.String(), 0
}

// execFailedBeforeGuest reports whether stderr looks like it came from
// something that failed before the guest command ever ran, rather than
// from the guest command itself -- the distinction ssh's own reserved exit
// status would give for free but `docker exec` cannot provide, since it
// reports the exit status of whatever it started and both failures below
// exit 1.
//
// Both are recognized by a prefix on the *first* line of stderr:
//
//   - "granule: exec: ..." -- cmd/granule-shim's own log prefix for its "exec"
//     mode, which it sets before anything can fail and which reaches this
//     stderr only via log.Fatalf (guestexec never logs on a successful
//     session, so nothing else in the stream can carry it).
//   - "Error ..." -- the docker CLI's own failures, e.g. "Error response
//     from daemon: ..." for a daemon that is not answering and "Error: No
//     such container: ..." for a VM whose container is not there.
//
// A guest command's own stderr is the only other thing on this stream,
// and would have to open with one of those two prefixes on its first line
// to be misread. The cost of that misreading is a -1 where a 1 belonged:
// the same "the command did not run" report an unreachable sandbox
// already gets, which is the safer of the two directions to err in.
func execFailedBeforeGuest(stderr string) bool {
	line := stderr
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "granule: exec: ") || strings.HasPrefix(line, "kontur: exec: ") || strings.HasPrefix(line, "Error")
}

// execArgs renders the `docker exec` arguments Run executes, kept
// separate from Run itself so a test can assert on the invocation without
// a docker daemon anywhere. interactive adds -i, which is needed only
// when there is stdin to pipe through: without it docker gives the
// exec'd process no stdin at all, which is what a command reading from a
// closed stdin should see anyway.
func (r *DockerExecRunner) execArgs(argv []string, interactive bool) []string {
	args := []string{"exec"}
	if interactive {
		args = append(args, "-i")
	}
	if r.User != "" {
		args = append(args, "-e", "GRANULE_EXEC_USER="+r.User)
	}
	if r.KeyPath != "" {
		args = append(args, "-e", "GRANULE_EXEC_KEY="+r.KeyPath)
	}
	if r.ConnectTimeout > 0 {
		args = append(args, "-e", "GRANULE_EXEC_CONNECT_TIMEOUT="+r.ConnectTimeout.String())
	}
	args = append(args, r.Container, "granule", "exec", "--")
	return append(args, argv...)
}
