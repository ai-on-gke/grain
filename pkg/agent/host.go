package agent

// Where an agent CLI's files are staged and where its process runs.
//
// Every built-in Framework here drives a real CLI as a subprocess, and
// that subprocess is a child of the grain daemon: the private HOME a run
// is given is a temp directory on the daemon's own disk, and the binary
// is a path in the daemon's own filesystem. LocalHost below is that, and
// today it is the only implementation.
//
// The seam is kept rather than inlined for two reasons that hold with
// one implementer. It is where a test double goes -- pkg/agent/codex's
// probeHost stages a home and captures an argv without starting codex,
// and pkg/agent/antigravity's cancelAwareHost is a host whose ReadTail
// honours a cancelled context, which is the contract readDiagnosticLog
// is written against and which a filesystem read cannot demonstrate. And
// it is a narrow, honest boundary: staging a run's configuration and
// starting its process, named once instead of spread across `os` and
// `os/exec` calls in three frameworks.
//
// It is deliberately small. It is not a filesystem abstraction and not a
// remote shell: it is exactly what a Framework does to set one CLI
// invocation up and run it, and nothing a Framework does not already do.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/bwsalmon/grain/pkg/authfix"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/procgroup"
)

// Command is one CLI invocation: the binary, its arguments, what it reads
// on stdin, what to add to its environment, and where to start it.
//
// Env is added to the environment of whatever runs the process, never
// substituted for it -- the same contract TerminalCommand.Env states, and
// the one every Framework already relied on when it built its own
// exec.Cmd. Dir is "" for "wherever the executing process would have
// started it", which is what a granule run (no local sandbox directory)
// passes.
type Command struct {
	Path  string
	Args  []string
	Stdin string
	Env   []string
	Dir   string
}

// Host is where one agent CLI's files are staged and where its process
// runs. Paths are the host's own, in the host's own filesystem, and a
// Framework builds them with path/filepath.
//
// Every method takes a context so an implementation can be
// cancelled and time out. LocalHost ignores it for the filesystem
// operations, which are not cancellable and never block for long.
type Host interface {
	// MkdirTemp creates a new directory with a name derived from pattern
	// in the host's temp area and returns its path -- os.MkdirTemp("",
	// pattern), on whichever machine this Host is.
	MkdirTemp(ctx context.Context, pattern string) (string, error)
	// MkdirAll creates a directory and any missing parent of it.
	MkdirAll(ctx context.Context, path string, perm fs.FileMode) error
	// WriteFile writes data to path, creating or truncating it.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	// CreateTemp creates a new empty file with a name derived from
	// pattern inside dir, mode 0600, and returns its path. The file is
	// closed again before this returns: a caller wants a name to hand a
	// subprocess, not a handle of its own.
	CreateTemp(ctx context.Context, dir, pattern string) (string, error)
	// Symlink creates link as a symbolic link to target, both being
	// paths on this host. It is here for the one thing a staged file
	// cannot express: a credential store the CLI shares with every other
	// process using the same account, and rewrites in place when the
	// provider rotates its tokens (agent/codex's WithAuthFile). Copying
	// such a file into a per-run home would hand back a refreshed login
	// that nothing else ever sees, so it is linked instead -- and the
	// link, like the home it sits in, has to be made on the machine the
	// CLI runs on.
	//
	// target is not staged, read or checked here: it is a path the
	// operator named on this host, and a Host that is another machine
	// cannot say anything about it that this one would believe.
	Symlink(ctx context.Context, target, link string) error
	// ReadTail returns the last limit bytes of the regular file at path,
	// and whether anything before them was left behind. A path that is
	// not a regular file is an error rather than a read: a symlink or a
	// FIFO substituted for a log file grain asked a CLI to write must
	// not become a read that hangs or that leaves this host's own
	// filesystem (antigravity/diagnostics.go, whose check this is).
	ReadTail(ctx context.Context, path string, limit int64) (data []byte, truncated bool, err error)
	// RemoveAll removes path and anything under it. It is what a run's
	// cleanup func calls, so it must tolerate a path that is already
	// gone.
	RemoveAll(ctx context.Context, path string) error
	// Run starts cmd, waits for it, and returns everything it wrote to
	// stdout. When tee is non-nil it also receives stdout as the process
	// produces it, not after it exits: a live transcript and the turn
	// caps that count steps as they stream past are both built on that.
	//
	// A non-zero exit is an error whose message carries the process's
	// stderr, and the stdout produced before it is returned alongside --
	// a run that failed halfway through has still said what it did, and
	// Framework.Run owes its caller that record.
	Run(ctx context.Context, cmd Command, tee io.Writer) (stdout string, err error)
}

// MCPArgsAuthorizer is a Host that must be told which "grain mcpserver"
// argvs it is expected to run. A remote executor (modelexec.RemoteHost)
// starts the mcpserver a Framework configures on its own machine, which
// forwards that argv back to grain to be served there -- and grain serves
// only an argv it was told about here, so that the executor cannot choose
// the server's sandbox, credentials or task for itself.
type MCPArgsAuthorizer interface {
	AuthorizeMCPArgs(args []string)
}

// AuthorizeMCPArgs tells h, if it needs telling, that args is an mcpserver
// argv a Framework built for it. Every Framework calls it on the argv it
// stages, and it does nothing for a Host that runs its mcpserver itself.
func AuthorizeMCPArgs(h Host, args []string) {
	if a, ok := h.(MCPArgsAuthorizer); ok && a != nil {
		a.AuthorizeMCPArgs(args)
	}
}

// UnconfinedHost is an agent.Host where the agent CLI runs directly in the host
// machine with full access to native tools, rather than being confined to Grain's
// sandboxing wrapper.
type UnconfinedHost interface {
	Host
	Unconfined() bool
}

// HostProvider is optionally implemented by Frameworks to expose the underlying
// Host where the agent process runs.
type HostProvider interface {
	Host() Host
}

// LocalHost runs the CLI as a child of this process, on this machine --
// what every Framework did before Host existed, and what a deployment
// with no remote execution configured still does.
type LocalHost struct {
	// Logf is where Run announces each invocation it is about to make,
	// and log.Printf -- the daemon's own log -- when nil, which is what
	// every Framework constructs. A test sets it to capture the line
	// instead of scattering it over the suite's output.
	Logf func(format string, args ...any)
}

// logf is Logf or the daemon's log, never nil.
func (h LocalHost) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// MkdirTemp implements Host.
func (LocalHost) MkdirTemp(_ context.Context, pattern string) (string, error) {
	return os.MkdirTemp("", pattern)
}

// IsLocal reports that LocalHost runs on the local controller machine.
func (LocalHost) IsLocal() bool { return true }

// MkdirAll implements Host.
func (LocalHost) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

// WriteFile implements Host.
func (LocalHost) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// CreateTemp implements Host.
func (LocalHost) CreateTemp(_ context.Context, dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// Symlink implements Host.
func (LocalHost) Symlink(_ context.Context, target, link string) error {
	return os.Symlink(target, link)
}

// ReadTail implements Host.
func (LocalHost) ReadTail(_ context.Context, path string, limit int64) ([]byte, bool, error) {
	// Lstat before Open, so a substituted symlink or special file is
	// refused rather than followed -- opening a FIFO here could hang a
	// run past its deadline.
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	// Again on the open handle: what Lstat described and what was opened
	// are only the same file if nothing swapped it in between.
	info, err = f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file", path)
	}
	start := max(int64(0), info.Size()-limit)
	data, err := io.ReadAll(io.NewSectionReader(f, start, limit))
	if err != nil {
		return nil, false, err
	}
	return data, start > 0, nil
}

// RemoveAll implements Host.
func (LocalHost) RemoveAll(_ context.Context, path string) error { return os.RemoveAll(path) }

// Run implements Host by forking the binary as a child of this process.
//
// procgroup.Prepare is what makes cancelling ctx reach the CLI's own
// children too -- the forked "mcpserver", and anything a tool call
// spawned under it -- rather than orphaning them; every Framework's own
// exec did this before Host existed, and each of them documents why.
//
// The invocation is written to the daemon's log first, credential
// material taken out of it (CommandLine). Before rather than after,
// because the failures this line is most wanted for are the ones where
// the process never comes back: a CLI that hangs to its deadline, or a
// daemon killed while a run was in flight, both leave the journal with
// nothing to say about what was running unless the line was already
// there.
func (h LocalHost) Run(ctx context.Context, c Command, tee io.Writer) (string, error) {
	h.logf("agent: running: %s", CommandLine(c))
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	procgroup.Prepare(cmd)
	cmd.Dir = c.Dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(os.Environ(), c.Env...)
	var stdout, stderr bytes.Buffer
	if tee != nil {
		cmd.Stdout = io.MultiWriter(&stdout, tee)
	} else {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &stderr
	cmd.WaitDelay = localWaitDelay
	err := cmd.Start()
	if err == nil {
		_ = procgroup.SacrificeOnOOM(cmd.Process.Pid)
		err = cmd.Wait()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The CLI itself exited cleanly; only something it left behind
		// still held its output open. What it wrote is all there is.
		h.logf("agent: %s exited, but something it started still held its output open after %s; stopped waiting for it", c.Path, localWaitDelay)
		err = nil
	}
	if err != nil {
		// A CLI the host would not start because its credentials have
		// lapsed is said as the fix a registered classifier names, which
		// orchestrator reads back as an environmental failure rather
		// than the task's (authfix).
		err = authfix.Wrap(err, stderr.String(), c.Path)
		return stdout.String(), fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// localWaitDelay bounds how long Run waits for the CLI's stdout and
// stderr to close once the CLI itself has exited, or once ctx is done.
// Without it, a descendant that inherited them -- the forked mcpserver,
// or anything a tool call left running -- keeps exec.Cmd.Wait blocked
// after the CLI is gone: procgroup's kill only fires on a ctx that is
// cancelled while the CLI is still alive, so an agent that died on its
// own left its run, and a daemon draining at shutdown, waiting on that
// descendant indefinitely (docs/test-runs/2026-10-12-upgrade-interruption-restart.md,
// finding 3). The same bound pkg/upgrade puts on its own commands.
const localWaitDelay = 5 * time.Second
