package guestwrap

// WrapDir: the same wrap with no docker anywhere in it.
//
// Wrap needs a docker daemon twice -- `docker image inspect` to read the
// wrapped image's configuration, and a `docker build` to assemble and
// pack the guest. A host that runs containers on containerd alone has
// neither, but it can hand over everything those two calls were for:
// an image's filesystem as a directory (a snapshot mount, or an unpacked
// export) and its OCI config as a file. WrapDir takes those, and the
// guest rootfs and granule's guest parts in the same form, and does on
// the host what the generated build did inside one:
//
//  1. copies the guest rootfs into a staging tree (treeCopier, which
//     keeps what a COPY keeps: ownership, modes, links, devices, xattrs);
//  2. writes granule-container, the container spec and the wrap service
//     into it, and enables that service -- in Go, where the build ran the
//     guest image's shell;
//  3. copies the wrapped filesystem in at containerspec.RootfsDir, last,
//     as the build did;
//  4. runs granule's own pack-guest over the result, with the host's
//     /bin/sh and mke2fs, which lays the agents and overlay down and
//     packs the disk.
//
// What comes out is a guest half (see guestimage.Files): disk.img, the
// contract sidecar, and the kernel that boots it, in a directory a VM
// container reads at guestimage.Dir.
//
// Nothing from the wrapped image is executed here either. It is read by
// treeCopier and by mke2fs and by nothing else; no program in it, nor in
// the guest rootfs, is run, and no step chroots anywhere. The programs
// that do run are the host's (sh, mke2fs and the coreutils pack-guest
// uses) and granule's own (pack-guest, out of the parts directory).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/guestimage"
)

// DirOptions configures one WrapDir.
type DirOptions struct {
	// Rootfs is the wrapped image's filesystem, as a directory: a
	// containerd snapshot mounted read-only, or an image's layers unpacked
	// in order. It is read and never written or executed.
	Rootfs string

	// Config is the wrapped image's OCI image config, as a file: the
	// config blob an image manifest names (with its "config" object), or
	// that object on its own as `docker image inspect -f '{{json
	// .Config}}'` prints it.
	Config string

	// Image is what the guest's spec records the wrapped image as. It is
	// provenance only -- nothing is fetched by it -- and empty records
	// Rootfs.
	Image string

	// GuestRootfs is the granule guest rootfs the container runs inside
	// (DefaultGuestImage's filesystem, or one built FROM it), as a
	// directory. It is copied, not modified.
	GuestRootfs string

	// Parts is the granule image's guest-parts directory
	// (guestimage.PartsDir), as a directory: the overlay trees, the
	// agents, granule-container and pack-guest. pack-guest is run from
	// here, so this has to be granule's own and not something a wrapped
	// image supplied.
	Parts string

	// RuntimeKernel is the granule image's own kernel
	// (guestimage.RuntimeKernel), as a file. It is copied into Out as
	// vmlinux when the guest rootfs brings no kernel of its own, which
	// granule's published one does not; a guest rootfs that does bring
	// one needs nothing here. A wrap whose guest boots neither is refused.
	RuntimeKernel string

	// Out is where the guest half is written: a directory whose contents
	// go at guestimage.Dir in the VM container. Created if it does not
	// exist; the files WrapDir writes are replaced.
	Out string

	// WorkDir is where the staging tree -- both filesystems, copied -- is
	// assembled and then removed. Empty means the system temp directory.
	// It needs room for both trees, and is best on the same filesystem as
	// Out.
	WorkDir string

	// Unprivileged assembles the disk without root: ownership is not
	// preserved (every file in the disk belongs to the user wrapping)
	// and device nodes are left out. That is a disk to inspect, not one to
	// boot, which is why it has to be asked for: without it WrapDir run
	// by anyone but root is refused before anything is copied.
	Unprivileged bool

	// The workload, exactly as in Options.
	ServiceName string
	Restart     string
	ExitAction  string
	Entrypoint  []string
	Cmd         []string
	Env         []string
	WorkingDir  string
	User        string
	HaltOnExit  *bool

	// Variant and DiskExtraMB are pack-guest's, as in Options.
	Variant     string
	DiskExtraMB int

	Stdout io.Writer
	Stderr io.Writer
}

// DirResult is what WrapDir produced.
type DirResult struct {
	// Spec is what the guest was told to run, as written to
	// containerspec.ConfigPath inside the disk.
	Spec containerspec.Spec

	// Ports and Volumes are what the wrapped image declared, for whatever
	// launches the result to grant: a disk has no image config to carry
	// them in.
	Ports   []string
	Volumes []string

	// Disk is the packed root disk, Out/disk.img.
	Disk string

	// Kernel is what boots it: the guest's own vmlinuz, or the vmlinux
	// copied from RuntimeKernel. Initramfs is set with the former only.
	Kernel    string
	Initramfs string
}

// WrapDir wraps the image at opts.Rootfs into a guest half in opts.Out,
// with no container runtime involved. See the comment at the top of this
// file.
func WrapDir(ctx context.Context, opts DirOptions) (DirResult, error) {
	opts, err := opts.resolve()
	if err != nil {
		return DirResult{}, err
	}

	if !opts.Unprivileged && os.Geteuid() != 0 {
		return DirResult{}, fmt.Errorf("assembling a guest disk from directories keeps the ownership and device nodes of both filesystems, which takes root; run it as root, or ask for an unprivileged wrap (-unprivileged) to get a disk to inspect whose files all belong to you")
	}
	cfg, spec, err := opts.spec()
	if err != nil {
		return DirResult{}, err
	}
	specJSON, err := spec.Marshal()
	if err != nil {
		return DirResult{}, fmt.Errorf("writing the container spec: %w", err)
	}

	packScript := filepath.Join(opts.Parts, path.Base(guestimage.PartsScript))
	runner := filepath.Join(opts.Parts, path.Base(guestimage.PartsRunner))
	for _, f := range []struct{ path, what string }{
		{packScript, "pack-guest, which packs the disk"},
		{runner, "granule-container, which runs the container inside the guest"},
	} {
		if !regularFile(f.path) {
			return DirResult{}, fmt.Errorf("the guest parts directory %s has no %s: it should be the contents of %s out of a granule image built with `granule wrap` support", opts.Parts, f.what, guestimage.PartsDir)
		}
	}

	work, err := os.MkdirTemp(opts.WorkDir, "granule-wrap-")
	if err != nil {
		return DirResult{}, fmt.Errorf("making a staging directory: %w", err)
	}
	defer os.RemoveAll(work)
	stage := filepath.Join(work, "rootfs")
	copier := newTreeCopier(!opts.Unprivileged)

	fmt.Fprintf(opts.Stdout, "wrapping %s: the VM will run %s\n", opts.Image, strings.Join(spec.Argv(), " "))
	if err := copier.copy(opts.GuestRootfs, stage); err != nil {
		return DirResult{}, fmt.Errorf("copying the guest rootfs %s: %w", opts.GuestRootfs, err)
	}

	// The guest rootfs brings a kernel, or RuntimeKernel has to. Asked
	// before anything is packed, for the reason the build asks it there:
	// the alternative is a disk nothing boots.
	guestKernels, _ := filepath.Glob(filepath.Join(stage, "boot", "vmlinuz-*"))
	if len(guestKernels) == 0 && opts.RuntimeKernel == "" {
		return DirResult{}, fmt.Errorf("the guest rootfs %s brings no kernel of its own -- there is no /boot/vmlinuz-* in it -- and no runtime kernel was given, so the disk would have nothing to boot it. Give the granule image's own %s (-runtime-kernel), or use a guest rootfs built with GUEST_KERNEL_PACKAGE", opts.GuestRootfs, guestimage.RuntimeKernel)
	}

	// Step 2: what the build's own stage wrote and the RUN that enabled
	// it, before anything of the wrapped image is in the tree.
	if err := installWorkload(stage, opts, runner, specJSON); err != nil {
		return DirResult{}, err
	}

	// Step 3: the wrapped filesystem, last.
	containerRoot, err := stagedPath(stage, containerspec.RootfsDir, true)
	if err != nil {
		return DirResult{}, err
	}
	if err := os.RemoveAll(containerRoot); err != nil {
		return DirResult{}, err
	}
	if err := copier.copy(opts.Rootfs, containerRoot); err != nil {
		return DirResult{}, fmt.Errorf("copying %s's filesystem: %w", opts.Image, err)
	}
	if len(copier.skipped) > 0 {
		fmt.Fprintf(opts.Stdout, "left %d sockets or device nodes out of the disk (device nodes need root to copy)\n", len(copier.skipped))
	}

	// Step 4: granule's own packing. pack-guest runs as this process does,
	// on the host, and writes into the staged guest tree; a link in that
	// tree pointing out of it would carry those writes onto the host, so
	// the directories it writes into are checked first.
	if err := refuseEscapes(stage, opts.Parts); err != nil {
		return DirResult{}, err
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return DirResult{}, err
	}
	for _, f := range guestimage.Files() {
		os.Remove(filepath.Join(opts.Out, path.Base(f.Path)))
	}
	os.Remove(filepath.Join(opts.Out, path.Base(guestimage.RuntimeKernel)))
	variant := opts.Variant
	if variant == "" {
		variant = "auto"
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", packScript, stage, opts.Parts, opts.Out)
	cmd.Env = append(os.Environ(),
		"PATH="+os.Getenv("PATH")+":/sbin:/usr/sbin",
		fmt.Sprintf("GUEST_DISK_EXTRA_MB=%d", opts.DiskExtraMB),
		"GRANULE_GUEST_VARIANT="+variant,
	)
	cmd.Stdout, cmd.Stderr = opts.Stdout, opts.Stderr
	if err := cmd.Run(); err != nil {
		return DirResult{}, fmt.Errorf("packing the wrapped guest with %s: %w", packScript, err)
	}

	result := DirResult{
		Spec:    spec,
		Ports:   exposedPorts(cfg),
		Volumes: volumes(cfg),
		Disk:    filepath.Join(opts.Out, path.Base(guestimage.DiskImage)),
	}
	if kernel := filepath.Join(opts.Out, path.Base(guestimage.GuestKernel)); regularFile(kernel) {
		result.Kernel = kernel
		result.Initramfs = filepath.Join(opts.Out, path.Base(guestimage.GuestInitramfs))
	} else {
		result.Kernel = filepath.Join(opts.Out, path.Base(guestimage.RuntimeKernel))
		if err := copyFile(opts.RuntimeKernel, result.Kernel); err != nil {
			return DirResult{}, fmt.Errorf("copying the runtime kernel %s: %w", opts.RuntimeKernel, err)
		}
		if err := os.Chmod(result.Kernel, 0o644); err != nil {
			return DirResult{}, err
		}
	}
	if err := guestimage.CheckPresent(func(p string) bool {
		return regularFile(filepath.Join(opts.Out, path.Base(p)))
	}, func(p string) string { return filepath.Join(opts.Out, path.Base(p)) }); err != nil {
		return DirResult{}, err
	}
	if _, err := guestimage.ReadBootContract(result.Disk); err != nil {
		return DirResult{}, err
	}
	fmt.Fprintf(opts.Stdout, "wrapped %s into %s\n", opts.Image, opts.Out)
	return result, nil
}

// DirSpec is what WrapDir would tell the guest to run, without copying
// or packing anything: `granule wrap -rootfs -print-spec`.
func DirSpec(opts DirOptions) (containerspec.Spec, error) {
	opts, err := opts.resolve()
	if err != nil {
		return containerspec.Spec{}, err
	}
	_, spec, err := opts.spec()
	return spec, err
}

func (opts DirOptions) spec() (imageConfig, containerspec.Spec, error) {
	data, err := os.ReadFile(opts.Config)
	if err != nil {
		return imageConfig{}, containerspec.Spec{}, fmt.Errorf("reading %s's configuration: %w", opts.Image, err)
	}
	cfg, err := parseOCIConfig(data, opts.Image)
	if err != nil {
		return imageConfig{}, containerspec.Spec{}, err
	}
	spec, err := newSpec(opts.Image, cfg, opts.overrides())
	return cfg, spec, err
}

// installWorkload writes what the generated build's wrapped-guest stage
// writes -- the runner, the spec and both service definitions -- and
// enables the wrap service the way its RUN does, into the staged guest
// tree at stage.
func installWorkload(stage string, opts DirOptions, runner string, spec []byte) error {
	runnerData, err := os.ReadFile(runner)
	if err != nil {
		return err
	}
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{containerspec.RunnerPath, runnerData, 0o755},
		{containerspec.ConfigPath, spec, 0o644},
		{"/etc/systemd/system/granule-wrap@.service", wrapUnit(opts.Restart), 0o644},
		{"/etc/init.d/granule-wrap", openrcWrapScript, 0o755},
	} {
		if err := writeStaged(stage, f.path, f.data, f.mode); err != nil {
			return err
		}
	}

	systemd, runlevels := isDir(filepath.Join(stage, "etc/systemd")), isDir(filepath.Join(stage, "etc/runlevels"))
	if !systemd && !runlevels {
		return fmt.Errorf("the guest rootfs %s runs neither systemd nor OpenRC, so nothing would start the container", opts.GuestRootfs)
	}
	// One service starts the workload and not two: see the generated
	// build's RUN, which this is.
	if systemd {
		if err := linkStaged(stage, "/etc/systemd/system/multi-user.target.wants/granule-wrap@"+opts.ServiceName+".service", "/etc/systemd/system/granule-wrap@.service",
			"/etc/systemd/system/multi-user.target.wants/granule-container.service"); err != nil {
			return err
		}
	}
	if runlevels {
		if err := linkStaged(stage, "/etc/runlevels/default/granule-wrap", "/etc/init.d/granule-wrap",
			"/etc/runlevels/default/granule-container"); err != nil {
			return err
		}
	}
	return nil
}

// stagedPath is guest path p inside the staged tree at stage, with every
// directory above it checked to be a directory and not a symlink --
// created, when mkdir is set and it is not there. Writing through a link
// in the guest tree would write wherever the link points on the host.
func stagedPath(stage, p string, mkdir bool) (string, error) {
	dir := stage
	parts := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist) && mkdir:
			if err := os.Mkdir(dir, 0o755); err != nil {
				return "", err
			}
			continue
		case err != nil:
			return "", err
		case info.Mode()&os.ModeSymlink != 0:
			return "", fmt.Errorf("the guest rootfs has a symlink at %s, where wrap writes %s: wrap writes nothing through a link, since on this host it could point anywhere", strings.TrimPrefix(dir, stage), p)
		case !info.IsDir():
			return "", fmt.Errorf("the guest rootfs has a file at %s, where wrap needs a directory to write %s", strings.TrimPrefix(dir, stage), p)
		}
	}
	return filepath.Join(dir, parts[len(parts)-1]), nil
}

// writeStaged replaces guest path p in the staged tree with data.
func writeStaged(stage, p string, data []byte, mode os.FileMode) error {
	target, err := stagedPath(stage, p, true)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

// linkStaged makes guest path link a symlink to target, after removing
// the guest paths in remove.
func linkStaged(stage, link, target string, remove ...string) error {
	for _, p := range append(remove, link) {
		staged, err := stagedPath(stage, p, true)
		if err != nil {
			return err
		}
		if err := os.Remove(staged); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	staged, _ := stagedPath(stage, link, true)
	return os.Symlink(target, staged)
}

// refuseEscapes checks the directories pack-guest writes into, in the
// staged guest tree, for a symlink that resolves outside it: the overlay
// files' directories, the agents', and the places services are enabled.
// A guest rootfs is the deployment's own choice rather than the wrapped
// image's, but pack-guest is a shell script running on the host, and
// `install` and `ln` follow a link wherever it leads.
func refuseEscapes(stage, parts string) error {
	root, err := filepath.EvalSymlinks(stage)
	if err != nil {
		return err
	}
	dirs := map[string]bool{"usr/local/bin": true, "etc/granule": true, "etc/systemd/system": true, "etc/runlevels": true}
	for _, overlay := range []string{"overlay-common", "overlay-debian", "overlay-alpine"} {
		base := filepath.Join(parts, overlay)
		filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(base, filepath.Dir(p))
				dirs[rel] = true
			}
			return nil
		})
	}
	for _, pattern := range []string{"etc/systemd/system/*.wants", "etc/runlevels/*"} {
		matches, _ := filepath.Glob(filepath.Join(stage, pattern))
		for _, m := range matches {
			rel, _ := filepath.Rel(stage, m)
			dirs[rel] = true
		}
	}
	for dir := range dirs {
		p := stage
		for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
			if part == "." || part == "" {
				continue
			}
			p = filepath.Join(p, part)
			if _, err := os.Lstat(p); err != nil {
				break
			}
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return fmt.Errorf("the guest rootfs has a link at /%s that does not resolve inside it, where pack-guest writes: %w", strings.TrimPrefix(p, stage+"/"), err)
			}
			if resolved != root && !strings.HasPrefix(resolved, root+"/") {
				return fmt.Errorf("the guest rootfs has a link at /%s that leads out of it (to %s on this host), and pack-guest writes there: give a guest rootfs whose links stay inside it", strings.TrimPrefix(p, stage+"/"), resolved)
			}
		}
	}
	return nil
}

func (opts DirOptions) resolve() (DirOptions, error) {
	for _, f := range []struct{ what, value string }{
		{"the wrapped image's rootfs", opts.Rootfs},
		{"the wrapped image's config", opts.Config},
		{"the guest rootfs", opts.GuestRootfs},
		{"the guest parts directory", opts.Parts},
		{"the output directory", opts.Out},
	} {
		if f.value == "" {
			return opts, fmt.Errorf("%s is required", f.what)
		}
	}
	for _, d := range []struct {
		what  string
		value *string
	}{
		{"the wrapped image's rootfs", &opts.Rootfs},
		{"the guest rootfs", &opts.GuestRootfs},
		{"the guest parts directory", &opts.Parts},
	} {
		if !isDir(*d.value) {
			return opts, fmt.Errorf("%s %s is not a directory", d.what, *d.value)
		}
		// Resolved, so that a directory named through a link is copied
		// as the directory rather than as the link.
		resolved, err := filepath.EvalSymlinks(*d.value)
		if err != nil {
			return opts, err
		}
		*d.value = resolved
	}
	if opts.RuntimeKernel != "" && !regularFile(opts.RuntimeKernel) {
		return opts, fmt.Errorf("the runtime kernel %s is not a file", opts.RuntimeKernel)
	}
	if opts.Image == "" {
		opts.Image = opts.Rootfs
	}

	// The checks Options.resolve makes of the same settings, by building
	// one: there is one statement of what a valid service name, restart
	// policy, exit action, variant or headroom is.
	wrap, err := Options{
		From:        "wrapped",
		Tag:         "wrapped",
		ServiceName: opts.ServiceName,
		Restart:     opts.Restart,
		ExitAction:  opts.ExitAction,
		HaltOnExit:  opts.HaltOnExit,
		Variant:     opts.Variant,
		DiskExtraMB: opts.DiskExtraMB,
		Env:         opts.Env,
	}.resolve()
	if err != nil {
		return opts, err
	}
	opts.ServiceName, opts.Restart, opts.HaltOnExit = wrap.ServiceName, wrap.Restart, wrap.HaltOnExit
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	return opts, nil
}

func (opts DirOptions) overrides() overrides {
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

// parseOCIConfig reads an image's configuration from an OCI image config
// blob -- whose "config" object holds the same fields docker's inspect
// prints -- or from that object on its own.
func parseOCIConfig(data []byte, ref string) (imageConfig, error) {
	var blob struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(data, &blob); err != nil {
		return imageConfig{}, fmt.Errorf("parsing %s's configuration: %w", ref, err)
	}
	if len(blob.Config) > 0 {
		data = blob.Config
	}
	return parseConfig(string(data), ref)
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func regularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
