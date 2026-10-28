package dockervm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/guestexec"
)

// ExecOptions describes one command run inside a VM's guest.
type ExecOptions struct {
	// Command is the guest command and its arguments. Empty asks for an
	// interactive login shell, which is what "granule exec" with no
	// arguments already means.
	Command []string

	// TTY allocates a terminal for the session ("docker exec -t"). Only
	// valid when Stdin is a terminal -- docker refuses it otherwise with
	// "the input device is not a TTY" -- so callers check that first.
	TTY bool

	// Env are extra "KEY=value" pairs set on the docker exec, i.e. on
	// "granule exec" itself rather than on the guest command: the guest
	// side takes its settings (GRANULE_EXEC_USER,
	// GRANULE_EXEC_CONNECT_TIMEOUT) from the environment of the process
	// that dials it.
	Env []string

	// Workdir and GuestEnv are the other half of that distinction: they
	// belong to the command inside the guest, so they are passed as
	// "granule exec"'s own -w and -e flags rather than as environment of
	// the process in the container. Empty leaves both to the guest's
	// defaults -- the account's home directory and the login environment
	// the agent builds (see pkg/agent).
	Workdir  string
	GuestEnv []string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Exec runs a command inside the guest of VM name and returns the
// command's own exit status.
//
// The path is "docker exec <container> granule exec -- <command>": the
// container is a scratch image with no shell in it, so "granule exec" is
// what bridges the gap between the container and the guest running inside
// it (see the top-level README's "Execing into a VM"). Everything about
// this is what a caller would otherwise type by hand; the point of having
// it here is that the container name is derived from the VM name rather
// than being an implementation detail callers have to know.
//
// The returned error is about being unable to *run* the command at all --
// no such VM, a docker daemon that refused. A guest command that ran and
// exited non-zero is not an error: its status comes back as the int, so
// callers can pass it on the way a shell would. The two are told apart by
// checking the container first, so that docker's own 125/126/127 aren't
// silently reported as the guest's.
func Exec(ctx context.Context, d *Docker, name string, opts ExecOptions) (int, error) {
	container := vmContainerName(name)
	running, err := d.running(ctx, container)
	if err != nil {
		return 0, err
	}
	if !running {
		return 0, fmt.Errorf("VM %q is not running: no container %s (check \"granule vm list\", or \"docker logs %s\" if it exited)", name, container, container)
	}

	// -i always, TTY or not: a session proxies stdin to the guest command
	// byte for byte, which is what makes "... -- sh -c 'cat > /tmp/f' <
	// file" work (see the README's Flow 4). Without it docker closes
	// stdin immediately and every such pipeline writes an empty file.
	args := []string{"exec", "-i"}
	if opts.TTY {
		args = append(args, "-t")
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	args = append(args, container, "granule", "exec")
	if opts.Workdir != "" {
		args = append(args, "-w", opts.Workdir)
	}
	for _, e := range opts.GuestEnv {
		args = append(args, "-e", e)
	}
	if len(opts.Command) > 0 {
		args = append(args, "--")
		args = append(args, opts.Command...)
	}

	cmd := exec.CommandContext(ctx, d.binary(), args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 0, fmt.Errorf("docker exec on %s: %w", container, err)
	}
	return 0, nil
}

// readyProbeArgs is the command a readiness probe runs inside a VM's
// container: "granule ready" makes one attempt to reach the guest and
// exits 0 only if it answered (see cmd/granule-shim's "ready" mode).
//
// "-timeout 0" -- one attempt, no retrying of its own -- is what makes
// it usable inside a loop that is already retrying. A probe that keeps
// trying for the 30 seconds "granule exec" gives a dial by default
// (guestexec's defaultConnectTimeout) swallows the caller's deadline
// whole: a "-ready-timeout 10s" then reported timing out after 10s three
// times as long after the fact, and the container-exited check below --
// the failure this loop is here to fail fast on -- did not get to run
// until the probe it is meant to cut short had finished anyway. With the
// retrying out here instead, a probe against a guest that is not up costs
// one dial -- guestexec gives that 3 seconds -- and the deadline below is
// the thing that decides when to stop.
var readyProbeArgs = []string{"granule", "ready", "-timeout", "0"}

// legacyProbeArgs is the probe every caller in this repo used before
// "granule ready" existed, kept as the fallback for a VM whose container
// image predates the mode: run the most trivial command there is inside
// the guest and read readiness off its having run at all.
//
// A VM keeps the image it was created from for as long as it runs, so
// upgrading granule does not upgrade a VM that is already up -- and the
// two commands built on this probe are exactly the ones a caller reaches
// for when a VM is misbehaving. Refusing to answer them until the VM has
// been re-created would take the tool away at the moment it is wanted,
// so an old image is probed the old way instead and said so about (see
// Status.Fallback).
//
// It is knowingly the weaker signal -- the agent answered, and nothing
// about what the guest is running -- which is the whole reason "granule
// ready" exists. What it is not is a *different* question: the guest's
// own declaration is checked here too, by legacyDeclarationArgs below,
// since an old image runs "test -e" as happily as a new one. What it
// lacks is the mode that knows to run it.
var legacyProbeArgs = []string{"granule", "exec", "--", guestexec.ProbeCommand}

// legacyProbeEnv bounds one fallback probe to a single dial, which is
// what "-timeout 0" does for "granule ready" above and what the loop in
// WaitReady needs of every probe it runs: an old "granule exec" has no
// flag for it, but it reads the same setting from its own environment,
// and "docker exec -e" is how a caller out here sets it. Without this a
// probe against a guest that is not up costs the 30 seconds a session
// gives a dial by default, which is longer than most of the timeouts a
// caller asks for.
var legacyProbeEnv = []string{"GRANULE_EXEC_CONNECT_TIMEOUT=0"}

// legacyDeclarationArgs is the second condition asked through an old
// image: the same "test -e" guestexec.Ready runs for a configured
// ReadyFile, sent as an ordinary "granule exec" command line rather than
// derived guest-side from GRANULE_READY_FILE, which an old "granule exec"
// does not read.
func legacyDeclarationArgs(path string) []string {
	return []string{"granule", "exec", "--", "test", "-e", path}
}

// envReadyFile is the variable the VM container carries the guest's own
// readiness declaration in (see Create, and guestexec.Config.ReadyFile).
const envReadyFile = "GRANULE_READY_FILE"

// errProbeUnsupported is what a probe against a container that has
// neither mode comes back as, so that callers stop rather than retry: no
// amount of waiting adds a granule binary to an image that doesn't have
// one. A VM whose image has "exec" but not "ready" is not this -- that
// one falls back (see probeLegacy).
var errProbeUnsupported = errors.New(`the VM's container image has no "granule ready" mode and no "granule exec" to fall back on, so its guest cannot be probed; check the image the VM was created with, then re-create the VM ("granule vm update <name>")`)

// Status is what is knowable about a VM from outside it: whether its
// container is running, and whether the guest inside that container
// answers.
//
// The two are separate on purpose. A container that is up with a guest
// that isn't is the ordinary state of a VM that was created a second
// ago, and it is also what a guest that failed to boot looks like
// forever -- which is the distinction "is it ready?" alone cannot draw.
type Status struct {
	// Container is the docker container this VM runs in, named here so a
	// caller can point at "docker logs" without deriving it again.
	Container string

	// State is what docker says about that container. A container that
	// has exited and one that was never created are both "not running",
	// but they are different things to be told to do something about, so
	// they are reported apart rather than as a bool.
	State ContainerState

	// Ready reports whether the guest passed the probe. Always false when
	// the container isn't running, since there is nothing to probe
	// through.
	Ready bool

	// Reachable reports whether the guest's granule-agent answered, which
	// is the first of the two conditions readiness can have (see
	// guestexec.Ready). It is true whenever Ready is; the state worth
	// having it for is Reachable without Ready, which is only possible
	// for a VM created with a -ready-file: the VM booted, the agent
	// answers, and the guest has not created the file that says its
	// workload is up. That is a different thing to be told than "still
	// booting", and the reason the probe has an exit status of its own
	// for it (guestexec.ExitNotDeclared).
	Reachable bool

	// Detail is why the guest did not pass, when it didn't: the probe's
	// own message, which says whether the socket was missing (the VMM has
	// not started), the port was refused (the guest has not started the
	// agent), or the guest answered without having declared itself ready.
	// Empty when Ready.
	Detail string

	// Fallback reports that this VM's image has no "granule ready" mode,
	// so the answer above came from the probe that predates it (see
	// legacyProbeArgs) rather than from the guest's own signal. Worth
	// carrying rather than hiding, because it says how much the answer is
	// worth: the two conditions are still both asked, but they are asked
	// from out here by a caller reading exit statuses, and an image that
	// old is a VM created before the mode existed.
	Fallback bool
}

// Running reports whether the VM's container exists and is running, which
// is all a caller that only means to reach the guest through it needs of
// State.
func (s Status) Running() bool { return s.State == ContainerRunning }

// Inspect answers "is this VM's guest up?" once, without waiting, which
// is the question "vm wait" only answers by blocking until it can say
// yes.
//
// An error means the question could not be asked -- docker was
// unreachable, or the image cannot be probed at all. A VM that is simply
// not ready is not an error: that is a Status with Ready false and
// Detail saying why.
func Inspect(ctx context.Context, d *Docker, name string) (Status, error) {
	st := Status{Container: vmContainerName(name)}
	state, err := d.containerState(ctx, st.Container)
	if err != nil {
		return st, err
	}
	st.State = state
	if state != ContainerRunning {
		return st, nil
	}

	out, notReady, err := probeReady(ctx, d, name)
	if err != nil {
		return st, err
	}
	st.Reachable, st.Fallback = out.Reachable, out.Fallback
	if notReady != nil {
		st.Detail = notReady.Error()
		return st, nil
	}
	st.Ready = true
	return st, nil
}

// WaitReady blocks until the guest of VM name accepts a command, giving
// up at timeout -- or sooner, if the VM's container exits, which no
// amount of further waiting would fix.
//
// This is the piece "granule vm create" doesn't do: it returns once the
// containers are started, which is before the guest has booted, so
// anything that wants to talk to the guest has to wait for it first. A
// caller doing it by hand polls "granule exec -- true" in a shell loop;
// this is the same poll, against the same definition of ready (see
// guestexec.Ready), written once.
func WaitReady(ctx context.Context, d *Docker, name string, timeout time.Duration) error {
	container := vmContainerName(name)
	deadline := time.Now().Add(timeout)
	interval := 10 * time.Millisecond
	for {
		st, err := Inspect(ctx, d, name)
		if err != nil {
			return err
		}
		if st.Ready {
			return nil
		}
		if !st.Running() {
			return fmt.Errorf("the VM container %s exited before its guest became reachable (see \"docker logs %s\")", container, container)
		}
		if time.Now().After(deadline) {
			// Which of the two conditions ran out of time: a guest whose
			// agent answered every attempt did not fail to "become
			// reachable", and saying so would send whoever reads this to
			// the wrong half of the boot.
			if st.Reachable {
				return fmt.Errorf("timed out after %s waiting for the guest of %q to declare itself ready; it is reachable, so this is the VM's own -ready-file and not the boot (see \"docker logs %s\"): %s", timeout, name, container, st.Detail)
			}
			return fmt.Errorf("timed out after %s waiting for the guest of %q to become reachable (see \"docker logs %s\"): %s", timeout, name, container, st.Detail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		switch {
		case interval < 25*time.Millisecond:
			interval = 25 * time.Millisecond
		case interval < 50*time.Millisecond:
			interval = 50 * time.Millisecond
		case interval < 100*time.Millisecond:
			interval = 100 * time.Millisecond
		default:
			interval = 100 * time.Millisecond
		}
	}
}

// probeOutcome is what one readiness probe learned besides whether the
// guest passed it -- the facts Status carries alongside Ready.
type probeOutcome struct {
	// Reachable is the first of the two readiness conditions on its own:
	// granule-agent answered, whatever the guest has or hasn't declared
	// about its workload. See Status.Reachable.
	Reachable bool

	// Fallback records that the answer came from legacyProbeArgs because
	// the image has no "ready" mode. See Status.Fallback.
	Fallback bool
}

// probeReady asks once whether the guest of VM name is ready.
//
// The two errors are two different answers, which is why they are not
// one: notReady is the guest's, and is the probe's own output rather
// than a wrapped "docker exec ... exit status 1", because that output is
// the only thing that says which half of the boot is still outstanding
// and it ends up in front of whoever asked. err is this package's --
// the question could not be asked at all -- and is what stops a caller
// retrying something no waiting will fix.
//
// probeOutcome.Reachable is the "which half" in a form a caller can
// branch on: the probe exits guestexec.ExitNotDeclared, rather than 1,
// when it got as far as the guest answering and the guest's own
// readiness file is what is missing. It is read from the status rather
// than from the message because a message is for a person and this is
// for Status.Reachable.
func probeReady(ctx context.Context, d *Docker, name string) (out probeOutcome, notReady, err error) {
	code, detail, err := runProbe(ctx, d, name, nil, readyProbeArgs)
	if err != nil {
		return out, nil, err
	}
	switch {
	case code == 0:
		out.Reachable = true
		return out, nil, nil
	case strings.Contains(detail, `unknown mode "ready"`):
		// An image predating the mode answers with main's own "unknown
		// mode" line. That is a fact about the image and not about the
		// guest, so it is neither retried nor reported as a boot that
		// never finished: the same VM is asked the older question
		// instead.
		return probeLegacy(ctx, d, name)
	case code == guestexec.ExitNotDeclared:
		out.Reachable = true
		return out, errors.New(detail), nil
	default:
		return out, errors.New(detail), nil
	}
}

// probeLegacy asks the same two conditions of a VM whose image has no
// "ready" mode, from out here: one "granule exec -- true" for the agent,
// and -- when the VM carries a ready file -- one "granule exec -- test -e"
// for the guest's own declaration of it.
//
// Asking both is what keeps "vm wait" and "vm status" the same commands
// against an old image as against a new one. Only the exit statuses
// differ in where they are interpreted: "granule ready" decides inside
// the container and reports one status, and this decides out here from
// two.
func probeLegacy(ctx context.Context, d *Docker, name string) (out probeOutcome, notReady, err error) {
	out.Fallback = true

	code, detail, err := runProbe(ctx, d, name, legacyProbeEnv, legacyProbeArgs)
	if err != nil {
		return out, nil, err
	}
	if code != 0 {
		if strings.Contains(detail, `unknown mode "exec"`) {
			// Neither mode: this is not a granule image, or not one any
			// version of this package could have probed. Nothing here
			// gets better by waiting.
			return out, nil, fmt.Errorf("%w (%s)", errProbeUnsupported, detail)
		}
		return out, fmt.Errorf("%s (asked with %q, since this VM's image has no \"granule ready\" mode)", detail, strings.Join(legacyProbeArgs, " ")), nil
	}
	out.Reachable = true

	// The declaration, if this VM has one. The path is on the container
	// rather than in the probe, so it is read from there -- an old
	// "granule exec" ignores GRANULE_READY_FILE, but the file it names is
	// as testable from an old image as from a new one, and a VM created
	// with -ready-file must not quietly become ready-at-the-agent just
	// because its image is old.
	readyFile, err := d.containerEnv(ctx, vmContainerName(name), envReadyFile)
	if err != nil {
		return out, nil, err
	}
	if readyFile == "" {
		return out, nil, nil
	}
	code, detail, err = runProbe(ctx, d, name, legacyProbeEnv, legacyDeclarationArgs(readyFile))
	if err != nil {
		return out, nil, err
	}
	switch code {
	case 0:
		return out, nil, nil
	case 1:
		// "test -e" says absent with 1 and nothing else, so this is the
		// ordinary case: the guest is up and has not got there yet. Worded
		// as guestexec.ErrNotDeclared words it, since it is the same
		// answer arrived at through an older image.
		return out, fmt.Errorf("%s: granule-agent answered, but %s does not exist inside the guest yet (%s)", guestexec.ErrNotDeclared, readyFile, envReadyFile), nil
	default:
		return out, fmt.Errorf("%s: granule-agent answered, but %q exited with status %d rather than reporting the file present or absent (%s): %s", guestexec.ErrNotDeclared, strings.Join(legacyDeclarationArgs(readyFile)[2:], " "), code, envReadyFile, detail), nil
	}
}

// runProbe runs one probe command inside the VM's container -- with env
// as "docker exec -e" pairs, which is how a probe with no flag for a
// setting is given one -- and reports its exit status and whatever it
// wrote to stderr.
//
// A non-zero status is not an error here: every probe this package runs
// answers with one, and turning it into an error would make the caller
// unpick it again. The error return is for docker itself being
// unrunnable, which is the caller's own failure rather than the guest's.
func runProbe(ctx context.Context, d *Docker, name string, env, probe []string) (code int, detail string, err error) {
	container := vmContainerName(name)
	args := []string{"exec"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(append(args, container), probe...)
	cmd := exec.CommandContext(ctx, d.binary(), args...)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	detail = strings.TrimSpace(stderr.String())
	if runErr == nil {
		return 0, detail, nil
	}
	if detail == "" {
		detail = runErr.Error()
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitCode(), detail, nil
	}
	return 0, detail, fmt.Errorf("running %q in %s: %w", strings.Join(probe, " "), container, runErr)
}

// running reports whether a container exists and is running. A container
// that isn't there at all is reported as "not running" rather than as an
// error: to a caller reaching for a VM the two are the same thing, and
// the message it then prints names the container either way. (Inspect,
// which reports rather than reaches, keeps the two apart -- see
// containerState.)
func (d *Docker) running(ctx context.Context, container string) (bool, error) {
	state, err := d.containerState(ctx, container)
	return state == ContainerRunning, err
}
