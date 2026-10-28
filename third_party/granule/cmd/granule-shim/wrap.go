package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/guestpack"
	"github.com/bwsalmon/granule/pkg/guestwrap"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// repeatedFlag collects a flag given more than once, in order.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// runWrap implements "granule wrap": turn an ordinary container image into
// a granule image that boots a VM and runs that container inside it. See
// pkg/guestwrap for what the generated build does and
// cmd/granule-container for what happens once the VM is up.
//
// Unlike granule's other modes this one runs where docker does -- a
// workstation or a CI job -- rather than inside the VM container: it
// produces an image rather than being part of one. It lives in this
// binary anyway because the image it produces is built out of this
// binary's own image, and because "wrap the thing and run it" should not
// need a second tool to be installed first.
func runWrap(args []string) error {
	fs := flag.NewFlagSet("granule wrap", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: granule wrap -t OUTPUT-IMAGE [flags] IMAGE [COMMAND [ARG...]]\n       granule wrap -rootfs DIR -config FILE -guest-rootfs DIR -parts DIR -out DIR [flags] IMAGE [COMMAND [ARG...]]\n\n")
		fmt.Fprintf(fs.Output(), "Wraps a container image in a granule image: the result boots a VM and\nruns that container inside it, with the image's own entrypoint,\nenvironment, working directory and user. Trailing arguments replace the\nimage's CMD, as they do for `docker run`.\n\n")
		fs.PrintDefaults()
	}
	tag := fs.String("t", "", "image reference to give the wrapped result (required)")
	guestImage := fs.String("guest-image", guestwrap.DefaultGuestImage, "granule guest rootfs the container runs inside -- the machine, as opposed to the container. The default is the published one; name a pinned tag, a mirror, or a rootfs of your own")
	konturImage := fs.String("kontur-image", vmspec.DockerImage, "granule image supplying the VMM half the result is built on, the guest-side runner, the agents and the kernel the guest boots. The full image is the one to name: a docker build --target runtime build carries no kernel, and only suits a guest rootfs that brings its own")
	packerImage := fs.String("packer-image", guestpack.DefaultPackerImage, "image the packing itself runs in; anything with /bin/sh and mke2fs will do")
	variant := fs.String("variant", "", "which overlay to lay down: \"debian\" (systemd) or \"alpine\" (OpenRC). The default asks the guest rootfs which init system it runs")
	diskMode := fs.String("disk-mode", "", "disk mode to bake into the image: \"overlay\" (qcow2 disk overlay), \"persistent\" (raw read-write flat file), or \"readonly\"")
	diskNumQueues := fs.Int("disk-num-queues", 0, "virtio-blk multi-queue count to bake into the image")
	diskIOThreads := fs.Bool("disk-iothreads", false, "dedicated host iothreads setting to bake into the image")
	diskIOEngine := fs.String("disk-io-engine", "", "host asynchronous I/O backend to bake into the image: \"io_uring\", \"epoll\", or \"sync\"")
	extraMB := fs.Int("disk-extra-mb", 0, "headroom in MiB on top of the 20% the guest disk is sized with. Every MiB is paid on every host that pulls the image; CHV_DISK_SIZE_MB grows the disk at boot instead and costs the image nothing")
	entrypoint := fs.String("entrypoint", "", "replace the image's ENTRYPOINT. As with docker run --entrypoint, this drops the image's CMD unless a command is given too")
	workdir := fs.String("w", "", "replace the image's WORKDIR")
	user := fs.String("u", "", "replace the image's USER (\"1000\", \"1000:1000\", \"nginx\"); names are resolved in the wrapped image's own /etc/passwd")
	haltOnExit := fs.Bool("halt-on-exit", true, "power the VM off when the container's process exits, so a docker run of the result ends when the container would have. Turn it off to keep the machine up for `granule exec` after a crash")
	pull := fs.Bool("pull", true, "pull the image being wrapped if it is not already on this machine; its configuration has to be read before anything can be built")
	printDockerfile := fs.Bool("print-dockerfile", false, "print the build wrap would run and exit, building nothing")
	printSpec := fs.Bool("print-spec", false, "print what the guest would be told to run and exit, building nothing")
	rootfs := fs.String("rootfs", "", "wrap without docker: IMAGE's filesystem as a directory (a containerd snapshot mount, or its layers unpacked). Needs -config, -guest-rootfs, -parts and -out, and writes a guest disk rather than an image; IMAGE is then only the name the guest records")
	configFile := fs.String("config", "", "with -rootfs: IMAGE's OCI image config, as a file")
	guestRootfs := fs.String("guest-rootfs", "", "with -rootfs: the granule guest rootfs the container runs inside, as a directory")
	partsDir := fs.String("parts", "", "with -rootfs: the granule image's guest-parts directory ("+guestimage.PartsDir+"), as a directory")
	runtimeKernel := fs.String("runtime-kernel", "", "with -rootfs: the granule image's own kernel ("+guestimage.RuntimeKernel+"), copied beside the disk when the guest rootfs brings none")
	outDir := fs.String("out", "", "with -rootfs: the directory to write the guest half to (disk.img and the kernel that boots it), for "+guestimage.Dir+" in the VM container")
	workDir := fs.String("work-dir", "", "with -rootfs: where to stage both filesystems while the disk is assembled (default the system temp directory)")
	unprivileged := fs.Bool("unprivileged", false, "with -rootfs: assemble the disk without root, which loses ownership and device nodes -- a disk to inspect rather than to boot")
	var env repeatedFlag
	fs.Var(&env, "e", "add or override an environment `KEY=VALUE` for the container (repeatable)")
	var buildArgs repeatedFlag
	fs.Var(&buildArgs, "docker-build-arg", "extra argument for the underlying docker build (repeatable) -- how a wrap reaches a private registry, a proxy or a builder other than the default")
	if err := fs.Parse(args); err != nil {
		// "granule wrap -h" asked a question and Parse has already
		// answered it with the usage above, so it exits zero.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	rest := fs.Args()
	if *rootfs != "" {
		if len(rest) == 0 {
			fs.Usage()
			return fmt.Errorf("an image to wrap is required")
		}
		return wrapDir(guestwrap.DirOptions{
			Rootfs:        *rootfs,
			Config:        *configFile,
			Image:         rest[0],
			GuestRootfs:   *guestRootfs,
			Parts:         *partsDir,
			RuntimeKernel: *runtimeKernel,
			Out:           *outDir,
			WorkDir:       *workDir,
			Unprivileged:  *unprivileged,
			Variant:       *variant,
			DiskExtraMB:   *extraMB,
			Entrypoint:    entrypointFlag(*entrypoint),
			Cmd:           cmdArgs(rest),
			Env:           env,
			WorkingDir:    *workdir,
			User:          *user,
			HaltOnExit:    haltOnExit,
			Stdout:        os.Stdout,
			Stderr:        os.Stderr,
		}, *printSpec)
	}
	if len(rest) == 0 || *tag == "" {
		fs.Usage()
		return fmt.Errorf("an image to wrap and -t are both required")
	}

	opts := guestwrap.Options{
		From:           rest[0],
		Tag:            *tag,
		GuestImage:     *guestImage,
		KonturImage:    *konturImage,
		PackerImage:    *packerImage,
		Variant:        *variant,
		DiskMode:       *diskMode,
		DiskNumQueues:  *diskNumQueues,
		DiskIOThreads:  *diskIOThreads,
		DiskIOEngine:   *diskIOEngine,
		DiskExtraMB:    *extraMB,
		Cmd:            cmdArgs(rest),
		Env:            env,
		WorkingDir:     *workdir,
		User:           *user,
		HaltOnExit:     haltOnExit,
		Pull:           pull,
		ExtraBuildArgs: buildArgs,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	}
	opts.Entrypoint = entrypointFlag(*entrypoint)

	ctx, cancel := signalledContext()
	defer cancel()

	if *printDockerfile || *printSpec {
		plan, err := guestwrap.Prepare(ctx, opts)
		if err != nil {
			return err
		}
		if *printSpec {
			spec, err := plan.Spec.Marshal()
			if err != nil {
				return err
			}
			os.Stdout.Write(spec)
		}
		if *printDockerfile {
			fmt.Print(plan.Dockerfile)
		}
		return nil
	}

	return guestwrap.Wrap(ctx, opts)
}

// cmdArgs is the command trailing IMAGE. fs.Args() is never nil-vs-empty
// ambiguous the way Options.Cmd is: no trailing arguments means "keep the
// image's CMD", not "run it with none".
func cmdArgs(rest []string) []string {
	if len(rest) <= 1 {
		return nil
	}
	return rest[1:]
}

func entrypointFlag(entrypoint string) []string {
	if entrypoint == "" {
		return nil
	}
	return []string{entrypoint}
}

// wrapDir is "granule wrap -rootfs": the wrap that needs no docker, for a
// host running containers on containerd alone. See guestwrap.WrapDir.
func wrapDir(opts guestwrap.DirOptions, printSpec bool) error {
	ctx, cancel := signalledContext()
	defer cancel()
	if printSpec {
		spec, err := guestwrap.DirSpec(opts)
		if err != nil {
			return err
		}
		data, err := spec.Marshal()
		if err != nil {
			return err
		}
		os.Stdout.Write(data)
		return nil
	}
	result, err := guestwrap.WrapDir(ctx, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "disk %s, kernel %s", result.Disk, result.Kernel)
	if result.Initramfs != "" {
		fmt.Fprintf(os.Stdout, ", initramfs %s", result.Initramfs)
	}
	fmt.Fprintln(os.Stdout)
	if len(result.Ports) > 0 {
		fmt.Fprintf(os.Stdout, "declared ports: %s\n", strings.Join(result.Ports, " "))
	}
	if len(result.Volumes) > 0 {
		fmt.Fprintf(os.Stdout, "declared volumes: %s\n", strings.Join(result.Volumes, " "))
	}
	return nil
}
