package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// localExecRunner implements remoteRunner by running argv on this machine
// instead of inside a sandbox guest -- exactly the same coreutils
// (cat/dd/mkdir/bash) ssh_tools.go's handlers send a real
// DockerExecRunner, just executed locally against a temp directory
// standing in for "the remote host's filesystem". DockerExecRunner's own
// command construction (flags, quoting, stdin plumbing) is covered
// separately in docker_exec_runner_test.go; this is
// what proves NewSSHSandboxTools sends the *right* remote commands to
// implement run_command/read_file/edit_file/write_file, the same
// coverage newTestClient's local NewSandboxTools gets in server_test.go.
//
// dir stands in for the directory a real SSH session starts in (normally
// the login user's $HOME) -- read_file/write_file/edit_file's relative
// file_paths resolve against it exactly the way mcp_server.py's own
// read_file/write_file/edit_file rely on the SSH session's own default
// directory rather than cd-ing anywhere themselves (only run_command
// does, via workspace).
type localExecRunner struct {
	dir string
}

func (r localExecRunner) Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.dir
	// The environment is this process's own, plus the sliver of Linux
	// userland a macOS host is missing (guestEnv, below). Only commands
	// the *guest shell* looks up are covered by it: exec.Command resolves
	// argv[0] against this process's PATH rather than cmd.Env, which is
	// fine, because every argv[0] the handlers send -- bash, cat, dd,
	// mkdir, chmod -- exists on either userland, and the one that does
	// not (`timeout`) is looked up by the guest-side bash in the shell
	// string sshRunCommandTool builds.
	cmd.Env = guestEnv()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return outBuf.String(), errBuf.String(), exitErr.ExitCode()
		}
		return outBuf.String(), err.Error(), -1
	}
	return outBuf.String(), errBuf.String(), 0
}

// A grain sandbox guest is always Linux, so ssh_tools.go's handlers are
// free to send it GNU coreutils, and sshRunCommandTool does: its bound is
// the remote `timeout`, with the `--kill-after` grace and the 124/137
// statuses only that coreutil has. localExecRunner runs those same argvs
// on whichever machine the tests are on, and on a macOS host that is BSD
// userland with no `timeout` at all -- every run_command test failing
// with `bash: timeout: command not found` and exit 127, which is a gap in
// the double, not in the code under test. So the double supplies the one
// piece of guest userland it is missing, on a PATH of its own, and the
// production argv is left exactly as it is: that argv is what these tests
// exist to pin, and portabilising it would throw the `--kill-after` the
// bound depends on (see runCommandKillGrace) away to please a host no
// guest resembles.
//
// The shim goes on every host's PATH rather than only darwin's, because
// both halves of that choice pull the same way. One code path means Linux
// CI exercises the installation and the shim's own argv check instead of
// leaving a mac-only branch to rot. And it cannot hide a regression in
// the argv, because where the host has a real GNU `timeout` (or
// Homebrew's `gtimeout`) the shim execs it: on Linux the production argv
// still reaches the actual coreutil, and an argv the coreutil would
// reject still fails there. The hand-written fallback only ever runs
// where there was no `timeout` for it to stand in front of.

// guestTimeoutShimBody is everything in the shim below its `real`
// assignment. What it must get right is the contract sshRunCommandTool
// reads back out of it: 124 when the bound ended the command, 137 when
// the command had to be killed --kill-after later, and otherwise the
// command's own status untouched -- TestSSHRunCommandReportsNonZeroExit
// is asserting that a 3 survives the wrapper, and would pass for the
// wrong reason against a shim that swallowed it.
const guestTimeoutShimBody = `set -u

# 125 is the status GNU timeout reserves for "timeout itself failed", so
# it is what this shim reports for an argv it does not recognise: any
# status in the command's own range would be indistinguishable from a
# result the command produced. Anything reaching here means the argv in
# sshRunCommandTool changed shape and this shim has stopped standing in
# for the coreutil -- which is the point of checking rather than guessing.
fail() {
	echo "timeout shim: $1" >&2
	exit 125
}

[ "$#" -ge 3 ] || fail "want --kill-after=<N>s <seconds> <command>..., got $# argument(s)"
case "$1" in
--kill-after=*s)
	grace=${1#--kill-after=}
	grace=${grace%s}
	;;
*)
	fail "want --kill-after=<N>s as the first argument, got '$1'"
	;;
esac
case "$grace" in
'' | *[!0-9]*) fail "want whole seconds in --kill-after, got '$1'" ;;
esac
# A zero bound means "no bound at all" to the real coreutil, the opposite
# of what asking for one means; runCommandBound.seconds() is what promises
# never to send one, so a zero here is that promise breaking and is worth
# a loud failure rather than a quiet reinterpretation.
duration=$2
case "$duration" in
'' | *[!0-9]* | 0) fail "want a whole second count of at least 1, got '$duration'" ;;
esac
shift 2

if [ -n "$real" ]; then
	exec "$real" --kill-after="${grace}s" "$duration" "$@"
fi

marks=$(mktemp -d) || fail "mktemp -d failed"
trap 'rm -rf "$marks"' EXIT

# Job control, so each background job leads a process group of its own and
# a kill aimed at -PID reaches the whole group. The command needs that for
# the same reason the real coreutil does it: the shell string this bounds
# is 'cd <workspace> && <command>', which is not a single simple command,
# so bash stays alive around whatever it forked and killing bash alone
# would leave that child running -- holding the stdout pipe open, which is
# enough on its own to hang the os/exec call reading it long past the
# bound. The watchdog needs it so that killing the watchdog takes its
# sleep with it, for the same reason.
set -m
"$@" &
command_pid=$!
(
	sleep "$duration"
	: >"$marks/termed"
	kill -TERM -"$command_pid" 2>/dev/null
	sleep "$grace"
	: >"$marks/killed"
	kill -KILL -"$command_pid" 2>/dev/null
) >/dev/null 2>&1 </dev/null &
watchdog_pid=$!
set +m

# The marker files, not the wait status, are what says the bound fired: a
# command stopping on the SIGTERM it was sent reports 143, which is a
# status a command could also have produced on its own, where 124 means
# one thing only.
#
# 2>/dev/null on the waits is not cosmetic: with job control on, bash
# announces a job that died on a signal ("Terminated: 15") on its own
# stderr, and its stderr here is the stderr run_command hands back to a
# model.
wait "$command_pid" 2>/dev/null
status=$?
kill -TERM -"$watchdog_pid" 2>/dev/null
wait "$watchdog_pid" 2>/dev/null

if [ -e "$marks/killed" ]; then
	exit 137
fi
if [ -e "$marks/termed" ]; then
	exit 124
fi
exit "$status"
`

// guestTimeoutShimScript renders the shim, where real is a shell-quoted
// absolute path to the host's own GNU `timeout`, or the empty string when
// it has none. It is spliced in as a shell assignment rather than
// templated into the body because the body strips a suffix with
// ${grace%s}, and a lone fmt verb sharing a string with a bash `%` is a
// trap for whoever edits it next.
func guestTimeoutShimScript(real string) string {
	return "#!/bin/bash\n" +
		"# A stand-in for the GNU coreutils `timeout` that every grain sandbox\n" +
		"# guest has and a BSD-userland host does not, written by this package's\n" +
		"# own tests (pkg/mcp/ssh_tools_test.go) so that localExecRunner can\n" +
		"# present the Linux userland it is impersonating. It implements the\n" +
		"# one contract sshRunCommandTool depends on and refuses everything\n" +
		"# else; it is not a general-purpose timeout.\n" +
		"real=" + shellQuote(real) + "\n" +
		guestTimeoutShimBody
}

// guestTimeoutShimDir is the directory holding that shim, for the life of
// the test binary. TestMain sets it before any test runs.
var guestTimeoutShimDir string

// guestEnv is the environment localExecRunner hands every command it
// runs: this process's own, so that ordinary userland still finds the
// HOME, TMPDIR and locale it expects, with the shim directory ahead of
// the host's PATH. The existing PATH entry is dropped rather than a
// second one appended, because which of two PATH entries a child's libc
// honours is not something to leave to chance.
func guestEnv() []string {
	host := os.Environ()
	if guestTimeoutShimDir == "" {
		// Only reachable if a test binary ran without TestMain, which Go
		// does not do -- the guard is here so that a bug there cannot
		// silently turn into a PATH starting with an empty entry, which
		// means "the current directory" to a shell.
		return host
	}
	env := make([]string, 0, len(host)+1)
	for _, kv := range host {
		if !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	path := guestTimeoutShimDir
	if hostPath := os.Getenv("PATH"); hostPath != "" {
		path += string(os.PathListSeparator) + hostPath
	}
	return append(env, "PATH="+path)
}

// TestMain lives in this file, the one that owns localExecRunner, because
// the shim above is the only thing this package needs standing before any
// test runs: it has to exist on disk before the first run_command call
// and be swept up after the last one, and t.TempDir() cannot do that for
// a double that five other files build from a bare struct literal with no
// *testing.T in reach.
//
// A shim that cannot be installed fails the binary rather than being
// skipped past. Skipping would hand the run_command tests a host with no
// `timeout`, which is the failure this exists to remove, and it would
// report it as something else.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "grain-mcp-guest-userland-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the guest userland directory: %v\n", err)
		os.Exit(1)
	}
	// A real `timeout` is preferred where the host has one, and required
	// nowhere: gtimeout is the name Homebrew's coreutils installs it
	// under, so a mac with that formula gets the genuine article and a
	// mac without it gets the fallback. Looked up against this process's
	// own PATH, which the shim directory is deliberately never added to,
	// so this cannot find the shim itself.
	real := ""
	for _, name := range []string{"timeout", "gtimeout"} {
		if path, lookErr := exec.LookPath(name); lookErr == nil {
			real = path
			break
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "timeout"), []byte(guestTimeoutShimScript(real)), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "writing the guest `timeout` shim: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	guestTimeoutShimDir = dir

	code := m.Run()
	// Not deferred: os.Exit runs no deferred functions.
	os.RemoveAll(dir)
	os.Exit(code)
}

func newSSHTestClient(t *testing.T, workspace string) *Client {
	t.Helper()
	registry := NewRegistry()
	registry.Register(NewSSHSandboxTools(localExecRunner{dir: workspace}, workspace)...)
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSSHSandboxToolsAdvertiseSameFourNamesAsLocal(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run_command", "read_file", "edit_file", "write_file"}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d: %+v", len(tools), len(want), tools)
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("tool %d: got %q, want %q", i, tools[i].Name, name)
		}
	}
}

func TestSSHRunCommandRunsInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	client := newSSHTestClient(t, workspace)

	res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("run_command reported error: %s", res.Text())
	}
	if !strings.Contains(res.Text(), workspace) {
		t.Errorf("run_command output %q does not mention workspace %q", res.Text(), workspace)
	}
}

func TestSSHRunCommandReportsNonZeroExit(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())

	res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("run_command with exit 3: want IsError true")
	}
	if !strings.Contains(res.Text(), "exit=3") {
		t.Errorf("run_command output %q does not report exit=3", res.Text())
	}
}

// A command's own 124 or 137 -- its own `timeout` expiring, a process the
// kernel killed -- is its result, not run_command's bound firing, and must
// not be reported as "Killed after 300s by run_command's default bound".
// Seen in the 2026-10-02 §5 walkthrough, where a guest's `timeout 4 bash
// -c 'echo > /dev/tcp/...'` came back with that notice after 4 seconds.
func TestSSHRunCommandLeavesACommandsOwn124And137Alone(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())
	for _, code := range []string{"124", "137"} {
		res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "exit " + code})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Text(), "exit="+code) {
			t.Errorf("exit %s: output %q does not report it", code, res.Text())
		}
		if strings.Contains(res.Text(), runCommandKilledMarker) {
			t.Errorf("exit %s after no time at all was reported as the bound killing it: %q", code, res.Text())
		}
	}
}

// TestSSHRunCommandAppliesDefaultTimeoutWhenNoneGiven is ssh_tools.go's
// side of bwsalmon/agents#575's regression coverage: unlike the local
// run_command (TestRunCommandAppliesDefaultTimeoutWhenNoneGiven), this
// path enforces its timeout with the remote `timeout` coreutil rather
// than ctx cancellation, so it needs its own proof that omitting
// "timeout" still gets wrapped in one rather than running unbounded on
// the remote side.
func TestSSHRunCommandAppliesDefaultTimeoutWhenNoneGiven(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	// There is deliberately no skip for a host without `timeout`: the
	// guest localExecRunner impersonates always has one, because the
	// double puts one there (guestEnv, above). Skipping on a host without
	// its own was how this test silently stopped running on macOS, which
	// is exactly where a missing `timeout` is the normal case.
	old := defaultRunCommandTimeout
	defaultRunCommandTimeout = time.Second
	t.Cleanup(func() { defaultRunCommandTimeout = old })

	client := newSSHTestClient(t, t.TempDir())
	start := time.Now()
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("run_command with no timeout took %s, want near-instant given the shrunk default, not close to the full 30s sleep", elapsed)
	}
	if !res.IsError {
		t.Error("run_command killed by the default timeout: want IsError true")
	}
}

func TestSSHWriteThenReadFileRoundTrips(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())
	ctx := context.Background()

	res, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "sub/dir/hello.txt",
		"content":   "line one\nline two\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("write_file reported error: %s", res.Text())
	}

	res, err = client.CallTool(ctx, "read_file", map[string]any{"file_path": "sub/dir/hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := "     1\tline one\n     2\tline two"
	if res.Text() != want {
		t.Errorf("read_file text = %q, want %q", res.Text(), want)
	}
}

func TestSSHReadFileMissingIsError(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())
	res, err := client.CallTool(context.Background(), "read_file", map[string]any{"file_path": "nope.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("read_file on a missing file: want IsError true")
	}
}

func TestSSHEditFileReplacesUniqueMatch(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())
	ctx := context.Background()

	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "hello world\n",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := client.CallTool(ctx, "edit_file", map[string]any{
		"file_path": "f.txt", "old_string": "world", "new_string": "there",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("edit_file reported error: %s", res.Text())
	}

	res, err = client.CallTool(ctx, "read_file", map[string]any{"file_path": "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "     1\thello there"; res.Text() != want {
		t.Errorf("read_file after edit = %q, want %q", res.Text(), want)
	}
}

func TestSSHEditFileAmbiguousMatchRequiresReplaceAll(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())
	ctx := context.Background()

	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "aa aa aa\n",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := client.CallTool(ctx, "edit_file", map[string]any{
		"file_path": "f.txt", "old_string": "aa", "new_string": "bb",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("edit_file with an ambiguous match and no replace_all: want IsError true")
	}

	res, err = client.CallTool(ctx, "edit_file", map[string]any{
		"file_path": "f.txt", "old_string": "aa", "new_string": "bb", "replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("edit_file with replace_all reported error: %s", res.Text())
	}

	res, err = client.CallTool(ctx, "read_file", map[string]any{"file_path": "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "     1\tbb bb bb"; res.Text() != want {
		t.Errorf("read_file after replace_all = %q, want %q", res.Text(), want)
	}
}

// The sandbox-guest half of the same guard NewSandboxTools got: an empty
// old_string is refused rather than interleaved through the file (task
// 244).
func TestSSHEditFileRefusesAnEmptyOldString(t *testing.T) {
	client := newSSHTestClient(t, t.TempDir())
	ctx := context.Background()

	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "abc",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := client.CallTool(ctx, "edit_file", map[string]any{
		"file_path": "f.txt", "old_string": "", "new_string": "X", "replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("empty old_string was accepted: %s", res.Text())
	}

	res, err = client.CallTool(ctx, "read_file", map[string]any{"file_path": "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "     1\tabc"; res.Text() != want {
		t.Errorf("read_file after the refused edit = %q, want %q", res.Text(), want)
	}
}
