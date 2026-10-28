package guestpack

// Tests for deploy/guest-image/pack-guest, the script the generated
// build runs -- exercised here directly on a directory tree in a temp
// dir, which is all it ever works on. Nothing here needs a docker
// daemon: `mke2fs -d` builds a filesystem from a tree, so the one thing
// these tests need from the machine is e2fsprogs, and they skip where it
// is absent.
//
// The tree is a fake rootfs rather than a real guest: a handful of files
// in the places the script looks. That is the point of the script only
// reading a filesystem -- a rootfs that would take a debootstrap and a
// gigabyte to produce is, as far as packing is concerned, a few marker
// files and a set of symlinks.

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/guestimage"
)

const (
	packScript = "../../deploy/guest-image/pack-guest"
	overlaySrc = "../../deploy/guest-image"
)

// packPath is PATH with the sbin directories added: mke2fs lives in
// /sbin, which an unprivileged shell's PATH routinely does not carry,
// and inside the generated build (where the script actually runs) the
// caller is root and already has them.
func packPath() string {
	return os.Getenv("PATH") + ":/sbin:/usr/sbin"
}

func requireMke2fs(t *testing.T) {
	t.Helper()
	for _, dir := range strings.Split(packPath(), ":") {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, "mke2fs")); err == nil && !info.IsDir() {
			return
		}
	}
	t.Skip("mke2fs is not installed, so there is nothing here to pack a disk with")
}

// parts assembles a guest-parts directory: the overlay trees straight
// out of deploy/guest-image (the same files the Dockerfile's guest-parts
// stage ships) and stand-ins for the two agent binaries, whose content
// the script only ever copies.
func parts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, overlay := range []string{"overlay-common", "overlay-debian", "overlay-alpine"} {
		out, err := exec.Command("cp", "-a", filepath.Join(overlaySrc, overlay), filepath.Join(dir, overlay)).CombinedOutput()
		if err != nil {
			t.Fatalf("copying %s: %v: %s", overlay, err, out)
		}
	}
	for _, agent := range []string{"granule-agent", "granule-mem-agent", "granule-container"} {
		write(t, filepath.Join(dir, agent), "#!/bin/sh\n# stands in for the real "+agent+"\n")
	}
	return dir
}

// rootfs builds a fake guest rootfs of the given variant: an operating
// system and nothing of granule's, which is what the Dockerfile's
// guest-rootfs-* stages now produce. Enough of one for pack-guest to
// recognize its init system and find the two programs the guest's own
// boot needs; everything of granule's -- the overlay, the agents and the
// enabling of the services -- is what the pack under test puts there.
func rootfs(t *testing.T, variant string) string {
	t.Helper()
	dir := t.TempDir()
	for _, program := range []string{"resize2fs", "modprobe", "ip", "acpid"} {
		write(t, filepath.Join(dir, "sbin", program), program+"\n")
	}
	// Something for the rootfs to weigh, so the sizing has a number to
	// work from that is not entirely the 64MiB floor.
	write(t, filepath.Join(dir, "usr/share/payload"), strings.Repeat("x", 1<<20))
	switch variant {
	case "debian":
		write(t, filepath.Join(dir, "lib/systemd/systemd"), "systemd\n")
		if err := os.Chmod(filepath.Join(dir, "lib/systemd/systemd"), 0o755); err != nil {
			t.Fatal(err)
		}
	case "alpine":
		write(t, filepath.Join(dir, "sbin/openrc"), "openrc\n")
		if err := os.Chmod(filepath.Join(dir, "sbin/openrc"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// services is the set of granule services the given variant's overlay
// installs and a pack is expected to enable, read out of the overlay
// itself so that adding one to deploy/guest-image does not need a
// corresponding edit here. granule-ready and granule-container are
// excluded: they ship everywhere and are enabled only when their
// configuration is present.
func services(t *testing.T, variant string) []string {
	t.Helper()
	dir := filepath.Join(overlaySrc, "overlay-"+variant, "etc/systemd/system")
	if variant == "alpine" {
		dir = filepath.Join(overlaySrc, "overlay-alpine", "etc/init.d")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), "granule-ready") || strings.HasPrefix(e.Name(), "granule-container") {
			continue
		}
		out = append(out, e.Name())
	}
	if len(out) == 0 {
		t.Fatalf("%s lists no services", dir)
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func enable(t *testing.T, path, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// pack runs the script the way the generated build does, with the
// staging layout granule passes it, and returns its output.
func pack(t *testing.T, root, partsDir string, env ...string) (out string, staged string, err error) {
	t.Helper()
	stagingRoot := t.TempDir()
	staged = guestimage.Under(stagingRoot, guestimage.Dir)
	cmd := exec.Command("/bin/sh", packScript, root, partsDir, staged)
	cmd.Env = append([]string{"PATH=" + packPath()}, env...)
	combined, err := cmd.CombinedOutput()
	return string(combined), stagingRoot, err
}

func TestPackGuestPacksADebianRootfs(t *testing.T) {
	requireMke2fs(t)
	root, partsDir := rootfs(t, "debian"), parts(t)

	// A file the consumer placed where granule's own copy goes. Pack
	// replaces it -- that is the whole reason the parts go on at pack
	// time -- but says so, since a consumer who put something there
	// meant something by it.
	stale := filepath.Join(root, "usr/local/bin/granule-agent")
	write(t, stale, "a granule-agent from months ago\n")

	// And a file the consumer has because their base had it -- which is
	// every overlay file, for a rootfs built FROM granule's own. Replacing
	// one of those with a byte-identical copy is not news, and saying so
	// for each of them would bury the line above in twenty like it.
	same := filepath.Join(root, "usr/local/libexec/granule-growfs")
	write(t, same, read(t, filepath.Join(partsDir, "overlay-common/usr/local/libexec/granule-growfs")))

	out, stagingRoot, err := pack(t, root, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	t.Log(out)

	if err := guestimage.Check(stagingRoot); err != nil {
		t.Errorf("the packed guest half does not check out: %v", err)
	}
	if hasDisk, hasKernel := guestimage.Present(stagingRoot); !hasDisk || hasKernel {
		t.Errorf("Present() = disk %v, kernel %v; want a disk and no kernel for a rootfs that brought none", hasDisk, hasKernel)
	}
	if !strings.Contains(out, "detected the debian variant") {
		t.Errorf("the variant was not detected from the rootfs:\n%s", out)
	}
	if !strings.Contains(out, "replacing /usr/local/bin/granule-agent") {
		t.Errorf("replacing the consumer's own granule-agent was not announced:\n%s", out)
	}
	if n := strings.Count(out, "replacing"); n != 1 {
		t.Errorf("one displaced file produced %d announcements, want 1:\n%s", n, out)
	}
	if strings.Contains(out, "replacing /usr/local/libexec/granule-growfs") {
		t.Errorf("replacing an identical copy was announced, which makes the announcements above noise:\n%s", out)
	}

	// The agent in the packed rootfs is the packer's, not the one the
	// image being packed carried.
	if got := read(t, stale); !strings.Contains(got, "stands in for the real granule-agent") {
		t.Errorf("granule-agent in the rootfs is %q, want the one from the parts directory", got)
	}
	if mode := mode(t, stale); mode != 0o755 {
		t.Errorf("granule-agent is mode %04o, want 0755", mode)
	}

	// The overlay went on, and its programs are executable: they are
	// exec'd by path by units and by acpid, and a build context is not a
	// reliable carrier of an executable bit.
	for path, want := range map[string]os.FileMode{
		"usr/local/libexec/granule-growfs":              0o755,
		"usr/local/libexec/granule-configure-net":       0o755,
		"etc/acpi/powerbtn.sh":                          0o755,
		"etc/acpi/events/powerbtn":                      0o644,
		"etc/systemd/system/granule-agent.service":      0o644,
		"etc/systemd/system/granule-net-routes.service": 0o644,
	} {
		if got := mode(t, filepath.Join(root, path)); got != want {
			t.Errorf("/%s is mode %04o, want %04o", path, got, want)
		}
	}

	// The alpine overlay stayed out of a debian guest: its init scripts
	// would be dead files there, and its /etc/acpi/powerbtn.sh is a
	// different script from debian's.
	if _, err := os.Stat(filepath.Join(root, "etc/init.d/granule-agent")); err == nil {
		t.Error("the alpine overlay's init scripts were laid over a debian rootfs")
	}

	// Sized to the rootfs plus 20% plus the 64MiB floor, measured after
	// the parts went on.
	assertSized(t, root, filepath.Join(staged(stagingRoot), "disk.img"), 0)

	// And it is a filesystem carrying what was in the tree, not just a
	// file of the right length.
	if debugfs, err := exec.LookPath("debugfs"); err == nil {
		disk := filepath.Join(staged(stagingRoot), "disk.img")
		cmd := exec.Command(debugfs, "-R", "stat /usr/local/bin/granule-agent", disk)
		got, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("debugfs on the packed disk: %v\n%s", err, got)
		}
		if !strings.Contains(string(got), "Inode:") {
			t.Errorf("the packed disk has no /usr/local/bin/granule-agent:\n%s", got)
		}
	}
}

// The guest-side contract version a pack lays down goes *inside* the
// guest, where it survives a repack by somebody else's tooling and a
// commit out of a running VM -- neither of which the packed image's
// label survives. What the script says out loud is the move, because a
// rootfs that has just been carried forward from an older contract is
// exactly the one whose agents were too old to have been left alone.
func TestPackGuestStampsTheContractInTheGuest(t *testing.T) {
	requireMke2fs(t)
	partsDir := parts(t)
	inGuest := strings.TrimPrefix(guestimage.ContractFile, "/")

	root := rootfs(t, "debian")
	out, _, err := pack(t, root, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(root, inGuest))); got != guestimage.ContractValue() {
		t.Errorf("%s in the packed rootfs is %q, want %q", guestimage.ContractFile, got, guestimage.ContractValue())
	}
	if !strings.Contains(out, "guest-side contract version "+guestimage.ContractValue()+" (this rootfs declared none") {
		t.Errorf("packing a rootfs that declared nothing did not say so:\n%s", out)
	}

	// A rootfs carried forward from an older contract: the move is the
	// news, and it is what a caller reading the log is looking for.
	older := rootfs(t, "debian")
	write(t, filepath.Join(older, inGuest), "0\n")
	out, _, err = pack(t, older, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "guest-side contract version 0 -> "+guestimage.ContractValue()) {
		t.Errorf("packing a rootfs built against an older contract did not say what moved:\n%s", out)
	}

	// And one already at this contract says it once, without a move.
	current := rootfs(t, "debian")
	write(t, filepath.Join(current, inGuest), guestimage.ContractValue()+"\n")
	out, _, err = pack(t, current, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "guest-side contract version "+guestimage.ContractValue()+"\n") || strings.Contains(out, "->") {
		t.Errorf("packing a rootfs already at this contract reported a move:\n%s", out)
	}

	// -lay-only stamps it too: that is the half of a pack a rebase runs,
	// and a rebase that refreshed the guest side without saying so would
	// leave the image declaring a version its guest has moved past.
	booted := rootfs(t, "debian")
	cmd := exec.Command("/bin/sh", packScript, "-lay-only", booted, partsDir)
	cmd.Env = []string{"PATH=" + packPath()}
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pack-guest -lay-only: %v\n%s", err, combined)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(booted, inGuest))); got != guestimage.ContractValue() {
		t.Errorf("-lay-only left %s at %q, want %q", guestimage.ContractFile, got, guestimage.ContractValue())
	}
}

// A parts directory with no contract file in it cannot say what it is
// laying down, and a pack that cannot say produces an image whose label
// would be a guess. The script and the overlay always ship together
// (both come out of the same granule image), so this is a broken parts
// directory rather than an old one -- and it is refused the way every
// other missing piece of one is.
func TestPackGuestRefusesPartsThatDeclareNoContract(t *testing.T) {
	root, partsDir := rootfs(t, "debian"), parts(t)
	if err := os.Remove(filepath.Join(partsDir, "overlay-common", strings.TrimPrefix(guestimage.ContractFile, "/"))); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", packScript, "-lay-only", root, partsDir)
	cmd.Env = []string{"PATH=" + packPath()}
	combined, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("pack-guest accepted a parts directory that declares no contract version:\n%s", combined)
	}
	if !strings.Contains(string(combined), "guest-contract") {
		t.Errorf("the refusal does not name what is missing:\n%s", combined)
	}
}

func TestPackGuestPacksAnAlpineRootfs(t *testing.T) {
	requireMke2fs(t)
	root, partsDir := rootfs(t, "alpine"), parts(t)

	out, stagingRoot, err := pack(t, root, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "detected the alpine variant") {
		t.Errorf("the variant was not detected from the rootfs:\n%s", out)
	}
	if err := guestimage.Check(stagingRoot); err != nil {
		t.Errorf("the packed guest half does not check out: %v", err)
	}
	// OpenRC init scripts are run by path, so they need their bit too.
	if got := mode(t, filepath.Join(root, "etc/init.d/granule-agent")); got != 0o755 {
		t.Errorf("/etc/init.d/granule-agent is mode %04o, want 0755", got)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/granule-agent.service")); err == nil {
		t.Error("the debian overlay's units were laid over an alpine rootfs")
	}
}

// The declaration goes beside the packed disk as well as inside it,
// because that copy is the only one a boot can read: there is no image
// to inspect at boot and no mounting the disk to look in it. A pack that
// laid today's guest side down and left the sidecar saying what the
// consumer's base said would get its own output refused at boot, for the
// skew it had just removed.
func TestPackGuestDeclaresThePackersContractBesideTheDisk(t *testing.T) {
	requireMke2fs(t)
	root, partsDir := rootfs(t, "debian"), parts(t)

	// What a rootfs built FROM an old granule guest-rootfs image carries.
	write(t, filepath.Join(root, strings.TrimPrefix(guestimage.ContractFile, "/")), "0\n")

	out, stagingRoot, err := pack(t, root, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	t.Log(out)

	inGuest := read(t, filepath.Join(root, strings.TrimPrefix(guestimage.ContractFile, "/")))
	sidecar := filepath.Join(staged(stagingRoot), guestimage.ContractSidecarName)
	if got := read(t, sidecar); got != inGuest {
		t.Errorf("the declaration beside the disk is\n%s\nwant the same bytes as the one in it:\n%s", got, inGuest)
	}

	declared, err := guestimage.ReadBootContract(filepath.Join(staged(stagingRoot), "disk.img"))
	if err != nil {
		t.Fatalf("reading the packed guest's declaration: %v", err)
	}
	if declared.Version != guestimage.Contract {
		t.Errorf("the packed guest declares version %d, want this granule's %d", declared.Version, guestimage.Contract)
	}
	if err := declared.Check(guestimage.MinimumBootContract); err != nil {
		t.Errorf("a guest this granule just packed would not boot under it: %v", err)
	}
}

func TestPackGuestHeadroomIsTheSameKnob(t *testing.T) {
	requireMke2fs(t)
	root, partsDir := rootfs(t, "debian"), parts(t)
	out, stagingRoot, err := pack(t, root, partsDir, "GUEST_DISK_EXTRA_MB=128")
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	assertSized(t, root, filepath.Join(staged(stagingRoot), "disk.img"), 128)
}

func TestPackGuestHeadroomInvalid(t *testing.T) {
	requireMke2fs(t)
	root, partsDir := rootfs(t, "debian"), parts(t)
	out, _, err := pack(t, root, partsDir, "GUEST_DISK_EXTRA_MB=notanumber")
	if err == nil {
		t.Fatalf("pack-guest succeeded with invalid GUEST_DISK_EXTRA_MB, want refusal:\n%s", out)
	}
	if !strings.Contains(out, "GUEST_DISK_EXTRA_MB must be a whole number of MiB") {
		t.Errorf("refusal does not mention GUEST_DISK_EXTRA_MB requirement:\n%s", out)
	}
}

// A guest that brings its own kernel publishes it beside the disk, and
// only as a pair: guestimage.Check refuses one without the other,
// because a vmlinuz booted without its initramfs produces a guest that
// never becomes reachable rather than a boot that fails.
func TestPackGuestStagesTheGuestsOwnKernel(t *testing.T) {
	requireMke2fs(t)
	root, partsDir := rootfs(t, "debian"), parts(t)
	write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-18-amd64"), "kernel\n")
	write(t, filepath.Join(root, "boot/initrd.img-6.1.0-18-amd64"), "initramfs\n")
	// An older pair, to check the newest is the one that travels.
	write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-9-amd64"), "older kernel\n")
	write(t, filepath.Join(root, "boot/initrd.img-6.1.0-9-amd64"), "older initramfs\n")

	out, stagingRoot, err := pack(t, root, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	if err := guestimage.Check(stagingRoot); err != nil {
		t.Errorf("the packed guest half does not check out: %v", err)
	}
	if _, hasKernel := guestimage.Present(stagingRoot); !hasKernel {
		t.Error("the guest's own kernel was not staged")
	}
	if got := read(t, guestimage.Under(stagingRoot, guestimage.GuestKernel)); got != "kernel\n" {
		t.Errorf("staged kernel is %q, want the newest one", got)
	}
}

func TestPackGuestRefusesABrokenRootfs(t *testing.T) {
	requireMke2fs(t)
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, root string)
		want string
	}{
		{
			// The pair again, from the other side: a kernel whose
			// initramfs is missing is the failure that looks like a
			// working image until the VM never answers.
			name: "a kernel without its initramfs",
			edit: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-18-amd64"), "kernel\n")
			},
			want: "no /boot/initrd.img-6.1.0-18-amd64",
		},
		{
			name: "no resize2fs",
			edit: func(t *testing.T, root string) { os.Remove(filepath.Join(root, "sbin/resize2fs")) },
			want: "granule-growfs runs resize2fs",
		},
		{
			name: "no modprobe",
			edit: func(t *testing.T, root string) { os.Remove(filepath.Join(root, "sbin/modprobe")) },
			want: "no modprobe",
		},
		{
			name: "no ip",
			edit: func(t *testing.T, root string) { os.Remove(filepath.Join(root, "sbin/ip")) },
			want: "no ip",
		},
		{
			name: "no acpid",
			edit: func(t *testing.T, root string) { os.Remove(filepath.Join(root, "sbin/acpid")) },
			want: "no acpid",
		},
		{
			name: "neither init system",
			edit: func(t *testing.T, root string) { os.Remove(filepath.Join(root, "lib/systemd/systemd")) },
			want: "runs no init system granule can use",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, partsDir := rootfs(t, "debian"), parts(t)
			tc.edit(t, root)
			out, stagingRoot, err := pack(t, root, partsDir)
			if err == nil {
				t.Fatalf("pack-guest succeeded, want a refusal:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("refusal does not mention %q:\n%s", tc.want, out)
			}
			if _, err := os.Stat(guestimage.Under(stagingRoot, guestimage.DiskImage)); err == nil {
				t.Error("a refused pack left a disk image behind")
			}
		})
	}
}

// TestPackGuestSaysWhichKindOfBaseItWasGiven: `granule guest build`
// lets a Dockerfile name any FROM, because a reference is a string;
// this script is the layer that can look inside, so it is where the
// three kinds of base are told apart. Each says which it is, because
// the fix differs -- rebuild on a guest rootfs, install an init, or
// start again from an image that is a machine at all.
func TestPackGuestSaysWhichKindOfBaseItWasGiven(t *testing.T) {
	requireMke2fs(t)
	for _, tc := range []struct {
		name string
		// build returns the rootfs tree standing in for that kind of
		// image.
		build func(t *testing.T) string
		// want is empty for a base that packs.
		want string
		// fix is what the refusal has to say to do instead; empty means
		// "build FROM granule's own guest rootfs image", which is the
		// answer for all but the already-packed one.
		fix string
	}{{
		name:  "a granule guest rootfs",
		build: func(t *testing.T) string { return rootfs(t, "debian") },
	}, {
		// scratch, a binary-only image, a toolchain's artifact: there
		// is no machine in it to boot.
		name:  "not an OS at all",
		build: func(t *testing.T) string { return t.TempDir() },
		want:  "no operating system in it at all",
	}, {
		// debian:13 and friends: an OS, but one that has never been
		// through the customisation that makes it bootable as a guest.
		name: "a plain OS image with no init",
		build: func(t *testing.T) string {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "bin/sh"), "sh\n")
			write(t, filepath.Join(dir, "etc/os-release"), "ID=debian\n")
			return dir
		},
		want: "runs no init system granule can use",
	}, {
		// The same thing one step further on: an init granule knows, and
		// nothing of granule's enabled against it.
		// The near miss: an image that has already been packed. There is
		// no tree in it to lay parts over, and saying "no init system"
		// about it would describe the wrong problem.
		name: "a granule image that is already packed",
		build: func(t *testing.T) string {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "usr/local/bin/granule-shim"), "kontur\n")
			write(t, guestimage.Under(dir, guestimage.DiskImage), "a packed disk\n")
			return dir
		},
		want: "a packed granule image, not a guest rootfs",
		fix:  "granule guest rebase",
	}, {
		// An init granule knows, and none of the packages granule's own
		// guest-side half runs: no iproute2 for the address and routes,
		// no acpid for a clean shutdown. It would pack and boot and then
		// behave as if granule were broken.
		name: "a plain OS image running systemd",
		build: func(t *testing.T) string {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "etc/os-release"), "ID=debian\n")
			write(t, filepath.Join(dir, "lib/systemd/systemd"), "systemd\n")
			write(t, filepath.Join(dir, "sbin/resize2fs"), "resize2fs\n")
			write(t, filepath.Join(dir, "sbin/modprobe"), "modprobe\n")
			return dir
		},
		want: "has never been through granule's guest customisation",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			out, stagingRoot, err := pack(t, tc.build(t), parts(t))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("pack-guest refused a guest rootfs: %v\n%s", err, out)
				}
				if err := guestimage.Check(stagingRoot); err != nil {
					t.Errorf("the packed guest half does not check out: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("pack-guest packed a base it cannot boot:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("refusal does not say which kind of base this is (%q):\n%s", tc.want, out)
			}
			// And every refusal says what to do instead, since that is
			// the reader's next question in every one of these cases.
			fix := tc.fix
			if fix == "" {
				fix = "guest-rootfs-image"
			}
			if !strings.Contains(out, fix) {
				t.Errorf("refusal does not say what to do instead (%q):\n%s", fix, out)
			}
		})
	}
}

// TestPackGuestRelaysGranuleSHalfOverAnOlderGuest is the rebase
// equivalence, at the layer that does the work: a `FROM <guest image>`
// with nothing after it means "take this image's payload, discard
// granule's half, re-lay the current one", and what makes that true is
// that pack lays its own parts over the rootfs before measuring it.
//
// The rootfs here is a guest whose granule half is stale in every file --
// which is what a guest image published months ago is -- and the
// assertion is that none of it survives the pack.
func TestPackGuestRelaysGranuleSHalfOverAnOlderGuest(t *testing.T) {
	requireMke2fs(t)
	root, partsDir := rootfs(t, "debian"), parts(t)

	// Every file granule's own half owns, as it was in some older build.
	stale := map[string]string{}
	for _, overlay := range []string{"overlay-common", "overlay-debian"} {
		for _, rel := range treeFiles(t, filepath.Join(partsDir, overlay)) {
			stale["/"+rel] = read(t, filepath.Join(partsDir, overlay, rel))
			write(t, filepath.Join(root, rel), "a granule from months ago\n")
		}
	}
	for _, agent := range []string{"granule-agent", "granule-mem-agent"} {
		rel := "usr/local/bin/" + agent
		stale["/"+rel] = read(t, filepath.Join(partsDir, agent))
		write(t, filepath.Join(root, rel), "an old "+agent+"\n")
	}
	if len(stale) < 6 {
		t.Fatalf("the parts directory holds only %d files; this test has lost its subject", len(stale))
	}

	out, stagingRoot, err := pack(t, root, partsDir)
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	if err := guestimage.Check(stagingRoot); err != nil {
		t.Errorf("the packed guest half does not check out: %v", err)
	}
	for path, want := range stale {
		if got := read(t, filepath.Join(root, strings.TrimPrefix(path, "/"))); got != want {
			t.Errorf("%s in the packed rootfs is the image's own copy, not this pack's:\n got %q\nwant %q", path, got, want)
		}
	}

	// Loud, once. Replacing every file granule owns is the *normal* case
	// for a repack of an older guest, so a line per file would be twenty
	// lines that nobody reads on every build of this shape.
	if n := strings.Count(out, "replacing"); n != 1 {
		t.Errorf("a wholesale re-lay printed %d announcements, want one summary:\n%s", n, out)
	}
	if !strings.Contains(out, fmt.Sprintf("replacing %d files", len(stale))) {
		t.Errorf("the summary does not say how many files were replaced:\n%s", out)
	}
	if !strings.Contains(out, "GRANULE_PACK_VERBOSE=1") {
		t.Errorf("the summary does not say how to see the whole list:\n%s", out)
	}

	// And the whole list is there for anyone who asks for it.
	root = rootfs(t, "debian")
	for path := range stale {
		write(t, filepath.Join(root, strings.TrimPrefix(path, "/")), "a granule from months ago\n")
	}
	verbose, _, err := pack(t, root, partsDir, "GRANULE_PACK_VERBOSE=1")
	if err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, verbose)
	}
	for path := range stale {
		if !strings.Contains(verbose, "replacing "+path+",") {
			t.Errorf("the verbose list does not name %s:\n%s", path, verbose)
		}
	}
}

// granule-ready is the one service the overlay ships without enabling:
// it is installed everywhere and enabled only where something named a
// path for it to create. The Dockerfile's GUEST_READY_FILE writes that
// path into /etc/granule/ready-file and stops there; the enabling is the
// pack's, against the file it finds.
func TestPackGuestEnablesReadyOnlyWhereAPathWasDeclared(t *testing.T) {
	requireMke2fs(t)
	for _, variant := range []string{"debian", "alpine"} {
		t.Run(variant, func(t *testing.T) {
			installed, enabled := readyPaths(variant)

			root, partsDir := rootfs(t, variant), parts(t)
			out, stagingRoot, err := pack(t, root, partsDir)
			if err != nil {
				t.Fatalf("pack-guest: %v\n%s", err, out)
			}
			if err := guestimage.Check(stagingRoot); err != nil {
				t.Errorf("the packed guest half does not check out: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, installed)); err != nil {
				t.Errorf("granule-ready was not installed: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, enabled)); err == nil {
				t.Errorf("granule-ready was enabled in a guest that declared no ready file: such a VM would be waited on for a declaration nothing makes")
			}

			// And the same rootfs with a declaration in it.
			declared := rootfs(t, variant)
			write(t, filepath.Join(declared, "etc/granule/ready-file"), "/run/granule/ready\n")
			if out, _, err := pack(t, declared, partsDir); err != nil {
				t.Fatalf("pack-guest: %v\n%s", err, out)
			}
			if _, err := os.Lstat(filepath.Join(declared, enabled)); err != nil {
				t.Errorf("granule-ready was not enabled in a guest carrying %s: %v", "/etc/granule/ready-file", err)
			}
		})
	}
}

// readyPaths is where granule-ready is installed and where enabling it
// puts a symlink, for each variant.
func readyPaths(variant string) (installed, enabled string) {
	if variant == "alpine" {
		return "etc/init.d/granule-ready", "etc/runlevels/default/granule-ready"
	}
	return "etc/systemd/system/granule-ready.service",
		"etc/systemd/system/sysinit.target.wants/granule-ready.service"
}

// -lay-only is the half of a pack that `granule guest rebase -boot`
// runs inside a booted guest: granule's own pieces go over a rootfs that
// is already a mounted filesystem, and no disk is made. Same script,
// same overlays, same refusals -- which is the point of it being a mode
// of this rather than a second implementation in Go.
//
// It needs no mke2fs either, so unlike every test above it runs
// everywhere.
func TestPackGuestLaysPartsDownWithoutPacking(t *testing.T) {
	root, partsDir := rootfs(t, "debian"), parts(t)
	stale := filepath.Join(root, "usr/local/bin/granule-agent")
	write(t, stale, "a granule-agent from months ago\n")

	cmd := exec.Command("/bin/sh", packScript, "-lay-only", root, partsDir)
	cmd.Env = []string{"PATH=" + packPath()}
	combined, err := cmd.CombinedOutput()
	out := string(combined)
	if err != nil {
		t.Fatalf("pack-guest -lay-only: %v\n%s", err, out)
	}
	t.Log(out)

	if got := read(t, stale); got != read(t, filepath.Join(partsDir, "granule-agent")) {
		t.Errorf("the agent was not replaced with the one being laid down: %q", got)
	}
	if !strings.Contains(out, "replacing /usr/local/bin/granule-agent") {
		t.Errorf("replacing the consumer's agent was not announced:\n%s", out)
	}
	if !strings.Contains(out, "no disk was packed") {
		t.Errorf("-lay-only did not say it produced no disk:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/granule-agent.service")); err != nil {
		t.Errorf("the overlay was not laid down: %v", err)
	}
	// Written by rename, because the same operation against a running
	// guest is replacing a program that is executing. The temporary name
	// is an implementation detail of that, and it is not something to
	// leave in an image.
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".granule-new") {
			t.Errorf("%s was left behind by the rename", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// -lay-only enables the services it lays down, the same as a pack does.
// That is the backstop against a guest built months ago under an older
// granule, which is exactly the guest a rebase turns up: a unit added
// since has no enabling symlink in it, and a rebase that only replaced
// files would leave the new one installed and never started.
func TestPackGuestLayOnlyEnablesTheServicesItLaysDown(t *testing.T) {
	root, partsDir := rootfs(t, "debian"), parts(t)

	cmd := exec.Command("/bin/sh", packScript, "-lay-only", root, partsDir)
	cmd.Env = []string{"PATH=" + packPath()}
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pack-guest -lay-only: %v\n%s", err, combined)
	}
	assertServicesEnabled(t, root, "debian")
}

// The script decides the overlay's modes by a rule about where a file
// sits (libexec helpers, init scripts and *.sh are programs; everything
// else is read), which is a rule that can be right about the wrong set.
// So this asserts it against the one thing a file says about itself: a
// program starts with a #! line and a unit file does not. Two
// independent statements, which have to agree.
func TestPackGuestMakesExactlyTheProgramsExecutable(t *testing.T) {
	requireMke2fs(t)
	partsDir := parts(t)
	// Both variants, since each lays down an overlay of its own.
	for _, variant := range []string{"debian", "alpine"} {
		root := rootfs(t, variant)
		if out, _, err := pack(t, root, partsDir); err != nil {
			t.Fatalf("pack-guest: %v\n%s", err, out)
		}
		checked := 0
		for _, overlay := range []string{"overlay-common", "overlay-" + variant} {
			for _, rel := range treeFiles(t, filepath.Join(partsDir, overlay)) {
				want := os.FileMode(0o644)
				if strings.HasPrefix(read(t, filepath.Join(partsDir, overlay, rel)), "#!") {
					want = 0o755
				}
				if got := mode(t, filepath.Join(root, rel)); got != want {
					t.Errorf("%s: /%s is mode %04o, want %04o (a file with a #! line is a program something execs by path; one without is read)", variant, rel, got, want)
				}
				checked++
			}
		}
		if checked == 0 {
			t.Fatalf("the %s overlays hold no files; this test has lost its subject", variant)
		}
	}

	// And the agents, which are not in an overlay tree and are programs
	// whatever they start with.
	root := rootfs(t, "debian")
	if out, _, err := pack(t, root, partsDir); err != nil {
		t.Fatalf("pack-guest: %v\n%s", err, out)
	}
	for _, agent := range []string{"granule-agent", "granule-mem-agent"} {
		if got := mode(t, filepath.Join(root, "usr/local/bin", agent)); got != 0o755 {
			t.Errorf("/usr/local/bin/%s is mode %04o, want 0755", agent, got)
		}
	}
}

// Where each of granule's services is enabled, asserted against the
// place the init system reads rather than against pack-guest's own
// answer: a symlink in a directory nothing looks in is a service that
// never starts, and it is indistinguishable from a working one until a
// guest boots.
//
// The expectations are written out here rather than derived from the
// overlay, because the overlay is where the script reads them from --
// deriving them would be the same statement twice. Adding a service to
// deploy/guest-image means adding a line here, and that is the point.
func TestPackGuestEnablesEachServiceWhereItBelongs(t *testing.T) {
	requireMke2fs(t)
	for _, variant := range []string{"debian", "alpine"} {
		t.Run(variant, func(t *testing.T) {
			root, partsDir := rootfs(t, variant), parts(t)
			if out, _, err := pack(t, root, partsDir); err != nil {
				t.Fatalf("pack-guest: %v\n%s", err, out)
			}
			assertServicesEnabled(t, root, variant)
		})
	}
}

// wantEnabled is every service granule's overlay installs, and where a
// pack has to enable it. granule-agent is the one whose placement is the
// point rather than a detail: it answers "granule exec", so it is
// reached in early boot (sysinit.target on systemd, the "boot" runlevel
// on OpenRC) and not at the end of one, because a guest is worth being
// able to exec into precisely when something later has gone wrong.
//
// granule-ready and granule-container are absent: they are enabled
// against a file in the guest rather than unconditionally, and have
// tests of their own.
var wantEnabled = map[string]map[string]string{
	"debian": {
		"granule-agent.service":         "sysinit.target.wants",
		"granule-configure-dns.service": "sysinit.target.wants",
		"granule-growfs.service":        "sysinit.target.wants",
		"granule-mem-online.service":    "sysinit.target.wants",
		"granule-net-cmdline.service":   "sysinit.target.wants",
		"granule-net-routes.service":    "sysinit.target.wants",
		"granule-net-ip6.service":       "sysinit.target.wants",
		"granule-control-net.service":   "multi-user.target.wants",
		"granule-mem-agent.service":     "multi-user.target.wants",
	},
	"alpine": {
		"granule-agent":         "boot",
		"granule-configure-dns": "boot",
		"granule-growfs":        "boot",
		"granule-mem-online":    "boot",
		"granule-net-routes":    "boot",
		"granule-net-ip6":       "boot",
		"granule-control-net":   "default",
		"granule-mem-agent":     "default",
	},
}

func assertServicesEnabled(t *testing.T, root, variant string) {
	t.Helper()
	want := wantEnabled[variant]
	// Every service the overlay ships is accounted for above, so a
	// service added to deploy/guest-image cannot slip in unenabled and
	// unnoticed.
	for _, service := range services(t, variant) {
		if _, ok := want[service]; !ok {
			t.Errorf("the %s overlay installs %s and this test does not say where it should be enabled", variant, service)
		}
	}
	for service, where := range want {
		var link, target string
		switch variant {
		case "debian":
			link = filepath.Join(root, "etc/systemd/system", where, service)
			target = "/etc/systemd/system/" + service
		case "alpine":
			link = filepath.Join(root, "etc/runlevels", where, service)
			target = "/etc/init.d/" + service
		}
		got, err := os.Readlink(link)
		if err != nil {
			t.Errorf("%s is not enabled in %s: %v", service, where, err)
			continue
		}
		if got != target {
			t.Errorf("%s in %s points at %q, want %q", service, where, got, target)
		}
	}
}

// The change this all exists for: a rootfs with none of granule's parts
// in it packs into the same guest as one that already carried them,
// which is what makes taking them out of the Dockerfile's rootfs stages
// safe. The comparison is of the packed trees, not of the rootfs images,
// because the packed tree is the artifact that has to be unchanged.
func TestPackGuestFromAPartsFreeRootfsMatchesOneThatCarriedThem(t *testing.T) {
	requireMke2fs(t)
	for _, variant := range []string{"debian", "alpine"} {
		t.Run(variant, func(t *testing.T) {
			partsDir := parts(t)

			fresh := rootfs(t, variant)
			if out, _, err := pack(t, fresh, partsDir); err != nil {
				t.Fatalf("pack-guest: %v\n%s", err, out)
			}

			// A rootfs as the old guest-rootfs-* stages built one: the
			// overlay and the agents already in it, and the services
			// already enabled -- which is also every guest published
			// before this change, so this is the compatibility case as
			// much as it is the equivalence one.
			prebuilt := rootfs(t, variant)
			for _, overlay := range []string{"overlay-common", "overlay-" + variant} {
				for _, rel := range treeFiles(t, filepath.Join(partsDir, overlay)) {
					write(t, filepath.Join(prebuilt, rel), read(t, filepath.Join(partsDir, overlay, rel)))
				}
			}
			for _, agent := range []string{"granule-agent", "granule-mem-agent"} {
				write(t, filepath.Join(prebuilt, "usr/local/bin", agent), read(t, filepath.Join(partsDir, agent)))
			}
			for service, where := range wantEnabled[variant] {
				switch variant {
				case "debian":
					enable(t, filepath.Join(prebuilt, "etc/systemd/system", where, service), "/etc/systemd/system/"+service)
				case "alpine":
					enable(t, filepath.Join(prebuilt, "etc/runlevels", where, service), "/etc/init.d/"+service)
				}
			}
			if out, _, err := pack(t, prebuilt, partsDir); err != nil {
				t.Fatalf("pack-guest over a rootfs that already carried the parts: %v\n%s", err, out)
			}

			if diff := treeDiff(t, fresh, prebuilt); diff != "" {
				t.Errorf("the two packed rootfs trees differ:\n%s", diff)
			}
		})
	}
}

// treeDiff describes how two rootfs trees differ in the things a boot
// can tell apart: which paths exist, what mode each carries, and what
// each file or symlink holds.
func treeDiff(t *testing.T, a, b string) string {
	t.Helper()
	var out []string
	for path, mode := range treeShape(t, a) {
		other, ok := treeShape(t, b)[path]
		if !ok {
			out = append(out, fmt.Sprintf("only in the parts-free pack: %s", path))
			continue
		}
		if mode != other {
			out = append(out, fmt.Sprintf("%s: %s in the parts-free pack, %s in the other", path, mode, other))
		}
	}
	for path := range treeShape(t, b) {
		if _, ok := treeShape(t, a)[path]; !ok {
			out = append(out, fmt.Sprintf("only in the pack over a rootfs that carried the parts: %s", path))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// treeShape maps every path under dir to a description of what is there.
func treeShape(t *testing.T, dir string) map[string]string {
	t.Helper()
	shape := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			shape[rel] = "symlink to " + target
		case info.IsDir():
			shape[rel] = "directory"
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			shape[rel] = fmt.Sprintf("file, mode %04o, %d bytes, sum %x", info.Mode().Perm(), len(data), sha256.Sum256(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return shape
}

// The other half of the same statement, made where it cannot be made by
// packing: the Dockerfile's rootfs stages build an operating system and
// lay none of granule's own parts into it. Asserted on the text rather
// than on a built image because building one is a debootstrap and a
// gigabyte, and because what this is guarding against is an edit -- a
// COPY of the overlay put back "just for the bundled guest", which would
// quietly restore the two-places-to-change it took a change to remove.
func TestDockerfileRootfsStagesLayNoGranuleParts(t *testing.T) {
	source, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	stages := dockerfileStages(t, string(source))
	for _, name := range []string{"guest-rootfs-debian", "guest-rootfs-alpine"} {
		body, ok := stages[name]
		if !ok {
			t.Fatalf("the Dockerfile has no %s stage; this test has lost its subject", name)
		}
		for _, banned := range []struct{ what, why string }{
			{"overlay-common", "the overlay trees go on at pack time"},
			{"overlay-debian", "the overlay trees go on at pack time"},
			{"overlay-alpine", "the overlay trees go on at pack time"},
			{"granule-agent", "the agents go on at pack time, from the granule doing the packing"},
			{"granule-mem-agent", "the agents go on at pack time, from the granule doing the packing"},
			{"target.wants", "enabling granule's units is the pack's job, beside the units themselves"},
			{"granule-growfs", "granule's own services go on at pack time"},
		} {
			if strings.Contains(body, banned.what) {
				t.Errorf("the %s stage mentions %q: %s (see deploy/guest-image/pack-guest)", name, banned.what, banned.why)
			}
		}
	}

	// And the bundled guest is packed by that same script, rather than
	// by a second `mke2fs -d` written out here.
	body, ok := stages["guest-image"]
	if !ok {
		t.Fatal("the Dockerfile has no guest-image stage")
	}
	if !strings.Contains(body, "pack-guest") {
		t.Error("the guest-image stage does not run pack-guest: the guest this image bundles is packed by something other than the thing every other guest is packed by")
	}
}

// dockerfileStages splits a Dockerfile into the body of each named
// stage, comments excluded -- a comment is where this change's reasoning
// is written down, and naming the files it stopped copying is exactly
// what those comments do.
func dockerfileStages(t *testing.T, source string) map[string]string {
	t.Helper()
	stages := map[string]string{}
	name := ""
	var body []string
	flush := func() {
		if name != "" {
			stages[name] = strings.Join(body, "\n")
		}
	}
	from := regexp.MustCompile(`^FROM\s+\S+\s+AS\s+(\S+)`)
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if m := from.FindStringSubmatch(line); m != nil {
			flush()
			name, body = m[1], nil
			continue
		}
		body = append(body, line)
	}
	flush()
	if len(stages) == 0 {
		t.Fatal("no stages found in the Dockerfile")
	}
	return stages
}

func TestPackGuestAutoEnablesGranuleContainerWhenSpecPresent(t *testing.T) {
	requireMke2fs(t)
	partsDir := parts(t)

	for _, variant := range []string{"debian", "alpine"} {
		t.Run(variant, func(t *testing.T) {
			root := rootfs(t, variant)
			// Write container.json
			specPath := filepath.Join(root, "etc/granule/container.json")
			write(t, specPath, "{}")

			out, _, err := pack(t, root, partsDir)
			if err != nil {
				t.Fatalf("pack-guest: %v\n%s", err, out)
			}

			// Verify granule-container binary is present and executable
			binPath := filepath.Join(root, "usr/local/bin/granule-container")
			if m := mode(t, binPath); m != 0o755 {
				t.Errorf("granule-container mode = %04o, want 0755", m)
			}

			// Verify service is enabled
			switch variant {
			case "debian":
				link := filepath.Join(root, "etc/systemd/system/multi-user.target.wants/granule-container.service")
				if _, err := os.Lstat(link); err != nil {
					t.Errorf("granule-container.service symlink missing: %v", err)
				}
			case "alpine":
				link := filepath.Join(root, "etc/runlevels/default/granule-container")
				if _, err := os.Lstat(link); err != nil {
					t.Errorf("granule-container openrc symlink missing: %v", err)
				}
			}
		})
	}
}

// TestPackGuestLeavesGranuleContainerAloneWhenAWrapStartsIt: a wrap
// enables the workload itself, under the service name the author gave
// it and with the restart policy they asked for. granule-container runs
// the same /etc/granule/container.json, so enabling it beside the wrap
// would start the workload twice -- two copies of a server racing for
// the port the first one bound, with the named unit the one that loses
// and start-limits. A rootfs that already starts its workload is left
// as it is.
func TestPackGuestDoesNotEnableGranuleContainerBesideAWrap(t *testing.T) {
	requireMke2fs(t)
	partsDir := parts(t)

	for _, variant := range []string{"debian", "alpine"} {
		t.Run(variant, func(t *testing.T) {
			root := rootfs(t, variant)
			write(t, filepath.Join(root, "etc/granule/container.json"), "{}")
			switch variant {
			case "debian":
				enable(t, filepath.Join(root, "etc/systemd/system/multi-user.target.wants/granule-wrap@redis.service"),
					"/etc/systemd/system/granule-wrap@.service")
			case "alpine":
				enable(t, filepath.Join(root, "etc/runlevels/default/granule-wrap"), "/etc/init.d/granule-wrap")
			}

			out, _, err := pack(t, root, partsDir)
			if err != nil {
				t.Fatalf("pack-guest: %v\n%s", err, out)
			}

			var link string
			switch variant {
			case "debian":
				link = filepath.Join(root, "etc/systemd/system/multi-user.target.wants/granule-container.service")
			case "alpine":
				link = filepath.Join(root, "etc/runlevels/default/granule-container")
			}
			if _, err := os.Lstat(link); err == nil {
				t.Errorf("%s was enabled beside the wrap's own service: the guest would run the workload twice", link)
			}
		})
	}
}

// assertSized checks the disk against the sizing the Dockerfile's
// guest-image stage uses: the rootfs plus 20%, plus a 64MiB floor,
// plus the headroom asked for.
func assertSized(t *testing.T, root, disk string, extraMB int) {
	t.Helper()
	out, err := exec.Command("du", "-sk", root).Output()
	if err != nil {
		t.Fatalf("du: %v", err)
	}
	sizeKB, err := strconv.ParseInt(strings.Fields(string(out))[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	want := (sizeKB*12/10 + 65536 + int64(extraMB)*1024) * 1024
	info, err := os.Stat(disk)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != want {
		t.Errorf("disk is %d bytes, want %d (%dK of rootfs + 20%% + 64MiB + %dMiB)", info.Size(), want, sizeKB, extraMB)
	}
}

func staged(stagingRoot string) string {
	return guestimage.Under(stagingRoot, guestimage.Dir)
}

func treeFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(fmt.Errorf("%s: %w", path, err))
	}
	return info.Mode().Perm()
}

func TestPackGuestMasksUnnecessaryDebianUnits(t *testing.T) {
	root, partsDir := rootfs(t, "debian"), parts(t)
	cmd := exec.Command("/bin/sh", packScript, "-lay-only", root, partsDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack-guest -lay-only failed: %v\n%s", err, out)
	}

	maskedUnits := []string{
		"systemd-logind.service",
		"getty.target",
		"systemd-timesyncd.service",
		"apt-daily.timer",
		"apt-daily-upgrade.timer",
		"dpkg-db-backup.timer",
		"e2scrub_all.timer",
		"fstrim.timer",
	}

	for _, unit := range maskedUnits {
		p := filepath.Join(root, "etc/systemd/system", unit)
		target, err := os.Readlink(p)
		if err != nil {
			t.Errorf("expected %s to be a symlink masking the unit: %v", unit, err)
			continue
		}
		if target != "/dev/null" {
			t.Errorf("%s symlink target = %q, want /dev/null", unit, target)
		}
	}
}

func TestGranuleDeclareReadyCustomCondition(t *testing.T) {
	scriptPath := filepath.Join(overlaySrc, "overlay-common/usr/local/libexec/granule-declare-ready")
	readyPath := filepath.Join(t.TempDir(), "ready")
	confPath := filepath.Join(t.TempDir(), "ready-file")
	condPath := filepath.Join(t.TempDir(), "ready-condition")
	gateFile := filepath.Join(t.TempDir(), "gate")

	write(t, confPath, readyPath+"\n")
	write(t, condPath, fmt.Sprintf("test -f %s\n", gateFile))

	// 1. Condition fails when gateFile does not exist
	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"GRANULE_READY_FILE_CONF="+confPath,
		"GRANULE_READY_CONDITION_CONF="+condPath,
	)
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected granule-declare-ready to fail when custom condition fails")
	}
	if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
		t.Fatalf("ready file should not exist when condition failed")
	}

	// 2. Condition passes when gateFile exists
	write(t, gateFile, "ready\n")
	cmd = exec.Command("/bin/sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"GRANULE_READY_FILE_CONF="+confPath,
		"GRANULE_READY_CONDITION_CONF="+condPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("granule-declare-ready failed unexpectedly: %v\n%s", err, out)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("ready file was not created: %v", err)
	}
}
