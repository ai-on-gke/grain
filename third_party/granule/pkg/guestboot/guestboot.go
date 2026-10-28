// Package guestboot boots a granule guest image in a throwaway VM, runs
// commands inside it over the same "granule exec" path everything else
// uses, and takes it down again.
//
// It exists because two verbs want that loop and differ only in what
// they do with the running guest. "granule guest build" (see
// pkg/guestbuild) provisions it and commits the result;
// "granule guest verify" (pkg/guestverify) asserts things about it
// and commits nothing. Everything up to that point -- starting the VM
// container, waiting for the guest to answer, waiting out the rest of
// its boot, staging a script somewhere the boot will not delete it,
// reading the console back when something goes wrong -- is the same
// sequence with the same hard-won details in it, and a second copy of it
// would be a second copy of the races it already knows about.
//
// What it does not decide is disk mode, which is exactly where the two
// verbs part company: a build writes through to the image's own disk so
// that a commit captures the changes, and a verify boots into a
// throwaway overlay so that the image it is judging cannot be changed by
// the act of judging it. The caller says which, because getting it wrong
// is silent in both directions.
package guestboot

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/dockervm"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

const (
	// DefaultReadyTimeout bounds the wait for a guest to answer its first
	// command and then finish booting.
	DefaultReadyTimeout = 3 * time.Minute

	// DefaultShutdownTimeout bounds the wait for a guest to power off
	// after SIGTERM.
	DefaultShutdownTimeout = 60 * time.Second
)

// Options configures one Boot.
type Options struct {
	// Image is the granule image to boot: its VMM half runs the VM and its
	// guest half is the machine that comes up.
	Image string

	// NamePrefix distinguishes one caller's throwaway VMs from another's
	// in "docker ps". Short, because netshim derives the VM's tap device
	// from the VM name and Linux caps that at 15 characters: the name is
	// this prefix plus six characters, so anything up to "xxxxx-" is safe.
	NamePrefix string

	// DiskMode is config.DiskModeOverlay, DiskModePersistent or
	// DiskModeReadOnly -- see the package comment on why this is the
	// caller's to choose, and why a caller that names one here wins over
	// the image's own recorded disk-mode: a verify boots into an overlay
	// because nothing it does may reach the image it is judging, which
	// is a property of the verb and not of the machine. Boot says so on
	// its report when the two disagree, so a machine verified in a mode
	// it did not ask for is not verified silently.
	//
	// Empty takes the image's recorded disk-mode
	// (docs/granulefile.md §5.5), or vmspec's own default (overlay) when
	// it records none.
	DiskMode string

	// NetMode is config.NetModeInline or config.NetModeNone ("inline" or "none").
	// Empty takes vmspec's own default (inline).
	NetMode string

	// MemoryMB and MemoryMaxMB override default VM memory sizing.
	MemoryMB    int
	MemoryMaxMB int

	// DockerBinary is the docker CLI to exec, resolved via PATH when it
	// contains no slash. Empty means dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker"); tests point it at a fake.
	DockerBinary string

	// ExtraRunArgs are passed to `docker run` before the image name. This
	// is how a boot reaches a network it would not otherwise: a proxy's
	// environment, a CA bundle, a private registry's credentials. The
	// guest is a VM inside the container and inherits none of the
	// caller's own network context by itself.
	ExtraRunArgs []string

	// DNS is the nameserver(s) the guest resolves through, spelled as
	// VMSpec.DNS and netshim's NETSHIM_DNS do: comma separated, at most
	// three and at most two IPv4, "auto" for the container's own resolver when the guest can
	// reach it (netshim.AutoDNS), and the empty string for "leave
	// whatever /etc/resolv.conf the image ships with alone".
	//
	// nil means vmspec.Defaults' own netshim.DefaultDNS, which is
	// "auto". It is a pointer rather than a string precisely so that
	// "say nothing" and "say nothing to the guest" stay different
	// requests.
	DNS *string

	// ReadyTimeout bounds how long to wait for the guest to accept a
	// command after the container starts, and then for its boot to
	// settle. A guest that never becomes reachable is the characteristic
	// granule failure -- see deploy/guest-image/README.md's "Networking".
	ReadyTimeout time.Duration

	// ShutdownTimeout bounds the wait for the guest to power off after
	// SIGTERM. It is what Stop passes to `docker stop -t` and what the
	// VM container is given as its own budget (CHV_SHUTDOWN_TIMEOUT),
	// which are the two halves of one wait: a `docker stop -t` shorter
	// than the container's own budget kills the VM while it is still
	// shutting down.
	//
	// Zero takes the shutdown-timeout the image records
	// (docs/granulefile.md §5.5), or DefaultShutdownTimeout when it
	// records none.
	ShutdownTimeout time.Duration

	Stdout io.Writer
	Stderr io.Writer
}

// Session is one booted guest: a VM container with a guest inside it
// that answers commands.
type Session struct {
	d         *CLI
	dv        *dockervm.Docker
	name      string
	container string
	opts      Options
}

// ExitError is a guest command that ran and exited non-zero, as opposed
// to one that could not be run at all. Code is the guest command's own
// status, which "granule exec" passes through -- the property that makes
// a check's verdict the guest's own rather than docker's.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("exit status %d: %s", e.Code, e.Stderr)
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// applyImageSettings boots the image the way the image says it wants to
// be booted: the settings a machine records in its own OCI config, which
// is where a Granule's VM SET writes them (docs/granulefile.md §5.5, "so
// that nothing has to read a Granule to boot the result").
//
// Without this the throwaway VM's sizing is vmspec.Defaults' own -- 2
// vCPUs and 2 GiB -- and it is passed to the VM container as an explicit
// CHV_CPUS/CHV_MEMORY_MB, which overrides the image's ENV the way
// `docker run -e` does. That is an override nobody asked for: a machine
// built to come up with a gigabyte comes up with two, and anything asked
// about the guest -- a `guest verify` check, a build that sizes itself
// off /proc/meminfo -- is answered by a machine that is not the one the
// image describes. A machine that records no sizing of its own still
// gets the defaults above, which is what everything booted before this
// got.
//
// The kernel command line is here for the same reason and reads worse
// without it: a machine that records one boots with granule's instead,
// so a `VM SET kernel-cmdline` was the setting in that table whose
// effect a verify of the image could never see -- including the case
// where the machine cannot boot with it at all.
//
// Everything else a VMSpec can carry is here on the same argument. The
// disk settings, the shutdown budget: each is a place where a machine
// that records one thing is asked about while running another, and a
// check that passes then says nothing about the machine anybody will
// run. The settings in §5.5's table with no VMSpec field of their own --
// memory-hotplug (CHV_MEMORY_HOTPLUG) and snapshot (CHV_SNAPSHOT_PATH)
// -- need nothing here: granule never passes those to the VM container
// itself (see dockervm.Create), so the image's own ENV is what the
// container starts with and the machine already boots with what it
// recorded. It is the ones granule passes explicitly that `docker run
// -e` would otherwise override.
//
// Only the spec is settled here; which of these the caller is allowed to
// overrule is Boot's, and it is the caller that wins -- see Options.
//
// Best effort: an image whose config cannot be read is still booted,
// with a line saying the defaults are granule's rather than its own. It
// is about to be started, so a daemon that cannot answer one question
// about it will have plenty more to say in a moment.
func applyImageSettings(ctx context.Context, dockerBinary, image string, spec *vmspec.VMSpec, report io.Writer) {
	env, err := (&dockercli.CLI{Binary: dockerBinary}).ConfigEnv(ctx, image)
	if err != nil {
		fmt.Fprintf(report, "could not read %s's own settings (%v); booting it with granule's own defaults\n", image, err)
	}
	// Read and applied by pkg/vmspec, which is where the same question is
	// answered for "granule vm create" and "granule vm run": one reader of
	// §5.5's table, so a machine cannot be booted one way by a verify and
	// another way by the command that runs it for real. It fills only what
	// the spec does not already name, which is why Boot clears what
	// vmspec.Defaults put there before calling this and puts its own
	// fallbacks back underneath afterwards.
	spec.ResolveSettings(env)
}

// Boot starts a VM on opts.Image and returns once its guest answers
// commands and its boot has settled. The caller owns the result and must
// Remove it.
func Boot(ctx context.Context, opts Options) (*Session, error) {
	if opts.Image == "" {
		return nil, fmt.Errorf("an image is required")
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = DefaultReadyTimeout
	}
	// Not defaulted here: a caller that named no shutdown timeout is one
	// the image's own may answer for, which is not known until its
	// config has been read. Resolved below, and put back on opts so that
	// Stop's `docker stop -t` waits what the VM container was given.
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	name := vmName(opts.NamePrefix)

	// The Docker backend launches a single container with inline
	// networking. The spec names no disk, so it boots the guest baked
	// into this image.
	spec := vmspec.Defaults()
	spec.Name = name
	spec.KonturImage = opts.Image
	spec.DiskImage = ""
	// Cleared, because the image is what should fill them: the resolution
	// below only writes into a field nothing has named yet (see
	// applyImageSettings), and leaving vmspec's own sizing, disk mode and
	// shutdown budget in place would be granule overriding the machine with
	// its defaults -- the very thing this is here to stop. Each gets its
	// fallback back underneath afterwards: vmspec's own for the sizing, this
	// command's longer budget below, and DiskModeOrDerived's overlay.
	spec.CPUs, spec.MemoryMB = 0, 0
	spec.DiskMode, spec.ShutdownTimeout = "", ""
	applyImageSettings(ctx, opts.DockerBinary, opts.Image, &spec, opts.Stdout)
	if opts.DiskMode != "" {
		if spec.DiskMode != "" && spec.DiskMode != opts.DiskMode {
			// Said rather than done quietly: this is the one setting a
			// caller overrules on purpose, so a machine that records
			// one disk mode and is judged in another should say so in
			// the same report the checks are printed under.
			fmt.Fprintf(opts.Stdout, "%s records disk-mode=%s; booting it in %s mode, which is what this caller needs\n", opts.Image, spec.DiskMode, opts.DiskMode)
		}
		spec.DiskMode = opts.DiskMode
	}
	if opts.NetMode != "" {
		spec.NetMode = opts.NetMode
	}
	if opts.MemoryMB > 0 {
		spec.MemoryMB = opts.MemoryMB
	}
	if opts.MemoryMaxMB > 0 {
		spec.MemoryMaxMB = opts.MemoryMaxMB
	}
	if opts.ShutdownTimeout > 0 {
		spec.ShutdownTimeout = opts.ShutdownTimeout.String()
	}
	if spec.ShutdownTimeout == "" {
		// A throwaway boot's own default, rather than vmspec's 20s, so
		// that an image recording no shutdown-timeout is still given what
		// every boot through here has always been given.
		spec.ShutdownTimeout = DefaultShutdownTimeout.String()
	}
	// The container's budget and this side's `docker stop -t` are one
	// wait, so whatever settled it above settles both.
	if d, err := time.ParseDuration(spec.ShutdownTimeout); err == nil && d > 0 {
		opts.ShutdownTimeout = d
	} else {
		opts.ShutdownTimeout = DefaultShutdownTimeout
	}
	spec.GuestUser = "root"
	spec.DockerRunOpts = opts.ExtraRunArgs
	if opts.DNS != nil {
		spec.DNS = *opts.DNS
	}
	if err := spec.Validate(); err != nil {
		// Named as the image's, because most of what is in here now is:
		// a machine that records a combination nothing can boot (a
		// disk-size-mb beside a disk-mode that never resizes anything,
		// say) is refused here rather than by the VM container three
		// seconds later, and the reader has to be told which of the two
		// wrote the settings being refused.
		return nil, fmt.Errorf("the throwaway VM's own settings, %s's own wherever it records any (docs/granulefile.md §5.5): %w", opts.Image, err)
	}

	s := &Session{
		d:         &CLI{Binary: opts.DockerBinary},
		dv:        &dockervm.Docker{BinaryPath: opts.DockerBinary},
		name:      name,
		container: vmspec.ContainerName(name),
		opts:      opts,
	}

	fmt.Fprintf(opts.Stdout, "booting %s\n", opts.Image)
	if err := dockervm.Create(ctx, s.dv, spec, io.Discard); err != nil {
		return nil, fmt.Errorf("starting the guest VM: %w", err)
	}
	if err := s.waitForGuest(ctx, opts.ReadyTimeout, opts.Stdout); err != nil {
		// The container is the caller's to keep or remove even now: a
		// guest that never came up is the case its console log is most
		// worth reading.
		return s, fmt.Errorf("waiting for the guest to become reachable: %w", err)
	}
	return s, nil
}

// Container is the name of the VM's docker container, for an error that
// tells its reader what to look at.
func (s *Session) Container() string { return s.container }

// Exec runs one command in the guest. A command that exits non-zero
// comes back as an *ExitError carrying its status.
func (s *Session) Exec(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	return s.d.guestExec(ctx, s.container, stdout, stderr, args...)
}

// Command is one command to run in the guest, with the context docker
// gives one of its own: the account it runs as, the directory it starts
// in, and the variables overlaid on the session's environment.
//
// It is what a Granulefile's `RUN` inside a VM stage compiles to (§6):
// the stage's `USER`, `WORKDIR` and `ENV` are build-time state that
// decides those three for each command in turn, and the same booted
// guest serves all of them.
type Command struct {
	// Argv is the command and its arguments. A shell-form RUN has
	// already had its `SHELL` put in front of it by the caller, since
	// what interprets a command string is the file's business and not
	// this package's.
	Argv []string

	// User is the guest account to run as. Empty means the container's
	// own default, which is root.
	User string

	// Dir is an absolute guest directory to run in. Empty means the
	// account's home directory.
	Dir string

	// Env are "KEY=value" entries overlaid on the session's environment.
	Env []string
}

// Run runs one Command in the guest. A command that exits non-zero comes
// back as an *ExitError carrying its status, exactly as Exec's does.
func (s *Session) Run(ctx context.Context, cmd Command, stdout, stderr io.Writer) error {
	var flags []string
	if cmd.User != "" {
		flags = append(flags, "-u", cmd.User)
	}
	if cmd.Dir != "" {
		flags = append(flags, "-w", cmd.Dir)
	}
	for _, e := range cmd.Env {
		flags = append(flags, "-e", e)
	}
	return s.d.guestExecAs(ctx, s.container, flags, stdout, stderr, cmd.Argv...)
}

// Script writes script to path inside the guest and runs it.
func (s *Session) Script(ctx context.Context, script, path string, stdout, stderr io.Writer) error {
	return s.d.guestScript(ctx, s.container, script, path, stdout, stderr)
}

// Unpack streams a tar archive into a directory in the guest.
//
// The counterpart to Script, and here for what a script cannot carry:
// Script travels on a command line, so anything the size of a binary --
// the agents `granule guest rebase -boot` lays over a guest, say --
// has to arrive as a stream instead.
func (s *Session) Unpack(ctx context.Context, archive io.Reader, dest string, stderr io.Writer) error {
	return s.d.guestUnpack(ctx, s.container, archive, dest, stderr)
}

// Place writes content to path in the VM *container* -- not in the guest
// it is running -- so that a caller about to Commit can correct
// something about the guest half that lives beside the disk rather than
// inside it.
//
// The one thing that needs it is the guest contract sidecar. A commit
// captures the container's filesystem, so it carries the base image's
// sidecar forward unchanged while the disk beside it has just had
// granule's guest side laid over it: the declaration would describe an
// image that no longer exists. See pkg/guestimage's ContractSidecar
// and pkg/guestrebase's boot().
//
// Deliberately a whole-file write into the container rather than
// anything the guest can reach: the guest is the untrusted half, and
// nothing running in it should be able to say what its own disk
// declares.
func (s *Session) Place(ctx context.Context, path string, content []byte) error {
	return s.d.placeInContainer(ctx, s.container, path, content)
}

// Snapshot captures a full snapshot of the running VM inside the container,
// saving memory, device registers, and the disk overlay into destDir.
func (s *Session) Snapshot(ctx context.Context, destDir string) error {
	if destDir == "" {
		destDir = guestimage.SnapshotDir
	}
	return s.d.guestSnapshot(ctx, s.container, destDir)
}

// Running reports whether the VM's container is still up, which is how a
// guest command that failed because the whole VM has gone is told from
// one that ran and said no.
func (s *Session) Running(ctx context.Context) (bool, error) {
	out, err := s.d.Output(ctx, "inspect", "-f", "{{.State.Running}}", s.container)
	if err != nil {
		return false, fmt.Errorf("inspecting the guest container: %w", err)
	}
	return strings.TrimSpace(out) == "true", nil
}

// Stop powers the guest off and waits out its shutdown.
//
// SIGTERM, which "granule run" turns into an ACPI power button press and
// then waits out (see the top-level README's "Shutdown"). The returned
// error names an unclean exit -- a guest docker had to SIGKILL leaves the
// filesystem in its disk image mid-write, which matters to a caller
// about to commit it and not at all to one that is about to throw it
// away, so what to do about that is the caller's call and not this one's.
func (s *Session) Stop(ctx context.Context) error {
	secs := int(s.opts.ShutdownTimeout.Seconds())
	if err := s.d.Run(ctx, io.Discard, "stop", "-t", strconv.Itoa(secs), s.container); err != nil {
		return fmt.Errorf("stopping the guest container: %w", err)
	}
	code, err := s.d.Output(ctx, "inspect", "-f", "{{.State.ExitCode}}", s.container)
	if err != nil {
		return fmt.Errorf("reading the guest container's exit status: %w", err)
	}
	switch exit := strings.TrimSpace(code); exit {
	case "0":
		return nil
	case "137":
		return fmt.Errorf("the guest did not power off within %s and was killed, so its filesystem may be inconsistent -- raise -shutdown-timeout, or check whether something in the guest blocks shutdown", s.opts.ShutdownTimeout)
	default:
		// Anything else: the VM supervisor itself failed on the way
		// down. Reported with the code rather than swallowed, because
		// the console will show a guest that looks perfectly healthy and
		// the number is the only thing that says otherwise.
		return fmt.Errorf("the guest container exited %s rather than powering off cleanly, so its filesystem may be inconsistent", exit)
	}
}

// Commit captures the stopped container as an image tagged tag, with
// each of changes passed to `docker commit --change`.
func (s *Session) Commit(ctx context.Context, tag string, changes ...string) error {
	args := []string{"commit"}
	for _, c := range changes {
		args = append(args, "--change", c)
	}
	args = append(args, s.container, tag)
	return s.d.Run(ctx, s.opts.Stdout, args...)
}

// Remove deletes the VM and everything it holds. Removing the VM also
// releases the namespace and its taps; Delete additionally handles
// holders left by older granule versions.
func (s *Session) Remove(ctx context.Context) error {
	return dockervm.Delete(ctx, s.dv, s.name, 5, io.Discard)
}

// Console is the tail of the VM container's log, or "" if there is none
// to be had.
func (s *Session) Console(ctx context.Context, lines int) string {
	out, err := s.d.Output(ctx, "logs", "--tail", strconv.Itoa(lines), s.container)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// WithConsole returns err with the guest's console log in front of it.
//
// Console first, error last. An error with 40 lines of boot log appended
// is an error nobody reads: `tail` on the job log shows the guest
// reaching multi-user.target and hides the sentence that says what
// actually went wrong.
func (s *Session) WithConsole(ctx context.Context, err error) error {
	if logs := s.Console(context.WithoutCancel(ctx), 40); logs != "" {
		return fmt.Errorf("guest console, last 40 lines:\n%s\n%w", logs, err)
	}
	return err
}

// vmName names a throwaway VM. Unique per boot so concurrent ones on a
// host -- a CI runner doing several guest variants at once -- don't
// collide, and short because netshim derives the VM's tap device name as
// "tap-<name>", which Linux caps at 15 characters (VMSpec.Validate
// rejects anything longer).
func vmName(prefix string) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	if prefix == "" {
		prefix = "gb-"
	}
	return prefix + string(b)
}

// CLI is the docker command line, as the one thing in this tree that
// execs it. Exported because a caller doing its own docker work around a
// boot -- guestverify resolving an image's digest, or assembling the
// image it is about to boot -- should be driving the same binary the
// boot will, including when that binary is a test's fake.
type CLI struct {
	// Binary is the docker CLI to exec, resolved via PATH when it
	// contains no slash. Empty means dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker").
	Binary string
}

func (d *CLI) bin() string {
	if d.Binary == "" {
		return dockercli.Binary()
	}
	return d.Binary
}

// Run execs "docker <args...>", streaming stdout to w and folding stderr
// into the returned error.
func (d *CLI) Run(ctx context.Context, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Output execs "docker <args...>" and returns its stdout.
func (d *CLI) Output(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	if err := d.Run(ctx, &out, args...); err != nil {
		return "", err
	}
	return out.String(), nil
}

// guestExec runs a command inside the VM guest of container name, via
// the "granule exec" the container already carries -- the same path
// `kubectl exec ... -- granule exec` takes, and the only way in, since
// the keypair it authenticates with was generated inside that container
// at boot and never leaves it.
func (d *CLI) guestExec(ctx context.Context, name string, stdout, stderr io.Writer, args ...string) error {
	return d.guestExecAs(ctx, name, nil, stdout, stderr, args...)
}

// guestExecAs is guestExec with flags for "granule exec" itself in front
// of the "--": the account, the directory and the environment one
// command runs with, which are per-command flags rather than properties
// of the container fronting the guest.
func (d *CLI) guestExecAs(ctx context.Context, name string, flags []string, stdout, stderr io.Writer, args ...string) error {
	full := append([]string{"exec", name, "granule", "exec"}, flags...)
	full = append(full, "--")
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, d.bin(), full...)
	cmd.Stdout = stdout
	var errBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(stderr, &errBuf)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &ExitError{Code: exit.ExitCode(), Stderr: strings.TrimSpace(errBuf.String())}
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// guestSnapshot runs "granule snapshot -path <destDir> -resume" in container name,
// capturing memory, device registers, and the disk overlay, then resuming the guest
// so it can receive ACPI shutdown.
func (d *CLI) guestSnapshot(ctx context.Context, name, destDir string) error {
	full := []string{"exec", name, "granule", "snapshot", "-path", destDir, "-resume"}
	cmd := exec.CommandContext(ctx, d.bin(), full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// guestUnpack streams a tar archive into a directory in the guest,
// through the `granule cp -tar` the container already carries -- the same
// door guestExec goes through, so it needs no second credential and
// nothing guest-side that exec does not already need. `cp` creates the
// directory itself and picks its own encoding for the session, which is
// what makes a binary survive a guest that wraps its sessions in a pty.
func (d *CLI) guestUnpack(ctx context.Context, name string, archive io.Reader, dest string, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, d.bin(), "exec", "-i", name, "granule", "cp", "-tar", "-", dest)
	cmd.Stdin = archive
	cmd.Stdout = io.Discard
	var errBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(stderr, &errBuf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// placeInContainer writes content to path in the container's own
// filesystem, via `docker cp -`, which works on a stopped container and
// needs nothing inside it -- the runtime image is `scratch` plus two
// binaries, so there is no shell to write the file with.
func (d *CLI) placeInContainer(ctx context.Context, name, path string, content []byte) error {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{
		Name: filepath.Base(path),
		Mode: 0o644,
		Size: int64(len(content)),
	}); err != nil {
		return fmt.Errorf("packing %s for the container: %w", path, err)
	}
	if _, err := w.Write(content); err != nil {
		return fmt.Errorf("packing %s for the container: %w", path, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("packing %s for the container: %w", path, err)
	}

	cmd := exec.CommandContext(ctx, d.bin(), "cp", "-", name+":"+filepath.Dir(path))
	cmd.Stdin = &buf
	cmd.Stdout = io.Discard
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("writing %s into the container: %w: %s", path, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// guestScript writes script to path inside the guest and runs it.
//
// The script travels base64-encoded on the command line rather than as
// an argument or over stdin: it is a whole shell script, with quotes and
// newlines and possibly a shebang, and base64 is the one encoding that
// survives a trip through two shells (the container's and the guest's)
// without any of it needing to be escaped. The guest is known to have
// base64 -- the Dockerfile's guest-rootfs stage asserts it, since
// granule-authorized-key needs it too.
func (d *CLI) guestScript(ctx context.Context, name, script, path string, stdout, stderr io.Writer) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	// path is a shell word in this line, so it is quoted as one: the
	// callers' paths are constants today, and a path with a space or a
	// `;` in it must stay a path rather than become a second command.
	quoted := ShellQuote(path)
	install := fmt.Sprintf("printf %%s %s | base64 -d > %s && chmod 0755 %s", encoded, quoted, quoted)
	if err := d.guestExec(ctx, name, io.Discard, stderr, "sh", "-c", install); err != nil {
		return fmt.Errorf("staging the script in the guest: %w", err)
	}
	return d.guestExec(ctx, name, stdout, stderr, path)
}

// waitForGuest polls until the guest accepts a command, giving up at
// timeout -- or sooner, if the container itself exits, which no amount
// of further waiting would fix -- and then waits out the rest of the
// guest's boot.
func (s *Session) waitForGuest(ctx context.Context, timeout time.Duration, out io.Writer) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := s.d.guestExec(ctx, s.container, io.Discard, io.Discard, "true"); err == nil {
			s.waitForBoot(ctx, deadline, out)
			return nil
		}
		running, err := s.Running(ctx)
		if err != nil {
			return err
		}
		if !running {
			return fmt.Errorf("the guest container exited before its guest became reachable")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitForBoot waits for the guest's service manager to finish starting,
// so that whatever the caller does next runs against a settled guest
// rather than one still coming up.
//
// The first `granule exec` that answers only means granule-agent.service
// is up, and on the reference guest that is 1.816s into a boot that
// finishes at 2.3s -- so a caller that starts work the instant it gets an
// answer is racing everything systemd has not run yet. The one that bit
// was systemd-tmpfiles emptying /tmp under a staged setup script (see
// guestbuild's setupPath), but that is a shape of problem rather than a
// single bug: a `systemctl start` of one's own, or an `apt-get update`
// issued before the guest has a route, races the same boot. Waiting here
// means every caller gets that for free rather than opening their script
// with a `systemctl is-system-running --wait` of their own.
//
// Best effort, and deliberately so, because nothing that follows depends
// on it: not every guest has systemd (granule's own alpine variant runs
// OpenRC, where this exits 127 with nothing to wait for), a guest that
// boots into "degraded" is still a guest worth provisioning or checking,
// and a guest with one job that never finishes would otherwise turn a
// slow boot into a failure. So a guest that cannot say it has finished
// booting is used anyway -- with a line saying so, since a run that then
// behaves oddly wants that in its log.
//
// deadline is the ready timeout's own, shared with the reachability poll
// above rather than added to it: -ready-timeout bounds how long to wait
// for a usable guest, and a guest that spends all of it starting units
// has spent it.
func (s *Session) waitForBoot(ctx context.Context, deadline time.Time, out io.Writer) {
	wait, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	start := time.Now()
	err := s.d.guestExec(wait, s.container, io.Discard, io.Discard, "systemctl", "is-system-running", "--wait")
	if err == nil || ctx.Err() != nil {
		return
	}
	if wait.Err() != nil {
		fmt.Fprintf(out, "the guest had still not finished booting %s in; carrying on anyway (raise -ready-timeout if what runs next then races something the boot has not started yet)\n", time.Since(start).Round(time.Second))
	}
}

// ShellQuote renders s as a single POSIX shell word, for a path or an
// argument that has to be written into a `sh -c` line.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
