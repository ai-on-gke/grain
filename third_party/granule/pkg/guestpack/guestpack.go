// Package guestpack turns an OCI image whose filesystem is a granule
// guest rootfs into a bootable granule image: it packs that filesystem
// into a raw ext4 disk and publishes it as the guest half of an image
// `docker run` boots.
//
// It is the counterpart of publishing the rootfs (the Dockerfile's
// guest-rootfs-image target) rather than the disk. A rootfs image is
// ordinary Docker layers, so a consumer customizes a guest with an
// ordinary Dockerfile and a rebuild pushes one small layer; the disk
// becomes a local derived artifact, packed by whoever is about to boot
// it. See docs/guest-images.md for the whole shape and
// deploy/guest-image/README.md for how a consumer uses it.
//
// # Nothing from the image being packed is executed
//
// This is the property the package exists to keep, and the reason pack
// is safe to run on the machine that runs the daemon -- against an image
// a repository somewhere authored. Pack reads a filesystem and writes a
// disk: the image being packed appears in the generated build exactly
// once, as `COPY --from`, and no RUN, entrypoint, hook or postinst of
// its own ever runs. The single RUN in that build is the packer image's
// own shell running granule's own pack-guest script (guestimage.PartsScript)
// over the copied tree.
//
// Two mechanisms preserve that, and either is fine: a docker build whose
// only contact with the image is `COPY --from`, which is what this does,
// or `docker create` plus `docker export`. Anything that boots or runs
// the consumer's image does not -- that is `guest build`, which needs
// KVM and belongs in the consumer's own CI rather than on a host whose
// isolation is the point.
//
// Nothing privileged is needed either. `mke2fs -d` builds a filesystem
// straight from a directory tree, so there is no loop mount anywhere in
// this path and nothing to grant beyond an ordinary docker build; the
// tree's ownership, modes and device nodes survive because buildkit does
// the copying as root, inside the build.
//
// # Pack is where granule's own guest-side pieces go on
//
// The overlay and the two agent binaries are laid over the consumer's
// rootfs before the filesystem is measured or packed, out of the granule
// image being packed onto (guestimage.PartsDir). That is the reason
// packing late is worth more than the registry dedup it also buys: the
// parts of a guest that must not come from a consumer are the packer's
// current build, so an image somebody published months ago boots with
// today's agent in it -- with no rebuild on their side, and no code of
// theirs executed to put it there. It turns "keep the guest contract
// small and hope nobody skews" into a property of how the image is
// built.
package guestpack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// DefaultPackerImage is the image the packing itself runs in: a base
// that supplies /bin/sh, coreutils and mke2fs, and that is *not* the
// image being packed.
//
// Debian's slim base is the default because the Dockerfile's own
// guest-image stage packs from it and because e2fsprogs is one apt call
// away where a packer image has not got it already. Anything with
// mke2fs will do -- an image that already carries it does no network
// I/O at all, which is what an air-gapped deployment should point
// -packer-image at.
//
// It is pinned by digest, and to the same one as the Dockerfile's
// guest-image stage (a test holds the two together): every pack runs
// its packing in this image, so whoever could move the tag would choose
// the userland that packs every guest. deploy/base-images.sh update
// bumps both.
const DefaultPackerImage = "debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8"

// partsIn is where guestimage.PartsDir is copied to inside the packing
// stage. A path of its own rather than the absolute one it has in the
// granule image: the packer image is a third image with its own
// filesystem, and what matters about the parts once they are in it is
// that pack-guest and the overlay trees are together in one directory.
const partsIn = "/parts"

// stagingRoot is where the generated build assembles the guest half
// before it is copied into the output image. A directory of its own,
// rather than writing straight to guestimage.Dir in the packer image, so
// that what the final stage copies is exactly what pack produced and
// nothing the packer image happened to have at those paths.
const stagingRoot = "/out"

// Options configures one Pack.
type Options struct {
	// From is the image to pack: an OCI image whose filesystem is a
	// granule guest rootfs, e.g. one built FROM granule's own
	// guest-rootfs-image. Nothing in it is executed -- see the package
	// comment.
	From string

	// Tag is the image reference to give the packed result.
	Tag string

	// GranuleImage supplies both halves of granule: the VMM half the
	// output image is built on, and the guest-side parts laid over From's
	// rootfs (guestimage.PartsDir). One image rather than two, so the
	// agents in the guest cannot skew from the granule that talks to them.
	//
	// A full granule image is the default and is what an ordinary guest
	// needs, because it supplies the kernel that boots one
	// (guestimage.RuntimeKernel) as well as the VMM half and the parts.
	// The price is its own bundled guest disk, which stays in the packed
	// image's lower layers even though the packed disk replaces it; pack
	// reports both sizes so that cost is visible rather than inferred.
	//
	// A `docker build --target runtime` build drops those bytes, and is
	// the one to name for a guest whose rootfs brings a kernel of its own
	// (built with GUEST_KERNEL_PACKAGE): that stage deliberately carries
	// no kernel, for exactly those guests. Packing a rootfs that brings
	// none onto it is refused -- see RequireGuestKernel -- rather than
	// producing an image whose boot stops at a missing CHV_KERNEL.
	KonturImage string

	// RequireGuestKernel makes the generated build fail unless the rootfs
	// being packed brings a kernel of its own, before it packs anything.
	//
	// Pack sets this for itself, from whether the granule image named
	// above carries guestimage.RuntimeKernel -- so the assertion is in
	// the build exactly when the VMM half has nothing to boot the result
	// with. A caller assembling its own build out of PackStages
	// (`granule wrap`) asks the same question and sets it the same way.
	//
	// It is a field rather than something PackStages works out because
	// PackStages generates text and this is a fact about an image, which
	// only docker can answer; -print-dockerfile inspects nothing and says
	// so where the assertion would be.
	RequireGuestKernel bool

	// PackerImage is the base the packing runs in. Empty means
	// DefaultPackerImage.
	PackerImage string

	// PackedFrom is what the packed image records as its provenance, when
	// From cannot say it: a caller assembling the rootfs in the same
	// build (see PackStages) passes a stage name as From, and a stage
	// name means nothing to anyone reading labels off the finished image.
	// Empty -- the ordinary case -- records From itself.
	PackedFrom string

	// Variant selects which overlay is laid down -- "debian" (systemd)
	// or "alpine" (OpenRC). Empty means "auto": pack-guest asks the
	// rootfs which init system it runs, which is the right question for a
	// guest customized past recognizing its distribution.
	Variant string

	// ExposedPorts are the ports the packed image should declare, as
	// docker writes them ("8080/tcp"). nil means "ask the image being
	// packed", which is the ordinary case: a consumer writes EXPOSE in
	// their guest Dockerfile, docker records it in the rootfs image's
	// config, and pack carries it across into the packed image's own.
	//
	// Carried, not opened. EXPOSE opens nothing under docker either --
	// `docker run -P` is its one functional consumer, and -p works
	// without it -- and here it is the image's declaration of what it
	// wants reachable, which something else has to grant. A granule guest
	// is shielded by default, and a declaration that opened a port by
	// itself would mean every casually written documentation line
	// punched a hole in that shield.
	//
	// The grant is `granule vm create -publish-declared`, which reads
	// this field back off the packed image and publishes those ports on
	// the VM's container (pkg/dockervm). That is the whole reason
	// to carry it: a declaration nothing can read is a comment.
	//
	// It has to be carried explicitly because the packed image is built
	// on the granule image rather than on the rootfs, so nothing of the
	// rootfs's own config reaches it on its own.
	ExposedPorts []string

	// Volumes are the volume mount points the packed image should declare.
	// nil means "ask the image being packed", which is the ordinary case.
	// Carried into the image metadata as dev.granule.guest.volumes label and VOLUME.
	Volumes []string

	// DiskMode is the default disk mode to bake into the packed image
	// (overlay, persistent, readonly). Empty leaves it unset so the runtime
	// default (overlay) or CHV_DISK_MODE at boot time applies.
	DiskMode string

	// DiskNumQueues configures virtio-blk multi-queue count (CHV_DISK_NUM_QUEUES).
	// 0 means single queue / hypervisor default.
	DiskNumQueues int

	// DiskIOThreads enables dedicated host iothreads for block devices (CHV_DISK_IOTHREADS).
	DiskIOThreads bool

	// DiskIOEngine configures the host asynchronous I/O backend (CHV_DISK_IO_ENGINE: "io_uring", "epoll", or "sync").
	DiskIOEngine string

	// StorageBackend selects the VM disk storage backend ("standard" or "extents").
	StorageBackend string

	// ExtentSizeMB configures the extent size in MiB (CHV_EXTENT_SIZE_MB / GRANULE_EXTENT_SIZE_MB).
	ExtentSizeMB int

	// DiskExtraMB is headroom in MiB on top of the 20% the disk is sized
	// with, for a guest that will be provisioned further after packing.
	// The same knob as the Dockerfile's GUEST_DISK_EXTRA_MB, and the same
	// warning applies: a docker layer is a tar, extracting one
	// materializes the hole truncate left, so every MiB here is a MiB on
	// every host that pulls the image. CHV_DISK_SIZE_MB grows the overlay
	// at boot instead and costs the image nothing.
	DiskExtraMB int

	// Entrypoint, if non-nil, overrides the image's ENTRYPOINT.
	Entrypoint []string

	// Cmd, if non-nil, overrides the image's CMD.
	Cmd []string

	// Env is added to the workload's environment, as KEY=VALUE.
	Env []string

	// WorkingDir and User override the workload's WORKDIR and USER when set.
	WorkingDir string
	User       string

	// StopSignal overrides the workload's STOPSIGNAL when set.
	StopSignal string

	// HaltOnExit powers the VM off when the workload process exits. Default true.
	HaltOnExit *bool

	// Spec is the workload spec to write into the packed rootfs (/etc/granule/container.json).
	// If nil and the image being packed defines an Entrypoint or Cmd, Pack constructs one with Rootfs: "/".
	Spec *containerspec.Spec

	// SpecFile is where Spec is in the build context, for a caller that
	// assembles its own context out of PackStages. Empty means
	// "container.json", which is where Pack puts it and what a context
	// holding one pack needs.
	//
	// A caller with two packed stages in one build needs two, and they
	// cannot both be that name -- see pkg/granulefile, which compiles a
	// file that may pack more than once.
	SpecFile string

	// DockerBinary is the docker CLI to exec, resolved via PATH when it
	// contains no slash. Empty means dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker"); tests point it at a fake.
	DockerBinary string

	// ExtraBuildArgs are passed to `docker build` before the context.
	// This is how a pack reaches what it would not otherwise: a private
	// registry holding the image being packed, a proxy the e2fsprogs
	// install needs, a builder other than the default.
	ExtraBuildArgs []string

	Stdout io.Writer
	Stderr io.Writer
}

// Pack packs From into a guest disk and publishes the result as Tag.
func Pack(ctx context.Context, opts Options) error {
	opts, err := opts.resolve()
	if err != nil {
		return err
	}

	if opts.Spec == nil {
		cfg, err := inspect(ctx, opts)
		if err == nil {
			if opts.ExposedPorts == nil {
				opts.ExposedPorts = exposedPortsFromConfig(cfg)
			}
			if opts.Entrypoint != nil || opts.Cmd != nil || len(cfg.Entrypoint) > 0 || len(cfg.Cmd) > 0 {
				spec := containerspec.Spec{
					Image:      opts.From,
					Rootfs:     "/",
					Entrypoint: cfg.Entrypoint,
					Cmd:        cfg.Cmd,
					Env:        cfg.Env,
					WorkingDir: cfg.WorkingDir,
					User:       cfg.User,
					StopSignal: cfg.StopSignal,
					HaltOnExit: true,
				}
				if opts.HaltOnExit != nil {
					spec.HaltOnExit = *opts.HaltOnExit
				}
				if opts.Entrypoint != nil {
					spec.Entrypoint = opts.Entrypoint
					spec.Cmd = nil
				}
				if opts.Cmd != nil {
					spec.Cmd = opts.Cmd
				}
				if opts.WorkingDir != "" {
					spec.WorkingDir = opts.WorkingDir
				}
				if opts.User != "" {
					spec.User = opts.User
				}
				if opts.StopSignal != "" {
					spec.StopSignal = opts.StopSignal
				}
				spec.Env = mergeEnv(spec.Env, opts.Env)
				if _, ok := spec.LookupEnv("PATH"); !ok {
					spec.Env = append(spec.Env, "PATH="+containerspec.DefaultPath)
				}
				if len(spec.Argv()) > 0 {
					if err := spec.Validate(); err != nil {
						return fmt.Errorf("%s workload configuration is invalid: %w", opts.From, err)
					}
					opts.Spec = &spec
				}
			}
		} else if opts.Entrypoint != nil || opts.Cmd != nil {
			spec := containerspec.Spec{
				Image:      opts.From,
				Rootfs:     "/",
				Entrypoint: opts.Entrypoint,
				Cmd:        opts.Cmd,
				Env:        opts.Env,
				WorkingDir: opts.WorkingDir,
				User:       opts.User,
				StopSignal: opts.StopSignal,
				HaltOnExit: true,
			}
			if opts.HaltOnExit != nil {
				spec.HaltOnExit = *opts.HaltOnExit
			}
			if _, ok := spec.LookupEnv("PATH"); !ok {
				spec.Env = append(spec.Env, "PATH="+containerspec.DefaultPath)
			}
			if len(spec.Argv()) > 0 {
				if err := spec.Validate(); err != nil {
					return fmt.Errorf("%s workload configuration is invalid: %w", opts.From, err)
				}
				opts.Spec = &spec
			}
		}
	}

	if opts.ExposedPorts == nil {
		opts.ExposedPorts = declaredPorts(ctx, opts)
	}
	if opts.Volumes == nil {
		opts.Volumes = declaredVolumes(ctx, opts)
	}

	// Whether the VMM half being packed onto brings a kernel decides
	// whether the rootfs has to. Asked before the build rather than
	// assumed, and asked of the image itself rather than of its name: a
	// `--target runtime` build, an image built before granule baked a
	// kernel in, and one a deployment assembled itself are all the same
	// question.
	hasKernel, err := KonturImageHasKernel(ctx, opts.DockerBinary, opts.KonturImage)
	if err != nil {
		return err
	}
	opts.RequireGuestKernel = !hasKernel

	dockerfile, err := Dockerfile(opts)
	if err != nil {
		return err
	}

	// The build context holds the generated Dockerfile and nothing else:
	// every input the build has is an image reference, so no part of the
	// host's filesystem is sent to the daemon and pack has nothing to
	// read out here.
	dir, err := os.MkdirTemp("", "granule-guest-pack-")
	if err != nil {
		return fmt.Errorf("making a build context: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte(dockerfile), 0o644); err != nil {
		return fmt.Errorf("writing the generated Dockerfile: %w", err)
	}

	if opts.Spec != nil {
		data, err := opts.Spec.Marshal()
		if err != nil {
			return fmt.Errorf("marshalling workload spec: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, opts.specFile()), data, 0o644); err != nil {
			return fmt.Errorf("writing %s into build context: %w", opts.specFile(), err)
		}
		fmt.Fprintf(opts.Stdout, "recording native workload for %s: %s\n", opts.Tag, strings.Join(opts.Spec.Argv(), " "))
	}

	d := &dockercli.CLI{Binary: opts.DockerBinary}
	fmt.Fprintf(opts.Stdout, "packing %s onto %s\n", opts.From, opts.KonturImage)
	args := append([]string{"build", "--file", path, "--tag", opts.Tag}, opts.ExtraBuildArgs...)
	args = append(args, dir)
	if err := d.Run(ctx, opts.Stdout, opts.Stderr, args...); err != nil {
		return packError(err, opts)
	}

	fmt.Fprintf(opts.Stdout, "packed %s\n", opts.Tag)
	reportSizes(ctx, d, opts)
	return nil
}

// resolve fills in the defaults and rejects what cannot be built,
// before anything is written or any image is pulled.
func (opts Options) resolve() (Options, error) {
	if opts.From == "" || opts.Tag == "" {
		return opts, fmt.Errorf("an image to pack and an output tag are both required")
	}
	if opts.KonturImage == "" {
		opts.KonturImage = vmspec.DockerImage
	}
	if opts.PackerImage == "" {
		opts.PackerImage = DefaultPackerImage
	}
	switch opts.Variant {
	case "", "auto", "debian", "alpine":
	default:
		return opts, fmt.Errorf("unknown guest variant %q: expected debian, alpine, or empty to detect which init system the rootfs runs", opts.Variant)
	}
	if opts.DiskExtraMB < 0 {
		return opts, fmt.Errorf("disk headroom cannot be negative, got %d", opts.DiskExtraMB)
	}
	switch opts.DiskMode {
	case "", config.DiskModeOverlay, config.DiskModePersistent, config.DiskModeReadOnly:
	default:
		return opts, fmt.Errorf("unknown disk mode %q: expected %q, %q, or %q", opts.DiskMode, config.DiskModeOverlay, config.DiskModePersistent, config.DiskModeReadOnly)
	}
	if opts.DiskNumQueues < 0 {
		return opts, fmt.Errorf("disk num queues cannot be negative, got %d", opts.DiskNumQueues)
	}
	if opts.DiskIOEngine != "" {
		switch strings.ToLower(opts.DiskIOEngine) {
		case "io_uring", "epoll", "sync":
		default:
			return opts, fmt.Errorf("unknown disk io engine %q: expected \"io_uring\", \"epoll\", or \"sync\"", opts.DiskIOEngine)
		}
	}
	switch opts.StorageBackend {
	case "", config.StorageBackendStandard, config.StorageBackendExtents:
	default:
		return opts, fmt.Errorf("unknown storage backend %q: expected %q or %q", opts.StorageBackend, config.StorageBackendStandard, config.StorageBackendExtents)
	}
	if opts.ExtentSizeMB < 0 {
		return opts, fmt.Errorf("extent size cannot be negative, got %d", opts.ExtentSizeMB)
	}
	if len(opts.ExposedPorts) > 0 {
		// A copy rather than a normalization in place: resolve is called
		// on a value, and rewriting the caller's slice through it would
		// be a surprise. Sorted and deduplicated because the source is a
		// map in an image's config, which has no order of its own, and a
		// generated Dockerfile that changes line for line between
		// identical packs is one nobody can diff.
		ports := make([]string, 0, len(opts.ExposedPorts))
		for _, port := range opts.ExposedPorts {
			normalized, err := dockercli.NormalizePort(port)
			if err != nil {
				return opts, err
			}
			ports = append(ports, normalized)
		}
		sort.Strings(ports)
		opts.ExposedPorts = slices.Compact(ports)
	}
	if len(opts.Volumes) > 0 {
		vols := make([]string, 0, len(opts.Volumes))
		for _, vol := range opts.Volumes {
			normalized, err := dockercli.NormalizeVolumePath(vol)
			if err != nil {
				return opts, err
			}
			vols = append(vols, normalized)
		}
		sort.Strings(vols)
		opts.Volumes = slices.Compact(vols)
	}
	for _, ref := range []struct{ what, value string }{
		{"the image to pack", opts.From},
		{"the packed-from record", opts.packedFrom()},
		{"the output tag", opts.Tag},
		{"the granule image", opts.KonturImage},
		{"the packer image", opts.PackerImage},
	} {
		if err := dockercli.ValidateRef(ref.value); err != nil {
			return opts, fmt.Errorf("%s: %w", ref.what, err)
		}
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Spec != nil {
		if opts.Spec.Rootfs == "" {
			opts.Spec.Rootfs = "/"
		}
		if err := opts.Spec.Validate(); err != nil {
			return opts, fmt.Errorf("workload spec is invalid: %w", err)
		}
	}
	return opts, nil
}

// imageConfig is the part of an image's OCI configuration that decides
// its workload and exposed ports.
type imageConfig struct {
	Entrypoint   []string            `json:"Entrypoint"`
	Cmd          []string            `json:"Cmd"`
	Env          []string            `json:"Env"`
	WorkingDir   string              `json:"WorkingDir"`
	User         string              `json:"User"`
	StopSignal   string              `json:"StopSignal"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
}

func inspect(ctx context.Context, opts Options) (imageConfig, error) {
	d := &dockercli.CLI{Binary: opts.DockerBinary}
	out, err := d.Output(ctx, "image", "inspect", "-f", "{{json .Config}}", opts.From)
	if err != nil {
		return imageConfig{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return imageConfig{}, fmt.Errorf("docker has nothing to say about %s's configuration", opts.From)
	}
	var cfg imageConfig
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		return imageConfig{}, fmt.Errorf("parsing %s's configuration: %w", opts.From, err)
	}
	return cfg, nil
}

func exposedPortsFromConfig(cfg imageConfig) []string {
	var ports []string
	for port := range cfg.ExposedPorts {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	return ports
}

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

// packedFrom is what the provenance label records: the caller's override
// where there is one, and otherwise the image that was packed.
func (opts Options) packedFrom() string {
	if opts.PackedFrom != "" {
		return opts.PackedFrom
	}
	return opts.From
}

// specFile is where the workload spec sits in the build context.
func (opts Options) specFile() string {
	if opts.SpecFile != "" {
		return opts.SpecFile
	}
	return "container.json"
}

// DeclaredPorts asks an image what it declares with EXPOSE, so a
// declaration a consumer wrote in their guest Dockerfile survives an
// operation that rebuilds the image around their machine -- a pack,
// where the result is built on the granule image rather than on their
// rootfs, or a rebase, where it is built on the granule image rather than
// on their packed one. Either way nothing of their config comes across
// on its own.
//
// Exported because both of those operations need it and there is one
// right answer to "what does this image say it wants reachable";
// pkg/guestrebase calls it for the same reason this package does.
// It reports what it did on report, which a caller with nowhere to print
// may set to io.Discard.
//
// The reading itself is guestimage.DeclaredPorts, which is shared with
// the other half of the relationship: a launcher reads the same field of
// a packed image in order to publish it (see "granule vm create
// -publish-declared"). This is the *build* half, and the difference
// between them is what a failed read means.
//
// Here it is best effort, and it says so when it fails: what a build
// does with the declaration is record it again, and failing a pack over
// an image docker cannot inspect (one that is not local, most often)
// would trade a working guest for a line of documentation. One that
// could not read it says so rather than quietly producing an image that
// declares nothing. A launcher asked to grant those ports cannot be so
// relaxed, and errors instead.
//
// What comes back is validated: every value is written into a generated
// Dockerfile, and it comes out of an image somebody else authored, so a
// "port" that is not one is dropped out loud rather than interpolated.
func DeclaredPorts(ctx context.Context, dockerBinary, ref string, report io.Writer) []string {
	if report == nil {
		report = io.Discard
	}
	ports, err := guestimage.DeclaredPorts(ctx, dockerBinary, ref, report)
	if err != nil {
		fmt.Fprintf(report, "could not read what %s declares with EXPOSE (%v), so the image built from it will declare nothing; pull it first, or pass the ports explicitly\n", ref, err)
		return nil
	}
	return ports
}

// declaredPorts is DeclaredPorts for the image being packed, with the
// line that says what was carried where.
func declaredPorts(ctx context.Context, opts Options) []string {
	usable := DeclaredPorts(ctx, opts.DockerBinary, opts.From, opts.Stdout)
	if len(usable) > 0 {
		fmt.Fprintf(opts.Stdout, "carrying %s's declared ports into %s: %s (declared, not opened -- a guest is shielded until something grants them)\n",
			opts.From, opts.Tag, strings.Join(usable, ", "))
	}
	return usable
}

// DeclaredVolumes asks an image what in-guest volume mount points it declares.
func DeclaredVolumes(ctx context.Context, dockerBinary, ref string, report io.Writer) []string {
	if report == nil {
		report = io.Discard
	}
	volumes, err := guestimage.DeclaredVolumes(ctx, dockerBinary, ref, report)
	if err != nil {
		fmt.Fprintf(report, "could not read what %s declares with VOLUME (%v), so the image built from it will declare no volumes; pull it first, or pass the volumes explicitly\n", ref, err)
		return nil
	}
	return volumes
}

func declaredVolumes(ctx context.Context, opts Options) []string {
	usable := DeclaredVolumes(ctx, opts.DockerBinary, opts.From, opts.Stdout)
	if len(usable) > 0 {
		fmt.Fprintf(opts.Stdout, "carrying %s's declared volume mount points into %s: %s\n",
			opts.From, opts.Tag, strings.Join(usable, ", "))
	}
	return usable
}

// Dockerfile is the build pack runs, as text.
//
// It is generated rather than shipped because three of its inputs are
// image references given at the command line, and it is exported (and
// printed by `granule guest pack -print-dockerfile`) because its whole
// security argument is something a reader should be able to check for
// themselves: the image being packed appears once, as a COPY.
//
// One thing it cannot show is what it was not told: the EXPOSE lines
// come from Options.ExposedPorts, which Pack fills in by inspecting the
// image and a print does not. The generated file says so where they are
// absent, since the alternative is a print that quietly differs from the
// build it claims to be.
func Dockerfile(opts Options) (string, error) {
	stages, err := PackStages(opts)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w(`# Generated by "granule guest pack" -- see pkg/guestpack.`)
	w(`#`)
	w(`# Nothing here executes anything out of the image being packed. It`)
	w(`# appears once, as a COPY of its filesystem, and no RUN, entrypoint`)
	w(`# or postinst of its own is ever run; the one RUN below is the packer`)
	w(`# image's own shell running granule's own pack-guest over the copied`)
	w(`# tree. That is what makes packing an image somebody else authored`)
	w(`# safe to do on the machine that runs the daemon.`)
	w(``)
	b.WriteString(stages)
	return b.String(), nil
}

// PackStages is the packing itself, as Dockerfile stages: the stage that
// lays granule's guest-side parts over From's filesystem and packs it into
// a disk, and the stage that publishes that disk on the VMM half.
//
// Exported separately from Dockerfile because a caller may be assembling
// the rootfs being packed in the same build rather than naming an image
// that already holds it -- `granule wrap` (pkg/guestwrap) builds a
// guest around a consumer's container image and then packs it, and doing
// that in one build leaves no intermediate image behind. Such a caller
// passes the name of its own stage as From.
func PackStages(opts Options) (string, error) {
	opts, err := opts.resolve()
	if err != nil {
		return "", err
	}
	variant := opts.Variant
	if variant == "" {
		variant = "auto"
	}
	staged := guestimage.Under(stagingRoot, guestimage.Dir)

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w(`FROM %s AS pack`, opts.PackerImage)
	w(``)
	w(`# mke2fs, where the packer image has not got it already. "mke2fs -d"`)
	w(`# builds the filesystem straight from a directory tree, which is why`)
	w(`# this build needs no loop mount and no privileges beyond an`)
	w(`# ordinary docker build.`)
	w(`RUN if ! command -v mke2fs >/dev/null 2>&1; then \`)
	w(`      if command -v apt-get >/dev/null 2>&1; then \`)
	w(`        apt-get update && apt-get install -y --no-install-recommends e2fsprogs && rm -rf /var/lib/apt/lists/*; \`)
	w(`      elif command -v apk >/dev/null 2>&1; then \`)
	w(`        apk add --no-cache e2fsprogs; \`)
	w(`      else \`)
	w(`        echo "the packer image has neither mke2fs nor a package manager to install e2fsprogs with" >&2; exit 1; \`)
	w(`      fi; \`)
	w(`    fi`)
	w(``)
	w(`# The rootfs being packed. Its ownership, modes and device nodes`)
	w(`# survive the copy because buildkit does it as root, inside the`)
	w(`# build -- the other half of needing no privileges out here.`)
	w(`COPY --from=%s / /rootfs`, opts.From)
	w(``)
	if opts.Spec != nil {
		w(`# Native workload specification (ENTRYPOINT/CMD) to run at boot.`)
		w(`COPY %s /rootfs/etc/granule/container.json`, opts.specFile())
		w(``)
	}
	if opts.RequireGuestKernel {
		// The granule image this is packed onto carries no kernel, so
		// the only thing that could boot the result is a kernel inside
		// the rootfs -- the pair pack-guest lifts out of /boot and
		// places beside the disk. Asked here, before the packing, so
		// that a combination nothing can boot costs a build rather than
		// an outage on whichever node ends up running it.
		w(`# %s carries no`, opts.KonturImage)
		w(`# %s, so the only kernel this result`, guestimage.RuntimeKernel)
		w(`# could boot is one the rootfs brings itself. Checked here, before`)
		w(`# anything is packed: the alternative is an image that builds,`)
		w(`# pushes, and then stops at a missing CHV_KERNEL on somebody`)
		w(`# else's node.`)
		//
		// What it echoes is one short line rather than the whole
		// explanation: buildkit prints a failing RUN's command back
		// four times, so a paragraph inside it is a paragraph read
		// four times and then a fifth. The line is a marker packError
		// recognizes, and the explanation is given out here, last,
		// where the caller is looking.
		w(`RUN ls /rootfs/boot/vmlinuz-* >/dev/null 2>&1 || \`)
		w(`    { echo "%s" >&2; exit 1; }`, NoKernelMarker)
		w(``)
	} else {
		w(`# No check that the rootfs brings a kernel of its own. One is put`)
		w(`# here instead when the granule image below carries no`)
		w(`# %s, since that build of granule has`, guestimage.RuntimeKernel)
		w(`# nothing to boot a kernel-less guest with; a pack decides which`)
		w(`# form to generate by looking at that image. A print of this build`)
		w(`# ("-print-dockerfile") looks at no image, so it always shows this`)
		w(`# one.`)
		w(``)
	}
	w(`# granule's own guest-side pieces, from the granule image this guest`)
	w(`# is packed onto: the overlay, the two agents, and the packing`)
	w(`# script. Taking them from that image rather than from the packer`)
	w(`# is what keeps a guest's agents and the granule that talks to them`)
	w(`# from skewing.`)
	w(`COPY --from=%s %s/ %s/`, opts.KonturImage, guestimage.PartsDir, partsIn)
	w(``)
	w(`# Lays those parts over the rootfs and packs the result. The`)
	w(`# settings are passed in the environment rather than as ARGs so`)
	w(`# that this file is the whole build and nothing has to be supplied`)
	w(`# alongside it.`)
	packEnv := fmt.Sprintf("GUEST_DISK_EXTRA_MB=%d GRANULE_GUEST_VARIANT=%s", opts.DiskExtraMB, variant)
	if opts.StorageBackend == config.StorageBackendExtents {
		packEnv += " GRANULE_STORAGE_BACKEND=extents"
		if opts.ExtentSizeMB > 0 {
			packEnv += fmt.Sprintf(" GRANULE_EXTENT_SIZE_MB=%d", opts.ExtentSizeMB)
		}
	}
	w(`RUN %s \`, packEnv)
	w(`    /bin/sh %s /rootfs %s %s`, path.Join(partsIn, path.Base(guestimage.PartsScript)), partsIn, staged)
	w(``)
	w(`# The VMM half, with the packed guest half copied in at the paths`)
	w(`# pkg/guestimage fixes. "docker run" on this boots it.`)
	w(`FROM %s`, opts.KonturImage)
	w(`COPY --from=pack %s/ %s/`, staged, guestimage.Dir)
	w(``)
	w(`# The last label is what this guest declares about itself: the`)
	w(`# guest-side contract the parts laid down above implement. It is`)
	w(`# restated here rather than inherited from the granule image because`)
	w(`# it describes the disk, and the disk is this build's own -- see`)
	w(`# pkg/guestimage's Contract, and %s`, guestimage.ContractFile)
	w(`# inside the disk, which says the same thing from the inside.`)
	w(`LABEL org.opencontainers.image.base.name="%s" \`, opts.KonturImage)
	w(`      dev.granule.guest.packed-from="%s" \`, opts.packedFrom())
	if len(opts.Volumes) > 0 {
		w(`      %s=%q \`, guestimage.VolumesLabel, guestimage.EncodeVolumes(opts.Volumes))
	}
	w(`      %s="%s"`, guestimage.ContractLabel, guestimage.ContractValue())
	if len(opts.Volumes) > 0 {
		w(``)
		w(`# Volume mount points declared by the guest, carried across from`)
		w(`# the image being packed.`)
		w(`VOLUME %s`, guestimage.EncodeVolumes(opts.Volumes))
	}
	if len(opts.ExposedPorts) > 0 {
		w(``)
		w(`# What the guest declares it wants reachable, carried across from`)
		w(`# the image being packed. Declared, not granted: this opens no port`)
		w(`# in the guest's shield and none in the container either, exactly as`)
		w(`# EXPOSE opens nothing under docker. It is here so that whatever`)
		w(`# launches this image can read the declaration and decide --`)
		w(`# "granule vm create -publish-declared", or "docker run -P".`)
		w(`EXPOSE %s`, strings.Join(opts.ExposedPorts, " "))
	} else {
		w(``)
		w(`# No EXPOSE: either the image being packed declares no ports, or this`)
		w(`# is a print of the build ("-print-dockerfile"), which inspects no`)
		w(`# image and so has no declaration to carry across.`)
	}
	if opts.DiskMode != "" {
		w(``)
		w(`# Default disk mode baked into the image.`)
		w(`ENV %s="%s"`, "CHV_DISK_MODE", opts.DiskMode)
	}
	if opts.DiskNumQueues > 0 {
		w(``)
		w(`# Default disk num queues baked into the image.`)
		w(`ENV %s="%d"`, "CHV_DISK_NUM_QUEUES", opts.DiskNumQueues)
	}
	if opts.DiskIOThreads {
		w(``)
		w(`# Default disk iothreads setting baked into the image.`)
		w(`ENV %s="true"`, "CHV_DISK_IOTHREADS")
	}
	if opts.DiskIOEngine != "" {
		w(``)
		w(`# Default disk io engine baked into the image.`)
		w(`ENV %s="%s"`, "CHV_DISK_IO_ENGINE", opts.DiskIOEngine)
	}
	if opts.StorageBackend == config.StorageBackendExtents {
		w(``)
		w(`# Default storage backend baked into the image.`)
		w(`ENV %s="%s"`, "CHV_STORAGE_BACKEND", config.StorageBackendExtents)
	}
	if opts.ExtentSizeMB > 0 {
		w(``)
		w(`# Default extent size in MiB baked into the image.`)
		w(`ENV %s="%d"`, "CHV_EXTENT_SIZE_MB", opts.ExtentSizeMB)
	}
	return b.String(), nil
}

// KonturImageHasKernel reports whether a granule image carries the
// kernel that boots a guest bringing none of its own
// (guestimage.RuntimeKernel).
//
// Exported because every command that builds one half onto the other has
// to ask it -- a pack, and `granule wrap`, which assembles its own build
// out of PackStages -- and there is one right answer. It starts nothing
// from the image: see dockercli.Files.
func KonturImageHasKernel(ctx context.Context, dockerBinary, ref string) (bool, error) {
	d := &dockercli.CLI{Binary: dockerBinary}
	has, err := d.HasFile(ctx, ref, guestimage.RuntimeKernel)
	if err != nil {
		// This read is the first thing any of these commands does with
		// the granule image, so it is also where a reference that will
		// not resolve stops them -- before a Dockerfile is generated,
		// and with `docker create`'s own message, which for a registry
		// that wants a login is the bare "error from registry: denied".
		// Explained here rather than beside the generated build, which
		// never gets that far.
		if dockercli.PullCouldNotResolve(err.Error()) {
			return false, fmt.Errorf("looking at what %s carries: %w\n\n%s", ref, err, UnresolvedKontur(ref))
		}
		return false, fmt.Errorf("looking at what %s carries: %w", ref, err)
	}
	return has, nil
}

// UnresolvedKontur explains a granule image reference a registry would
// not hand over. It says what that image is for, because the reference
// is usually a default nobody typed: every command here builds onto it,
// so "denied" on its own reads as a failure of the image being packed or
// wrapped rather than of the base underneath it.
//
// Exported for the same reason KernelRefusal is: `granule wrap`
// assembles its own build and asks the same question of the same image.
func UnresolvedKontur(ref string) string {
	return fmt.Sprintf("the granule image %s could not be resolved, so nothing could be read from it and there is nothing to build onto. That is a registry, a credential or a network answer about the reference itself rather than anything about the image being built: name one this machine can reach with -kontur-image, log in to the registry holding it (`docker login`), or build one from a granule checkout with `docker build -t %s .`", ref, ref)
}

// NoKernelMarker is what the generated build echoes when the rootfs
// brings no kernel and the granule image has none to lend it. Short,
// because buildkit prints a failing RUN's command back several times;
// the explanation is KernelRefusal below, given by whoever ran the build
// rather than by the build, where the caller is actually looking.
//
// It contains no backtick, dollar or double quote: it is echoed from a
// generated shell command, where any of those would be the shell's
// rather than the message's. Exported with KernelRefusal because
// `granule wrap` runs these stages inside a build of its own and has to
// recognize the same stop.
const NoKernelMarker = "granule: neither half of this image brings a kernel"

// KernelRefusal explains a build that stopped on NoKernelMarker. It
// names both halves and both ways out, because the person reading it has
// a granule image and a rootfs in front of them and either one can be
// the one to change.
func KernelRefusal(rootfs, konturImage string) string {
	return fmt.Sprintf("the guest rootfs (%s) brings no kernel of its own -- there is no /boot/vmlinuz-* in it -- and %s carries no %s either, so the image would have had nothing to boot. A `docker build --target runtime` granule image deliberately carries no kernel: it is the VMM half for guests that bring their own. Either build onto a full granule image (a `docker build` with no --target, which supplies %s), or build that rootfs with --build-arg GUEST_KERNEL_PACKAGE=%s so that it brings a kernel and an initramfs of its own",
		rootfs, konturImage, guestimage.RuntimeKernel, guestimage.RuntimeKernel, guestKernelPackage(runtime.GOARCH))
}

// guestKernelPackage is the Debian kernel package a rootfs built for
// goarch installs to bring its own kernel. Debian names those packages
// after the same amd64/arm64 Go does, and a guest runs on the
// architecture granule itself runs on (see config.GuestConsole), so the
// package KernelRefusal suggests is the one for this binary's own.
// Anything else gets amd64's, the one granule has always named.
func guestKernelPackage(goarch string) string {
	switch goarch {
	case "arm64":
		return "linux-image-arm64"
	default:
		return "linux-image-amd64"
	}
}

// packError turns a failed build into an error that says which of pack's
// assumptions was not met, where the build's own message would not.
func packError(err error, opts Options) error {
	text := err.Error()
	if strings.Contains(text, NoKernelMarker) {
		return fmt.Errorf("%w\n\n%s", err, KernelRefusal(opts.From, opts.KonturImage))
	}
	if dockercli.BuildCouldNotFind(text, guestimage.PartsDir) {
		// Asked of one log line at a time -- see BuildCouldNotFind. The
		// generated build copies the parts dir in the step before it
		// packs, so buildkit has that path in the tail it prints for any
		// later failure, and a message matched across the whole text
		// blamed the granule image for a packing step that had gone
		// wrong on its own.
		//
		// The remedy names the stock build rather than `--target
		// runtime`, which is what it used to say: that build carries no
		// kernel, so a caller following this sentence for a rootfs that
		// brings none would land straight on KernelRefusal above, having
		// been sent there by the message that was meant to unblock them.
		return fmt.Errorf("%w\n\nthe granule image %s has no %s, so it cannot supply the guest-side pieces a pack lays down. An image built before `guest pack` existed does not carry them: rebuild it from a granule that does -- a stock `docker build .`, which carries the %s a rootfs bringing no kernel of its own needs as well, or `docker build --target runtime` for a rootfs built with GUEST_KERNEL_PACKAGE",
			err, opts.KonturImage, guestimage.PartsDir, guestimage.RuntimeKernel)
	}
	// The two references the build is the first thing to pull: the
	// granule image is read before a Dockerfile is generated at all (see
	// KonturImageHasKernel), but the rootfs is only inspected best-effort
	// and the packer image is not touched out here, so for these two the
	// build log is where an unresolvable reference surfaces. Different
	// failure from the one above -- nothing was read, so nothing about
	// what an image carries is the answer -- and asked of one log line at
	// a time for the same reason: see dockercli.BuildCouldNotResolve.
	switch {
	case dockercli.BuildCouldNotResolve(text, opts.From):
		return fmt.Errorf("%w\n\nthe rootfs image %s could not be resolved. That is a registry, a credential or a network answer about the reference itself rather than anything about what the image holds -- nothing in it was read, let alone executed. Check the reference, and whether this machine is logged in to the registry holding it; -from names the image to pack",
			err, opts.From)
	case dockercli.BuildCouldNotResolve(text, opts.PackerImage):
		return fmt.Errorf("%w\n\nthe packer image %s could not be resolved. It is where the packing itself runs, not part of what comes out, so this is a registry or a network answer rather than anything about the images being packed: name one this machine can reach with -packer-image -- anything with /bin/sh and mke2fs will do",
			err, opts.PackerImage)
	}
	return err
}

// reportSizes says how large the packed image is and how much of it came
// from the granule image underneath.
//
// The second number is the one worth printing: a full granule image
// carries a bundled guest disk, and packing onto it leaves those bytes
// in the lower layers of the result even though the packed disk has
// replaced them. Nothing can delete them from out here -- the VMM half
// is a scratch image with no shell to run a removal in -- and the point
// of saying so is that the cost is otherwise invisible until a registry
// bill or a pull.
//
// What the advice is depends on the guest that was just packed, which is
// why it is given here rather than in the flag's help. A `--target
// runtime` build sheds the bundled guest, but it sheds the kernel with
// it: it is the right thing to pack onto only for a guest that brings a
// kernel of its own, and recommending it unconditionally is how a pack
// that succeeds becomes a boot that does not.
//
// Best effort: this is a report, and a docker that cannot answer it has
// still produced the image.
func reportSizes(ctx context.Context, d *dockercli.CLI, opts Options) {
	packed, perr := d.ImageSize(ctx, opts.Tag)
	base, berr := d.ImageSize(ctx, opts.KonturImage)
	if perr != nil || berr != nil {
		return
	}
	fmt.Fprintf(opts.Stdout, "%s is %s, of which %s below it is %s\n",
		opts.Tag, dockercli.HumanSize(packed), opts.KonturImage, dockercli.HumanSize(base))
	const bundledGuestThreshold = 256 << 20
	if base < bundledGuestThreshold {
		return
	}
	fmt.Fprintf(opts.Stdout, "%s looks like it carries a bundled guest of its own; those bytes stay in the packed image's lower layers even though the packed disk replaces them.\n", opts.KonturImage)

	// Whether the packed guest brought its own kernel is a question about
	// the image that was just built, so it is asked of that image rather
	// than inferred from the rootfs. A docker that will not answer leaves
	// the advice off entirely: half of it is wrong for each guest, and
	// the two sentences above stand on their own.
	own, err := d.HasFile(ctx, opts.Tag, guestimage.GuestKernel)
	if err != nil {
		return
	}
	if own {
		fmt.Fprintf(opts.Stdout, "This guest brings its own kernel, so it needs nothing else from the VMM half: pack onto a `docker build --target runtime` build of granule to stop paying for two guests.\n")
		return
	}
	fmt.Fprintf(opts.Stdout, "A `docker build --target runtime` build of granule sheds them, but sheds %s with them, and this guest brings no kernel of its own -- so the full image is what boots it and those bytes are the price. A rootfs built with GUEST_KERNEL_PACKAGE brings its own kernel and can be packed onto the lean build instead.\n", guestimage.RuntimeKernel)
}
