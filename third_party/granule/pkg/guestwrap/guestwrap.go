// Package guestwrap turns an ordinary container image into a granule
// image: one that boots a VM and runs that container inside it.
//
// It is the build-time half of `granule wrap`; cmd/granule-container is the
// half that runs in the guest. What it produces is an image `docker run`
// or a Kubernetes pod spec treats exactly like the image it wrapped --
// same process, same environment, same port -- with a kernel of its own
// underneath it instead of a shared one.
//
// # What a wrap is made of
//
// Three images go in and one comes out:
//
//	the wrapped image   the consumer's. Its filesystem becomes the
//	                    container inside the guest, and its OCI config
//	                    (entrypoint, command, env, workdir, user, stop
//	                    signal) becomes the spec that runs it.
//	the guest image     a granule guest rootfs -- an init system, the
//	                    agents, the network and shutdown handling. This
//	                    is the machine the container runs in.
//	the granule image    the VMM half the result is built on, and the
//	                    source of the guest-side pieces laid into it:
//	                    the agents (via pack) and granule-container.
//
// The wrapped image's filesystem is not merged into the guest's. It is
// placed whole at containerspec.RootfsDir and entered with a chroot at
// boot, so an image that carries its own /usr/bin/env, its own libc or no
// shell at all keeps the filesystem it was built against, and the guest
// around it keeps the one granule built.
//
// # Nothing from the wrapped image is executed
//
// This is the same property `granule guest pack` keeps, for the same
// reason, and wrap has to keep it too: wrapping is something a deployment
// does to an image a repository somewhere authored, on a machine whose
// isolation is the point. The wrapped image appears in the generated
// build as `COPY --from` and nowhere else -- no RUN, entrypoint, hook or
// postinst of its own ever runs -- and its configuration reaches wrap
// through `docker image inspect`, which reads the image's manifest rather
// than starting anything from it.
//
// The one RUN in the wrapped-guest stage runs the *guest image's* own
// shell, over paths granule itself has just written, before the wrapped
// filesystem is copied in at all. That ordering is deliberate: the COPY
// of somebody else's filesystem is the last instruction in the stage.
//
// # Without docker
//
// WrapDir (`granule wrap -rootfs`) is the same wrap for a host with no
// docker daemon -- one running containers on containerd alone. It takes
// the wrapped image as a directory and its OCI config as a file, the
// guest rootfs and granule's guest parts as directories too, and
// assembles the guest on the host: the trees are copied in Go, the
// workload is installed and enabled in Go, and granule's own pack-guest
// packs the disk with the host's mke2fs. What comes out is the guest half
// -- a disk and the kernel that boots it -- rather than an image. The
// property above holds there in the same words: the wrapped image is
// only ever read, and the wrapped filesystem is copied in last. See
// dir.go.
package guestwrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	_ "embed"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/guestpack"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// DefaultGuestImage is the machine a wrapped container runs in: granule's
// own guest rootfs, the published one.
//
// `-guest-image` names a pinned tag, a registry mirror, or a rootfs of
// your own.
//
// It has to be a *granule* guest rootfs rather than any distribution
// image: the packing step refuses a rootfs that does not enable granule's
// units, since a guest missing them boots and then never answers.
const DefaultGuestImage = "ghcr.io/gke-labs/granule/debian.rootfs:latest"

// wrappedStage is the build stage that holds the assembled guest -- the
// guest rootfs with the container and its spec inside it -- and is what
// the packing stages then pack. A stage rather than an intermediate image
// so that one `docker build` produces the result and leaves nothing
// behind to garbage-collect.
const wrappedStage = "wrapped-guest"

// The two service definitions that start the wrapped workload at boot,
// one per init system. Both are installed: which one runs is a property
// of the guest image, the pack step already detects it by asking the
// rootfs rather than by being told, and a few hundred bytes of the other
// one sitting unread in the disk is cheaper than a flag the caller has to
// get right.
//
// granule-container's own unit is not among them. The packing stages that
// follow this build lay granule's guest overlay down unconditionally, and
// that overlay owns /etc/systemd/system/granule-container.service and
// /etc/init.d/granule-container -- so a copy written here would be
// overwritten before the disk was packed, and only ever read as a second,
// drifting statement of a file pack already supplies.
var (
	//go:embed guest/granule-wrap@.service
	systemdWrapUnit []byte
	//go:embed guest/granule-wrap.openrc
	openrcWrapScript []byte
)

// Options configures one Wrap.
type Options struct {
	// From is the container image to wrap. Nothing in it is executed.
	From string

	// Tag is the image reference to give the result.
	Tag string

	// ServiceName names the generated unit and workload layout.
	// Empty means "app".
	ServiceName string

	// Restart sets the unit's restart policy: "always", "on-failure", "no".
	// Empty means "no" for standalone wrap, or "always" when wrapping via Granulefile.
	Restart string

	// ExitAction is "ignore" (leaves guest running) or "poweroff" (shuts guest down).
	ExitAction string

	// Runtime names the in-guest container runtime. Empty means "granule-container".
	Runtime string

	// StageName is the stage name for Granulefile metadata labels.
	StageName string

	// GuestImage is the granule guest rootfs the container runs inside.
	// Empty means DefaultGuestImage.
	GuestImage string

	// GranuleImage supplies the VMM half the result is built on, the
	// guest-side pieces that go into the guest (granule-container, and the
	// agents the packing lays down), and the kernel that guest boots.
	// Empty means vmspec.DockerImage.
	//
	// That last one is why this is the full granule image rather than the
	// `--target runtime` build `guest pack` recommends: the runtime stage
	// carries no kernel, deliberately, for derived guests that bring
	// their own -- and a wrapped guest rootfs normally does not, so
	// wrapping onto it yields an image whose boot stops at a missing
	// CHV_KERNEL. The price is the bundled disk in the result's lower
	// layers, which the wrapped guest replaces but cannot remove.
	KonturImage string

	// RequireGuestKernel makes the packing stages refuse a guest rootfs
	// that brings no kernel, which is the combination that cannot boot
	// where KonturImage has none either. Prepare fills it in from the
	// image itself -- see guestpack.KonturImageHasKernel -- so a caller
	// does not set it.
	RequireGuestKernel bool

	// PackerImage is the base the packing runs in. Empty means
	// guestpack.DefaultPackerImage.
	PackerImage string

	// Variant selects the overlay the packing lays down -- "debian"
	// (systemd), "alpine" (OpenRC), or empty to ask the guest rootfs
	// which init system it runs.
	Variant string

	// DiskMode is the default disk mode to bake into the wrapped image
	// (overlay, persistent, readonly). Empty leaves it unset.
	DiskMode string

	// DiskNumQueues configures virtio-blk multi-queue count (CHV_DISK_NUM_QUEUES).
	DiskNumQueues int

	// DiskIOThreads enables dedicated host iothreads for block devices (CHV_DISK_IOTHREADS).
	DiskIOThreads bool

	// DiskIOEngine configures the host asynchronous I/O backend (CHV_DISK_IO_ENGINE: "io_uring", "epoll", or "sync").
	DiskIOEngine string

	// DiskExtraMB is headroom in MiB on top of the 20% the guest disk is
	// sized with. A wrapped container's disk is sized to the image it was
	// built from, which is right for a workload that writes to a volume
	// or to /tmp and wrong for one that fills its own filesystem; this
	// and CHV_DISK_SIZE_MB (which costs the image nothing, and is the one
	// to reach for first) are the two ways to give it more.
	DiskExtraMB int

	// Entrypoint, if non-nil, replaces the image's ENTRYPOINT. As with
	// `docker run --entrypoint`, replacing it drops the image's CMD
	// unless Cmd is given as well -- the image's default arguments were
	// arguments to the entrypoint that is no longer there.
	Entrypoint []string

	// Cmd, if non-nil, replaces the image's CMD, the way trailing
	// arguments to `docker run` do.
	Cmd []string

	// Env is added to the image's own environment, as KEY=VALUE. A key
	// the image already sets is overridden rather than repeated.
	Env []string

	// WorkingDir and User override the image's WORKDIR and USER when set.
	WorkingDir string
	User       string

	// HaltOnExit powers the VM off when the container's process exits, so
	// that a `docker run` of the wrapped image ends when the container it
	// wraps would have ended. Default true; the CLI's -halt-on-exit=false
	// keeps the machine up for debugging instead.
	HaltOnExit *bool

	// DockerBinary is the docker CLI to exec, resolved via PATH when it
	// contains no slash. Empty means dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker"); tests point it at a fake.
	DockerBinary string

	// Pull allows wrap to fetch the wrapped image when it is not already
	// local, which it has to be for its configuration to be read. Default
	// true; turned off, an image that is not there is an error rather
	// than a network call.
	Pull *bool

	// ExtraBuildArgs are passed to `docker build` before the context.
	ExtraBuildArgs []string

	Stdout io.Writer
	Stderr io.Writer
}

// Plan is what a wrap decided to build, before it builds it: the spec
// that goes into the guest, the ports the wrapped image declared, and the
// build that puts them there.
//
// Separated from Wrap so that `granule wrap -print-dockerfile` can show
// the build, and the spec that drives it, without producing an image.
type Plan struct {
	Spec       containerspec.Spec
	Ports      []string
	Dockerfile string
}

// Wrap wraps From and publishes the result as Tag.
func Wrap(ctx context.Context, opts Options) error {
	opts, err := opts.resolve()
	if err != nil {
		return err
	}
	plan, err := Prepare(ctx, opts)
	if err != nil {
		return err
	}

	// The build context is four generated files: the Dockerfile, the
	// container spec, and a service definition per init system. Every
	// other input is an image reference, so nothing of the host's
	// filesystem is sent to the daemon and wrap reads nothing out here.
	dir, err := os.MkdirTemp("", "granule-wrap-")
	if err != nil {
		return fmt.Errorf("making a build context: %w", err)
	}
	defer os.RemoveAll(dir)

	spec, err := plan.Spec.Marshal()
	if err != nil {
		return fmt.Errorf("writing the container spec: %w", err)
	}
	for name, content := range map[string][]byte{
		"Dockerfile":        []byte(plan.Dockerfile),
		specFile:            spec,
		systemdWrapUnitFile: wrapUnit(opts.Restart),
		openrcWrapFile:      openrcWrapScript,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return fmt.Errorf("writing %s into the build context: %w", name, err)
		}
	}

	d := &dockercli.CLI{Binary: opts.DockerBinary}
	fmt.Fprintf(opts.Stdout, "wrapping %s: the VM will run %s\n", opts.From, strings.Join(plan.Spec.Argv(), " "))
	args := append([]string{"build", "--file", filepath.Join(dir, "Dockerfile"), "--tag", opts.Tag}, opts.ExtraBuildArgs...)
	args = append(args, dir)
	if err := d.Run(ctx, opts.Stdout, opts.Stderr, args...); err != nil {
		return wrapError(err, opts)
	}

	fmt.Fprintf(opts.Stdout, "wrapped %s as %s\n", opts.From, opts.Tag)
	if size, err := d.ImageSize(ctx, opts.Tag); err == nil {
		fmt.Fprintf(opts.Stdout, "%s is %s; `docker run --privileged %s` boots it\n", opts.Tag, dockercli.HumanSize(size), opts.Tag)
	}
	return nil
}

// Prepare reads the wrapped image's configuration and works out what the
// guest will run, without building anything.
func Prepare(ctx context.Context, opts Options) (Plan, error) {
	opts, err := opts.resolve()
	if err != nil {
		return Plan{}, err
	}

	cfg, err := inspect(ctx, opts)
	if err != nil {
		return Plan{}, err
	}

	spec, err := newSpec(opts.From, cfg, opts.overrides())
	if err != nil {
		return Plan{}, err
	}

	// Whether the granule image brings the kernel that boots a guest
	// bringing none decides whether the guest assembled below has to
	// bring one. A wrapped guest normally does not, so naming a
	// `--target runtime` build here is the way to get an image that
	// builds, pushes and then stops at a missing CHV_KERNEL; the packing
	// stages refuse it instead, and this is where they are told to.
	// Asked after the spec, so that an image with nothing to run is
	// still refused by the sentence about that rather than by a docker
	// call about something else.
	konturKernel, err := guestpack.KonturImageHasKernel(ctx, opts.DockerBinary, opts.KonturImage)
	if err != nil {
		return Plan{}, err
	}
	opts.RequireGuestKernel = !konturKernel

	ports := exposedPorts(cfg)
	dockerfile, err := Dockerfile(opts, ports)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Spec: spec, Ports: ports, Dockerfile: dockerfile}, nil
}

// overrides are the caller's edits to what the wrapped image's own
// configuration says to run: the part of Options and DirOptions that
// newSpec reads, so that a wrap built by docker and one assembled on a
// directory tell the guest the same thing for the same image.
type overrides struct {
	ServiceName string
	HaltOnExit  bool
	Entrypoint  []string
	Cmd         []string
	Env         []string
	WorkingDir  string
	User        string
}

func (opts Options) overrides() overrides {
	return overrides{
		ServiceName: opts.ServiceName,
		HaltOnExit:  *opts.HaltOnExit,
		Entrypoint:  opts.Entrypoint,
		Cmd:         opts.Cmd,
		Env:         opts.Env,
		WorkingDir:  opts.WorkingDir,
		User:        opts.User,
	}
}

// newSpec is what the guest is told to run: image's configuration with
// the caller's overrides applied, refused if there is nothing in it to
// run.
func newSpec(image string, cfg imageConfig, o overrides) (containerspec.Spec, error) {
	spec := containerspec.Spec{
		Name:       o.ServiceName,
		Image:      image,
		Rootfs:     containerspec.RootfsDir,
		Entrypoint: cfg.Entrypoint,
		Cmd:        cfg.Cmd,
		Env:        cfg.Env,
		WorkingDir: cfg.WorkingDir,
		User:       cfg.User,
		Volumes:    volumes(cfg),
		StopSignal: cfg.StopSignal,
		HaltOnExit: o.HaltOnExit,
	}
	if o.Entrypoint != nil {
		// Replacing the entrypoint drops the image's default arguments,
		// which were arguments to the entrypoint being replaced. `docker
		// run --entrypoint` does the same.
		spec.Entrypoint = o.Entrypoint
		spec.Cmd = nil
	}
	if o.Cmd != nil {
		spec.Cmd = o.Cmd
	}
	if o.WorkingDir != "" {
		spec.WorkingDir = o.WorkingDir
	}
	if o.User != "" {
		spec.User = o.User
	}
	spec.Env = mergeEnv(spec.Env, o.Env)
	if _, ok := spec.LookupEnv("PATH"); !ok {
		// An image with no PATH of its own gets the one a container
		// runtime would have given it, written down rather than left for
		// the guest to assume: the spec in the guest should say exactly
		// what the process's environment is.
		spec.Env = append(spec.Env, "PATH="+containerspec.DefaultPath)
	}
	if err := spec.Validate(); err != nil {
		return containerspec.Spec{}, fmt.Errorf("%s cannot be wrapped: %w", image, err)
	}
	return spec, nil
}

// wrapUnit is the systemd unit that starts the workload, carrying the
// wrap's restart policy.
func wrapUnit(restart string) []byte {
	return bytes.ReplaceAll(systemdWrapUnit, []byte("Restart=always"), []byte("Restart="+restart))
}

// The names the generated build gives the files wrap puts in its context.
const (
	specFile            = "container.json"
	systemdWrapUnitFile = "granule-wrap@.service"
	openrcWrapFile      = "granule-wrap.openrc"
)

// serviceNamePattern validates service names according to docs/granulefile.md §5.2.
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// Dockerfile is the build wrap runs, as text: the wrapped-guest stage,
// then guestpack's own stages packing it into a granule image.
func Dockerfile(opts Options, ports []string) (string, error) {
	opts, err := opts.resolve()
	if err != nil {
		return "", err
	}
	packStages, err := guestpack.PackStages(guestpack.Options{
		From: wrappedStage,
		// The guest that was packed was assembled in this build, so the
		// provenance the packed image records is the rootfs that guest
		// was built from; the container inside it is the label wrap adds
		// below.
		PackedFrom:         opts.GuestImage,
		Tag:                opts.Tag,
		KonturImage:        opts.KonturImage,
		PackerImage:        opts.PackerImage,
		Variant:            opts.Variant,
		DiskMode:           opts.DiskMode,
		DiskNumQueues:      opts.DiskNumQueues,
		DiskIOThreads:      opts.DiskIOThreads,
		DiskIOEngine:       opts.DiskIOEngine,
		DiskExtraMB:        opts.DiskExtraMB,
		RequireGuestKernel: opts.RequireGuestKernel,
	})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w(`# Generated by "granule wrap" -- see pkg/guestwrap.`)
	w(`#`)
	w(`# Nothing here executes anything out of the image being wrapped. It`)
	w(`# appears once, as a COPY of its filesystem -- the last instruction`)
	w(`# of the first stage -- and no RUN, entrypoint or postinst of its`)
	w(`# own is ever run. What it *will* run, once the VM has booted, is`)
	w(`# in %s, read at boot by granule-container.`, containerspec.ConfigPath)
	w(``)
	w(`# The machine the container runs in: granule's guest rootfs, with an`)
	w(`# init system, the vsock agent, and the network and shutdown`)
	w(`# handling a VM needs. The container's filesystem goes inside it,`)
	w(`# not over it.`)
	w(`FROM %s AS %s`, opts.GuestImage, wrappedStage)
	w(``)
	w(`# The guest-side runner, from the granule image this guest is packed`)
	w(`# onto -- the same source the agents come from, so the program that`)
	w(`# runs the container and the granule that built the image around it`)
	w(`# cannot skew.`)
	w(`COPY --from=%s %s %s`, opts.KonturImage, guestimage.PartsRunner, containerspec.RunnerPath)
	w(``)
	w(`# What that runner runs: the wrapped image's own OCI configuration,`)
	w(`# read with "docker image inspect" rather than by starting anything.`)
	w(`COPY %s %s`, specFile, containerspec.ConfigPath)
	w(``)
	w(`# One service definition per init system. The guest rootfs decides`)
	w(`# which of them is read; the other is inert. granule-container's own`)
	w(`# unit is not written here: the packing stages below lay granule's`)
	w(`# guest overlay down unconditionally and that overlay owns it, so a`)
	w(`# copy from this build would be overwritten before the disk is`)
	w(`# packed.`)
	w(`COPY %s /etc/systemd/system/granule-wrap@.service`, systemdWrapUnitFile)
	w(`COPY %s /etc/init.d/granule-wrap`, openrcWrapFile)
	w(``)
	w(`# Enabling that service, and nothing else. This runs the guest`)
	w(`# image's own shell over paths granule has just written, before the`)
	w(`# wrapped filesystem below is copied in -- systemd and OpenRC are`)
	w(`# not running here to be asked, so the symlinks a "systemctl`)
	w(`# enable" would write are written by hand, the same way pack-guest`)
	w(`# enables the services it lays down.`)
	w(`#`)
	w(`# One service starts the workload and not two: granule-container and`)
	w(`# granule-wrap@%s run the same %s, so a guest`, opts.ServiceName, containerspec.ConfigPath)
	w(`# that started both would run the wrapped workload twice. The unit`)
	w(`# this build wrote is the one enabled, since it is the one carrying`)
	w(`# the wrap's restart policy; the granule-container the packing lays`)
	w(`# down stays in the disk unenabled, for anything that refers to it`)
	w(`# by name. The two rm -f below are about a guest image that arrived`)
	w(`# with granule-container already enabled, which is a different`)
	w(`# question from who supplies the unit.`)
	w(`RUN set -eu; \`)
	w(`    chmod 0755 %s /etc/init.d/granule-wrap; \`, containerspec.RunnerPath)
	w(`    if [ -d /etc/systemd ]; then \`)
	w(`      mkdir -p /etc/systemd/system/multi-user.target.wants; \`)
	w(`      rm -f /etc/systemd/system/multi-user.target.wants/granule-container.service; \`)
	w(`      ln -sf /etc/systemd/system/granule-wrap@.service \`)
	w(`        /etc/systemd/system/multi-user.target.wants/granule-wrap@%s.service; \`, opts.ServiceName)
	w(`    fi; \`)
	w(`    if [ -d /etc/runlevels ]; then \`)
	w(`      mkdir -p /etc/runlevels/default; \`)
	w(`      rm -f /etc/runlevels/default/granule-container; \`)
	w(`      ln -sf /etc/init.d/granule-wrap \`)
	w(`        /etc/runlevels/default/granule-wrap; \`)
	w(`    fi; \`)
	w(`    [ -d /etc/systemd ] || [ -d /etc/runlevels ] || \`)
	w(`      { echo "%s runs neither systemd nor OpenRC, so nothing would start the container" >&2; exit 1; }`, opts.GuestImage)
	w(``)
	w(`# The wrapped image's filesystem, and the only instruction in this`)
	w(`# build that touches it. Last on purpose: everything above has`)
	w(`# already run, so no step of this build can see a file of the`)
	w(`# consumer's, let alone execute one.`)
	w(`COPY --from=%s / %s`, opts.From, containerspec.RootfsDir)
	w(``)
	b.WriteString(packStages)

	// The ports the wrapped image declared, re-declared on the wrapping
	// image. The VM takes over the container's own IP, so a port the
	// container listened on is a port the VM listens on, and `docker run
	// -P` should keep working across a wrap.
	if len(ports) > 0 {
		w(`EXPOSE %s`, strings.Join(ports, " "))
	}
	w(`LABEL dev.granule.wrapped-image="%s"`, opts.From)
	w(`LABEL dev.granule.granulefile.kind="wrap"`)
	if opts.StageName != "" {
		w(`LABEL dev.granule.granulefile.stage="%s"`, opts.StageName)
		w(`LABEL dev.granule.granulefile.version="granulefile/1.0-draft"`)
	}
	return b.String(), nil
}

// resolve fills in the defaults and rejects what cannot be built, before
// anything is written or any image is pulled.
func (opts Options) resolve() (Options, error) {
	if opts.From == "" || opts.Tag == "" {
		return opts, fmt.Errorf("an image to wrap and an output tag are both required")
	}
	if opts.GuestImage == "" {
		opts.GuestImage = DefaultGuestImage
	}
	if opts.KonturImage == "" {
		opts.KonturImage = vmspec.DockerImage
	}
	if opts.PackerImage == "" {
		opts.PackerImage = guestpack.DefaultPackerImage
	}
	if opts.ServiceName == "" {
		opts.ServiceName = "app"
	}
	if !serviceNamePattern.MatchString(opts.ServiceName) {
		return opts, fmt.Errorf("invalid service name %q: must match [a-z0-9][a-z0-9_.-]*", opts.ServiceName)
	}
	if opts.ExitAction != "" {
		switch opts.ExitAction {
		case "poweroff":
			halt := true
			opts.HaltOnExit = &halt
		case "ignore":
			halt := false
			opts.HaltOnExit = &halt
		default:
			return opts, fmt.Errorf("unknown exit action %q: expected poweroff or ignore", opts.ExitAction)
		}
	}
	if opts.HaltOnExit == nil {
		halt := true
		opts.HaltOnExit = &halt
	}
	if opts.Restart == "" {
		opts.Restart = "no"
	}
	switch opts.Restart {
	case "always", "on-failure", "no":
	default:
		return opts, fmt.Errorf("unknown restart policy %q: expected always, on-failure, or no", opts.Restart)
	}
	if opts.Pull == nil {
		pull := true
		opts.Pull = &pull
	}
	// The two settings that are the packing's rather than wrap's are
	// still checked here, where nothing has been pulled yet: they are
	// passed straight to guestpack, which would refuse them with the same
	// words, but only after wrap had fetched an image to read the
	// configuration of.
	switch opts.Variant {
	case "", "auto", "debian", "alpine":
	default:
		return opts, fmt.Errorf("unknown guest variant %q: expected debian, alpine, or empty to detect which init system the guest rootfs runs", opts.Variant)
	}
	if opts.DiskExtraMB < 0 {
		return opts, fmt.Errorf("disk headroom cannot be negative, got %d", opts.DiskExtraMB)
	}
	for _, ref := range []struct{ what, value string }{
		{"the image to wrap", opts.From},
		{"the output tag", opts.Tag},
		{"the guest image", opts.GuestImage},
		{"the granule image", opts.KonturImage},
		{"the packer image", opts.PackerImage},
	} {
		if err := dockercli.ValidateRef(ref.value); err != nil {
			return opts, fmt.Errorf("%s: %w", ref.what, err)
		}
	}
	for _, e := range opts.Env {
		if !strings.Contains(e, "=") {
			return opts, fmt.Errorf("the environment entry %q is not KEY=VALUE", e)
		}
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	return opts, nil
}

// imageConfig is the part of an image's OCI configuration that decides
// what its container is. The field names are docker's own, since this is
// unmarshalled straight from `docker image inspect -f {{json .Config}}`.
type imageConfig struct {
	Entrypoint   []string            `json:"Entrypoint"`
	Cmd          []string            `json:"Cmd"`
	Env          []string            `json:"Env"`
	WorkingDir   string              `json:"WorkingDir"`
	User         string              `json:"User"`
	StopSignal   string              `json:"StopSignal"`
	Volumes      map[string]struct{} `json:"Volumes"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
}

func volumes(cfg imageConfig) []string {
	var vols []string
	for vol := range cfg.Volumes {
		vols = append(vols, vol)
	}
	sort.Strings(vols)
	return vols
}

// portPattern is what an exposed port may look like to be written into
// the generated build: a number, optionally with a protocol. Docker
// stores these as strings in an image somebody else authored, and they
// end up in an EXPOSE instruction, so they are checked rather than
// trusted -- and a port that does not match is dropped rather than
// failing the wrap, since EXPOSE is documentation and nothing depends on
// it being complete.
var portPattern = regexp.MustCompile(`^[0-9]{1,5}(/(tcp|udp|sctp))?$`)

func exposedPorts(cfg imageConfig) []string {
	var ports []string
	for port := range cfg.ExposedPorts {
		if portPattern.MatchString(port) {
			ports = append(ports, port)
		}
	}
	sort.Strings(ports)
	return ports
}

// mergeEnv adds the caller's environment entries to the image's, with a
// key given twice keeping the caller's value in the image's position --
// so the result reads like the image's environment with edits, rather
// than like a list with contradictions in it.
func mergeEnv(base, extra []string) []string {
	merged := append([]string{}, base...)
	for _, e := range extra {
		key, _, _ := strings.Cut(e, "=")
		replaced := false
		for i, existing := range merged {
			if name, _, _ := strings.Cut(existing, "="); name == key {
				merged[i], replaced = e, true
				break
			}
		}
		if !replaced {
			merged = append(merged, e)
		}
	}
	return merged
}

// inspect reads the wrapped image's configuration, pulling it first if it
// is not local and wrap is allowed to.
//
// This is the only thing wrap asks about the image, and it asks docker
// rather than the image: `image inspect` reads a manifest that is already
// on the machine. Nothing is started, and no code of the image's runs.
func inspect(ctx context.Context, opts Options) (imageConfig, error) {
	d := &dockercli.CLI{Binary: opts.DockerBinary}
	out, err := d.Output(ctx, "image", "inspect", "-f", "{{json .Config}}", opts.From)
	if err != nil {
		if !*opts.Pull {
			return imageConfig{}, fmt.Errorf("%s is not on this machine and wrap was told not to pull it: %w", opts.From, err)
		}
		fmt.Fprintf(opts.Stdout, "%s is not here yet, pulling it\n", opts.From)
		if perr := d.Run(ctx, opts.Stdout, opts.Stderr, "pull", opts.From); perr != nil {
			return imageConfig{}, fmt.Errorf("pulling %s to read its configuration: %w", opts.From, perr)
		}
		out, err = d.Output(ctx, "image", "inspect", "-f", "{{json .Config}}", opts.From)
		if err != nil {
			return imageConfig{}, fmt.Errorf("reading %s's configuration: %w", opts.From, err)
		}
	}
	return parseConfig(out, opts.From)
}

// parseConfig reads what `docker image inspect` printed. An image with no
// config at all is refused here rather than turning into a spec with
// nothing in it, whose failure would only show up on a VM's console.
func parseConfig(out, ref string) (imageConfig, error) {
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return imageConfig{}, fmt.Errorf("docker has nothing to say about %s's configuration, so there is nothing to wrap", ref)
	}
	var cfg imageConfig
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		return imageConfig{}, fmt.Errorf("parsing %s's configuration: %w", ref, err)
	}
	return cfg, nil
}

// wrapError turns a failed build into an error that says which of wrap's
// assumptions was not met, where the build's own message would not.
func wrapError(err error, opts Options) error {
	text := err.Error()
	// Every question below is asked of one log line at a time -- see
	// dockercli.BuildCouldNotFind and BuildCouldNotResolve. The generated
	// build names the runner, the guest image and the packer image in
	// steps of its own, so buildkit has all of them in the tail it prints
	// for a failure anywhere later, and matching a phrase anywhere
	// against a name anywhere explained a failure that was neither.
	//
	// The two questions are asked with different words on purpose: "not
	// found" is what docker says about a path an image does not carry,
	// and an unresolvable *reference* says nothing of the kind on a
	// modern daemon.
	//
	// The granule image is missing from this list because wrap reads it
	// before it generates anything (guestpack.KonturImageHasKernel): a
	// reference to it that will not resolve stops there, and is
	// explained there, in docker's registry wording rather than a build
	// log's.
	switch {
	case strings.Contains(text, guestpack.NoKernelMarker):
		// The packing stages stopped because nothing would boot the
		// result -- see guestpack.NoKernelMarker. The explanation is
		// theirs; the rootfs it is about is the guest image being
		// wrapped, since that is the half a kernel would have come in.
		return fmt.Errorf("%w\n\n%s", err, guestpack.KernelRefusal(opts.GuestImage, opts.KonturImage))
	case dockercli.BuildCouldNotFind(text, guestimage.PartsRunner):
		// The whole path rather than its base name, which is what this
		// used to match: "granule-container" is also the name of the
		// unit file, the init script and the program's destination in
		// the guest, all of them lines of this same build, so the base
		// name on a "not found" line is as likely to be one of those as
		// it is to be the runner. What a build that cannot find the
		// runner prints is the COPY's source path in full:
		// `"/usr/local/share/granule/guest-parts/granule-container":
		// not found`.
		//
		// A stock build, because that is what wrap wants: the guest
		// rootfs it wraps brings no kernel, so a `--target runtime`
		// granule image -- which this sentence used to name on its own --
		// would satisfy the runner and then fail the same build on
		// NoKernelMarker above.
		return fmt.Errorf("%w\n\nthe granule image %s carries no %s, so it cannot supply the program that runs the container inside the guest. An image built before `granule wrap` existed does not have it: rebuild it from a granule that does -- a stock `docker build .`, which carries the %s a wrapped guest boots as well; a `docker build --target runtime` build has neither unless the guest rootfs brings its own kernel",
			err, opts.KonturImage, guestimage.PartsRunner, guestimage.RuntimeKernel)
	case dockercli.BuildCouldNotResolve(text, opts.PackerImage):
		return fmt.Errorf("%w\n\nthe packer image %s could not be resolved. It is where the packing itself runs -- anything with /bin/sh and mke2fs will do -- and it is pulled from a registry like any other image, so this is a registry or a network answer rather than anything about the images being wrapped. Name one this machine can reach with -packer-image",
			err, opts.PackerImage)
	case dockercli.BuildCouldNotResolve(text, opts.GuestImage):
		return fmt.Errorf("%w\n\nthe guest image %s could not be resolved. It is the granule guest rootfs the container runs beside -- the init system, the agents, the network and shutdown handling -- and %s is the published one, so a pull that fails is a registry or a network answer rather than a missing build. Name another with -guest-image, or build one from a granule checkout with `docker build --target guest-rootfs-image -t <tag> .`",
			err, opts.GuestImage, DefaultGuestImage)
	}
	return err
}
