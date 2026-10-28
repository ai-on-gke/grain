// Package guestrebase makes an existing guest image's granule half
// current: it takes a granule image somebody else built, keeps the
// machine inside it, and replaces the parts that have to be the
// operator's with this build's own.
//
// It is `pack`'s intent (pkg/guestpack -- make the trusted parts
// current) applied to an image that was already packed, by somebody
// else, some time ago. See docs/guest-images.md for where the seam is
// and why it is where it is.
//
// # One verb, and what it costs is what you asked for
//
// A rebase has a cheap form and a complete one, and this is one verb
// rather than two because the two forms are the same intent -- they
// differ in how *much* of the image a caller is in a position to
// refresh, which depends on what they have rather than on what they
// want. Nobody should have to know that granule calls one of them
// something else.
//
// The expense is never silent, though, which is the other half of that
// choice. The default is the cheap form; the complete forms happen only
// when the caller hands over the rootfs image to repack from, or asks
// for a boot. A cheap rebase ends by saying what it did not refresh, and
// names the flag that would -- including the rootfs image the image
// itself records, when `guest pack` built it and left the label behind.
//
// # The cheap form: the VMM half, and no RUN anywhere
//
// `/usr/local/bin/granule-shim`, `cloud-hypervisor`, the shims and the
// entrypoint, with /var/lib/granule/guest/ left alone. It is expressed as
// a build whose base is the granule image and whose only contact with the
// image being rebased is `COPY --from`: seconds, no code of theirs run,
// and it works on any guest image whatever produced it. That is what
// makes a granule or cloud-hypervisor fix reach every consumer's image
// without a rebuild.
//
// The direction matters. The result is granule's image with their guest
// copied in, not their image with granule's binaries copied over it: an
// image's entrypoint, environment and labels are as much the VMM half as
// its binaries are, and taking their image as the base would keep
// theirs. It also decides what happens to vmlinux, which lives in the
// guest directory but belongs to the VMM half (see guestimage's
// RuntimeKernel): the files copied across are guestimage.Files(), which
// excludes it, so the result boots the deployment's own kernel rather
// than the one their image pinned.
//
// # The complete forms: repack, or a boot
//
// The cheap form cannot touch what is *inside* disk.img -- the
// guest-side agents and the overlay -- because the disk is an opaque
// ext4 image here. Two ways to refresh those, and deliberately not a
// third:
//
//   - Repack (-rootfs), where the rootfs image the guest was packed from
//     is still held. Then a rebase is a `pack` with the current agents,
//     which is exactly what a deployment that pulled a consumer's rootfs
//     image is in a position to do. Preferred: it runs nothing of
//     theirs, needs no KVM, and produces a disk sized to what is in it.
//   - A boot (-boot), otherwise: start the guest on the current VMM
//     half, lay granule's own guest-side parts over the running
//     filesystem, scrub the per-boot identity, power off and seal. This
//     one does run their code -- but in a VM, which is the distinction
//     the whole design rests on -- and it needs /dev/kvm.
//
// The third option, an ext4 writer of granule's own, is the one to avoid:
// much larger than the disk writer this tree has, for a job the other
// two already do.
package guestrebase

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestbuild"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/guestpack"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// PackedFromLabel is the label `granule guest pack` leaves on an image
// naming the rootfs image it packed. A rebase reads it to tell a caller
// that the complete form is available to them -- it is the one thing in
// an image that says where its guest came from -- and never acts on it
// by itself: repacking is minutes and a new disk, and an image is not
// where the decision to spend that belongs.
const PackedFromLabel = "dev.granule.guest.packed-from"

// RebasedFromLabel records the image a rebase took its guest half from,
// so a rebased image says where its machine came from the way a packed
// one says where its rootfs did.
const RebasedFromLabel = "dev.granule.guest.rebased-from"

// stageDir is where `-boot` stages granule's guest-side parts inside the
// guest before laying them down.
//
// Under /root, and for the same reason pkg/guestbuild stages its
// setup script there: a boot clears /tmp, and clears it late enough to
// delete what was staged a second earlier. Nothing survives into the
// committed image -- the setup script removes the directory, and a
// failure anywhere before that fails the build instead of committing.
const stageDir = "/root/.granule-rebase-parts"

// Options configures one Rebase.
type Options struct {
	// From is the guest image to rebase: any image that boots a granule
	// guest, whatever built it. Only its guest half is read, and nothing
	// in it is executed unless Boot is set.
	From string

	// Tag is the image reference to give the result.
	Tag string

	// GranuleImage supplies the halves being made current: the VMM half
	// the result is built on, and -- for the complete forms -- the
	// guest-side parts (guestimage.PartsDir) laid over the guest's own
	// filesystem. One image rather than two, so the agents in the guest
	// and the granule that talks to them over vsock cannot skew.
	//
	// A `docker build --target runtime` build is the one to name. A full
	// granule image works too, but it carries a bundled guest of its own:
	// its disk is replaced by the one being rebased, but those bytes stay
	// in the result's lower layers, and a guest *kernel* it carries is
	// refused outright rather than left to boot somebody else's rootfs.
	KonturImage string

	// Rootfs asks for the complete form by repacking: the OCI image whose
	// filesystem is the guest rootfs From was packed from. The guest half
	// is rebuilt from it with the current agents rather than carried
	// over, so From's own disk is replaced rather than edited.
	Rootfs string

	// Boot asks for the complete form by booting: run the guest on the
	// current VMM half, lay the current guest-side parts over its
	// filesystem, and commit. Needs /dev/kvm, and runs the guest's own
	// code -- inside the VM.
	Boot bool

	// Force rebases a guest whose declared contract version is below what
	// this granule requires, which is otherwise refused (see guestSide's
	// refuse). It is here because the refusal is a judgement made from
	// one number in a label, and the caller may know something it does
	// not: that the label is wrong, that the guest was patched by hand,
	// or that a broken image now is better than no image now. It does not
	// make the guest any newer, and it says so.
	Force bool

	// Variant selects which overlay a complete form lays down --
	// "debian" (systemd) or "alpine" (OpenRC). Empty asks the guest
	// which init system it runs, which is the right question for a guest
	// customized past recognizing its distribution.
	Variant string

	// PackerImage and DiskExtraMB are passed through to the repack form;
	// see pkg/guestpack.
	PackerImage string
	DiskExtraMB int

	// DockerBinary is the docker CLI to exec, resolved via PATH when it
	// contains no slash. Empty means dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker"); tests point it at a fake.
	DockerBinary string

	// ExtraBuildArgs are passed to the underlying `docker build`, and
	// ExtraRunArgs to the `docker run` the boot form boots with.
	ExtraBuildArgs []string
	ExtraRunArgs   []string

	// ReadyTimeout, ShutdownTimeout and KeepOnFailure apply to the boot
	// form only, and mean what pkg/guestbuild means by them.
	ReadyTimeout    time.Duration
	ShutdownTimeout time.Duration
	KeepOnFailure   bool

	Stdout io.Writer
	Stderr io.Writer
}

// Rebase rebases From onto the current granule image and publishes the
// result as Tag.
func Rebase(ctx context.Context, opts Options) error {
	opts, err := opts.resolve()
	if err != nil {
		return err
	}
	d := &dockerCLI{binary: opts.DockerBinary}

	if opts.Rootfs != "" {
		return repack(ctx, d, opts)
	}

	// What the image says about the half a cheap rebase cannot refresh,
	// read before anything is built: a guest too old for this granule is
	// refused here, and refusing after the build would mean having
	// produced the image being refused. The complete forms skip the
	// question because they are the answer to it.
	var side guestSide
	if !opts.Boot {
		side = readGuestSide(ctx, d, opts)
		if err := side.refuse(opts); err != nil {
			return err
		}
	}

	// The boot form builds the cheap one first and boots *that*, so the
	// VMM half running the guest is already the operator's. Booting From
	// itself would run the consumer's granule and cloud-hypervisor to
	// refresh their guest, which is the one thing a rebase must not
	// become.
	if opts.Boot {
		base := bootBaseTag(opts.Tag)
		defer func() { _ = d.run(context.WithoutCancel(ctx), io.Discard, io.Discard, "image", "rm", "-f", base) }()
		if err := cheapBuild(ctx, d, opts, base); err != nil {
			return err
		}
		if err := boot(ctx, d, opts, base); err != nil {
			return err
		}
		return checkResult(ctx, d, opts, opts.Tag)
	}

	if err := cheapInto(ctx, d, opts, opts.Tag); err != nil {
		return err
	}
	reportGuestSide(opts, side)
	return nil
}

// Cheap is the cheap form on its own: From's guest half over
// GranuleImage's VMM half, under Tag, and nothing else.
//
// It exists for a caller that assembles the two halves for a purpose of
// its own rather than to publish what comes out. `granule guest
// verify` (pkg/guestverify) is that caller: it boots an image under
// the VMM half that will actually run it, and what it boots is this
// build under a throwaway tag it deletes again once the verdict is in.
// Sharing the build rather than generating a second one of its own is
// what keeps the machine verify judges and the machine a rebase produces
// the same machine.
//
// Everything a cheap `Rebase` checks is checked here -- that From is a
// guest image at all, that GranuleImage is a granule image, that a bundled
// guest kernel in the base is not left beside a stranger's disk, that
// the guest half declares a contract this granule can still drive, and
// that what came out is bootable. The contract refusal in particular
// belongs on this side of the seam rather than in `Rebase`: a caller
// that boots what this builds is the caller a guest too old to answer
// hurts most, since without it `guest verify` spends a boot and a
// timeout discovering by silence what the label says outright.
//
// What is left out is the closing report about the guest half a cheap
// rebase did not refresh: that is advice for a human publishing an
// artifact, not for a caller about to throw this one away.
func Cheap(ctx context.Context, opts Options) error {
	if opts.Rootfs != "" || opts.Boot {
		return fmt.Errorf("Cheap builds the VMM-half-only rebase; the complete forms (-rootfs, -boot) are Rebase's, and they produce an image somebody keeps")
	}
	opts, err := opts.resolve()
	if err != nil {
		return err
	}
	d := &dockerCLI{binary: opts.DockerBinary}
	if err := readGuestSide(ctx, d, opts).refuse(opts); err != nil {
		return err
	}
	return cheapInto(ctx, d, opts, opts.Tag)
}

// cheapInto is the cheap rebase as a whole: build it, then check that
// what came out is a guest image that can boot.
func cheapInto(ctx context.Context, d *dockerCLI, opts Options, tag string) error {
	if err := cheapBuild(ctx, d, opts, tag); err != nil {
		return err
	}
	return checkResult(ctx, d, opts, tag)
}

// cheapBuild builds the VMM-half-only rebase and tags it.
func cheapBuild(ctx context.Context, d *dockerCLI, opts Options, tag string) error {
	dockerfile, err := dockerfileFor(ctx, d, opts)
	if err != nil {
		return err
	}

	// The build context holds the generated Dockerfile and nothing else:
	// every input is an image reference, so no part of the host's
	// filesystem is sent to the daemon.
	dir, err := os.MkdirTemp("", "granule-guest-rebase-")
	if err != nil {
		return fmt.Errorf("making a build context: %w", err)
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(file, []byte(dockerfile), 0o644); err != nil {
		return fmt.Errorf("writing the generated Dockerfile: %w", err)
	}

	fmt.Fprintf(opts.Stdout, "rebasing %s onto %s\n", opts.From, opts.KonturImage)
	args := append([]string{"build", "--file", file, "--tag", tag}, opts.ExtraBuildArgs...)
	args = append(args, dir)
	if err := d.run(ctx, opts.Stdout, opts.Stderr, args...); err != nil {
		return rebaseError(err, opts)
	}
	fmt.Fprintf(opts.Stdout, "rebased %s\n", tag)
	return nil
}

// repack is the complete form where the rootfs image is still held: a
// pack, with the current agents, onto the current VMM half.
func repack(ctx context.Context, d *dockerCLI, opts Options) error {
	fmt.Fprintf(opts.Stdout, "rebasing %s by repacking %s onto %s: the guest half is rebuilt from that rootfs with the current agents in it, so %s's own disk is replaced rather than carried over\n",
		opts.From, opts.Rootfs, opts.KonturImage, opts.From)
	if err := guestpack.Pack(ctx, guestpack.Options{
		From:           opts.Rootfs,
		Tag:            opts.Tag,
		KonturImage:    opts.KonturImage,
		PackerImage:    opts.PackerImage,
		Variant:        opts.Variant,
		DiskExtraMB:    opts.DiskExtraMB,
		DockerBinary:   opts.DockerBinary,
		ExtraBuildArgs: opts.ExtraBuildArgs,
		Stdout:         opts.Stdout,
		Stderr:         opts.Stderr,
	}); err != nil {
		return err
	}
	return checkResult(ctx, d, opts, opts.Tag)
}

// boot is the complete form for a guest whose rootfs image nobody has
// any more: run it, lay granule's own guest-side parts over the running
// filesystem, and let pkg/guestbuild scrub, power off and commit.
//
// The laying down is pack-guest's own -lay-only mode -- the same script,
// over the same overlay trees, with the same "this rootfs enables none
// of our units" refusal -- so the guest half a rebase produces and the
// guest half a pack produces come out of one implementation rather than
// two that have to be kept saying the same thing.
func boot(ctx context.Context, d *dockerCLI, opts Options, base string) error {
	dir, err := os.MkdirTemp("", "granule-guest-parts-")
	if err != nil {
		return fmt.Errorf("making a staging directory: %w", err)
	}
	defer os.RemoveAll(dir)

	// Out of the granule image, with docker create + docker cp: the
	// parts have to reach the guest as a stream, and this takes them
	// from an image without starting a container from it.
	if err := d.copyOut(ctx, opts.KonturImage, guestimage.PartsDir+"/.", dir); err != nil {
		// The stock build first, and the lean one with the condition on
		// it: `--target runtime` on its own is what this used to say, and
		// a caller who took it for a guest that brings no kernel would be
		// refused again by dockerfileFor for a second reason.
		return fmt.Errorf("%w\n\nthe granule image %s has no %s, so it cannot supply the guest-side pieces a rebase lays down. An image built before `guest pack` existed does not carry them: rebuild it from a granule that does -- a stock `docker build .`, which carries the %s a guest bringing no kernel of its own needs as well, or `docker build --target runtime` for a guest that brings one", err, opts.KonturImage, guestimage.PartsDir, guestimage.RuntimeKernel)
	}
	parts, err := tarDir(dir)
	if err != nil {
		return fmt.Errorf("packing the guest-side parts for the guest: %w", err)
	}

	return guestbuild.Build(ctx, guestbuild.Options{
		From: base,
		Tag:  opts.Tag,
		Stage: []guestbuild.StagedTree{{
			Description: "granule's own guest-side parts",
			Tar:         parts,
			Dest:        stageDir,
		}},
		Setup: setupScript(opts.Variant),
		// The image this boots is the intermediate below, which is
		// deleted when the rebase returns; what the result was rebased
		// onto is the granule image, and that is what its provenance
		// should say.
		BaseLabel: opts.KonturImage,
		// The guest side has just been replaced with this granule's own,
		// so the result implements this granule's contract -- whatever
		// the image being rebased declared, and whatever the
		// intermediate carried that forward as.
		Labels: []string{guestimage.ContractLabel + "=" + guestimage.ContractValue()},
		// And the same number beside the disk, which is the copy a boot
		// reads. pack-guest wrote it inside the guest just now; the one
		// in the container came from the image being rebased, and a
		// commit would carry that stale version forward to describe a
		// guest side it no longer matches -- including, for a rebase
		// onto an *older* granule, a version that would then refuse the
		// very image this rebase produced.
		Place:           map[string][]byte{guestimage.ContractSidecar: []byte(guestimage.ContractValue() + "\n")},
		DockerBinary:    opts.DockerBinary,
		ExtraRunArgs:    opts.ExtraRunArgs,
		ReadyTimeout:    opts.ReadyTimeout,
		ShutdownTimeout: opts.ShutdownTimeout,
		KeepOnFailure:   opts.KeepOnFailure,
		Stdout:          opts.Stdout,
		Stderr:          opts.Stderr,
	})
}

// setupScript is what runs inside the booted guest: one instruction, and
// the cleanup that keeps the staged copy out of the committed image.
//
// Everything that decides what a refresh means -- which overlay, which
// files, what to say about replacing one the consumer edited, and
// whether the guest enables the units being laid down -- is pack-guest's,
// so there is nothing here to drift from what a pack does.
func setupScript(variant string) string {
	if variant == "" {
		variant = "auto"
	}
	return fmt.Sprintf(`#!/bin/sh
set -eu
GRANULE_GUEST_VARIANT=%s sh %s/pack-guest -lay-only / %s
rm -rf %s
`, variant, stageDir, stageDir, stageDir)
}

// Dockerfile is the build a cheap rebase runs, as text.
//
// Exported, and printed by `granule guest rebase -print-dockerfile`,
// because its security argument is something a reader should be able to
// check for themselves: the image being rebased appears only as the
// source of a COPY, and there is no RUN in the file at all.
//
// It takes a context because which files the build copies depends on
// what the image being rebased actually carries -- the guest kernel pair
// is present in some guests and absent in most -- and the only way to
// know is to ask docker. Asking starts no container: see imageHas.
func Dockerfile(ctx context.Context, opts Options) (string, error) {
	opts, err := opts.resolve()
	if err != nil {
		return "", err
	}
	return dockerfileFor(ctx, &dockerCLI{binary: opts.DockerBinary}, opts)
}

func dockerfileFor(ctx context.Context, d *dockerCLI, opts Options) (string, error) {
	guest, err := d.guestHalf(ctx, opts.From)
	if err != nil {
		// Both reads here are `docker create` (see dockercli.Files), so
		// they are what a reference that will not resolve stops, before
		// any Dockerfile is generated -- and what docker says then is
		// its registry wording rather than the build log's. Said plainly
		// here, because "error from registry: denied" on its own reads
		// as a fault of the image rather than of the reference to it.
		if dockercli.PullCouldNotResolve(err.Error()) {
			return "", fmt.Errorf("looking at %s's guest half: %w\n\nthe guest image %s could not be resolved. That is a registry, a credential or a network answer about the reference itself: if it is a moving tag, something may have removed or republished it; otherwise check the reference, and whether this machine is logged in to the registry holding it (`docker login`)", opts.From, err, opts.From)
		}
		return "", fmt.Errorf("looking at %s's guest half: %w", opts.From, err)
	}
	if err := guestimage.CheckPresent(guest.has, named(opts.From)); err != nil {
		return "", fmt.Errorf("%s is not a guest image a rebase can carry forward: %w", opts.From, err)
	}
	base, err := d.guestHalf(ctx, opts.KonturImage)
	if err != nil {
		if dockercli.PullCouldNotResolve(err.Error()) {
			return "", fmt.Errorf("looking at %s: %w\n\n%s", opts.KonturImage, err, guestpack.UnresolvedKontur(opts.KonturImage))
		}
		return "", fmt.Errorf("looking at %s: %w", opts.KonturImage, err)
	}
	contract := declaredContract(ctx, d, opts.From)
	if !base.granule {
		return "", fmt.Errorf("%s has no %s in it, so it is not a granule image to rebase onto", opts.KonturImage, granuleBinary)
	}

	// A kernel pair in the base and none in the guest is the one
	// combination this build cannot express its way out of: there is no
	// RUN to delete with, so the base's vmlinuz and initrd.img would
	// stay beside somebody else's disk and pkg/config would boot
	// that guest on a stranger's kernel -- modules and all -- which
	// surfaces as a VM that boots and never answers rather than as an
	// error. Refused here, where it can still be said plainly.
	if base.kernel && !guest.kernel {
		return "", fmt.Errorf("%s carries a guest kernel of its own (%s and %s) and %s does not, so a rebase onto it would leave that pair beside %s's disk and boot the guest on a kernel built for a different rootfs. Rebase onto a granule image that brings no guest kernel instead: a stock `docker build .`, which carries only the VMM half's %s, or a `docker build --target runtime` build if %s brings a kernel of its own",
			opts.KonturImage, guestimage.GuestKernel, guestimage.GuestInitramfs, opts.From, opts.From, guestimage.RuntimeKernel, opts.From)
	}

	// The other way for a rebase to produce an image with nothing to
	// boot: a granule image that brings no kernel either. The `runtime`
	// target is deliberately one of those -- it is the VMM half for a
	// deployment that supplies its own kernel with its own guest -- so
	// following the advice to rebase onto one, with an ordinary guest
	// that brings no kernel, yields an image whose boot stops at
	// `CHV_KERNEL (<vmlinux>): no such file or directory` on whichever
	// node happens to run it. Refused here, where it costs a build
	// rather than an outage.
	if !base.runtimeKernel && !guest.kernel {
		return "", fmt.Errorf("neither %s nor %s carries a kernel, so the result would have nothing to boot: %s has no %s (a `docker build --target runtime` build deliberately carries none -- it is for guests that bring their own) and %s brings no %s and %s of its own. Rebase onto a granule image that carries the runtime kernel -- a stock `docker build .` -- or rebase a guest built with GUEST_KERNEL_PACKAGE, which brings its own pair",
			opts.KonturImage, opts.From, opts.KonturImage, guestimage.RuntimeKernel, opts.From, guestimage.GuestKernel, guestimage.GuestInitramfs)
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w(`# Generated by "granule guest rebase" -- see pkg/guestrebase.`)
	w(`#`)
	w(`# The VMM half is the granule image's: this is FROM it, so the`)
	w(`# binaries, the shims, the entrypoint and the environment the`)
	w(`# result runs with are the operator's rather than whatever the`)
	w(`# image being rebased carried. What comes across from that image`)
	w(`# is its guest half and nothing else.`)
	w(`#`)
	w(`# There is no RUN in this file. The image being rebased appears`)
	w(`# only as the source of a COPY, so no entrypoint, hook or postinst`)
	w(`# of its own is ever run -- which is what makes rebasing an image`)
	w(`# somebody else authored safe to do on the machine that runs the`)
	w(`# daemon, and what keeps a rebase down to seconds.`)
	w(``)
	w(`FROM %s`, opts.KonturImage)
	w(``)
	w(`# The guest half, as pkg/guestimage's Files() lists it. The`)
	w(`# runtime kernel (%s) is`, guestimage.RuntimeKernel)
	w(`# deliberately not in that list: it sits in this directory but`)
	w(`# belongs to the VMM half, so the one that boots this guest is the`)
	w(`# FROM's own rather than the one the image being rebased pinned.`)
	for _, f := range guestimage.Files() {
		if !guest.has(f.Path) {
			continue
		}
		w(`COPY --from=%s %s %s`, opts.From, f.Path, f.Path)
	}
	w(``)
	// The contract label describes the guest inside the image, and this
	// build does not touch that guest -- so the value is the one the
	// image being rebased declared, restated rather than left to the
	// FROM. Inheriting the granule image's own would be the image
	// claiming, on the strength of binaries outside the disk, that the
	// agents inside it are current: exactly the mistake the label exists
	// to prevent. An image that declared nothing goes on declaring
	// nothing, which is an empty value rather than an absent label.
	w(`# The last label is the guest half's own, carried across unchanged:`)
	w(`# this build replaces the VMM half and leaves the disk alone, so`)
	w(`# whatever contract that guest was built against is still the`)
	w(`# contract it implements. See pkg/guestimage.`)
	w(`LABEL org.opencontainers.image.base.name="%s" \`, opts.KonturImage)
	w(`      %s="%s" \`, RebasedFromLabel, opts.From)
	w(`      %s="%s"`, guestimage.ContractLabel, contract)

	// What the guest declares it wants reachable travels with the guest.
	// A rebase is FROM the granule image, so nothing of the rebased
	// image's own config comes across on its own -- and an EXPOSE its
	// author wrote is a statement about the machine inside, not about
	// the VMM half being replaced. Dropping it would undo silently what
	// `guest build` and `guest pack` carried this far.
	//
	// Declared, not granted, exactly as it is everywhere else here: this
	// opens no port in the guest's shield and none in the container.
	// guestpack.DeclaredPorts validates every value, which matters
	// because they come out of an image somebody else authored and are
	// written into this file.
	if ports := guestpack.DeclaredPorts(ctx, opts.DockerBinary, opts.From, opts.Stdout); len(ports) > 0 {
		sort.Strings(ports)
		w(``)
		w(`# What %s declares it wants reachable, carried across with its`, opts.From)
		w(`# guest. Declared, not granted: this opens nothing, here or under`)
		w(`# docker, and a granule guest stays shielded until something else`)
		w(`# says otherwise -- "granule vm create -publish-declared", which`)
		w(`# publishes these on the VM's own container, or "docker run -P".`)
		w(`EXPOSE %s`, strings.Join(ports, " "))
	}
	if volumes := guestpack.DeclaredVolumes(ctx, opts.DockerBinary, opts.From, opts.Stdout); len(volumes) > 0 {
		w(``)
		w(`# Volume mount points declared by %s, carried across with its guest.`, opts.From)
		w(`LABEL %s=%q`, guestimage.VolumesLabel, guestimage.EncodeVolumes(volumes))
		w(`VOLUME %s`, guestimage.EncodeVolumes(volumes))
	}
	if env, err := (&dockercli.CLI{Binary: opts.DockerBinary}).ConfigEnv(ctx, opts.From); err == nil {
		var chvEnvs []string
		for _, entry := range env {
			k, v, ok := strings.Cut(entry, "=")
			if ok && strings.HasPrefix(k, "CHV_") {
				chvEnvs = append(chvEnvs, fmt.Sprintf("%s=%q", k, v))
			}
		}
		if len(chvEnvs) > 0 {
			sort.Strings(chvEnvs)
			w(``)
			w(`# Machine settings declared by %s:`, opts.From)
			for _, e := range chvEnvs {
				w(`ENV %s`, e)
			}
		}
	}
	return b.String(), nil
}

// declaredContract is the contract version an image declares, as a value
// that is safe to write into a generated Dockerfile.
//
// Safe because it is re-rendered from the parsed number rather than
// passed through: the label comes off an image somebody else built, it
// reaches docker by being interpolated into `LABEL x="..."`, and a value
// carrying a quote or a newline is a value that can append instructions
// to a file whose whole argument is that it has none (the same reasoning
// as refPattern, one layer in). Anything that is not a version reads as
// no declaration at all, which is what the report then says out loud.
//
// Best effort in the other direction too: a docker that cannot answer
// leaves the result declaring nothing, which is the honest answer when
// the question could not be asked.
func declaredContract(ctx context.Context, d *dockerCLI, ref string) string {
	value, err := d.label(ctx, ref, guestimage.ContractLabel)
	if err != nil {
		return ""
	}
	if _, version := guestimage.ReadContract(value); version > 0 {
		return strconv.Itoa(version)
	}
	return ""
}

// checkResult runs guestimage's own check over the image that was
// produced, so a rebase that emitted an image with no disk in it fails
// naming that rather than being discovered by a boot into nothing.
//
// It asks docker what is in the image rather than unpacking it: the
// answer is three stat-sized questions, and the disk they are about is
// measured in gigabytes.
func checkResult(ctx context.Context, d *dockerCLI, opts Options, tag string) error {
	guest, err := d.guestHalf(ctx, tag)
	if err != nil {
		return fmt.Errorf("checking the guest half of %s: %w", tag, err)
	}
	if err := guestimage.CheckPresent(guest.has, named(tag)); err != nil {
		return fmt.Errorf("%s was built but is not a bootable guest image: %w", tag, err)
	}
	return nil
}

// guestSide is what the image being rebased says about the half a cheap
// rebase does not touch: which guest-side contract its guest was built
// against, and the rootfs image it was packed from, if it recorded one.
//
// Both come off labels, which is what makes them affordable here: the
// cheap form's whole argument is that it never opens disk.img, and a
// `docker image inspect` is the only question that can be asked about a
// guest without unpacking a multi-gigabyte disk onto the host.
type guestSide struct {
	contract string
	state    guestimage.ContractState
	rootfs   string
}

// contractState is guestimage.ReadContract, reached through a variable
// so that a test can put a guest below the floor in front of a rebase.
// The floor sits at the first version anything has ever declared, so no
// real image can be too old for this build yet -- and a refusal that is
// first exercised on the day it has to work is a refusal nobody has
// tested.
var contractState = guestimage.ReadContract

// readGuestSide asks those two questions. Best effort on purpose: a
// docker that cannot answer them leaves a rebase reporting less, not
// failing -- the answers decide what is said and whether a cheap rebase
// is refused, and an unanswerable question is the same position granule
// was in before any image declared anything.
func readGuestSide(ctx context.Context, d *dockerCLI, opts Options) guestSide {
	var side guestSide
	if value, err := d.label(ctx, opts.From, guestimage.ContractLabel); err == nil {
		side.contract = value
	}
	side.state, _ = contractState(side.contract)
	if value, err := d.label(ctx, opts.From, PackedFromLabel); err == nil {
		side.rootfs = value
	}
	return side
}

// refuse is the backstop a cheap rebase gets from a declared contract
// version: a guest whose granule-side pieces predate what this granule
// requires is one this cannot simply leave alone, and saying so here --
// before anything is built -- is the difference between a named refusal
// and a VM that boots, looks healthy and never answers.
//
// Only ContractTooOld refuses. An image that declares nothing is every
// image published before granule started writing a version down, and
// making that a refusal would mean the first thing this feature did was
// break every existing guest; an image that declares more than this
// granule writes is skewed the other way, which no flag here can fix.
// Both are reported instead -- see reportGuestSide.
func (s guestSide) refuse(opts Options) error {
	if s.state != guestimage.ContractTooOld {
		return nil
	}
	if opts.Force {
		fmt.Fprintf(opts.Stderr, "warning: %s %s, and -force was given: the result will carry a guest this granule may not be able to drive\n", opts.From, guestimage.DescribeContract(s.contract))
		return nil
	}
	return fmt.Errorf("%s %s\n\nA cheap rebase replaces the VMM half and leaves that guest alone, so the result would carry agents, units and helpers this granule can no longer drive -- a VM that boots, looks healthy and never answers. Refresh the guest side instead:\n\n  %s\n  -boot            lays the current guest side over the running guest (needs /dev/kvm)\n  -force           rebases anyway, accepting a guest this granule may not be able to drive",
		opts.From, guestimage.DescribeContract(s.contract), s.repackRemedy())
}

// repackRemedy is the -rootfs line of that refusal, naming the image
// where the pack recorded one: the rootfs a repack needs is the one
// thing an image can say for itself, and a caller who has to go and find
// it is a caller who takes the -force.
func (s guestSide) repackRemedy() string {
	if s.rootfs != "" {
		return fmt.Sprintf("-rootfs %s  repacks the rootfs this image records being packed from, with the current guest side in it", s.rootfs)
	}
	return "-rootfs <image>  repacks the rootfs it was packed from, with the current guest side in it"
}

// reportGuestSide is what a cheap rebase says about the half it did not
// touch.
//
// The guest-side agents and the overlay inside disk.img are still
// whoever built that disk's, and a guest packed months ago under an
// older granule may not satisfy today's guest-side contract. Overwriting
// them is the mitigation and it is what the complete forms do, so this
// says which one is available: the rootfs image the pack recorded, if
// there is one, and a boot otherwise.
//
// What is printed depends on what the image declared, because that is
// what turns this from advice into a verdict. A guest declaring a
// contract this granule supports gets one line saying so and nothing
// else: the paragraph below is a warning, and warning about a guest that
// has just been checked and found current is how a caller learns to skip
// the output. A guest declaring nothing, or declaring something that is
// not a version, gets that paragraph, since nothing here can tell its
// age -- with a different sentence for each, because "nobody had
// stamped one yet" and "somebody stamped this and it is not a number"
// call for different things from whoever reads it. A guest too old for
// this granule never reaches here at all unless -force was given -- see
// refuse above.
func reportGuestSide(opts Options, side guestSide) {
	switch side.state {
	case guestimage.ContractSupported:
		fmt.Fprintf(opts.Stdout, "%s %s: %s was left alone because it is current enough to leave alone, not for want of a way to check\n",
			opts.From, guestimage.DescribeContract(side.contract), guestimage.Dir)
		return
	case guestimage.ContractAhead:
		fmt.Fprintf(opts.Stdout, "%s %s. Nothing here can fix that: the guest is newer than the VMM half it has just been put on, so rebase onto a granule at least as new as the one that packed it\n",
			opts.From, guestimage.DescribeContract(side.contract))
		return
	case guestimage.ContractTooOld:
		fmt.Fprintf(opts.Stdout, "%s %s, and was rebased anyway on -force: %s still holds the guest side it was built with\n",
			opts.From, guestimage.DescribeContract(side.contract), guestimage.Dir)
		return
	}

	fmt.Fprintf(opts.Stdout, "%s is untouched: the agents and the overlay inside that disk are still the ones %s was built with\n", guestimage.Dir, opts.From)
	if side.state == guestimage.ContractUnreadable {
		// Not the sentence below: this image did stamp something, so
		// "published before granule wrote one down" would be telling the
		// reader the opposite of what happened. Whatever is on it was
		// put there by hand or by tooling that got the label wrong, and
		// the useful thing to say is that a complete rebase replaces it
		// with one granule wrote.
		fmt.Fprintf(opts.Stdout, "%s %s -- so it is treated as declaring nothing, which is a report rather than a refusal, and a complete rebase both refreshes that guest side and stamps a version in place of it\n",
			opts.From, guestimage.DescribeContract(side.contract))
	} else {
		fmt.Fprintf(opts.Stdout, "%s %s, which is what every image published before granule wrote one down does -- so this is a report rather than a refusal, and a complete rebase both refreshes that guest side and stamps the version it now implements\n",
			opts.From, guestimage.DescribeContract(side.contract))
	}
	if side.rootfs != "" {
		fmt.Fprintf(opts.Stdout, "%s records that it was packed from %s -- `-rootfs %s` repacks that rootfs with the current agents in it, which is minutes and a new disk rather than seconds\n", opts.From, side.rootfs, side.rootfs)
		return
	}
	fmt.Fprintf(opts.Stdout, "pass -rootfs <image> to repack the rootfs it was built from with the current agents in it, or -boot to refresh them by booting the guest (needs /dev/kvm)\n")
}

// resolve fills in the defaults and rejects what cannot be built, before
// anything is written or any image is pulled.
func (opts Options) resolve() (Options, error) {
	if opts.From == "" || opts.Tag == "" {
		return opts, fmt.Errorf("an image to rebase and an output tag are both required")
	}
	if opts.Rootfs != "" && opts.Boot {
		return opts, fmt.Errorf("-rootfs and -boot are two ways of doing the same refresh and only one of them can happen: -rootfs repacks the rootfs you still have, which runs nothing of the guest's, and -boot is for when nobody has it any more")
	}
	if opts.KonturImage == "" {
		opts.KonturImage = vmspec.DockerImage
	}
	switch opts.Variant {
	case "", "auto", "debian", "alpine":
	default:
		return opts, fmt.Errorf("unknown guest variant %q: expected debian, alpine, or empty to detect which init system the guest runs", opts.Variant)
	}
	if opts.Variant != "" && opts.Rootfs == "" && !opts.Boot {
		return opts, fmt.Errorf("-variant picks the overlay a complete rebase lays down, and a rebase with neither -rootfs nor -boot lays nothing down: it replaces the VMM half and leaves the disk alone")
	}
	for _, ref := range []struct{ what, value string }{
		{"the image to rebase", opts.From},
		{"the output tag", opts.Tag},
		{"the granule image", opts.KonturImage},
		{"the rootfs image", opts.Rootfs},
	} {
		if ref.value == "" {
			continue
		}
		if err := validateRef(ref.value); err != nil {
			return opts, fmt.Errorf("%s: %w", ref.what, err)
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

// refPattern is what an image reference may contain to be interpolated
// into the generated Dockerfile. The same rule, for the same reason, as
// pkg/guestpack's: a reference reaches docker by being written into
// a Dockerfile, so one carrying a newline is one that can append
// instructions to a file whose whole point is that it has none.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)

func validateRef(ref string) error {
	if !refPattern.MatchString(ref) {
		return fmt.Errorf("%q is not a usable image reference", ref)
	}
	return nil
}

// bootBaseTag names the intermediate image the boot form boots: the
// cheap rebase, which is what gets provisioned and committed over.
// Removed when the rebase returns, whichever way it went.
func bootBaseTag(tag string) string {
	base, _, _ := strings.Cut(tag, "@")
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		base = base[:i]
	}
	return base + ":granule-rebase-base"
}

// rebaseError turns a failed build into an error that names the
// assumption that was not met, where the build's own message would not.
func rebaseError(err error, opts Options) error {
	text := err.Error()
	// Asked of one log line at a time -- see
	// dockercli.BuildCouldNotFind. This build is a COPY of every file of
	// the guest half, so buildkit has that directory in the log tail it
	// prints for any failure at all; "not found" anywhere in the text
	// and the directory anywhere else would explain a registry answer
	// about the granule image as the image being rebased having been
	// republished underneath us.
	if dockercli.BuildCouldNotFind(text, guestimage.Dir) {
		return fmt.Errorf("%w\n\n%s no longer carries the guest half it was checked for a moment ago -- if it is a moving tag, something republished it mid-rebase", err, opts.From)
	}
	// Both references this build pulls are read before it is generated
	// (see dockerfileFor), so a reference that will not resolve never
	// reaches the build: that explanation lives up there, where docker
	// actually stops.
	return err
}

// named renders a guest-half path the way an error about one image
// should name it: the image, then the path inside it.
func named(ref string) func(string) string {
	return func(p string) string { return ref + ":" + p }
}

const granuleBinary = "/usr/local/bin/granule-shim"

// half is what an image carries of the guest half, and whether it is a
// granule image at all.
type half struct {
	disk      bool
	kernel    bool
	initramfs bool
	granule   bool

	// runtimeKernel is guestimage.RuntimeKernel, which sits in the same
	// directory as the three above but belongs to the VMM half: it is
	// what boots a guest that brings no kernel of its own, and a
	// `docker build --target runtime` granule image deliberately has
	// none. Only asked of the image being rebased *onto*.
	runtimeKernel bool
}

func (h half) has(path string) bool {
	switch path {
	case guestimage.DiskImage:
		return h.disk
	case guestimage.GuestKernel:
		return h.kernel
	case guestimage.GuestInitramfs:
		return h.initramfs
	}
	return false
}

type dockerCLI struct{ binary string }

func (d *dockerCLI) bin() string {
	if d.binary == "" {
		return dockercli.Binary()
	}
	return d.binary
}

// guestHalf reports what an image carries of the guest half, without
// starting a container from it -- dockercli.Files is `docker create`
// (which runs nothing) plus `docker cp` (which reads out of a container
// that has never been started).
//
// One container for the five questions, removed on the way out.
func (d *dockerCLI) guestHalf(ctx context.Context, ref string) (half, error) {
	files, err := d.files(ctx, ref)
	if err != nil {
		return half{}, err
	}
	defer files.Close()

	var h half
	for _, q := range []struct {
		path string
		into *bool
	}{
		{guestimage.DiskImage, &h.disk},
		{guestimage.GuestKernel, &h.kernel},
		{guestimage.GuestInitramfs, &h.initramfs},
		{guestimage.RuntimeKernel, &h.runtimeKernel},
		{granuleBinary, &h.granule},
	} {
		present, err := files.Has(ctx, q.path)
		if err != nil {
			return half{}, err
		}
		*q.into = present
	}
	return h, nil
}

// files opens an image for reading, without starting anything from it.
func (d *dockerCLI) files(ctx context.Context, ref string) (*dockercli.ImageFiles, error) {
	return (&dockercli.CLI{Binary: d.binary}).Files(ctx, ref)
}

// copyOut copies a path out of an image into a directory on this
// machine, again without starting anything from it.
func (d *dockerCLI) copyOut(ctx context.Context, ref, from, dest string) error {
	files, err := d.files(ctx, ref)
	if err != nil {
		return err
	}
	defer files.Close()
	return files.CopyOut(ctx, from, dest)
}

// label reads one label off an image, or "" when it has none. Best
// effort: it decides what a report says, not what a rebase does.
func (d *dockerCLI) label(ctx context.Context, ref, name string) (string, error) {
	var out bytes.Buffer
	if err := d.run(ctx, &out, io.Discard, "image", "inspect", "-f", "{{index .Config.Labels "+quote(name)+"}}", ref); err != nil {
		return "", err
	}
	value := strings.TrimSpace(out.String())
	if value == "<no value>" || value == "<nil>" {
		return "", nil
	}
	return value, nil
}

func quote(s string) string { return `"` + s + `"` }

// run execs "docker <args...>", streaming both streams to the caller's
// writers and keeping the tail of stderr for the error message.
func (d *dockerCLI) run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var errTail bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, &errTail)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, lastLines(errTail.String(), 20))
	}
	return nil
}

// tarDir packs a directory tree into a tar archive in memory, which is
// how the guest-side parts reach the guest: `granule cp -tar` unpacks one
// over the same vsock session everything else in a build uses.
//
// In memory because the parts are a few megabytes of static binaries and
// a few dozen small files, and because the stream has to be replayable
// by the caller that hands it over rather than read from a directory
// that has since been cleaned up.
func tarDir(dir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		// Symlinks and device nodes have no business in a parts
		// directory, and a tar member that is neither a file nor a
		// directory is something this did not expect to be copying.
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("%s is neither a file nor a directory", p)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// lastLines keeps the end of a log, which is where the failure is.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
