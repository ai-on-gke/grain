package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/dockervm"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// exitStatusError carries a guest command's own exit status out to Run,
// which turns it into granule's exit status.
//
// A command that ran in the guest and exited non-zero is not a granule
// failure: the whole point of "vm exec" is to be transparent, so the
// status has to come back to the caller's shell as the guest's own rather
// than as granule's blanket 1, and nothing extra gets printed -- the
// command's own stderr has already been passed through.
type exitStatusError struct{ code int }

func (e *exitStatusError) Error() string {
	return fmt.Sprintf("the command exited with status %d", e.code)
}

// statusError returns nil for a successful command and an exitStatusError
// for any other status, so callers can "return statusError(code)".
func statusError(code int) error {
	if code == 0 {
		return nil
	}
	return &exitStatusError{code: code}
}

// execSettings is what "vm exec", "vm shell" and "vm run" all need once
// their own flags have been parsed: which VM, how to reach the guest, and
// what to run in it.
type execSettings struct {
	stateDir string
	tty      bool
	user     string
	// connectTimeout, when set, becomes GRANULE_EXEC_CONNECT_TIMEOUT for
	// this one session -- how long "granule exec" keeps retrying the dial
	// into a guest that may still be booting (30s by default).
	connectTimeout time.Duration
	// workdir and env belong to the guest command rather than to the
	// session that carries it, so they are passed through as "granule
	// exec"'s own -w/-e (see dockervm.ExecOptions) rather than as
	// environment on the docker exec.
	workdir string
	env     repeatedFlag
	command []string
}

// registerExecFlags registers the flags "vm exec" and "vm shell" share.
func registerExecFlags(fs *flag.FlagSet, s *execSettings, ttyDefault bool) {
	fs.StringVar(&s.stateDir, "state-dir", defaultStateDir(), "directory granule stores VM state in")
	fs.BoolVar(&s.tty, "it", ttyDefault, "allocate a terminal for the session, the way \"docker exec -it\" does; needs a terminal on granule's own stdin")
	fs.StringVar(&s.user, "user", "", "guest account to run as, overriding the VM's own -guest-user for this one command (empty means root)")
	fs.DurationVar(&s.connectTimeout, "connect-timeout", 0, "how long to keep retrying the connection into a guest that may still be booting; 0 leaves the guest side's own default (30s)")
	// -w and -e, spelled the way "docker exec" spells them, since that
	// is where someone reaching for them has just come from. The long
	// forms are registered too: Go's flag package treats -workdir and
	// --workdir as the same flag.
	fs.StringVar(&s.workdir, "w", "", "absolute guest directory to run the command in (empty means the account's home directory)")
	fs.StringVar(&s.workdir, "workdir", "", "absolute guest directory to run the command in (empty means the account's home directory)")
	fs.Var(&s.env, "e", "environment variable for the guest command, KEY=value, repeatable; overlaid on the session's own environment rather than replacing it")
	fs.Var(&s.env, "env", "environment variable for the guest command, KEY=value, repeatable; overlaid on the session's own environment rather than replacing it")
}

// checkGuestEnv rejects a -e that is not KEY=value here, rather than
// letting it travel to the guest and come back as a refused request with
// a container name in the message.
//
// A bare "-e NAME" is refused too, even though "docker exec -e NAME"
// takes the value from the client's own environment: the value would
// have to cross into the container and then into the guest, and a
// variable that quietly did not make that trip is worse than one that
// was never accepted. "-e NAME=$NAME" says the same thing out loud.
func (s execSettings) checkGuestEnv() error {
	for _, e := range s.env {
		key, _, ok := strings.Cut(e, "=")
		if !ok || key == "" {
			return fmt.Errorf("-e %q: expected KEY=value (a bare variable name is not taken from granule's own environment -- write -e %s=\"$%s\" if that is what you meant)", e, e, e)
		}
	}
	return nil
}

// execEnv turns the settings that belong to the guest side into the
// environment "granule exec" reads them from.
func (s execSettings) execEnv() []string {
	var env []string
	if s.user != "" {
		env = append(env, "GRANULE_EXEC_USER="+s.user)
	}
	if s.connectTimeout > 0 {
		env = append(env, "GRANULE_EXEC_CONNECT_TIMEOUT="+s.connectTimeout.String())
	}
	// The caller's own terminal type, for a session that asked for a
	// terminal. "granule exec" forwards whatever TERM it is running under
	// into the guest (see pkg/guestexec), and what it would
	// otherwise be running under is docker's own default for an exec with
	// -t: a flat "xterm", regardless of what is actually on this screen.
	// So the value has to make the first hop explicitly for the second
	// one to be worth anything.
	if s.tty {
		if t := os.Getenv("TERM"); t != "" {
			env = append(env, "TERM="+t)
		}
	}
	return env
}

// isTerminal reports whether r is a terminal, which is what decides
// whether a session can ask docker for a tty at all. A non-*os.File
// reader -- a pipe, a test's buffer -- never is.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// /dev/null is a character device too, and "< /dev/null" is an
	// ordinary way to run something with no input -- but docker refuses
	// -t on it ("the input device is not a TTY"), so it must not count as
	// a terminal here.
	if devNull, err := os.Stat(os.DevNull); err == nil && os.SameFile(info, devNull) {
		return false
	}
	return true
}

// runVMExec runs one command inside a VM's guest, or opens an interactive
// session in it when given no command.
func runVMExec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm exec")
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule vm exec "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var s execSettings
	registerExecFlags(fs, &s, false)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	// Everything after the flags -- with or without a "--" in front of it,
	// since Go's flag package stops at the first non-flag argument either
	// way -- is the guest command.
	s.command = fs.Args()

	code, err := execInVM(ctx, name, s, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	return statusError(code)
}

// runVMShell opens an interactive login shell in a VM's guest: "vm exec"
// with a terminal asked for by default and no command to run.
func runVMShell(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm shell")
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule vm shell "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var s execSettings
	// A terminal by default, but only if there is one: "granule vm
	// shell web < script.sh" is a perfectly good way to feed a guest
	// shell a script, and asking docker for a tty there fails outright.
	registerExecFlags(fs, &s, isTerminal(stdin))
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if extra := fs.Args(); len(extra) > 0 {
		return fmt.Errorf("vm shell takes no command (got %q); use \"granule vm exec %s -- %s\"", extra[0], name, extra[0])
	}

	code, err := execInVM(ctx, name, s, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	return statusError(code)
}

// execInVM checks that the named VM exists -- its saved state is what
// makes it one granule knows about -- and runs a command in its guest,
// returning the command's own exit status.
func execInVM(ctx context.Context, name string, s execSettings, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if _, err := vmspec.Load(s.stateDir, name); err != nil {
		return 0, fmt.Errorf("VM %q not found in %s (\"granule vm list -state-dir %s\" shows what is)%s: %w", name, s.stateDir, s.stateDir, stateDirNoteSuffix(s.stateDir), err)
	}
	if s.tty && !isTerminal(stdin) {
		return 0, fmt.Errorf("-it needs a terminal on stdin, and granule's stdin is not one; drop -it (stdin is still passed through to the guest command either way)")
	}
	if err := s.checkGuestEnv(); err != nil {
		return 0, err
	}

	return dockervm.Exec(ctx, &dockervm.Docker{}, name, dockervm.ExecOptions{
		Command:  s.command,
		TTY:      s.tty,
		Env:      s.execEnv(),
		Workdir:  s.workdir,
		GuestEnv: s.env,
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
	})
}

// runVMRun is the whole of the top-level README's Flow 1 in one command:
// create a VM, wait for its guest, run one command in it, report that
// command's exit status, and delete the VM again.
//
// It takes every flag "vm create" does, since the VM it makes is an
// ordinary one, plus the two this shape needs of its own: how long to
// wait for the guest, and whether to keep the VM when something goes
// wrong on the way to running the command.
func runVMRun(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm run")
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule vm run "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var s execSettings
	registerExecFlags(fs, &s, false)
	backend := fs.String("backend", backendDocker, `deprecated: "docker" (run against the local docker daemon) is the only way granule runs a VM, and the only one a command can be run in`)
	readyTimeout := fs.Duration("ready-timeout", defaultReadyTimeout, "how long to wait for the guest to become reachable after the VM starts")
	keepOnFailure := fs.Bool("keep-on-failure", false, "leave the VM in place if it never becomes reachable, for inspection (a command that merely exits non-zero still deletes it -- that is a result, not a failure)")
	// Every VM flag "vm create" takes, on the same defaults, and with the
	// machine image having the same say over what they leave unset.
	v := registerCreateVMFlags(fs)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	s.command = fs.Args()
	if len(s.command) == 0 {
		return fmt.Errorf("vm run: a command is required (granule vm run %s [flags] -- <command>); use \"granule vm create\" for a VM that stays up", name)
	}
	if err := checkBackend(*backend); err != nil {
		return err
	}
	if err := v.checkDeprecated(); err != nil {
		return err
	}
	if s.tty && !isTerminal(stdin) {
		return fmt.Errorf("-it needs a terminal on stdin, and granule's stdin is not one; drop -it (stdin is still passed through to the guest command either way)")
	}
	// Before anything is created: a mistyped -e is not worth a VM's
	// worth of setup and teardown to find out about.
	if err := s.checkGuestEnv(); err != nil {
		return err
	}
	if _, err := vmspec.Load(s.stateDir, name); err == nil {
		return fmt.Errorf("VM %q already exists (\"granule vm run\" creates and deletes the VM it names, so pick a name nothing else is using)", name)
	}

	spec := v.toSpec(name)

	// Every progress line goes to stderr, because stdout belongs to the
	// guest command: "granule vm run oneoff -- cat /etc/os-release >
	// os-release" has to write the guest's bytes and nothing else.
	fmt.Fprintf(stderr, "creating VM %q\n", name)
	resolveImageSettings(ctx, &spec, stderr)
	if err := submitVM(ctx, spec, s.stateDir, stderr); err != nil {
		return err
	}

	// Deleted on the way out however this ends, including a signal that
	// cancelled ctx: the VM exists only for this one command, and leaving
	// it behind leaks a container, a tap device and an overlay per run.
	cleanup := func() {
		fmt.Fprintf(stderr, "deleting VM %q\n", name)
		if err := deleteVM(context.WithoutCancel(ctx), name, s.stateDir, stderr); err != nil {
			fmt.Fprintf(stderr, "granule: deleting VM %q: %v\n", name, err)
		}
	}
	keep := func(err error) error {
		if *keepOnFailure {
			fmt.Fprintf(stderr, "leaving VM %q in place (-keep-on-failure); delete it with \"granule vm delete %s -state-dir %s\"\n", name, name, s.stateDir)
			return err
		}
		cleanup()
		return err
	}

	// On stderr, like every other progress line here: stdout belongs to
	// the guest command.
	if err := waitForGuest(ctx, name, *readyTimeout, stderr); err != nil {
		return keep(err)
	}

	code, err := dockervm.Exec(ctx, &dockervm.Docker{}, name, dockervm.ExecOptions{
		Command:  s.command,
		TTY:      s.tty,
		Env:      s.execEnv(),
		Workdir:  s.workdir,
		GuestEnv: s.env,
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
	})
	if err != nil {
		return keep(err)
	}
	// A non-zero status is the command's answer, not a failure of this
	// one: the VM goes away either way and the status is passed on.
	cleanup()
	return statusError(code)
}
