// Package guestbuild customizes a granule guest image by booting it,
// provisioning the guest from inside, and committing the result as a new
// image.
//
// This is the counterpart to the Dockerfile's own GUEST_SETUP_SCRIPT
// hook, and the two differ in what they can do rather than in what they
// produce. That hook runs in a container during the image build, on the
// build host's kernel, with no service manager -- so it can install
// packages and drop in files, and cannot start anything, load a module,
// or run a container. This boots the guest for real: the environment the
// setup script sees is the environment the guest will actually run in,
// so `systemctl start`, a warm docker image cache, and a test suite run
// against the finished image are all available.
//
// What that costs is /dev/kvm on the machine doing the build, and the
// scrub below -- a booted rootfs accumulates per-boot identity that must
// not be baked into an image every VM is then cloned from.
//
// The output is an ordinary OCI image, and specifically the same *kind*
// of image as the input: granule, cloud-hypervisor and a bootable guest
// disk, so `docker run` on it boots a VM exactly as the base does. A
// customized guest is therefore something a consumer derives and
// publishes with the tools they already have, rather than a disk image
// they have to build, host and hand to granule separately.
//
// The boot-and-exec loop itself lives in pkg/guestboot, which
// pkg/guestverify runs too: the difference between building and
// verifying is what happens to the guest once it is up, not how it is
// got up.
package guestbuild

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/guestboot"
	"github.com/bwsalmon/granule/pkg/guestimage"
)

// setupPath is where the caller's script is staged inside the guest.
//
// Deliberately not /tmp, which is where this used to be, for exactly the
// reason it was chosen: a boot clears /tmp, and it clears it *late*. On
// the reference guest `/usr/lib/tmpfiles.d/tmp.conf` carries
// `D /tmp 1777 root root -`, so `systemd-tmpfiles --create --remove
// --boot` empties it a second or two in -- after granule-agent.service is
// up and answering, which is when a build stages. That lost two guest
// builds in three on one host, with the staged script deleted between
// the write and the exec and the caller left holding an exit 127 (a
// guest running OpenRC rather than systemd cleans /tmp at boot too, so
// this is not one init system's quirk). guestboot's own boot wait closes
// the window from the other side; staging somewhere no boot clears is
// what makes the window irrelevant.
//
// A dotfile in /root rather than a tmpfs: /run would also survive the
// boot clean, but it is mounted noexec on guests whose initramfs mounts
// it (Debian's does), and the script is executed by path so that its own
// shebang decides what interprets it. /root is on the guest's root
// filesystem, exists on every rootfs granule builds, and is only ever
// readable by root. Nothing survives into the committed image: the
// removal below runs before the commit, and every path that skips it
// fails the build instead of committing.
const setupPath = "/root/.granule-guest-setup.sh"

// scrub is run in the guest after the setup script and before shutdown.
//
// Everything here is per-boot identity, and every line of it is a bug if
// left in: an image is cloned into many VMs, and state that is supposed
// to be unique per machine stops being unique the moment it is baked in.
// A shared /etc/machine-id gives every VM the same journald identity and
// the same DHCP DUID; shared SSH host keys make host-key verification
// meaningless across the fleet; a shared random-seed feeds every VM's
// entropy pool the same bytes at boot.
//
// Deliberately not here: anything the setup script chose to install or
// leave behind (package caches, build trees). Those are the caller's to
// clean up if they want a smaller image -- they are size, not
// correctness, and guessing at them would silently delete things a
// caller meant to keep.
//
// Nor any authorized_keys: granule's own guest has none, since exec no
// longer logs in (see pkg/guestexec), and one on a derived guest is
// there because a setup script deliberately put it there.
//
// The SSH host keys below are scrubbed even though the base image runs
// no sshd, and that is not vestigial: a setup script is free to install
// one, and a guest image that shipped the host keys openssh-server's
// postinst generated would hand every VM cloned from it the same
// identity -- the exact failure this list exists to prevent, and one
// that would now arrive through a caller's own choice rather than
// through anything granule installed.
const scrub = `set -eu
rm -f /etc/ssh/ssh_host_*
: > /etc/machine-id
rm -f /var/lib/dbus/machine-id
rm -f /var/lib/systemd/random-seed
rm -rf /var/lib/dhcp/* /var/lib/dhcpcd/*
if [ -d /var/log ]; then
	find /var/log -type f -exec sh -c ': > "$1"' _ {} \;
fi
`

// defaultSnapshotMemoryMB is how big a snapshot build's guest is when
// nobody has said: small, because the snapshot writes every byte of it
// into the image that comes out.
const defaultSnapshotMemoryMB = 384

// Options configures one Build.
type Options struct {
	// From is the base image: any image that boots a granule guest, i.e.
	// granule's own or one this function produced earlier.
	From string

	// Setup is the script's text (not a path), run as root inside the
	// booted guest. It reaches the guest base64-encoded on a command
	// line rather than over stdin, so nothing in it needs quoting and a
	// script with a shebang, quotes or newlines travels unchanged.
	//
	// Either this or Steps is required, and a build may have both: the
	// script runs first.
	Setup string

	// Steps are commands run in the booted guest one at a time, after
	// Setup. The first that fails fails the build, naming itself.
	//
	// The difference from Setup is that each step is a command of its
	// own rather than a line in one script, so each has its own account,
	// directory and environment, and a failure is reported against the
	// step rather than against a script the caller composed. That is
	// what a Granulefile's VM stage needs: its `RUN`s are separate
	// instructions on separate lines, with `USER`, `WORKDIR` and `ENV`
	// between them, and a reader has to be told which line failed.
	Steps []Step

	// Tag is the image reference to commit the result to.
	Tag string

	// Stage places files in the guest before the setup script runs, for
	// input a script cannot carry in its own text. Setup travels to the
	// guest on a command line, so anything the size of a binary has to
	// arrive as a stream instead; each entry here is one, unpacked in
	// the guest with `granule cp -tar` over the same vsock session the
	// script itself runs over.
	//
	// It is what `granule guest rebase -boot` hands the current
	// guest-side agents over with (see pkg/guestrebase), and it is
	// staging rather than installing on purpose: what a staged tree
	// means -- where its files belong, what they replace -- is the
	// setup script's business, and nothing here writes outside Dest.
	Stage []StagedTree

	// BaseLabel overrides the reference recorded on the result as
	// org.opencontainers.image.base.name. Empty means From, which is
	// right whenever From is an image somebody else could pull.
	//
	// `granule guest rebase -boot` is the case it is not: what that
	// boots is an intermediate image it builds and deletes on the way
	// out, and a provenance label pointing at a tag that no longer
	// exists anywhere is worse than none. It names the granule image the
	// rebase was onto instead.
	BaseLabel string

	// Labels are extra labels to record on the committed image, as
	// "name=value". A commit carries the base image's own labels
	// forward, so this is for the ones that are no longer true of the
	// result: `granule guest rebase -boot` restates the guest-side
	// contract version here, because what it has just laid over the
	// running filesystem is granule's current guest side and the base it
	// booted said whatever the image being rebased said.
	Labels []string

	// Volumes are volume mount points to record on the committed image.
	Volumes []string

	// DiskMode is the default disk mode to bake into the committed image
	// (overlay, persistent, readonly).
	DiskMode string

	// MemoryMB and MemoryMaxMB size the guest this boots, overriding
	// what the base image's own CHV_MEMORY_MB says. Zero leaves the
	// image's figure alone -- except for a snapshot build, which is
	// small by default for the reason below.
	//
	// A snapshot is the guest's whole memory written into the image it
	// produces, so a snapshot build nobody has sized is deliberately
	// small: `granule guest build -snapshot` on a stock guest should
	// not produce a multi-gigabyte image because the guest happened to
	// declare a large default. A caller that knows what the machine's
	// memory is meant to be says so here instead, and a Granulefile
	// build does: a `VM SET memory-mb=` next to a `VM SNAPSHOT` is the
	// size of the machine the file describes, and a restore replays the
	// snapshot's own memory size rather than the image's, so a build
	// that ignored the setting would produce a machine that could never
	// honour it.
	MemoryMB    int
	MemoryMaxMB int

	// DiskNumQueues configures virtio-blk multi-queue count (CHV_DISK_NUM_QUEUES).
	DiskNumQueues int

	// DiskIOThreads enables dedicated host iothreads for block devices (CHV_DISK_IOTHREADS).
	DiskIOThreads bool

	// DiskIOEngine configures the host asynchronous I/O backend (CHV_DISK_IO_ENGINE: "io_uring", "epoll", or "sync").
	DiskIOEngine string

	// StorageBackend selects the VM disk storage backend ("standard" or "extents").
	StorageBackend string

	// ExtentSizeMB configures the extent size in MiB (CHV_EXTENT_SIZE_MB).
	ExtentSizeMB int

	// Place are files to write into the *container* between the guest
	// powering off and the commit, as path -> contents. Not into the
	// guest: these are the parts of the guest half that live beside the
	// disk rather than inside it, which a commit would otherwise carry
	// forward from the base unchanged while the disk they describe has
	// moved on.
	//
	// The one caller is `granule guest rebase -boot`, restating the
	// guest contract sidecar for the same reason it restates the label
	// above. The directory has to exist in the base image already --
	// every granule image has guestimage.Dir, and a base without it has
	// no guest for this to be about.
	Place map[string][]byte

	// DockerBinary is the docker CLI to exec, resolved via PATH when it
	// contains no slash. Empty means dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker"); tests point it at a fake.
	DockerBinary string

	// ExtraRunArgs are passed to `docker run` before the image name.
	// This is how a build reaches a network it would not otherwise: a
	// proxy's environment, a CA bundle, a private registry's
	// credentials. The guest is a VM inside the container and inherits
	// none of the builder's own network context by itself.
	ExtraRunArgs []string

	// DNS is the nameserver(s) the build VM's guest resolves through,
	// spelled as VMSpec.DNS and netshim's NETSHIM_DNS do: comma
	// separated, at most three and two IPv4, "auto" for the build container's own
	// resolver when the guest can reach it (netshim.AutoDNS), and the
	// empty string for "leave whatever /etc/resolv.conf the image ships
	// with alone".
	//
	// nil means the VM spec's own default, netshim.DefaultDNS, which is
	// "auto". It is a pointer rather than a string precisely so that
	// "say nothing" and "say nothing to the guest" stay different
	// requests.
	//
	// It is here because a setup script is the one thing in a booting
	// build that reaches the internet by name, and the network it does
	// that from is the builder's rather than the deployment's: a build
	// on a restricted, air-gapped or heavily contended network resolves
	// through whatever that network offers, which is routinely not a
	// public resolver, and before this the build VM could not be told
	// about it.
	DNS *string

	// ReadyTimeout bounds how long to wait for the guest to accept a
	// command after the container starts. A guest that never becomes
	// reachable is the characteristic granule failure -- see
	// deploy/guest-image/README.md's "Networking" -- so this failing is
	// informative, and its message carries the container's console log.
	ReadyTimeout time.Duration

	// ShutdownTimeout bounds the wait for the guest to power off after
	// SIGTERM. Exceeding it means docker SIGKILLs the container out from
	// under a still-running guest, leaving the filesystem in the disk
	// image dirty -- so Build fails rather than committing that.
	ShutdownTimeout time.Duration

	// KeepOnFailure leaves the container in place when something goes
	// wrong, so its console log and its guest can be inspected. The
	// error names it.
	KeepOnFailure bool

	// Snapshot pauses the guest after running the setup script and scrub,
	// snapshots guest memory, vCPU/device registers, and the writable disk
	// overlay into /var/lib/granule/guest/snapshot/ in the output image artifact,
	// and labels the image with dev.granule.guest.snapshotted="true" for
	// instant resume on boot.
	Snapshot bool

	Stdout io.Writer
	Stderr io.Writer
}

// Step is one command run inside the booted guest.
type Step struct {
	// Description is what this step is, for the line printed before it
	// runs and for the error if it fails. A Granulefile build puts the
	// instruction and its line number here, so a failure reads the way
	// a failed container build's does.
	Description string

	// Command is what to run, and the account, directory and
	// environment to run it with.
	Command guestboot.Command
}

// StagedTree is one tar archive to unpack into the guest before the
// setup script runs.
type StagedTree struct {
	// Description is what this is, for the line that says it is being
	// copied -- a multi-megabyte copy over a vsock session is long
	// enough that a caller should be told what is taking the time.
	Description string

	// Tar is the archive, read once.
	Tar io.Reader

	// Dest is the directory in the guest to unpack it into, created if
	// it is not there. Somewhere no boot clears, and somewhere the setup
	// script cleans up: nothing staged should survive into the committed
	// image.
	Dest string
}

// Build boots From, runs Setup inside the guest, and commits the result
// as Tag.
func Build(ctx context.Context, opts Options) error {
	if opts.From == "" {
		return fmt.Errorf("a base image is required")
	}
	if opts.Tag == "" {
		return fmt.Errorf("an output tag is required")
	}
	if strings.TrimSpace(opts.Setup) == "" && len(opts.Steps) == 0 {
		return fmt.Errorf("a setup script or at least one step is required")
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	diskMode := config.DiskModePersistent
	if opts.DiskMode != "" {
		diskMode = opts.DiskMode
	}
	netMode := ""
	memMB, memMaxMB := opts.MemoryMB, opts.MemoryMaxMB
	if opts.Snapshot {
		netMode = config.NetModeNone
		if memMB == 0 {
			memMB = defaultSnapshotMemoryMB
			memMaxMB = defaultSnapshotMemoryMB
		}
	}

	s, err := guestboot.Boot(ctx, guestboot.Options{
		Image:      opts.From,
		NamePrefix: "gb-",
		// persistent, not the overlay every other VM gets: the whole
		// point here is for the setup script's changes to land in the
		// image's own disk, which is what `docker commit` then captures.
		// Writes into a per-boot overlay would be discarded with the
		// container and the committed image would be identical to its
		// base.
		DiskMode:        diskMode,
		NetMode:         netMode,
		MemoryMB:        memMB,
		MemoryMaxMB:     memMaxMB,
		DockerBinary:    opts.DockerBinary,
		ExtraRunArgs:    opts.ExtraRunArgs,
		DNS:             opts.DNS,
		ReadyTimeout:    opts.ReadyTimeout,
		ShutdownTimeout: opts.ShutdownTimeout,
		Stdout:          opts.Stdout,
		Stderr:          opts.Stderr,
	})
	if s == nil {
		return err
	}

	committed := false
	defer func() {
		if committed || opts.KeepOnFailure {
			return
		}
		_ = s.Remove(context.WithoutCancel(ctx))
	}()

	fail := func(format string, args ...any) error {
		wrapped := fmt.Errorf(format, args...)
		if opts.KeepOnFailure {
			return fmt.Errorf("%w (left running for inspection: docker logs %s; docker exec %s granule exec)", wrapped, s.Container(), s.Container())
		}
		return s.WithConsole(ctx, wrapped)
	}

	if err != nil {
		return fail("%w", err)
	}

	for _, tree := range opts.Stage {
		fmt.Fprintf(opts.Stdout, "copying %s into the guest\n", tree.Description)
		if err := s.Unpack(ctx, tree.Tar, tree.Dest, opts.Stderr); err != nil {
			return fail("copying %s into the guest: %w", tree.Description, err)
		}
	}

	if strings.TrimSpace(opts.Setup) != "" {
		fmt.Fprintf(opts.Stdout, "provisioning\n")
		if err := s.Script(ctx, opts.Setup, setupPath, opts.Stdout, opts.Stderr); err != nil {
			return fail("running the setup script in the guest: %w", err)
		}
		if err := s.Exec(ctx, io.Discard, io.Discard, "sh", "-c", "rm -f -- "+guestboot.ShellQuote(setupPath)); err != nil {
			return fail("removing the setup script from the guest: %w", err)
		}
	}

	for _, step := range opts.Steps {
		fmt.Fprintf(opts.Stdout, "%s\n", step.Description)
		if err := s.Run(ctx, step.Command, opts.Stdout, opts.Stderr); err != nil {
			return fail("%s: %w", step.Description, err)
		}
	}

	// The scrub runs after the setup script and the steps on purpose: a
	// setup script that starts services (which is the reason to be
	// booting at all) generates logs and machine state of its own, and
	// scrubbing before it would leave exactly that behind.
	if err := s.Exec(ctx, io.Discard, opts.Stderr, "sh", "-c", scrub); err != nil {
		return fail("scrubbing per-boot state from the guest: %w", err)
	}

	if opts.Snapshot {
		fmt.Fprintf(opts.Stdout, "snapshotting the guest VM\n")
		if err := s.Snapshot(ctx, guestimage.SnapshotDir); err != nil {
			return fail("snapshotting the guest VM: %w", err)
		}
	}

	// The wait for a clean power-off matters more here than anywhere
	// else: what is being committed is the guest's filesystem, and a
	// guest killed mid-write commits a dirty one that every VM cloned
	// from this image then inherits. A snapshotted guest reaches this
	// running rather than paused, which is what Snapshot's own -resume
	// is for: a paused guest never sees the power button, and a build
	// that asked one for a shutdown spent the whole timeout before
	// docker killed the container anyway.
	fmt.Fprintf(opts.Stdout, "shutting the guest down\n")
	if err := s.Stop(ctx); err != nil {
		return fail("%w", err)
	}

	// Anything beside the disk that the base image's copy of no longer
	// describes. After Stop, so it cannot race the running VM, and
	// before the commit, so it is in what the commit captures.
	for path, content := range opts.Place {
		if err := s.Place(ctx, path, content); err != nil {
			return fail("%w", err)
		}
	}

	// commit carries the base image's own config forward -- entrypoint,
	// env, labels -- so the result runs exactly the way its base does
	// with nothing to restate here. The one addition records what it was
	// built from, since an image with a guest disk in it otherwise says
	// nothing about its own provenance.
	base := opts.BaseLabel
	if base == "" {
		base = opts.From
	}
	changes := []string{"LABEL org.opencontainers.image.base.name=" + base}
	if opts.Snapshot {
		changes = append(changes, "LABEL "+guestimage.SnapshottedLabel+"=true")
	}
	if len(opts.Volumes) > 0 {
		changes = append(changes, fmt.Sprintf("LABEL %s=%s", guestimage.VolumesLabel, guestimage.EncodeVolumes(opts.Volumes)))
		changes = append(changes, fmt.Sprintf("VOLUME %s", guestimage.EncodeVolumes(opts.Volumes)))
	}
	for _, label := range opts.Labels {
		changes = append(changes, "LABEL "+label)
	}
	if opts.DiskMode != "" {
		changes = append(changes, "ENV CHV_DISK_MODE="+opts.DiskMode)
	}
	if opts.DiskNumQueues > 0 {
		changes = append(changes, fmt.Sprintf("ENV CHV_DISK_NUM_QUEUES=%d", opts.DiskNumQueues))
	}
	if opts.DiskIOThreads {
		changes = append(changes, "ENV CHV_DISK_IOTHREADS=true")
	}
	if opts.DiskIOEngine != "" {
		changes = append(changes, "ENV CHV_DISK_IO_ENGINE="+opts.DiskIOEngine)
	}
	if opts.StorageBackend != "" {
		changes = append(changes, "ENV CHV_STORAGE_BACKEND="+opts.StorageBackend)
	}
	if opts.ExtentSizeMB > 0 {
		changes = append(changes, fmt.Sprintf("ENV CHV_EXTENT_SIZE_MB=%d", opts.ExtentSizeMB))
	}
	if err := s.Commit(ctx, opts.Tag, changes...); err != nil {
		return fail("committing the provisioned guest: %w", err)
	}
	committed = true

	if err := s.Remove(ctx); err != nil {
		return fmt.Errorf("removing the build VM (the image %s was committed successfully): %w", opts.Tag, err)
	}
	fmt.Fprintf(opts.Stdout, "built %s\n", opts.Tag)
	return nil
}
