package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bwsalmon/granule/pkg/guestbuild"
	"github.com/bwsalmon/granule/pkg/guestdockerfile"
	"github.com/bwsalmon/granule/pkg/guestpack"
	"github.com/bwsalmon/granule/pkg/guestrebase"
	"github.com/bwsalmon/granule/pkg/qcow2"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// repeatedFlag collects a flag given more than once, in order.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return fmt.Sprint(*r) }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// runGuest dispatches "granule guest <subcommand>".
func runGuest(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("guest: expected a subcommand (build, pack, rebase, verify, flatten)")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "build":
		return runGuestBuild(ctx, rest, stdout, stderr)
	case "pack":
		return runGuestPack(ctx, rest, stdout, stderr)
	case "rebase":
		return runGuestRebase(ctx, rest, stdout, stderr)
	case "verify":
		return runGuestVerify(ctx, rest, stdout, stderr)
	case "flatten":
		return runGuestFlatten(ctx, rest, stdout, stderr)
	default:
		return fmt.Errorf("guest: unknown subcommand %q", cmd)
	}
}

// guestBuildUsage says which of the two routes a bare `guest build` is,
// because that is the one thing a caller cannot read off the flag list.
//
// A bare `granule guest build` is the Dockerfile route, and -setup is
// the explicit opt-in to the booting one. The two overlap for most of
// what people actually do -- install packages, drop files in, enable
// units -- and where they overlap the Dockerfile route is strictly the
// cheaper machine to be on: no /dev/kvm, no VM to wait for, docker's
// layer cache across rebuilds, and it runs on any machine and any CI
// runner that has docker. Booting costs a KVM host, a boot and a
// shutdown per build, and it is the only thing that can start a service,
// run a container or open /dev/kvm during provisioning. So the default
// is the one that works everywhere, and the one with the requirements
// is the one you ask for by name.
//
// Both routes end at the same kind of thing, which is what makes them
// alternatives rather than two commands sharing a word: -t is a bootable
// granule image either way. The Dockerfile route gets there in two steps,
// because a docker build produces a rootfs image and a rootfs image is
// what guestpack takes -- so the command builds one and packs it, and
// the rootfs keeps a name of its own (-rootfs-tag) because that, not the
// packed disk, is what a consumer publishes for somebody else to pack.
const guestBuildUsage = `usage: granule guest build [-f Dockerfile] -t <tag> [flags] [context]
       granule guest build -setup <script> -from <image> -t <tag> [flags]

Two routes to a customized guest, and the first is what a bare command
means:

  Dockerfile (the default). Validates the instruction list, supplies the
  base where the file names none (-from, default granule's own guest
  rootfs image), runs an ordinary docker build, and packs what comes out
  into a bootable granule image (the same packing as "granule guest
  pack"). No /dev/kvm, no VM, docker's own layer cache. It runs on the
  build host's kernel with no init, so it cannot start a service, run a
  container or open /dev/kvm -- see deploy/guest-image/README.md for the
  whole list, and for the failure that does not announce itself.

  A file may name its own FROM, and a pinned base is the reason to: the
  packing lays granule's own guest half (the agents, the control-net
  overlay, the units that start them) over whatever the base carried, so
  the guest that boots has the current one however old the base is. A
  file whose only instruction is a FROM of a guest *rootfs* image is
  the same thing "granule guest rebase -rootfs" does: that image's
  payload, repacked onto today's granule. (An already-packed image is
  the other case, and "guest rebase" without -rootfs is the verb for
  it.)

  -setup. Boots the guest for real and runs a script inside it over
  "granule exec", then commits the machine. Needs /dev/kvm and a granule
  image to boot, and is the only route that can do any of the above.

Either way -t names a bootable granule image. The rootfs image the
Dockerfile route packs is kept under its own name as well -- that is what
you publish for somebody else to pack, and -rootfs-only stops there.

Flags:
`

// runGuestBuild builds a guest from a consumer's Dockerfile, or -- with
// -setup -- boots a guest image, provisions it from inside and commits
// the result. See guestBuildUsage for which is which and why the
// Dockerfile route is the default.
func runGuestBuild(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule guest build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, guestBuildUsage)
		fs.PrintDefaults()
	}

	tag := fs.String("t", "", "image reference to give the result: a bootable granule image on either route (required)")
	from := fs.String("from", "", "base image: the guest rootfs a Dockerfile build is based on when the file names no FROM of its own (default "+guestdockerfile.DefaultBase+"), or the granule image a -setup build boots (required there)")

	// The Dockerfile route.
	file := fs.String("f", "", "Dockerfile to build the guest rootfs from (default \"Dockerfile\" in the build context). It may name its own FROM -- granule's own guest half is re-laid over whatever it names at pack time -- and the instructions it may use are a subset; see deploy/guest-image/README.md")
	printOnly := fs.Bool("print-dockerfile", false, "validate and print the file docker would be given for the rootfs build -- granule's FROM plus your instructions -- and build nothing. \"granule guest pack -print-dockerfile\" prints the packing's own build")
	var buildArgs repeatedFlag
	fs.Var(&buildArgs, "build-arg", "value for an `ARG` the Dockerfile declares, as KEY=VALUE (repeatable)")
	var dockerBuildArgs repeatedFlag
	fs.Var(&dockerBuildArgs, "docker-build-arg", "extra `argument` for the underlying docker build (repeatable), given to the rootfs build and to the packing alike -- how a build reaches a private registry or a proxy, or asks for --no-cache")

	// The packing half of the Dockerfile route. What a docker build
	// produces is a rootfs image, and what boots is a packed one, so the
	// command does both -- see runGuestPack for what the packing is.
	rootfsTag := fs.String("rootfs-tag", "", "name to give the guest rootfs image the packing takes as input (default: -t's tag with \"-rootfs\" appended). It is an ordinary OCI image and the thing to publish: whoever boots it packs it themselves, and gets that day's agents in the guest rather than this build's")
	rootfsOnly := fs.Bool("rootfs-only", false, "stop at the rootfs image and pack nothing, with -t naming it. For publishing a rootfs for others to pack, or on a machine where the granule image to pack onto is not to hand")
	konturImage := fs.String("kontur-image", vmspec.DockerImage, "granule image supplying the VMM half the packed result is built on and the guest-side pieces laid over the rootfs (the agents, the overlay); see \"granule guest pack\"")
	packerImage := fs.String("packer-image", guestpack.DefaultPackerImage, "image the packing itself runs in; anything with /bin/sh and mke2fs will do")
	variant := fs.String("variant", "", "which overlay the packing lays down: \"debian\" (systemd) or \"alpine\" (OpenRC). The default asks the rootfs which init system it runs")
	diskMode := fs.String("disk-mode", "", "disk mode to bake into the image: \"overlay\" (qcow2 disk overlay), \"persistent\" (raw read-write flat file), or \"readonly\"")
	diskNumQueues := fs.Int("disk-num-queues", 0, "virtio-blk multi-queue count to bake into the image")
	diskIOThreads := fs.Bool("disk-iothreads", false, "dedicated host iothreads setting to bake into the image")
	diskIOEngine := fs.String("disk-io-engine", "", "host asynchronous I/O backend to bake into the image: \"io_uring\", \"epoll\", or \"sync\"")
	extraMB := fs.Int("disk-extra-mb", 0, "headroom in MiB on the packed disk, on top of the 20% it is sized with, for a guest that will be provisioned further after packing")

	// The booting route.
	setup := fs.String("setup", "", "path to a script to run as root inside a booted guest, instead of building from a Dockerfile. Needs /dev/kvm, and is the only route that can start a service or run a container while provisioning")
	snapshot := fs.Bool("snapshot", false, "-setup only: snapshot guest VM state (memory, device registers, writable disk overlay) upon completing provisioning, enabling instant resume on runtime boot")
	readyTimeout := fs.Duration("ready-timeout", 3*time.Minute, "-setup only: how long to wait for the guest to become reachable and finish booting before running the setup script")
	shutdownTimeout := fs.Duration("shutdown-timeout", 60*time.Second, "-setup only: how long to wait for the guest to power off before giving up")
	keep := fs.Bool("keep-on-failure", false, "-setup only: leave the container in place when the build fails, for inspection")
	var runArgs repeatedFlag
	fs.Var(&runArgs, "docker-run-arg", "-setup only: extra `argument` for the underlying docker run (repeatable) -- how a build reaches a proxy, a CA bundle or a private registry, none of which the guest inherits on its own")
	// Spelled and defaulted exactly as "vm create"'s own -dns, because
	// it is the same setting on the same kind of VM: the build VM is a
	// guest like any other, and the setup script running inside it is
	// the one part of a booting build that resolves names at all. A
	// build host whose network has its own resolver -- or none, or one
	// that is simply not 8.8.8.8 -- had no way to say so, and a setup
	// script that fetches a tarball is dead in the water without it.
	dns := fs.String("dns", vmspec.Defaults().DNS, "-setup only: nameserver(s) the build VM's guest resolves through, comma separated, IPv4 or IPv6, at most three and at most two IPv4; \"auto\" uses the build container's own resolver when the guest can reach it and 8.8.8.8 (2001:4860:4860::8888 for a v6-only guest) when it cannot; empty leaves the guest image's own /etc/resolv.conf alone")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tag == "" {
		fs.Usage()
		return fmt.Errorf("-t is required")
	}

	// A flag that belongs to the other route is an error rather than
	// something quietly ignored: a build told -no-cache through
	// -docker-build-arg and then given -setup would boot a guest and
	// honour none of it.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if err := checkRouteFlags(set); err != nil {
		return err
	}

	if *setup != "" {
		if *from == "" {
			fs.Usage()
			return fmt.Errorf("-setup needs -from: a booting build boots an existing granule image")
		}
		// Read the script here rather than in guestbuild: a path that
		// doesn't exist should fail before anything boots, not after a
		// VM has come up and the caller has waited for it.
		script, err := os.ReadFile(*setup)
		if err != nil {
			return fmt.Errorf("reading the setup script: %w", err)
		}
		return guestbuild.Build(ctx, guestbuild.Options{
			From:            *from,
			Setup:           string(script),
			Tag:             *tag,
			Snapshot:        *snapshot,
			DiskMode:        *diskMode,
			DiskNumQueues:   *diskNumQueues,
			DiskIOThreads:   *diskIOThreads,
			DiskIOEngine:    *diskIOEngine,
			ExtraRunArgs:    runArgs,
			DNS:             dns,
			ReadyTimeout:    *readyTimeout,
			ShutdownTimeout: *shutdownTimeout,
			KeepOnFailure:   *keep,
			Stdout:          stdout,
			Stderr:          stderr,
		})
	}

	// -t names what the command produces, and what it produces is a
	// bootable image; the rootfs is the intermediate and gets a name of
	// its own so it can be published and packed again later. -rootfs-only
	// makes the rootfs the product, so there -t names that instead.
	rootfs := *tag
	if !*rootfsOnly {
		rootfs = *rootfsTag
		if rootfs == "" {
			derived, err := rootfsTagFor(*tag)
			if err != nil {
				return err
			}
			rootfs = derived
		}
	}

	opts, err := dockerfileOptions(fs.Args(), *file, *from, rootfs, buildArgs, dockerBuildArgs, stdout, stderr)
	if err != nil {
		return err
	}
	if *printOnly {
		text, err := guestdockerfile.Dockerfile(opts)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, text)
		return nil
	}
	if err := guestdockerfile.Build(ctx, opts); err != nil {
		return err
	}
	if *rootfsOnly {
		fmt.Fprintf(stdout, "%s is a guest rootfs image, not a bootable one: pack it with `granule guest pack -from %s -t <tag>`\n", rootfs, rootfs)
		return nil
	}
	return guestpack.Pack(ctx, guestpack.Options{
		From:           rootfs,
		Tag:            *tag,
		KonturImage:    *konturImage,
		PackerImage:    *packerImage,
		Variant:        *variant,
		DiskMode:       *diskMode,
		DiskNumQueues:  *diskNumQueues,
		DiskIOThreads:  *diskIOThreads,
		DiskIOEngine:   *diskIOEngine,
		DiskExtraMB:    *extraMB,
		ExtraBuildArgs: dockerBuildArgs,
		Stdout:         stdout,
		Stderr:         stderr,
	})
}

// rootfsTagFor names the intermediate rootfs image of a build whose
// caller did not name one, by appending "-rootfs" to the tag component
// of the output reference.
//
// Derived rather than left dangling on purpose: an untagged intermediate
// is a name nobody can type, so the rootfs a consumer is supposed to
// publish would be findable only as an image ID, and every rebuild would
// leave another one behind. A name derived from -t is stable across
// rebuilds (so a rebuild replaces it), pushable, and re-packable when
// this repo's agents move on.
func rootfsTagFor(ref string) (string, error) {
	if strings.Contains(ref, "@") {
		return "", fmt.Errorf("-t %s names a digest, which a build cannot tag: give the rootfs image a name of its own with -rootfs-tag, or a tag rather than a digest to -t", ref)
	}
	repo, tag := ref, "latest"
	// A colon in the registry host's port is not the tag separator, and
	// what tells them apart is a "/" after it.
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i+1:], "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	return repo + ":" + tag + "-rootfs", nil
}

// routeFlags names each flag that only means something on one of the two
// routes, mapped to that route's own flag.
var routeFlags = map[string]string{
	"f":                "",
	"print-dockerfile": "",
	"build-arg":        "",
	"docker-build-arg": "",
	"rootfs-tag":       "",
	"rootfs-only":      "",
	"kontur-image":     "",
	"packer-image":     "",
	"variant":          "",
	"disk-extra-mb":    "",

	"ready-timeout":    "setup",
	"shutdown-timeout": "setup",
	"keep-on-failure":  "setup",
	"snapshot":         "setup",
	"docker-run-arg":   "setup",
	"dns":              "setup",
}

// packFlags are the ones that configure the packing at the end of the
// Dockerfile route, and so mean nothing to a build that stops short of
// it.
var packFlags = []string{"disk-extra-mb", "disk-io-engine", "disk-iothreads", "disk-mode", "disk-num-queues", "kontur-image", "packer-image", "rootfs-tag", "variant"}

func checkRouteFlags(set map[string]bool) error {
	booting := set["setup"]
	var wrong []string
	for name, route := range routeFlags {
		if set[name] && (route == "setup") != booting {
			wrong = append(wrong, "-"+name)
		}
	}
	if len(wrong) > 0 {
		sort.Strings(wrong)
		if booting {
			return fmt.Errorf("%s belong(s) to the Dockerfile route and -setup asks for the booting one; drop one of the two", strings.Join(wrong, ", "))
		}
		return fmt.Errorf("%s only mean(s) something with -setup, which boots a guest; this build has no guest to boot", strings.Join(wrong, ", "))
	}

	// -rootfs-only is the same mistake one step further in: a flag that
	// configures a packing this build is not going to do.
	if set["rootfs-only"] {
		var unpacked []string
		for _, name := range packFlags {
			if set[name] {
				unpacked = append(unpacked, "-"+name)
			}
		}
		if len(unpacked) > 0 {
			return fmt.Errorf("%s configure(s) the packing, and -rootfs-only stops before it: -t already names the rootfs image there", strings.Join(unpacked, ", "))
		}
	}
	return nil
}

// dockerfileOptions works out which file to build and in which context,
// and reads the file, so a path that does not exist fails before docker
// is invoked.
func dockerfileOptions(rest []string, file, from, tag string, buildArgs, dockerBuildArgs []string, stdout, stderr io.Writer) (guestdockerfile.Options, error) {
	if len(rest) > 1 {
		return guestdockerfile.Options{}, fmt.Errorf("a build takes at most one context directory, and was given %d: %s", len(rest), strings.Join(rest, " "))
	}

	// Context and file each default from the other, so both the docker
	// habit (`... -t x .`) and the short form (`... -f path/Dockerfile`)
	// mean what they look like.
	context := ""
	if len(rest) == 1 {
		context = rest[0]
	}
	switch {
	case file == "" && context == "":
		context, file = ".", "Dockerfile"
	case file == "":
		file = filepath.Join(context, "Dockerfile")
	case context == "":
		context = filepath.Dir(file)
	}

	source, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return guestdockerfile.Options{}, fmt.Errorf("%s: no such file. `granule guest build` builds a guest rootfs from a Dockerfile: name one with -f, or boot a guest and provision it from inside with -setup <script> -from <image>", file)
		}
		return guestdockerfile.Options{}, fmt.Errorf("reading the Dockerfile: %w", err)
	}

	return guestdockerfile.Options{
		File:           file,
		Source:         string(source),
		Context:        context,
		From:           from,
		Tag:            tag,
		BuildArgs:      buildArgs,
		ExtraBuildArgs: dockerBuildArgs,
		Stdout:         stdout,
		Stderr:         stderr,
	}, nil
}

// runGuestPack packs an OCI image's filesystem into a bootable guest
// disk and emits a granule image around it -- see pkg/guestpack.
//
// The difference from `build` above is what it is allowed to do, not
// what it produces: pack executes nothing from the image it is packing
// and needs no KVM, so it can run on the machine that runs the daemon
// against an image a repository somewhere authored. `build` boots that
// image and runs a script in it, which belongs in a consumer's own CI.
func runGuestPack(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule guest pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "OCI image whose filesystem is a granule guest rootfs, e.g. one built FROM granule's guest-rootfs-image (required). Nothing in it is executed")
	tag := fs.String("t", "", "image reference to give the packed result (required)")
	konturImage := fs.String("kontur-image", vmspec.DockerImage, "granule image supplying the VMM half the result is built on, the guest-side pieces laid over the rootfs (the agents, the overlay) and the kernel the guest boots. The full image is the one to name for an ordinary guest: a `docker build --target runtime` build is smaller -- no bundled guest disk in the result's lower layers -- but carries no kernel either, so it suits only a rootfs that brings its own (GUEST_KERNEL_PACKAGE), and a pack onto it without one is refused")
	packerImage := fs.String("packer-image", guestpack.DefaultPackerImage, "image the packing itself runs in; anything with /bin/sh and mke2fs will do, and one that already has mke2fs needs no network at all")
	variant := fs.String("variant", "", "which overlay to lay down: \"debian\" (systemd) or \"alpine\" (OpenRC). The default asks the rootfs which init system it runs")
	diskMode := fs.String("disk-mode", "", "disk mode to bake into the image: \"overlay\" (qcow2 disk overlay), \"persistent\" (raw read-write flat file), or \"readonly\"")
	diskNumQueues := fs.Int("disk-num-queues", 0, "virtio-blk multi-queue count to bake into the image")
	diskIOThreads := fs.Bool("disk-iothreads", false, "dedicated host iothreads setting to bake into the image")
	diskIOEngine := fs.String("disk-io-engine", "", "host asynchronous I/O backend to bake into the image: \"io_uring\", \"epoll\", or \"sync\"")
	extraMB := fs.Int("disk-extra-mb", 0, "headroom in MiB on top of the 20% the disk is sized with, for a guest that will be provisioned further after packing. Every MiB is paid on every host that pulls the image; -disk-size-mb on `vm create` grows the overlay at boot instead and costs the image nothing")
	printOnly := fs.Bool("print-dockerfile", false, "print the build pack would run and exit, building nothing")
	var buildArgs repeatedFlag
	fs.Var(&buildArgs, "docker-build-arg", "extra argument for the underlying `docker build` (repeatable) -- how a pack reaches a private registry, a proxy or a builder other than the default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *tag == "" {
		fs.Usage()
		return fmt.Errorf("-from and -t are both required")
	}

	opts := guestpack.Options{
		From:           *from,
		Tag:            *tag,
		KonturImage:    *konturImage,
		PackerImage:    *packerImage,
		Variant:        *variant,
		DiskMode:       *diskMode,
		DiskNumQueues:  *diskNumQueues,
		DiskIOThreads:  *diskIOThreads,
		DiskIOEngine:   *diskIOEngine,
		DiskExtraMB:    *extraMB,
		ExtraBuildArgs: buildArgs,
		Stdout:         stdout,
		Stderr:         stderr,
	}
	if *printOnly {
		dockerfile, err := guestpack.Dockerfile(opts)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, dockerfile)
		return nil
	}
	return guestpack.Pack(ctx, opts)
}

// runGuestRebase makes an existing guest image's granule half current --
// see pkg/guestrebase.
//
// One verb rather than two, with the cheap form as the default and the
// complete ones behind the flag that says which of them is possible:
// `-rootfs` where the rootfs image is still held, `-boot` where it is
// not. The cost of a call is therefore what the caller asked for rather
// than something granule decides for them, and the cheap form ends by
// naming the flag that would have refreshed the rest -- or refuses,
// where the image declares a guest-side contract version this granule can
// no longer drive and `-force` was not given.
func runGuestRebase(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule guest rebase", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "guest image to rebase: any image that boots a granule guest, whatever built it (required). Nothing in it is executed unless -boot is given")
	tag := fs.String("t", "", "image reference to give the result (required)")
	konturImage := fs.String("kontur-image", vmspec.DockerImage, "granule image supplying the VMM half the result is built on, the guest-side pieces a complete rebase lays down, and the kernel the guest boots. The full image is the one to name for a guest that brings no kernel of its own: a `docker build --target runtime` build is smaller -- no bundled guest disk in the result's lower layers -- but carries no kernel either, and a rebase onto it of a guest without one is refused")
	rootfs := fs.String("rootfs", "", "refresh the guest side too, by repacking this rootfs image with the current agents in it -- the OCI image `-from` was packed from. Minutes and a new disk rather than seconds, and it still runs nothing out of the guest")
	boot := fs.Bool("boot", false, "refresh the guest side by booting the guest and laying the current agents over its filesystem, for a guest whose rootfs image nobody has any more. Needs /dev/kvm, and this is the one form that runs the guest's own code -- in a VM")
	force := fs.Bool("force", false, "rebase a guest whose declared guest-side contract version is below what this granule requires, which is otherwise refused. It does not make that guest any newer: the result carries agents, units and helpers granule may not be able to drive")
	variant := fs.String("variant", "", "with -rootfs or -boot: which overlay to lay down, \"debian\" (systemd) or \"alpine\" (OpenRC). The default asks the guest which init system it runs")
	packerImage := fs.String("packer-image", guestpack.DefaultPackerImage, "with -rootfs: the image the repacking runs in; anything with /bin/sh and mke2fs will do")
	extraMB := fs.Int("disk-extra-mb", 0, "with -rootfs: headroom in MiB on top of the 20% the repacked disk is sized with")
	readyTimeout := fs.Duration("ready-timeout", 3*time.Minute, "with -boot: how long to wait for the guest to become reachable and finish booting")
	shutdownTimeout := fs.Duration("shutdown-timeout", 60*time.Second, "with -boot: how long to wait for the guest to power off before giving up")
	keep := fs.Bool("keep-on-failure", false, "with -boot: leave the container in place when the rebase fails, for inspection")
	printOnly := fs.Bool("print-dockerfile", false, "print the build a cheap rebase would run and exit, building nothing")
	var buildArgs repeatedFlag
	fs.Var(&buildArgs, "docker-build-arg", "extra argument for the underlying `docker build` (repeatable) -- how a rebase reaches a private registry, a proxy or a builder other than the default")
	var runArgs repeatedFlag
	fs.Var(&runArgs, "docker-run-arg", "with -boot: extra argument for the underlying `docker run` (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *tag == "" {
		fs.Usage()
		return fmt.Errorf("-from and -t are both required")
	}

	opts := guestrebase.Options{
		From:            *from,
		Tag:             *tag,
		KonturImage:     *konturImage,
		Rootfs:          *rootfs,
		Boot:            *boot,
		Force:           *force,
		Variant:         *variant,
		PackerImage:     *packerImage,
		DiskExtraMB:     *extraMB,
		ExtraBuildArgs:  buildArgs,
		ExtraRunArgs:    runArgs,
		ReadyTimeout:    *readyTimeout,
		ShutdownTimeout: *shutdownTimeout,
		KeepOnFailure:   *keep,
		Stdout:          stdout,
		Stderr:          stderr,
	}
	if *printOnly {
		dockerfile, err := guestrebase.Dockerfile(ctx, opts)
		if err != nil {
			return err
		}
		fmt.Fprint(stdout, dockerfile)
		return nil
	}
	return guestrebase.Rebase(ctx, opts)
}

func runGuestFlatten(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("guest flatten", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base flat raw image if not detected from backing file")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: granule guest flatten [flags] <qcow-input> <flat-output>\n\n")
		fmt.Fprintf(stderr, "Replay a chain of qcow2 layers against a flat file and generate a single layer.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fs.Usage()
		return fmt.Errorf("guest flatten: requires <qcow-input> and <flat-output>")
	}
	inPath, outPath := rest[0], rest[1]
	if *base != "" {
		if err := qcow2.FlattenChain([]string{*base, inPath}, outPath); err != nil {
			return fmt.Errorf("flattening %s with base %s to %s: %w", inPath, *base, outPath, err)
		}
	} else {
		if err := qcow2.Flatten(inPath, outPath); err != nil {
			return fmt.Errorf("flattening %s to %s: %w", inPath, outPath, err)
		}
	}
	fmt.Fprintf(stdout, "Flattened %s to %s\n", inPath, outPath)
	return nil
}
