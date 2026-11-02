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

package orchestrator

// The transport a caller that is not an agent runs commands over.
//
// Everything else in this package reaches into a sandbox through the MCP
// tools it hands an agent (Sandbox.Tools, and prepareCheckout's own
// run_command call). That is the right seam for an agent -- the tool
// call is what the model can see, and its bound and its output cap are
// there so one call cannot eat a run's context or hold a turn open
// forever -- and it is the wrong one for grain's own CI: run_command
// clamps every call to ten minutes (mcp.maxRunCommandTimeout) and cuts
// its answer to a few tens of kilobytes (mcp.maxToolResultBytes), and a
// CI job is a forty-five-minute `go test -race` whose whole log is the
// thing somebody came to read.
//
// So a second, narrower seam beside it: one script, one bound the caller
// chose, everything it printed. Both backends implement it over the
// transport they already have -- a subprocess for a host directory, the
// guest's own shell for a granule VM -- so a caller (cmd/grain's CI
// sandbox adapter, the only one today) works on either without knowing
// which it has.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/gitrefs"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/procgroup"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// ShellRequest is one script to run in a sandbox.
type ShellRequest struct {
	// Script is the shell to run, with bash, `set -e` applied over it --
	// so a multi-line script fails on its first failing line, which is
	// GitHub Actions' own default and the behaviour a two-line CI step
	// looks like it has.
	Script string
	// Dir is the working directory, relative to the sandbox's own
	// workspace. Empty means the workspace itself.
	Dir string
	// Env is every variable the script should see, layered over whatever
	// the sandbox's shell starts with -- which is a curated few on a
	// host directory (hostShellPassthrough) and the guest's own login
	// environment in a VM, so a variable a caller actually depends on
	// belongs here rather than in either.
	Env map[string]string
	// Timeout bounds this one script inside the sandbox, in addition to
	// whatever ctx bounds: a context deadline only ends the *call*, and
	// on a VM the call and the command are on opposite sides of a
	// transport. Zero leaves it to ctx alone.
	Timeout time.Duration
	// Output, when set, is written with what the script prints as it
	// prints it, so a caller need not wait out a forty-minute step to see
	// its first line (ci.StreamingSandbox, and pkg/ci's live.go for what
	// is done with it). ShellResult still carries the whole output either
	// way, so a caller that ignores this loses nothing.
	//
	// Written to from whichever goroutine the transport reads its output
	// on, but never from two at once: a backend that has two streams to
	// deliver serializes them onto this one writer, which is also why what
	// arrives here is interleaved even where ShellResult.Output is one
	// stream after the other.
	//
	// A backend that cannot stream at all -- a transport that answers once,
	// with everything -- simply never writes here, and the caller is left
	// with the same "nothing until it ends" it had before.
	Output io.Writer
	// StepID, when non-empty, drives the script through an idempotent
	// in-sandbox step runner under $HOME/.grain-ci-step/<StepID>: the first
	// call starts the script detached inside the sandbox, spools its
	// combined output to disk and records its exit status when it ends, and
	// a later call with the same StepID (from a daemon that restarted and
	// readopted the sandbox) re-attaches from byte 0 without running the
	// script a second time.
	StepID string
	// Attach, when non-nil, is reported the exec id and output cursor of
	// the command where the transport is a sandbox's dial-out channel, and
	// when its ExecID is set the call reattaches to that exec instead of
	// starting the script (design/design/grain-on-gke.design.md#GKE-4). A
	// transport without the channel ignores the reporting, and refuses a
	// reattach with sandboxaccess.ErrChannelLost: it has no exec to find.
	Attach *sandboxaccess.Attachment
}

// ShellResult is what one script did.
type ShellResult struct {
	// Output is stdout and stderr together, uncut -- interleaved as the
	// shell produced them where the transport keeps them that way (a
	// host directory), stdout then stderr where it does not (a VM,
	// whose two streams arrive as two strings).
	Output string
	// ExitCode is the script's status. A non-zero one is an answer, not
	// an error: RunShell's error is the transport failing.
	ExitCode int
}

// SandboxShell is implemented by a Sandbox that can run a script for a
// caller that is not an agent -- hostSandbox, granuleSandbox,
// k8sGranuleSandbox, k8sSandbox and containerSandbox all do.
//
// An optional interface, the way SandboxPlacer and rootedSandbox are: a
// caller type-asserts for it and says something useful when a backend
// does not have it, rather than every Sandbox implementation in every
// test having to grow a method.
type SandboxShell interface {
	// RunShell runs req to completion and reports what it printed and
	// what it exited with. The error is the transport: the machine gone,
	// the connection dropped, the shell never started.
	RunShell(ctx context.Context, req ShellRequest) (ShellResult, error)
}

// shellScript is the bash one RunShell actually runs: the caller's
// script, with the working directory and the environment applied ahead
// of it, under `set -e`.
//
// Composed here rather than in each backend so the two agree exactly on
// what a step's `working-directory` and `env` mean -- a job that passes
// on a host directory and fails in a VM because one of them exported the
// environment differently would be the worst kind of CI bug to chase.
func shellScript(workspace string, req ShellRequest) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	dir := workspace
	if req.Dir != "" {
		if workspace == "" {
			dir = req.Dir
		} else {
			dir = strings.TrimSuffix(workspace, "/") + "/" + req.Dir
		}
	}
	if dir != "" {
		fmt.Fprintf(&b, "cd %s\n", shellQuote(dir))
	}
	// Sorted, so the script this composes is the same script for the
	// same request -- a map's own order would make two identical jobs
	// produce two different scripts, which is a difference nobody can
	// see and everybody would have to rule out.
	names := make([]string, 0, len(req.Env))
	for k := range req.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(req.Env[k]))
	}
	b.WriteString(req.Script)
	return b.String()
}

// guestCommandPath puts protocol.SandboxBinDir first on a guest
// script's PATH.
const guestCommandPath = "PATH=" + protocol.SandboxBinDir + ":$PATH; export PATH\n"

// shellQuote wraps s so bash reads it as one literal word.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// hostShellPassthrough is every variable a script run through
// hostSandbox.RunShell inherits from the daemon's own environment, and
// the whole of it. Everything else this process holds is dropped.
//
// The caller RunShell exists for is grain's CI, and a CI step's script
// comes out of a workflow file -- a file every agent grain dispatches
// can rewrite (pkg/ci's "What a workflow file is trusted with"). On a
// host-directory backend the daemon's environment is right there:
// os.Environ() is whatever an operator's unit file, login shell or
// `make run` exported, so handing it to a step wholesale makes `env` in
// a workflow a read of the daemon. Nothing grain itself puts a
// credential in an environment variable, and an *agent* on this backend
// already inherits the same environment, so this is a fence rather than
// a hole being closed -- but "a CI job's sandbox is given no capability
// material and no credential" is a much easier thing to believe when it
// does not depend on which backend is wired, or on nobody ever having
// exported a token beside the daemon.
//
// So: an allowlist, and it is short. What is on it, and why:
//
//   - PATH, because a host sandbox *is* this machine, and the tools a
//     step runs -- go, node, make -- are the ones on the daemon's own
//     PATH. Without it bash has builtins and nothing else.
//   - LANG and LC_ALL, because a step's output is text and the locale
//     decides how tools sort and encode it. A job that passes for the
//     daemon and fails under a different collation is the kind of
//     difference nobody thinks to look for.
//   - GOMODCACHE, GOCACHE and npm_config_cache, deliberately: a host
//     sandbox pins HOME to itself, so without them every job downloads
//     the whole module and package tree into a directory it then
//     deletes -- which is exactly what pkg/cli/ci_e2e_test.go's host
//     arm asks for by setting them in its own environment. They name a
//     directory to read and write, not a secret, and passing a cache
//     in is the one thing this list is meant to stay able to say. A
//     build cache grain learns to share later belongs here beside them.
//
// Anything not on this list a step needs is the caller's to pass in
// ShellRequest.Env, where it is a decision somebody made rather than
// whatever the daemon happened to be started with.
//
// mcp's run_command, the seam beside this one, similarly sanitizes its
// environment via sandboxPassthrough so host credentials and tokens are
// scrubbed before launching subprocesses.
var hostShellPassthrough = []string{
	"PATH",
	"LANG",
	"LC_ALL",
	"GOMODCACHE",
	"GOCACHE",
	"npm_config_cache",
}

// hostShellFallbackPath is a step's PATH when the daemon has none of its
// own -- a daemon started by something that exported no environment at
// all. The value login(1) and cron fall back to, so a step in that case
// finds the system's own tools rather than nothing.
const hostShellFallbackPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// hostShellEnv is the environment hostSandbox.RunShell gives a script:
// HOME pinned to the sandbox, GIT_CONFIG_NOSYSTEM set, the files ref
// format pinned for any repository it creates (pkg/gitrefs: the daemon
// reads those checkouts back with whatever git it has), and
// hostShellPassthrough's variables where this process has them.
//
// HOME is pinned exactly as mcp's run_command pins it -- so anything the
// script reads or writes there (a .gitconfig, most of all) is the
// sandbox's rather than the daemon's -- and it is set first so that a
// daemon that somehow has HOME on the list could not put its own back.
//
// GIT_CONFIG_NOSYSTEM=1 finishes the job HOME starts, because pinning
// HOME only moves the *global* config: git still reads the machine's
// system config (/etc/gitconfig, or on macOS the one Apple's Command
// Line Tools ship at
// /Library/Developer/CommandLineTools/usr/share/git-core/gitconfig), and
// what the host has put there is the host operator's, not this sandbox's.
// A credential helper is the case that bites -- git appends helpers
// rather than replacing them, so a system `helper = osxkeychain` runs
// alongside the `store` mcp.GitCredentialFiles writes and is handed the
// sandbox's gitproxy token to file in the operator's keychain -- but the
// same reasoning covers any system-level setting a step would otherwise
// inherit from the machine it happens to be running on.
//
// It is set here beside HOME rather than added to hostShellPassthrough
// because that list passes *through* what the daemon holds, and this is a
// value grain decides: on the list it would appear only when an operator
// had already exported it, which is exactly the case that needs no help.
func hostShellEnv(root string) []string {
	env := []string{"HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", gitrefs.EnvEntry()}
	havePath := false
	for _, name := range hostShellPassthrough {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		if name == "PATH" {
			havePath = value != ""
			if !havePath {
				continue
			}
		}
		env = append(env, name+"="+value)
	}
	if !havePath {
		env = append(env, "PATH="+hostShellFallbackPath)
	}
	return env
}

// validStepID reports whether id is a safe, single-component step identifier
// ([a-zA-Z0-9_-]+).
func validStepID(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// stepRunnerScript wraps innerScript in an idempotent in-sandbox step runner
// rooted at $HOME/.grain-ci-step/<stepID>. On the first call for stepID it
// starts innerScript in a detached session, spooling stdout+stderr to
// $step_dir/out and recording its exit status in $step_dir/exit when it ends.
// On a subsequent call for the same stepID (after a daemon restart readopts
// the sandbox), it skips starting a second copy and tails $step_dir/out from
// byte 0 until $step_dir/exit appears.
func stepRunnerScript(stepID, innerScript string) (string, error) {
	if !validStepID(stepID) {
		return "", fmt.Errorf("orchestrator: invalid CI step ID %q", stepID)
	}
	quotedInner := shellQuote(innerScript)
	const detachedBody = `'bash "$1/run.sh" </dev/null >>"$1/out" 2>&1; rc=$?; printf "%d\n" "$rc" >"$1/exit.tmp" && mv "$1/exit.tmp" "$1/exit"'`
	var b strings.Builder
	fmt.Fprintf(&b, "step_dir=\"$HOME/.grain-ci-step/%s\"\n", stepID)
	b.WriteString("if [ ! -e \"$step_dir/started\" ]; then\n")
	b.WriteString("  mkdir -p \"$step_dir\" || exit 1\n")
	b.WriteString("  : > \"$step_dir/out\" || exit 1\n")
	fmt.Fprintf(&b, "  printf '%%s' %s > \"$step_dir/run.sh\" || exit 1\n", quotedInner)
	b.WriteString("  if command -v setsid >/dev/null 2>&1; then\n")
	b.WriteString("    setsid bash -c " + detachedBody + " _ \"$step_dir\" </dev/null >/dev/null 2>&1 &\n")
	b.WriteString("  elif command -v python3 >/dev/null 2>&1; then\n")
	b.WriteString("    python3 -c 'import os,sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])' bash -c " + detachedBody + " _ \"$step_dir\" </dev/null >/dev/null 2>&1 &\n")
	b.WriteString("  else\n")
	b.WriteString("    nohup bash -c " + detachedBody + " _ \"$step_dir\" </dev/null >/dev/null 2>&1 &\n")
	b.WriteString("  fi\n")
	b.WriteString("  printf '%s\\n' \"$!\" > \"$step_dir/pid\"\n")
	b.WriteString("  touch \"$step_dir/started\" || exit 1\n")
	b.WriteString("fi\n")
	b.WriteString("offset=0\n")
	b.WriteString("while [ ! -f \"$step_dir/exit\" ]; do\n")
	b.WriteString("  size=$(wc -c < \"$step_dir/out\" 2>/dev/null | tr -d ' ' || echo 0)\n")
	b.WriteString("  [ -n \"$size\" ] || size=0\n")
	b.WriteString("  if [ \"$size\" -gt \"$offset\" ]; then\n")
	b.WriteString("    tail -c +\"$((offset + 1))\" \"$step_dir/out\" | head -c \"$((size - offset))\"\n")
	b.WriteString("    offset=$size\n")
	b.WriteString("  fi\n")
	b.WriteString("  if [ -f \"$step_dir/pid\" ]; then\n")
	b.WriteString("    pid=$(cat \"$step_dir/pid\" 2>/dev/null || true)\n")
	b.WriteString("    if [ -n \"$pid\" ] && ! kill -0 \"$pid\" 2>/dev/null && [ ! -f \"$step_dir/exit\" ]; then\n")
	b.WriteString("      sleep 0.1\n")
	b.WriteString("      if [ ! -f \"$step_dir/exit\" ]; then\n")
	b.WriteString("        printf '137\\n' > \"$step_dir/exit.tmp\" && mv \"$step_dir/exit.tmp\" \"$step_dir/exit\"\n")
	b.WriteString("        break\n")
	b.WriteString("      fi\n")
	b.WriteString("    fi\n")
	b.WriteString("  fi\n")
	b.WriteString("  sleep 0.05\n")
	b.WriteString("done\n")
	b.WriteString("size=$(wc -c < \"$step_dir/out\" 2>/dev/null | tr -d ' ' || echo 0)\n")
	b.WriteString("[ -n \"$size\" ] || size=0\n")
	b.WriteString("if [ \"$size\" -gt \"$offset\" ]; then\n")
	b.WriteString("  tail -c +\"$((offset + 1))\" \"$step_dir/out\" | head -c \"$((size - offset))\"\n")
	b.WriteString("fi\n")
	b.WriteString("exit \"$(cat \"$step_dir/exit\")\"\n")
	return b.String(), nil
}

// HasStepRunner reports whether sandbox sb has an in-sandbox step runner for
// stepID that is either still running or has already recorded its exit code.
func HasStepRunner(ctx context.Context, sb Sandbox, stepID string) (bool, error) {
	if !validStepID(stepID) {
		return false, fmt.Errorf("orchestrator: invalid CI step ID %q", stepID)
	}
	shell, ok := sb.(SandboxShell)
	if !ok {
		return false, fmt.Errorf("orchestrator: sandbox %q does not implement SandboxShell", sb.Name())
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	script := fmt.Sprintf("step_dir=\"$HOME/.grain-ci-step/%s\"\n"+
		"[ -f \"$step_dir/started\" ] || exit 1\n"+
		"[ -f \"$step_dir/exit\" ] && exit 0\n"+
		"pid=$(cat \"$step_dir/pid\" 2>/dev/null || true)\n"+
		"[ -n \"$pid\" ] && kill -0 \"$pid\" 2>/dev/null\n", stepID)
	res, err := shell.RunShell(probeCtx, ShellRequest{Script: script, Timeout: 15 * time.Second})
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// killDetachedStepRunners terminates any detached CI step runners recorded
// under root/.grain-ci-step before a host sandbox directory is removed.
func killDetachedStepRunners(root string) {
	if root == "" {
		return
	}
	matches, err := filepath.Glob(filepath.Join(root, ".grain-ci-step", "*", "pid"))
	if err != nil {
		return
	}
	for _, pidFile := range matches {
		killStepRunnerPIDFile(pidFile)
	}
}

func killStepRunner(root, stepID string) {
	if root == "" || !validStepID(stepID) {
		return
	}
	killStepRunnerPIDFile(filepath.Join(root, ".grain-ci-step", stepID, "pid"))
}

func killStepRunnerPIDFile(pidFile string) {
	// A runner that recorded its exit has ended, and the pid it left
	// behind may since have been handed to an unrelated process.
	if _, err := os.Stat(filepath.Join(filepath.Dir(pidFile), "exit")); err == nil {
		return
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	// The pid file sits in the sandbox, where the job's own code can
	// write anything into it -- and KillPID signals -pid, so 1 would be
	// every process this daemon may signal. Only a pid that is not this
	// process and that is still the runner this file names is killed.
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return
	}
	if !isStepRunnerProcess(pid, filepath.Dir(pidFile)) {
		return
	}
	_ = procgroup.KillPID(pid)
}

// isStepRunnerProcess reports whether pid is the detached runner
// stepRunnerScript started for stepDir: that runner is `bash -c <body> _
// <stepDir>`, so its command line ends in stepDir. A pid the kernel has
// since handed to another process, or one the job wrote there itself,
// fails the check. Where there is no /proc to read the command line from
// (not Linux) the pid is taken on trust, as it was before this check.
func isStepRunnerProcess(pid int, stepDir string) bool {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		return true
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(args) == 0 {
		return false
	}
	return filepath.Clean(args[len(args)-1]) == filepath.Clean(stepDir)
}

// RunShell implements SandboxShell for a host-directory sandbox: bash as
// a subprocess of this process, in the sandbox's own directory, with the
// environment hostShellEnv composes -- HOME pinned to the sandbox and a
// short allowlist of the daemon's own variables, rather than everything
// this process happens to hold.
func (s *hostSandbox) RunShell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	script := shellScript("", req)
	if req.StepID != "" {
		var err error
		script, err = stepRunnerScript(req.StepID, script)
		if err != nil {
			return ShellResult{ExitCode: -1}, err
		}
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	// The whole process group, so a script that backgrounded something
	// is not still running after the bound killed the shell.
	procgroup.Prepare(cmd)
	cmd.Dir = s.root
	cmd.Env = hostShellEnv(s.root)
	var out bytes.Buffer
	// One writer for both streams, and the *same* writer value: os/exec
	// gives a command whose Stdout and Stderr are equal a single pipe and
	// a single goroutine to drain it, which is what keeps the two
	// interleaved as bash produced them -- and, where a caller is
	// following along, what keeps its Output free of two concurrent
	// writes.
	var sink io.Writer = &out
	if req.Output != nil {
		sink = io.MultiWriter(&out, req.Output)
	}
	cmd.Stdout, cmd.Stderr = sink, sink
	err := cmd.Start()
	if err == nil {
		_ = procgroup.SacrificeOnOOM(cmd.Process.Pid)
		err = cmd.Wait()
	}
	if err != nil {
		if req.StepID != "" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			killStepRunner(s.root, req.StepID)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return ShellResult{Output: out.String(), ExitCode: -1},
				fmt.Errorf("orchestrator: running a script in sandbox %q: %w", s.name, ctx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ShellResult{Output: out.String(), ExitCode: exitErr.ExitCode()}, nil
		}
		return ShellResult{Output: out.String(), ExitCode: -1},
			fmt.Errorf("orchestrator: running a script in sandbox %q: %w", s.name, err)
	}
	return ShellResult{Output: out.String()}, nil
}

// shellGrace is how long past its own bound RunShell waits for a guest
// to answer before giving up on the transport -- mcp.sshRunCommandGrace's
// counterpart, and here for the same reason: the guest-side bound bounds
// the command, not the call.
const shellGrace = 60 * time.Second

// streamingRunner is a sandboxRunner that can hand over a command's
// output while the command is still running, rather than only as two
// strings once it is over -- what mcp's DockerExecRunner,
// KubectlExecRunner and ContainerExecRunner all grew for
// ShellRequest.Output's sake.
//
// Optional, and asked for rather than required, for sandboxRunner's own
// reason: a test's runner is a function with one method, and a caller
// that wanted this and did not get it is a caller with no live output
// rather than a broken one.
type streamingRunner interface {
	RunStream(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int)
}

type execStreamRunner interface {
	Exec(ctx context.Context, argv []string, stdin string, out io.Writer) (stdout, stderr string, exitCode int, err error)
}

// attachingRunner is a runner whose execs have ids that outlive this
// process -- sandboxaccess.Runner, over a sandbox's dial-out channel.
type attachingRunner interface {
	ExecAttached(ctx context.Context, argv []string, stdin string, out io.Writer, att *sandboxaccess.Attachment) (stdout, stderr string, exitCode int, err error)
}

// RunShell implements SandboxShell for a granule VM: the guest's own bash,
// over the same transport every tool call and every credential write
// already goes over.
//
// The bound is applied inside the guest with the `timeout` coreutil, the
// way mcp's own SSH run_command applies it, rather than by cancelling
// this side: killing the call leaves the command running in a VM nobody
// is watching, which for a CI job means a `go test` still burning the
// sandbox's CPUs after the job that asked for it was concluded.
func (s *granuleSandbox) RunShell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	var workspace string
	if s.owner != nil {
		workspace = s.owner.cfg.Workspace
	}
	return runGuestShell(ctx, s.runner, workspace, req, "granule VM "+strconv.Quote(s.vmName))
}

// RunShell implements SandboxShell for a Kubernetes sandbox pod: the pod's
// own bash, run over `kubectl exec`.
func (s *k8sSandbox) RunShell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	var workspace string
	if s.owner != nil {
		workspace = s.owner.cfg.Workspace
	}
	return runGuestShell(ctx, s.runner, workspace, req, "sandbox pod "+strconv.Quote(s.podName))
}

// RunShell implements SandboxShell for a Docker sandbox container: the
// container's own bash, run over `docker exec`.
func (s *containerSandbox) RunShell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	var workspace string
	if s.owner != nil {
		workspace = s.owner.cfg.Workspace
	}
	return runGuestShell(ctx, s.runner, workspace, req, "sandbox container "+strconv.Quote(s.container))
}

// runGuestShell is RunShell for a remote sandbox reached over runner,
// whichever backend put it there: granuleSandbox's VM on a docker host,
// k8sGranuleSandbox's VM in a pod, k8sSandbox's pod, and
// containerSandbox's container all run bash over a sandboxRunner and
// differ only in the transport. what names the sandbox in a transport
// error.
func runGuestShell(ctx context.Context, runner sandboxRunner, workspace string, req ShellRequest, what string) (ShellResult, error) {
	// Sandbox commands a capability grant installs (the host-script
	// capability's stand-in grain) are on a step's PATH ahead of the
	// image's, as they are on run_command's -- which is how a CI step on
	// every other backend runs, so a step calls one by name wherever it
	// lands. Said
	// in the script rather than in req.Env, which is a fixed value and
	// part of the job's cache fingerprint: PATH has to extend the
	// guest's own, and this changes no job's key.
	script := guestCommandPath + shellScript(workspace, req)
	if req.Timeout > 0 {
		// Round up rather than truncate down: when req.Timeout comes from
		// time.Until(deadline) on a job's context, truncating a fractional
		// second makes guest `timeout` kill the step up to 0.999s *before*
		// the host context deadline expires, returning a plain non-zero exit
		// while stepCtx.Err() is still nil and misclassifying a job timeout
		// as a test failure.
		seconds := int(math.Ceil(req.Timeout.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		// `exit $?` for the reason mcp's own sshRunCommandTool spells
		// out: without it the process the transport watches is
		// `timeout` itself, and a status arriving as death-by-signal is
		// no status at all.
		script = fmt.Sprintf("timeout --kill-after=10s %d bash -c %s; exit $?", seconds, shellQuote(script))
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout+shellGrace)
		defer cancel()
	}
	if req.StepID != "" {
		var err error
		script, err = stepRunnerScript(req.StepID, script)
		if err != nil {
			return ShellResult{ExitCode: -1}, err
		}
	}
	argv := []string{"bash", "-c", script}
	var exitCode int
	var stderr string
	var execErr error
	out := ""
	attacher, canAttach := runner.(attachingRunner)
	if req.Attach != nil && req.Attach.ExecID != "" && !canAttach {
		err := &sandboxaccess.ChannelLostError{Cause: fmt.Errorf("%s has no channel to reattach exec %s over", what, req.Attach.ExecID)}
		return ShellResult{ExitCode: -1}, fmt.Errorf("orchestrator: %s: %w", what, err)
	}
	if req.Attach != nil && canAttach {
		var whole bytes.Buffer
		sink := io.Writer(&whole)
		if req.Output != nil {
			sink = io.MultiWriter(&whole, req.Output)
		}
		_, stderr, exitCode, execErr = attacher.ExecAttached(ctx, argv, "", sink, req.Attach)
		out = whole.String()
		if exitCode == -1 && stderr != "" && !strings.Contains(out, stderr) {
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			out += stderr
		}
	} else if execer, ok := runner.(execStreamRunner); ok {
		var whole bytes.Buffer
		sink := io.Writer(&whole)
		if req.Output != nil {
			sink = io.MultiWriter(&whole, req.Output)
		}
		_, stderr, exitCode, execErr = execer.Exec(ctx, argv, "", sink)
		out = whole.String()
		if exitCode == -1 && stderr != "" && !strings.Contains(out, stderr) {
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			out += stderr
		}
	} else if streamer, ok := runner.(streamingRunner); ok && req.Output != nil {
		// The live copy is the result as well, and deliberately: what the
		// caller has been reading line by line is the two streams
		// interleaved, and handing it a differently ordered transcript at
		// the end -- all of stdout, then all of stderr -- would rewrite a
		// log somebody has already read half of.
		var whole bytes.Buffer
		_, stderr, exitCode = streamer.RunStream(ctx, argv, "",
			io.MultiWriter(&whole, req.Output))
		out = whole.String()
		if exitCode == -1 && stderr != "" && !strings.Contains(out, stderr) && !strings.Contains(out, mcp.SessionCutOffNotice) {
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			out += stderr
		}
	} else {
		var stdout string
		stdout, stderr, exitCode = runner.Run(ctx, argv, "")
		out = stdout
		if stderr != "" {
			out += stderr
		}
	}
	if exitCode == -1 && ctx.Err() != nil {
		return ShellResult{Output: out, ExitCode: -1},
			fmt.Errorf("orchestrator: %s stopped answering while running a script: %w", what, ctx.Err())
	}
	if execErr != nil {
		return ShellResult{Output: out, ExitCode: -1},
			fmt.Errorf("orchestrator: %s: %w", what, execErr)
	}
	if exitCode == -1 && strings.Contains(out, mcp.SessionCutOffNotice) {
		// A `kubectl exec` session that ended before the script said how
		// it ended (mcp.KubectlExecRunner.RunStream): the transport, not
		// the script -- which may have done half of what it does.
		return ShellResult{Output: out, ExitCode: -1},
			fmt.Errorf("orchestrator: %s: %s", what, mcp.SessionCutOffNotice)
	}
	if exitCode == -1 {
		// The transport's own complaint is stderr's last line; a step's
		// whole output would make this error a second copy of its log.
		detail := lastLine(stderr, 512)
		if detail == "" {
			detail = lastLine(out, 512)
		}
		if detail == "" {
			detail = "exec failed before the command reported an exit status"
		}
		return ShellResult{Output: out, ExitCode: -1},
			fmt.Errorf("orchestrator: %s: %s", what, detail)
	}
	return ShellResult{Output: out, ExitCode: exitCode}, nil
}

// lastLine is s's last non-blank line, cut to its final max bytes.
func lastLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if len(s) > max {
		s = "..." + s[len(s)-max:]
	}
	return s
}
