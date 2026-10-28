// Package guestexec implements "granule exec": running a command inside
// the VM guest rather than in the container itself -- so `kubectl exec`
// on a granule VM container ends up in the workload the VM is actually
// running, not the otherwise-empty scratch container wrapping it.
//
// # Why this is not SSH any more
//
// It used to be. The guest ran an sshd, granule generated a keypair per
// boot and handed the public half over on the kernel command line, and
// this package dialed the guest's address on netshim's control link.
// That worked, and cost the guest an account to log into, a host
// keypair, an authorized_keys file, a service to keep running and --
// most of all -- a working network before anything could be run inside
// it at all.
//
// The last of those is what made it worth replacing rather than tidying.
// A guest whose networking had not come up, or had come up wrong, was a
// guest granule could not get into to find out why: perfectly healthy,
// booted, sitting at a login prompt on its console, and unreachable by
// the one mechanism that could have asked it anything. Every part of
// bringing that network up -- the control link's address, the udev rules
// that decide the interface's name, the unit that configures it -- was
// therefore load-bearing for debugging the network.
//
// virtio-vsock has none of that in the path. The transport is the virtio
// device itself, so it works before the guest has an address, while its
// network is broken, and with no NIC attached at all. Under
// cloud-hypervisor the host end is a unix socket inside the VM's own
// container ("hybrid" vsock, the same shape firecracker's is), so
// nothing has to be addressable and nothing has to be routed.
//
// One thing to know rather than assume: cloud-hypervisor's own docs list
// CONFIG_VHOST_VSOCK under host kernel requirements, even though the
// device it implements is derived from firecracker's userspace one.
// Whether that is a real requirement for the socket= mode or a
// copied-across prerequisite has not been established here; a VM
// container is privileged and has /dev/kvm either way, so this has not
// been a constraint in practice, but a host that turns out to need the
// module is the first place to look if the socket never appears.
//
// The guest side is cmd/granule-agent; the wire format between them is
// pkg/execwire, whose package comment covers why the protocol
// carries no authentication of its own.
package guestexec

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/bwsalmon/granule/pkg/execwire"
)

const (
	envSocket         = "CHV_VSOCK_SOCKET"
	envUser           = "GRANULE_EXEC_USER"
	envConnectTimeout = "GRANULE_EXEC_CONNECT_TIMEOUT"
	envCancelGrace    = "GRANULE_EXEC_CANCEL_GRACE"
	envReadyFile      = "GRANULE_READY_FILE"

	// DefaultSocket must agree with pkg/config's own
	// defaultVsockSocket: "granule run" tells cloud-hypervisor where to
	// put this socket and "granule exec" has to find it, and they are two
	// modes of the same binary in the same container rather than two
	// configured things.
	DefaultSocket = "/run/granule/vsock.sock"

	defaultUser           = "root"
	defaultConnectTimeout = 30 * time.Second

	dialTimeout   = 3 * time.Second
	retryInterval = 500 * time.Millisecond

	readyInitialInterval = 25 * time.Millisecond
	readyFastWindow      = 500 * time.Millisecond
	readyMaxInterval     = 500 * time.Millisecond

	// DefaultCancelGrace is how long a cancelled session stays open
	// after asking the guest to interrupt the command, before the
	// connection is closed regardless -- Config.CancelGrace when it is
	// left unset.
	//
	// It is a ceiling rather than a cost: the session ends the moment
	// the exit frame arrives, so this is only spent in full by a command
	// that does not stop when asked. What it has to cover is everything
	// between the signal frame going out and that exit frame coming
	// back: the guest delivering SIGINT to the command's process group,
	// the command's own handler or trap running, the agent draining what
	// it wrote, and the exit frame being written. The last of those has
	// a bound of its own on the guest side -- pkg/agent's
	// outputGrace, five seconds -- which is why two seconds was wrong:
	// it was shorter than the window the other end is allowed to take,
	// so a session whose output was held open by something the command
	// left running lost the answer every time, and a guest merely busy
	// enough to be slow lost it intermittently. Ten seconds sits behind
	// outputGrace with room for a loaded guest, and still well inside
	// the termination grace a container runtime gives the client itself.
	//
	// The guest's own escalation to SIGKILL (pkg/agent's
	// terminateGrace) sits behind all of this and is what guarantees the
	// process actually goes, whatever this end decides to wait for.
	DefaultCancelGrace = 10 * time.Second
)

// Config holds everything needed to run a command inside the guest.
type Config struct {
	// Socket is the host end of the guest's vsock device: the unix
	// socket cloud-hypervisor created in this container. Defaults to
	// DefaultSocket.
	Socket string

	// User is the guest account to run as. Defaults to "root", the only
	// account the reference guest image (deploy/guest-image) creates.
	User string

	// ConnectTimeout bounds how long to keep retrying the initial
	// connection before giving up: granule-agent starts when the guest
	// does, so how long that takes is guest boot time rather than
	// anything granule controls.
	ConnectTimeout time.Duration

	// CancelGrace bounds the second half of cancellation: how long a
	// cancelled session stays open after asking the guest to interrupt
	// the command, waiting for the command's last words and its exit
	// code, before the connection is closed regardless. Zero, the
	// default, means DefaultCancelGrace.
	//
	// Read from the environment like the fields above it, because how
	// long an interrupted command takes to answer is a property of the
	// guest rather than of one command: a guest slow enough to miss the
	// default is one an operator can give more room
	// (GRANULE_EXEC_CANCEL_GRACE) without rebuilding anything.
	CancelGrace time.Duration

	// ReadyFile is a path inside the guest that must exist before Ready
	// says yes -- the guest's own declaration that whatever it boots
	// into is up, on top of the agent answering (see Ready). Empty, the
	// default, leaves readiness meaning exactly what it meant before
	// this existed: the agent answered.
	//
	// Read from the environment like the three fields above, because it
	// is a property of the guest this container fronts rather than of
	// one command: GRANULE_READY_FILE, set on the VM container by
	// "granule vm create -ready-file", and by a pod manifest that sets
	// it directly (see deploy/k8s/), so the readinessProbe in that
	// manifest picks it up without its command line changing.
	//
	// Nothing but Ready reads it: an ordinary "granule exec" into a guest
	// that has not declared itself ready is exactly how one finds out
	// why it hasn't, so a session must not be gated on it.
	ReadyFile string

	// Dir is the guest directory to run the command in, absolute.
	// Empty means the home directory of User, which is where a session
	// has always started.
	//
	// Unlike the three fields above this is not read from the
	// environment: it belongs to one command rather than to the guest
	// this container fronts, and "granule exec -w" is where it comes
	// from. FromEnv leaves it, and Env below, at their zero values.
	Dir string

	// Env are extra "KEY=value" entries for the guest command, overlaid
	// on the environment the agent builds for User rather than replacing
	// it -- see execwire.Request.Env. Set by "granule exec -e".
	Env []string

	// Container runs the command inside the container a `granule wrap`
	// image wrapped rather than in the guest around it -- see
	// execwire.Request.Container. User, Dir and Env then mean the
	// container's: an account in the wrapped image (empty for the
	// container's own USER), a directory in its filesystem (empty for
	// its WORKDIR), entries over its environment. Set by "granule exec
	// --container".
	Container bool
}

// UserFromEnv returns the guest account GRANULE_EXEC_USER names, or "" if
// it is unset. "granule run" uses it to tell the guest which account to
// prepare, so that the account a caller execs as and the account the
// guest expects are one setting rather than two that can disagree.
func UserFromEnv() string {
	return os.Getenv(envUser)
}

// FromEnv builds a Config from the process environment.
//
// Unlike the SSH transport this replaced, there is nothing here that has
// to be supplied per VM: the socket has a default that "granule run" uses
// too, and there is no key to point at. GRANULE_EXEC_ADDR and
// GRANULE_EXEC_KEY are accordingly gone rather than deprecated -- both
// named things that no longer exist (a guest address to dial, a private
// key to authenticate with), so keeping them accepted-and-ignored would
// only preserve the impression that exec still depends on guest
// networking.
func FromEnv() (Config, error) {
	cfg := Config{
		Socket:    getEnvDefault(envSocket, DefaultSocket),
		User:      getEnvDefault(envUser, defaultUser),
		ReadyFile: os.Getenv(envReadyFile),
	}

	cfg.ConnectTimeout = defaultConnectTimeout
	if v := os.Getenv(envConnectTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q", envConnectTimeout, v)
		}
		cfg.ConnectTimeout = d
	}

	cfg.CancelGrace = DefaultCancelGrace
	if v := os.Getenv(envCancelGrace); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q", envCancelGrace, v)
		}
		cfg.CancelGrace = d
	}

	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// Usage is the synopsis ParseArgs quotes back at a caller who got the
// arguments wrong, and cmd/granule-shim's own -h for this mode.
const Usage = `usage: granule exec [--container] [-u <user>] [-w <dir>] [-e KEY=value]... [--] [<command> [args...]]

Runs one command inside the guest, or an interactive login shell when
given none:

  docker exec    granule-vm-web granule exec -- uname -a
  docker exec -it granule-vm-web granule exec
  docker exec    granule-vm-web granule exec -w /src -e GOFLAGS=-mod=vendor -- go build ./...
  docker exec    granule-vm-web granule exec -u redis -- redis-server --test-config

  -u, --user <user>      guest account to run as (default: GRANULE_EXEC_USER,
                         or root)
  -w, --workdir <dir>    absolute guest directory to run in (default: the
                         account's home directory)
  -e, --env KEY=value    add or override one environment variable,
                         repeatable; everything else the session would
                         have had (PATH, HOME, USER, ...) stays
  --container            run inside the container a "granule wrap" image
                         wrapped, the way "docker exec" enters one: in its
                         root, as its USER, in its WORKDIR, with its
                         environment. -u, -w and -e then mean the
                         container's, and <command> is exec'd directly
                         rather than through a shell`

// Options is one exec, as ParseArgs understands the command line.
type Options struct {
	// User is -u/--user: the guest account to run as, or "" to leave
	// the container's own default (GRANULE_EXEC_USER, or root) alone.
	//
	// Per command rather than per container, which is what a Granulefile
	// `USER` in a VM stage needs: one stage runs some of its `RUN`s as
	// the service account and the rest as root, and the container
	// fronting the guest is the same one throughout.
	User string

	// Workdir is -w/--workdir: the guest directory to run in, or "" for
	// the account's home directory.
	Workdir string

	// Env is every -e/--env, in the order given.
	Env []string

	// Container is --container: run inside the wrapped container rather
	// than in the guest. See Config.Container.
	Container bool

	// Command is what is left after the flags -- the guest command and
	// its arguments, empty for an interactive login shell.
	Command []string
}

// ParseArgs turns the arguments after "granule exec" into an Options.
//
// Flags are read up to the first thing that is not one, which is where
// the guest command starts; "--" ends them explicitly, and is how every
// documented invocation spells it ("granule exec -- <command>"). A guest
// command whose own first argument begins with "-" therefore needs that
// "--", the same way it does for docker exec.
func ParseArgs(args []string) (Options, error) {
	fs := flag.NewFlagSet("granule exec", flag.ContinueOnError)
	// The flag package's own dump lists flags and says nothing about the
	// "--" or the command after it, so errors carry Usage instead.
	fs.SetOutput(io.Discard)
	var opts Options
	// Both spellings of each, single- and double-dash alike (Go's flag
	// package treats them the same), because this is the flag someone
	// arrives at from "docker exec -w"/"--workdir" and "-e"/"--env".
	fs.StringVar(&opts.User, "u", "", "guest account to run the command as")
	fs.StringVar(&opts.User, "user", "", "guest account to run the command as")
	fs.StringVar(&opts.Workdir, "w", "", "guest directory to run the command in")
	fs.StringVar(&opts.Workdir, "workdir", "", "guest directory to run the command in")
	fs.BoolVar(&opts.Container, "container", false, "run inside the wrapped container rather than in the guest")
	env := (*repeatedFlag)(&opts.Env)
	fs.Var(env, "e", "environment variable to set, KEY=value, repeatable")
	fs.Var(env, "env", "environment variable to set, KEY=value, repeatable")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Options{}, err
		}
		return Options{}, fmt.Errorf("%w\n\n%s", err, Usage)
	}

	for _, e := range opts.Env {
		key, _, ok := strings.Cut(e, "=")
		if !ok || key == "" {
			// docker exec reads a bare "-e NAME" out of its own
			// environment; this does not, because the environment it
			// would be read from is a scratch container nobody set up
			// for it, and a variable that quietly did not arrive is
			// worse than one that was refused.
			return Options{}, fmt.Errorf("-e %q: expected KEY=value (a bare variable name is not taken from this container's environment -- write -e %s=\"$%s\" if that is what you meant)\n\n%s", e, e, e, Usage)
		}
	}
	if opts.Workdir != "" && !strings.HasPrefix(opts.Workdir, "/") {
		// Refused here as well as in the guest so the message names the
		// flag rather than arriving as a rejected request.
		return Options{}, fmt.Errorf("-w %q: the guest working directory must be an absolute path\n\n%s", opts.Workdir, Usage)
	}

	opts.Command = fs.Args()
	return opts, nil
}

// repeatedFlag collects a flag given more than once, in order.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, " ") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// Run connects to the guest and runs command, returning once the session
// ends. command is shell-quoted and joined into a single command line
// for the guest's shell to interpret; an empty command instead requests
// an interactive login shell.
//
// When stdin is a terminal, Run puts it into raw mode for the duration
// of the session (the same way an ordinary interactive `ssh` client did)
// and forwards SIGWINCH as terminal resizes; otherwise stdin/stdout/
// stderr are simply piped through. Either way, closing ctx tears the
// session down.
//
// The returned int is the remote command's exit code, following
// os/exec.Cmd.Wait's convention of being meaningful even when err is
// non-nil (a non-zero remote exit is reported through the code, not
// err).
//
// With cfg.Container the command is not joined into a line at all: it is
// sent as argv and exec'd directly inside the container, which need not
// have a shell to interpret one.
func Run(ctx context.Context, cfg Config, command []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if cfg.Container && len(command) > 0 {
		return run(ctx, cfg, execwire.Request{Argv: command}, stdin, stdout, stderr)
	}
	return RunLine(ctx, cfg, shellJoin(command), stdin, stdout, stderr)
}

// RunLine behaves exactly like Run, except line is already the single
// command line to send verbatim, rather than discrete arguments for Run
// to shell-quote and join itself. An empty line requests an interactive
// login shell, exactly like Run(ctx, cfg, nil, ...) does.
//
// This exists for callers that already have a shell command line in
// hand -- namely ShellCommandLine, used by cmd/granule-shim's "sh"/"bash"
// shims -- and would otherwise have Run's own shellJoin re-quote (and so
// corrupt: single-quoting the whole line defeats any variable expansion
// or word splitting a real sh/bash -c would have applied) a string
// that's already meant to be interpreted by a shell, not treated as a
// single literal argument.
func RunLine(ctx context.Context, cfg Config, line string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return run(ctx, cfg, execwire.Request{Line: line}, stdin, stdout, stderr)
}

// run is one session: req carries the command (Line or Argv), and cfg
// everything else about it.
func run(ctx context.Context, cfg Config, req execwire.Request, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if cfg.Socket == "" {
		cfg.Socket = DefaultSocket
	}
	if cfg.CancelGrace <= 0 {
		cfg.CancelGrace = DefaultCancelGrace
	}

	conn, err := dialWithRetry(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	// One writer at a time: the stdin pump and the SIGWINCH forwarder
	// below both write frames onto this connection.
	var mu sync.Mutex
	writeFrame := func(typ byte, payload []byte) error {
		mu.Lock()
		defer mu.Unlock()
		return execwire.WriteFrame(conn, typ, payload)
	}

	req.User, req.Dir, req.Env, req.Container = cfg.User, cfg.Dir, cfg.Env, cfg.Container
	tty, _ := stdin.(*os.File)
	if tty != nil && term.IsTerminal(int(tty.Fd())) {
		req.TTY = true
		// The terminal is on this side of the connection, so this is the
		// only end that knows what it can draw: the guest's agent has
		// nothing but its own environment to go on, and in a guest that
		// is empty, which left every pty session running under
		// TERM=dumb. Empty here (no TERM set at all) leaves the choice
		// to the agent rather than asserting a type this end does not
		// have either.
		req.Term = os.Getenv("TERM")
		if w, h, err := term.GetSize(int(tty.Fd())); err == nil {
			req.Cols, req.Rows = uint16(w), uint16(h)
		}
	} else {
		tty = nil
	}

	if err := execwire.WriteRequest(conn, req); err != nil {
		return 0, fmt.Errorf("sending the request: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		return 0, fmt.Errorf("reading the guest's reply: %w", err)
	}
	if !resp.OK {
		return 0, fmt.Errorf("the guest could not start the command: %s", resp.Error)
	}
	// The agent said yes; it did not necessarily say yes to all of it.
	// An agent older than a field ignores that field rather than
	// refusing it, so a caller who asked for -w /src would otherwise get
	// a clean, successful run in the account's home directory instead.
	// The command has already started by the time this can be checked
	// -- the response is what reports the start -- so this is a loud
	// error in place of a wrong answer, not a way to avoid running
	// anything. See execwire's package comment.
	if missing := resp.MissingFeatures(req); len(missing) > 0 {
		return 0, fmt.Errorf("the guest's granule-agent does not support %s, so the command ran without it: the guest image is older than this binary, and the two ship from one commit (see pkg/execwire)", strings.Join(missing, ", "))
	}

	if tty != nil {
		restore, err := attachTTY(tty, writeFrame)
		if err != nil {
			return 0, err
		}
		defer restore()
	}

	// Cancellation, in two steps.
	//
	// The first asks the guest to interrupt the command, and keeps the
	// session open afterwards: SIGINT is what a ^C on a terminal would
	// have delivered, and the point of sending it in band is that the
	// command's own last words -- the "interrupted, cleaning up" a build
	// prints, or the stack a test runner dumps -- arrive over a
	// connection that is still there to carry them.
	//
	// The second closes the connection, which is all a client could do
	// before there was a signal frame, and is what unblocks the read
	// loop below and the stdin pump's next write. It happens either way,
	// so a command that ignores SIGINT still costs a caller only
	// cfg.CancelGrace beyond its own cancellation, and the guest's agent
	// reads the closed connection as "end it" (pkg/agent).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		if !resp.Supports(execwire.FeatureSignal) {
			// The guest's agent is older than the signal frame, and
			// would step over one without acting on it. Say so rather
			// than appear to have asked: an agent and a client from
			// different commits is a build mistake (see
			// pkg/execwire), and this is the moment it has a
			// visible consequence.
			log.Printf("the guest agent predates this client's signal frame; ending the session by closing the connection instead of interrupting the command")
		} else if err := writeFrame(execwire.TypeSignal, execwire.EncodeSignal(int(syscall.SIGINT))); err == nil {
			select {
			case <-time.After(cfg.CancelGrace):
			case <-done:
				return
			}
		}
		conn.Close()
	}()

	if stdin != nil {
		go pumpStdin(stdin, writeFrame)
	}

	return readSession(br, stdout, stderr)
}

// ProbeCommand is what Ready runs inside the guest to decide the guest
// is up, when no ReadyFile is configured. It is deliberately the same
// trivial command every hand-written poll loop around "granule exec"
// already used, so that moving a caller off its own loop and onto Ready
// does not change what is being asked.
const ProbeCommand = "true"

// ErrNotDeclared is what a probe against a reachable guest that has not
// created its ReadyFile comes back as. It is a distinct error because it
// is a distinct situation, and the one a caller most needs told apart
// from the others: the VM booted, granule-agent answered, a command ran
// -- and the guest itself says its workload is not up yet. Everything
// else Ready can fail with means the guest could not be reached at all.
var ErrNotDeclared = errors.New("the guest has not declared itself ready")

// ExitNotDeclared is the exit status "granule ready" reports for
// ErrNotDeclared, as against the 1 every other failure exits with (see
// cmd/granule-shim's runReady). It exists so that a caller running the probe
// through "docker exec"/"crictl exec" -- pkg/dockervm's Inspect is
// the one in this repo -- can tell "the agent answers but the workload
// is not up" from "the guest is still booting" without matching on a
// message.
const ExitNotDeclared = 2

// Ready reports whether the guest is up: it connects to granule-agent
// over the VM's vsock device and runs one command in it, retrying until
// that succeeds, cfg.ConnectTimeout elapses, or ctx is cancelled. A nil
// return means the guest is ready.
//
// # What "ready" means here
//
// Two conditions, the second of them opt-in:
//
//   - granule-agent answers and can run a command. Always checked, and on
//     its own it is the honest limit of what this path can observe from
//     outside the guest -- a smaller claim than a caller usually wants,
//     since a guest boots into being reachable some seconds before
//     whatever it is running is actually serving, so "I can exec" and
//     "the workload is up" are two different instants.
//   - cfg.ReadyFile exists inside the guest, when one is configured.
//     This is the guest's own statement about the second instant: a
//     setup script's last line, a systemd ExecStartPost=, an OpenRC
//     start_post -- one "touch /run/granule/ready" wherever the guest
//     image knows its workload is serving.
//
// The second is opt-in because a convention every guest image has to
// follow is a convention some guest will not follow, and such a guest
// must not silently become "never ready". Absent a ReadyFile this means
// exactly what it meant before the file existed, and a guest image that
// knows nothing about any of this is unaffected.
//
// The two conditions are reported apart when the wait fails: a guest
// that is still booting comes back as a connection that could not be
// made, and one whose agent answers while its file is absent comes back
// as ErrNotDeclared, naming the path it is waiting for.
//
// A cfg.ConnectTimeout of zero makes this a single attempt, which is
// what a container readiness probe wants: whatever runs the probe is
// already doing the retrying, on a schedule of its own.
func Ready(ctx context.Context, cfg Config) error {
	deadline := time.Now().Add(cfg.ConnectTimeout)

	// Each attempt dials once rather than leaving the retrying to
	// RunLine's own dial loop, so that a guest which accepts the
	// connection and then fails the command -- an agent up before the
	// account it has to switch to exists, say -- is retried too, and not
	// only a dial that never lands.
	attempt := cfg
	attempt.ConnectTimeout = 0

	start := time.Now()
	interval := readyInitialInterval
	for {
		err := probe(ctx, attempt)
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			if cfg.ConnectTimeout <= 0 {
				return err
			}
			// Which of the two conditions ran out of time, said in the
			// message rather than left to be inferred from a path
			// appearing in it: "did not answer" would be plainly wrong
			// about a guest whose agent answered every attempt.
			if errors.Is(err, ErrNotDeclared) {
				return fmt.Errorf("still not ready after %s, and the guest is reachable: %w", cfg.ConnectTimeout, err)
			}
			return fmt.Errorf("the guest did not answer within %s: %w", cfg.ConnectTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		if time.Since(start) < readyFastWindow {
			if interval < 50*time.Millisecond {
				interval = 50 * time.Millisecond
			}
		} else {
			if interval < 100*time.Millisecond {
				interval = 100 * time.Millisecond
			} else if interval < 250*time.Millisecond {
				interval = 250 * time.Millisecond
			} else {
				interval = readyMaxInterval
			}
		}
	}
}

// probe makes one attempt at the readiness command, folding a non-zero
// exit into an error: unlike an ordinary session, where the command's
// status is the answer the caller wanted, here anything but zero means
// the guest is not (yet) ready.
//
// Both conditions travel in one session rather than two, because the
// exit status already tells them apart: a guest that is not reachable
// fails before any command runs (RunLine's error), and a status can only
// come back from a guest whose agent answered.
func probe(ctx context.Context, cfg Config) error {
	line := ProbeCommand
	if cfg.ReadyFile != "" {
		line = readyFileCommand(cfg.ReadyFile)
	}
	code, err := RunLine(ctx, cfg, line, nil, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	if cfg.ReadyFile == "" {
		return fmt.Errorf("the guest ran %q and it exited with status %d", ProbeCommand, code)
	}
	if code == 1 {
		// "test -e" says absent with 1 and nothing else, so this is the
		// ordinary case: the guest is up and has not got there yet.
		return fmt.Errorf("%w: granule-agent answered, but %s does not exist inside the guest yet (%s)", ErrNotDeclared, cfg.ReadyFile, envReadyFile)
	}
	// Any other status is the check itself having gone wrong -- no shell
	// builtin "test", a path the account cannot stat. Still the second
	// condition that is outstanding, so still ErrNotDeclared, but said
	// differently: waiting longer will not fix this one.
	return fmt.Errorf("%w: granule-agent answered, but %q exited with status %d rather than reporting the file present or absent (%s)", ErrNotDeclared, readyFileCommand(cfg.ReadyFile), code, envReadyFile)
}

// readyFileCommand is the guest command a configured ReadyFile becomes:
// one "test -e", which is a shell builtin everywhere and so needs
// nothing installed in the guest -- the same reason the declaration
// itself is a file rather than a unit to query or an endpoint to dial.
func readyFileCommand(path string) string {
	return "test -e " + shellJoin([]string{path})
}

// readSession consumes frames until the exit frame, which is the only
// thing that ends a session normally. A stream that stops before one is
// a guest that went away mid-command, and is reported as an error rather
// than as an exit code nobody sent.
func readSession(br *bufio.Reader, stdout, stderr io.Writer) (int, error) {
	for {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, errors.New("the guest closed the connection before the command reported an exit status")
			}
			return 0, err
		}
		switch typ {
		case execwire.TypeStdout:
			if stdout != nil {
				if _, err := stdout.Write(payload); err != nil {
					return 0, err
				}
			}
		case execwire.TypeStderr:
			if stderr != nil {
				if _, err := stderr.Write(payload); err != nil {
					return 0, err
				}
			}
		case execwire.TypeNote:
			// The agent explaining something about the session itself,
			// rather than anything the command wrote. It goes to this
			// process's own log rather than into the stderr stream a
			// caller may be capturing -- the same place the warning
			// about an agent too old to signal goes, and for the same
			// reason: a note is about the session, and mixing it into
			// the command's output would make it part of the answer.
			log.Printf("%s", strings.TrimRight(string(payload), "\n"))
		case execwire.TypeExit:
			code, err := execwire.DecodeExit(payload)
			if err != nil {
				return 0, err
			}
			return code, nil
		}
	}
}

func pumpStdin(stdin io.Reader, writeFrame func(byte, []byte) error) {
	buf := make([]byte, 32<<10)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			if werr := writeFrame(execwire.TypeStdin, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			_ = writeFrame(execwire.TypeStdinClose, nil)
			return
		}
	}
}

// attachTTY puts f (stdin, already known to be a terminal) into raw mode
// and forwards SIGWINCH as resizes for as long as the session lasts. The
// returned func restores the local terminal and must be called once the
// session ends.
func attachTTY(f *os.File, writeFrame func(byte, []byte) error) (restore func(), err error) {
	fd := int(f.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("setting local terminal to raw mode: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for range sigCh {
			if w, h, err := term.GetSize(fd); err == nil {
				_ = writeFrame(execwire.TypeWinsize, execwire.EncodeWinsize(uint16(w), uint16(h)))
			}
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(sigCh)
		<-stopped
		term.Restore(fd, state)
	}, nil
}

// dialWithRetry keeps trying to reach granule-agent until it succeeds,
// ctx is cancelled, or cfg.ConnectTimeout elapses. Both halves can fail
// for the same ordinary reason -- the VM is still booting -- so both are
// retried: the socket does not exist until cloud-hypervisor creates it,
// and nothing is listening on the far side until the guest has started
// the agent.
func dialWithRetry(ctx context.Context, cfg Config) (net.Conn, error) {
	deadline := time.Now().Add(cfg.ConnectTimeout)
	var lastErr error
	interval := readyInitialInterval
	for {
		conn, err := dialOnce(cfg.Socket)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connecting to the guest over %s: timed out after %s, last error: %w", cfg.Socket, cfg.ConnectTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connecting to the guest over %s: %w", cfg.Socket, ctx.Err())
		case <-time.After(interval):
		}
		if interval < 50*time.Millisecond {
			interval = 50 * time.Millisecond
		} else if interval < 100*time.Millisecond {
			interval = 100 * time.Millisecond
		} else if interval < 250*time.Millisecond {
			interval = 250 * time.Millisecond
		} else {
			interval = retryInterval
		}
	}
}

// dialOnce makes one attempt: connect to cloud-hypervisor's vsock socket
// and ask it to open a connection to the agent's port inside the guest.
//
// The handshake is cloud-hypervisor's "hybrid vsock" protocol (the same
// one firecracker uses): the host writes "CONNECT <port>\n" and gets
// back "OK <port>\n" once the guest has accepted, after which the same
// stream carries the connection. A guest that is up but has nothing
// listening on that port makes cloud-hypervisor close the stream instead
// -- which is what a boot still in progress looks like, and why the
// caller retries.
func dialOnce(socket string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", socket, dialTimeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", execwire.Port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("requesting vsock port %d: %w", execwire.Port, err)
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 64)).ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock port %d is not being listened on inside the guest yet: %w", execwire.Port, err)
	}
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, fmt.Errorf("cloud-hypervisor refused the vsock connection: %q", strings.TrimSpace(line))
	}

	// The deadline covered the handshake only; clear it so it does not
	// also cut off the session this connection goes on to carry, which
	// has no time limit of its own.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// ShellCommandLine translates argv a POSIX sh/bash would receive as its
// own os.Args[1:] into the command line RunLine should send to the
// guest, for cmd/granule-shim's "sh"/"bash" shims (see the Dockerfile's
// "final" stage, which symlinks /bin/sh and /bin/bash to the same granule
// binary): docker/kubectl's "exec" always resolves and runs a command
// already present in the container by name, never through this binary's
// own mode dispatch, so a bare `kubectl exec -it <pod> -- sh` (or
// "bash") lands on that symlink and needs to look enough like a real
// shell invocation to be worth shimming at all.
//
// Only the two shapes docker/kubectl's own generated commands (and
// scripts built on top of them) actually produce are supported:
//
//   - No arguments at all: returns "", requesting an interactive login
//     shell the same way Run(ctx, cfg, nil, ...) does.
//   - "-c <command>", optionally with other short flags fused in front
//     of the "c" (e.g. "-ec", the way real sh/bash accept elsewhere):
//     returns command verbatim, so RunLine sends it to the guest exactly
//     as a real "sh -c"/"bash -c" would have interpreted it (variable
//     expansion, word splitting, and so on happen guest-side, not here).
//
// Anything else -- a script file argument, "--login" alone, or
// positional arguments after -c's command (which would become $0/$1/...
// for a real sh/bash -c, but cannot be threaded through a protocol that
// carries one command line) -- returns an error naming "granule exec" as
// the alternative that does support it, rather than silently behaving
// unlike a real sh/bash would.
func ShellCommandLine(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}

	flag := args[0]
	if len(flag) < 2 || flag[0] != '-' || flag[1] == '-' || flag[len(flag)-1] != 'c' {
		return "", fmt.Errorf("unsupported shell invocation %q: only no arguments (interactive shell) or \"-c <command>\" are supported here -- use \"granule exec\" directly for anything else", strings.Join(args, " "))
	}
	if len(args) < 2 {
		return "", fmt.Errorf("%q requires a command argument", flag)
	}
	if len(args) > 2 {
		return "", fmt.Errorf("unsupported shell invocation: positional arguments after -c's command (%q) aren't supported here -- use \"granule exec\" directly", strings.Join(args[2:], " "))
	}
	return args[1], nil
}

// shellJoin renders args as a single POSIX shell command line, so a
// command containing spaces or shell metacharacters round-trips to the
// guest's shell the same way it would as an ordinary local argv.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
