package orchestrator

// A sandbox that holds one long-lived server, reached by exec'ing into it.
//
// Every other sandbox in this package exists for the length of a command
// or a run: something is dispatched into it, it answers, it is released.
// This one is the opposite shape -- it runs one process and is then idle
// until somebody dials it -- and the two things that make it different are
// the two things this file is about.
//
// It boots an image named per sandbox rather than per deployment. A
// serving guest is assembled from this deployment's container half and one
// repository's disk (pkg/lifecycle's DockerImages), so there is a distinct
// image per (stock guest, repository digest) and GranuleConfig.SandboxImage
// has no answer for any of them. acquire takes the image for that reason.
//
// And it is reached over a connection rather than a command. There is no
// route to a guest -- pkg/granule "never resolves an address for a VM,
// because nothing here needs one" -- so a server listening on a socket
// inside one is dialled by exec'ing grain's own forwarder there and
// treating that exec's stdio as a net.Conn (pkg/mcp's DialStream,
// cmd/grain-connect). DialStream below is that, forwarded.
//
// Written for docs/task-lifecycle-sandbox.md's serving sandbox, which is a
// repository's task lifecycle definition served over HTTP with
// NetworkAccess none, no checkout, no capability material and no inbound
// route but that socket. Nothing here knows what is being served.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ServeSpec is one server to start in a sandbox of its own.
type ServeSpec struct {
	// Name is the sandbox's name, as Acquire takes one. It has to fit
	// whatever the backend derives from it -- a granule VM name, and the
	// network interfaces named after that -- so a caller minting these
	// gets the same 9-byte budget a CI job's does (ci.MaxSandboxName).
	Name string
	// Image is the exact guest image to boot. Required: a serving sandbox
	// whose image resolved to the deployment's own would be serving
	// something other than what its caller pinned, which is worse than
	// failing.
	Image string
	// Shape is the size and the network. NetworkAccess is honoured or the
	// Serve fails -- a backend that cannot withhold the internet must not
	// pretend, because "no network" is the whole of what makes it safe to
	// run somebody else's code in here.
	Shape Shape
	// Command is the shell command that starts the server. It is expected
	// not to return: it is run as a service, and a command that exits
	// immediately is a server that failed to start.
	Command string
	// Dir is the working directory Command runs in. Empty is the guest's
	// own default.
	Dir string
	// Env is literal environment for Command. Values are passed as
	// systemd-run --setenv arguments rather than spliced into a shell
	// line, so a value with a space or a quote in it arrives intact.
	Env map[string]string
	// Unit names the transient service Command runs as. Empty is
	// DefaultServeUnit.
	Unit string
	// Prepare, when set, is given the sandbox once its guest is up and
	// before Command starts -- where a standing sandbox is pointed at the
	// deployment's package cache (PrepareSandbox), as every other kind of
	// sandbox is before its first command.
	Prepare func(ctx context.Context, sandbox Sandbox)
}

// DefaultServeUnit is the transient unit a served command runs as.
const DefaultServeUnit = "grain-serve"

// serveOutputLines is how much of a failed server's output is fetched for
// a diagnostic. Enough to carry a traceback, bounded because this ends up
// in an error string somebody reads in a UI.
const serveOutputLines = 50

// ServingSandbox is a sandbox running one server.
//
// Its first two methods are deliberately exactly what a caller needs to
// use one -- pkg/lifecycle's Serving is that pair and nothing else, which
// this satisfies structurally, so wiring the two together needs no adapter
// and no import between them.
type ServingSandbox interface {
	// DialStream opens one connection to something inside the sandbox by
	// exec'ing argv there. One exec per connection.
	DialStream(ctx context.Context, argv []string) (net.Conn, error)
	// Release destroys the sandbox and the server with it.
	Release(ctx context.Context) error
	// Output is what the server has written, most recent last, for a
	// diagnostic. Bounded; best-effort, since the reason it is being
	// asked is usually that something is already wrong.
	Output(ctx context.Context) string
}

// ServingSandboxes is a backend that can hold one.
//
// An optional interface, like NetworkIsolator and SandboxPlacer: a
// deployment whose backend does not implement it cannot serve anything
// this way, and is told so rather than given a sandbox that quietly is not
// isolated. Of the backends here only GranuleSandboxes implements it --
// see CannotServe for the two reasons.
type ServingSandboxes interface {
	Serve(ctx context.Context, spec ServeSpec) (ServingSandbox, error)
}

// CannotServe is what a caller is told when this deployment's backend
// cannot hold a serving sandbox, and it names the reason rather than the
// interface.
//
// The two backends that cannot are not equally far from it.
// ContainerSandboxes refuses model.NetworkNone outright, because a
// container that keeps access to grain's git proxy keeps the segment that
// proxy is on -- so it cannot give this the one property the thing being
// served relies on. HostSandboxes has no image, no guest and no VM at all.
// K8sSandboxes is the interesting gap: it enforces NetworkNone from
// outside the image entirely, with a pod label and a NetworkPolicy, which
// is a stronger answer than granule's cooperative one -- but a
// repository-assembled *guest* is not a pod image, so booting one there
// needs an answer of its own rather than this method.
func CannotServe(sandboxes Sandboxes) error {
	switch sandboxes.(type) {
	case *ContainerSandboxes:
		return errors.New("orchestrator: the local container backend cannot hold a serving sandbox: it refuses " +
			"NetworkAccess none rather than pretending, and a server given somebody else's code to run needs it")
	case *K8sSandboxes:
		return errors.New("orchestrator: the Kubernetes backend cannot hold a serving sandbox yet: it isolates " +
			"the network properly, with a pod label and a NetworkPolicy, but an assembled granule guest is not a " +
			"pod image and booting one there is not this method")
	}
	return errors.New("orchestrator: this deployment's sandbox backend cannot hold a serving sandbox -- only the " +
		"granule backend can, because it is the only one that boots a guest image and withholds the network")
}

// Serve implements ServingSandboxes: boot spec.Image, start spec.Command
// as a transient service inside it, and hand back something to dial.
//
// It does not wait for the server to answer, and must not. What "ready"
// means belongs to whatever is being served -- for a lifecycle definition
// it is GET /v1/health reporting a protocol this grain speaks -- and a
// wait here would be this package guessing at it. What is waited for is
// the guest: acquire does not return until a command has run inside it and
// its network has been applied.
func (k *GranuleSandboxes) Serve(ctx context.Context, spec ServeSpec) (ServingSandbox, error) {
	if strings.TrimSpace(spec.Image) == "" {
		return nil, errors.New("orchestrator: a serving sandbox needs the image to boot -- " +
			"resolving the deployment's own would serve something other than what the caller pinned")
	}
	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("orchestrator: a serving sandbox needs a command to serve")
	}

	sb, err := k.acquire(ctx, spec.Name, spec.Shape, spec.Image, "")
	if err != nil {
		return nil, err
	}
	// The dial is an exec, so the transport has to be one that can hand
	// back a connection rather than a finished command. Checked before the
	// server is started rather than at the first dial, so a backend wired
	// up with a runner that cannot stream fails where somebody can see it.
	dialer, ok := sb.runner.(streamRunner)
	if !ok {
		k.releaseQuietly(ctx, sb)
		return nil, fmt.Errorf("orchestrator: sandbox %q's transport cannot open a connection into the guest, "+
			"so nothing served inside it could be reached", spec.Name)
	}

	served := &servingSandbox{sandbox: sb, dialer: dialer, unit: spec.unitName()}
	if err := served.start(ctx, spec); err != nil {
		k.releaseQuietly(ctx, sb)
		return nil, err
	}
	return served, nil
}

// releaseQuietly gives back a sandbox that never became usable, on a
// context detached from the caller's -- granuleSandbox.Release execs
// granule, so on an already-cancelled ctx the delete would not merely
// fail but never run, leaving the VM behind. deleteQuietly's own reasoning
// for the same hazard one step earlier.
func (k *GranuleSandboxes) releaseQuietly(ctx context.Context, sb *granuleSandbox) {
	released, cancel := context.WithTimeout(context.WithoutCancel(ctx), acquireCleanupTimeout)
	defer cancel()
	_ = sb.Release(released)
}

func (s ServeSpec) unitName() string {
	if unit := strings.TrimSpace(s.Unit); unit != "" {
		return unit
	}
	return DefaultServeUnit
}

// streamRunner is a sandboxRunner that can also hand back a connection --
// mcp.DockerExecRunner's DialStream, named here for the same reason
// sandboxRunner is: mcp's own equivalent is unexported.
type streamRunner interface {
	DialStream(ctx context.Context, argv []string) (net.Conn, error)
}

// servingSandbox is one VM running one server.
type servingSandbox struct {
	sandbox *granuleSandbox
	dialer  streamRunner
	unit    string
}

// start runs the command as a transient systemd service.
//
// systemd-run rather than backgrounding it from the shell, for two
// reasons that both come from what this process is. It has to outlive the
// exec that started it: `granule exec` is one SSH session, and a command
// left running behind a closing session is at the mercy of whatever reaps
// its process group. And when it fails, its output has to still be
// somewhere -- a unit's is in the journal, where Output can ask for it,
// while a backgrounded process's went to a pipe that closed with the
// session.
//
// The guest is Debian with a running systemd, which is what
// the repository's Granulefile builds and what a serving
// guest derived from it therefore has.
//
// The unit is a system one, started through sudo and run as the account
// grain execs in (serveAsExecUser). The SSH user is not root -- grain's own
// guest logs in as "debian" -- and an unprivileged systemd-run is refused
// by the system manager with "Access denied". `systemd-run --user` would
// avoid sudo but needs a user manager kept alive by lingering, and puts
// the unit where neither `systemctl is-active` nor `journalctl -u` looks
// without --user; the Granulefile already grants that account passwordless
// sudo, so the system manager it is.
func (s *servingSandbox) start(ctx context.Context, spec ServeSpec) error {
	if spec.Prepare != nil {
		spec.Prepare(ctx, s.sandbox)
	}
	stdout, stderr, code := s.sandbox.runner.Run(ctx, serveArgs(spec, s.unit), "")
	if code != 0 {
		return fmt.Errorf("orchestrator: starting %q in sandbox %q: exit %d: %s",
			s.unit, s.sandbox.name, code, firstLine(stderr, stdout))
	}
	return nil
}

// serveArgs is the invocation start runs, kept separate so a test can
// assert on it without a guest anywhere -- the separation
// DockerExecRunner.execArgs makes, for its reason.
//
// --collect so a unit that has already failed does not block starting one
// under the same name; --same-dir is not used because the working
// directory a caller means is inside the guest and has nothing to do with
// this process's.
func serveArgs(spec ServeSpec, unit string) []string {
	args := []string{"sh", "-c", serveAsExecUser, "sh", "--unit=" + unit, "--collect", "--quiet"}
	if dir := strings.TrimSpace(spec.Dir); dir != "" {
		args = append(args, "--property=WorkingDirectory="+dir)
	}
	// Sorted, so the invocation a test asserts on and the one an operator
	// reads in a log line are the same every time.
	names := make([]string, 0, len(spec.Env))
	for name := range spec.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		// --setenv rather than a property, so a value with an equals sign
		// or a space in it needs no quoting of its own.
		args = append(args, "--setenv="+name+"="+spec.Env[name])
	}
	// The command goes through a shell because that is what a declaration
	// writes: "python3 -m lifecycle.server" is one string somebody typed,
	// not an argv this package is entitled to split on spaces.
	return append(args, "/bin/sh", "-lc", spec.Command)
}

// asGuestRoot runs its arguments as root inside the guest: directly when
// the exec already is root, through `sudo -n` otherwise. -n so a guest
// without passwordless sudo fails at once rather than waiting on a prompt
// nobody will answer; and a guest with no sudo at all gets the command
// as-is, so whatever it says about permissions is the error reported.
const asGuestRoot = `if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1; then exec sudo -n "$@"; fi; exec "$@"`

// serveAsExecUser is `systemd-run "$@"` started as root and running its
// command as the account the exec logged in as. --uid is resolved in the
// guest, before sudo, so the server runs as the SSH user whichever one a
// deployment configured, rather than as root: privilege is what starting a
// system unit needs, not what the served command was given.
const serveAsExecUser = `set -- systemd-run --uid="$(id -un)" "$@"; ` + asGuestRoot

// DialStream opens one connection into the guest.
func (s *servingSandbox) DialStream(ctx context.Context, argv []string) (net.Conn, error) {
	return s.dialer.DialStream(ctx, argv)
}

// Output is the served command's own output, out of the journal.
//
// Read as root (asGuestRoot): the unit is a system one, and grain's guest
// account is in neither adm nor systemd-journal, so an unprivileged
// journalctl -u sees none of it.
//
// Best-effort and never an error: every caller of this is already
// reporting something else -- a readiness timeout, a server that died --
// and a failure to fetch the explanation must not replace the thing being
// explained.
func (s *servingSandbox) Output(ctx context.Context) string {
	fetch, cancel := context.WithTimeout(ctx, serveOutputTimeout)
	defer cancel()
	stdout, stderr, code := s.sandbox.runner.Run(fetch, []string{
		"sh", "-c", asGuestRoot, "sh",
		"journalctl", "--unit=" + s.unit, "--no-pager", "--lines=" + fmt.Sprint(serveOutputLines),
	}, "")
	if code != 0 {
		if text := strings.TrimSpace(stderr); text != "" {
			return "(its output could not be read: " + text + ")"
		}
		return "(no output)"
	}
	if text := strings.TrimSpace(stdout); text != "" {
		return text
	}
	return "(no output)"
}

// Alive asks systemd whether the served unit is still running. A unit
// whose command exited is gone (it was started with --collect), and a VM
// that has died fails the exec itself; either is a nonzero exit here.
// Querying a system unit needs no privilege, so this runs as the exec user.
func (s *servingSandbox) Alive(ctx context.Context) error {
	stdout, stderr, code := s.sandbox.runner.Run(ctx, []string{"systemctl", "is-active", s.unit}, "")
	if code != 0 {
		return fmt.Errorf("unit %q in sandbox %q is not active: exit %d: %s",
			s.unit, s.sandbox.name, code, firstLine(stderr, stdout))
	}
	return nil
}

// serveOutputTimeout bounds the journal read. Short: the thing asking has
// already failed and is holding an error open waiting for this.
const serveOutputTimeout = 10 * time.Second

// Release destroys the VM, and the server inside it with no separate stop
// -- the unit dies with the guest, which is the point of it being a
// sandbox rather than a process.
func (s *servingSandbox) Release(ctx context.Context) error { return s.sandbox.Release(ctx) }

// Name is the sandbox's name, for a caller that logs which one it holds.
func (s *servingSandbox) Name() string { return s.sandbox.Name() }

// firstLine is the most useful line of a failed command's output: stderr's
// if it wrote any, and stdout's otherwise, since systemd-run reports some
// refusals on stdout.
func firstLine(stderr, stdout string) string {
	for _, stream := range []string{stderr, stdout} {
		if text := strings.TrimSpace(stream); text != "" {
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				return text[:i]
			}
			return text
		}
	}
	return "(it said nothing)"
}

// ServeShape is the shape a serving sandbox is acquired at: the size asked
// for, and NetworkNone whatever was asked for.
//
// The network is not the caller's to choose, and that is deliberate. A
// declaration cannot ask for one (pkg/repodecl refuses `network` on a
// serve sandbox with its own reason), and neither can a caller here: it is
// the property the whole arrangement rests on, so it is set in one place
// rather than passed through several.
func ServeShape(cpus, memoryMB int) Shape {
	return Shape{CPUs: cpus, MemoryMB: memoryMB, NetworkAccess: model.NetworkNone}
}
