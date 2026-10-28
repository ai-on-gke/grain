package guestwrap

// Tests for WrapDir, the wrap that needs no docker. None of them needs a
// daemon, root or (except the last) e2fsprogs: the disk is made by a
// stand-in mke2fs that writes the tree it was given as a tar, so what
// went into the disk can be read back file by file.

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/guestimage"
)

const guestImageSrc = "../../deploy/guest-image"

// fakeMke2fs puts a mke2fs first on PATH that archives its -d tree into
// the image file instead of making a filesystem.
func fakeMke2fs(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
dir=
while [ $# -gt 1 ]; do
	case "$1" in
	-d) dir=$2; shift ;;
	esac
	shift
done
exec tar -C "$dir" -cf "$1" .
`
	if err := os.WriteFile(filepath.Join(bin, "mke2fs"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func writeFile(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// guestParts is a guest-parts directory as a granule image carries it:
// deploy/guest-image's overlays and pack-guest, and stand-ins for the
// programs.
func guestParts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"overlay-common", "overlay-debian", "overlay-alpine", "pack-guest"} {
		if out, err := exec.Command("cp", "-a", filepath.Join(guestImageSrc, name), filepath.Join(dir, name)).CombinedOutput(); err != nil {
			t.Fatalf("copying %s: %v: %s", name, err, out)
		}
	}
	for _, program := range []string{"granule-agent", "granule-mem-agent", "granule-container"} {
		writeFile(t, filepath.Join(dir, program), "#!/bin/sh\n# stands in for "+program+"\n", 0o755)
	}
	return dir
}

// guestRootfs is enough of a Debian guest rootfs for pack-guest.
func guestRootfs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, program := range []string{"resize2fs", "modprobe", "ip", "acpid"} {
		writeFile(t, filepath.Join(dir, "sbin", program), program+"\n", 0o755)
	}
	writeFile(t, filepath.Join(dir, "lib/systemd/systemd"), "systemd\n", 0o755)
	// A guest image that arrived with granule-container enabled, which
	// the wrap service replaces.
	if err := os.MkdirAll(filepath.Join(dir, "etc/systemd/system/multi-user.target.wants"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/systemd/system/granule-container.service", filepath.Join(dir, "etc/systemd/system/multi-user.target.wants/granule-container.service")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// wrappedImage is an image's filesystem and OCI config blob. Its sh and
// mke2fs write a marker if anything ever runs them.
func wrappedImage(t *testing.T) (rootfs, config, marker string) {
	t.Helper()
	rootfs = t.TempDir()
	marker = filepath.Join(t.TempDir(), "executed")
	trap := "#!/bin/sh\ntouch " + marker + "\n"
	for _, p := range []string{"bin/sh", "sbin/mke2fs", "usr/sbin/mke2fs", "usr/bin/tar"} {
		writeFile(t, filepath.Join(rootfs, p), trap, 0o755)
	}
	writeFile(t, filepath.Join(rootfs, "app/server"), "the workload\n", 0o755)
	if err := os.Chmod(filepath.Join(rootfs, "app/server"), 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(rootfs, "app/server"), filepath.Join(rootfs, "app/server-again")); err != nil {
		t.Fatal(err)
	}
	// A link out of the tree, which stays a link in the guest.
	if err := os.Symlink("/etc/shadow", filepath.Join(rootfs, "app/escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootfs, "readonly"), 0o555); err != nil {
		t.Fatal(err)
	}

	config = filepath.Join(t.TempDir(), "config.json")
	blob := `{
  "architecture": "amd64",
  "os": "linux",
  "config": {
    "Entrypoint": ["/app/server"],
    "Cmd": ["--port", "8080"],
    "Env": ["PATH=/app:/usr/bin", "MODE=prod"],
    "WorkingDir": "/app",
    "User": "1000",
    "ExposedPorts": {"8080/tcp": {}, "not a port": {}},
    "Volumes": {"/data": {}}
  },
  "rootfs": {"type": "layers", "diff_ids": []}
}`
	writeFile(t, config, blob, 0o644)
	return rootfs, config, marker
}

func dirOptions(t *testing.T) (DirOptions, string) {
	t.Helper()
	rootfs, config, marker := wrappedImage(t)
	kernel := filepath.Join(t.TempDir(), "vmlinux")
	writeFile(t, kernel, "kernel\n", 0o644)
	return DirOptions{
		Rootfs:        rootfs,
		Config:        config,
		Image:         "example.com/app:1",
		GuestRootfs:   guestRootfs(t),
		Parts:         guestParts(t),
		RuntimeKernel: kernel,
		Out:           filepath.Join(t.TempDir(), "guest"),
		WorkDir:       t.TempDir(),
		Unprivileged:  true,
		Env:           []string{"MODE=debug"},
	}, marker
}

type diskEntry struct {
	hdr  *tar.Header
	data string
}

func readDisk(t *testing.T, disk string) map[string]diskEntry {
	t.Helper()
	f, err := os.Open(disk)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries := map[string]diskEntry{}
	r := tar.NewReader(f)
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatalf("reading the disk: %v", err)
		}
		data, _ := io.ReadAll(r)
		entries["/"+strings.TrimPrefix(path.Clean(hdr.Name), "./")] = diskEntry{hdr, string(data)}
	}
}

func TestWrapDirAssemblesTheGuest(t *testing.T) {
	fakeMke2fs(t)
	opts, marker := dirOptions(t)
	result, err := WrapDir(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"/app/server", "--port", "8080"}; !reflect.DeepEqual(result.Spec.Argv(), want) {
		t.Errorf("argv = %q, want %q", result.Spec.Argv(), want)
	}
	if want := []string{"PATH=/app:/usr/bin", "MODE=debug"}; !reflect.DeepEqual(result.Spec.Env, want) {
		t.Errorf("env = %q, want %q", result.Spec.Env, want)
	}
	if result.Spec.Image != "example.com/app:1" || result.Spec.Rootfs != containerspec.RootfsDir || result.Spec.User != "1000" || result.Spec.WorkingDir != "/app" {
		t.Errorf("spec = %+v", result.Spec)
	}
	if !reflect.DeepEqual(result.Ports, []string{"8080/tcp"}) || !reflect.DeepEqual(result.Volumes, []string{"/data"}) {
		t.Errorf("ports %q, volumes %q", result.Ports, result.Volumes)
	}
	if result.Kernel != filepath.Join(opts.Out, "vmlinux") || result.Initramfs != "" {
		t.Errorf("kernel %q, initramfs %q", result.Kernel, result.Initramfs)
	}
	if data, err := os.ReadFile(result.Kernel); err != nil || string(data) != "kernel\n" {
		t.Errorf("the runtime kernel was not copied beside the disk: %q, %v", data, err)
	}
	if !regularFile(filepath.Join(opts.Out, guestimage.ContractSidecarName)) {
		t.Errorf("no contract sidecar beside the disk")
	}

	disk := readDisk(t, result.Disk)
	root := containerspec.RootfsDir
	// One of the two names is the file and the other a link to it,
	// whichever order the tree was walked in.
	server, again := disk[root+"/app/server"], disk[root+"/app/server-again"]
	if server.hdr == nil || again.hdr == nil {
		t.Fatalf("the wrapped filesystem is not at %s in the disk", root)
	}
	if server.hdr.Typeflag == tar.TypeLink {
		server, again = again, server
	}
	if server.data != "the workload\n" {
		t.Errorf("the workload's content is %q", server.data)
	}
	if server.hdr.FileInfo().Mode()&os.ModeSetuid == 0 {
		t.Errorf("the setuid bit was lost: %v", server.hdr.FileInfo().Mode())
	}
	if again.hdr.Typeflag != tar.TypeLink {
		t.Errorf("a hard link was not copied as one")
	}
	if escape := disk[root+"/app/escape"]; escape.hdr == nil || escape.hdr.Typeflag != tar.TypeSymlink || escape.hdr.Linkname != "/etc/shadow" {
		t.Errorf("a symlink was not copied as the link it is")
	}
	if ro := disk[root+"/readonly"]; ro.hdr == nil || ro.hdr.FileInfo().Mode().Perm() != 0o555 {
		t.Errorf("a read-only directory's mode was not kept")
	}

	var spec containerspec.Spec
	if err := json.Unmarshal([]byte(disk[containerspec.ConfigPath].data), &spec); err != nil {
		t.Fatalf("the spec in the disk: %v", err)
	}
	if !reflect.DeepEqual(spec.Argv(), result.Spec.Argv()) {
		t.Errorf("the spec in the disk runs %q", spec.Argv())
	}
	if runner := disk[containerspec.RunnerPath]; runner.hdr == nil || runner.hdr.FileInfo().Mode().Perm() != 0o755 {
		t.Errorf("granule-container is not installed executable")
	}
	if unit := disk["/etc/systemd/system/granule-wrap@.service"].data; !strings.Contains(unit, "Restart=no") {
		t.Errorf("the wrap unit does not carry the restart policy:\n%s", unit)
	}
	wants := "/etc/systemd/system/multi-user.target.wants/"
	if link := disk[wants+"granule-wrap@app.service"]; link.hdr == nil || link.hdr.Linkname != "/etc/systemd/system/granule-wrap@.service" {
		t.Errorf("the wrap service is not enabled")
	}
	if _, ok := disk[wants+"granule-container.service"]; ok {
		t.Errorf("granule-container is enabled beside the wrap service, which would run the workload twice")
	}
	if _, ok := disk["/usr/local/bin/granule-agent"]; !ok {
		t.Errorf("pack-guest did not lay the agents down")
	}

	if _, err := os.Stat(marker); err == nil {
		t.Errorf("something out of the wrapped image was executed")
	}
	if entries, _ := os.ReadDir(opts.WorkDir); len(entries) != 0 {
		t.Errorf("the staging tree was left behind: %v", entries)
	}
}

func TestWrapDirTakesTheGuestsOwnKernel(t *testing.T) {
	fakeMke2fs(t)
	opts, _ := dirOptions(t)
	opts.RuntimeKernel = ""
	writeFile(t, filepath.Join(opts.GuestRootfs, "boot/vmlinuz-6.1.0"), "vmlinuz\n", 0o644)
	writeFile(t, filepath.Join(opts.GuestRootfs, "boot/initrd.img-6.1.0"), "initrd\n", 0o644)
	result, err := WrapDir(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kernel != filepath.Join(opts.Out, "vmlinuz") || result.Initramfs != filepath.Join(opts.Out, "initrd.img") {
		t.Errorf("kernel %q, initramfs %q", result.Kernel, result.Initramfs)
	}
}

func TestWrapDirRefusesAGuestNothingBoots(t *testing.T) {
	fakeMke2fs(t)
	opts, _ := dirOptions(t)
	opts.RuntimeKernel = ""
	_, err := WrapDir(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "brings no kernel") {
		t.Fatalf("err = %v, want a refusal naming the missing kernel", err)
	}
}

func TestWrapDirWritesNothingThroughAGuestLink(t *testing.T) {
	fakeMke2fs(t)
	opts, _ := dirOptions(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(opts.GuestRootfs, "etc/granule")); err != nil {
		t.Fatal(err)
	}
	_, err := WrapDir(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "symlink at /etc/granule") {
		t.Fatalf("err = %v, want a refusal naming the link", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrap wrote through the link: %v", entries)
	}
}

func TestWrapDirRefusesALinkPackGuestWouldFollow(t *testing.T) {
	fakeMke2fs(t)
	opts, _ := dirOptions(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(opts.GuestRootfs, "usr/local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(opts.GuestRootfs, "usr/local/bin")); err != nil {
		t.Fatal(err)
	}
	_, err := WrapDir(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "/usr/local/bin") {
		t.Fatalf("err = %v, want a refusal naming the link", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrap wrote through the link: %v", entries)
	}
}

func TestWrapDirNeedsRootUnlessToldOtherwise(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	opts, _ := dirOptions(t)
	opts.Unprivileged = false
	if _, err := WrapDir(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "takes root") {
		t.Fatalf("err = %v, want a refusal asking for root", err)
	}
}

func TestWrapDirRefusesAnImageWithNothingToRun(t *testing.T) {
	opts, _ := dirOptions(t)
	writeFile(t, opts.Config, `{"config": {"Env": ["A=b"]}}`, 0o644)
	if _, err := WrapDir(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "nothing to run") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseOCIConfigTakesEitherForm(t *testing.T) {
	for _, data := range []string{
		`{"architecture": "amd64", "config": {"Cmd": ["run"], "User": "app"}}`,
		`{"Cmd": ["run"], "User": "app"}`,
	} {
		cfg, err := parseOCIConfig([]byte(data), "img")
		if err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if !reflect.DeepEqual(cfg.Cmd, []string{"run"}) || cfg.User != "app" {
			t.Errorf("%s: parsed %+v", data, cfg)
		}
	}
}

// TestWrapDirPacksARealDisk runs the real mke2fs, where there is one.
func TestWrapDirPacksARealDisk(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH")+":/sbin:/usr/sbin")
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs is not installed")
	}
	opts, marker := dirOptions(t)
	// As root, the privileged copy: ownership, devices and all.
	opts.Unprivileged = os.Geteuid() != 0
	result, err := WrapDir(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if debugfs, err := exec.LookPath("debugfs"); err == nil {
		out, err := exec.Command(debugfs, "-R", "cat "+containerspec.ConfigPath, result.Disk).Output()
		if err != nil || !strings.Contains(string(out), "/app/server") {
			t.Errorf("the disk's spec: %v\n%s", err, out)
		}
		out, err = exec.Command(debugfs, "-R", "cat "+containerspec.RootfsDir+"/app/server", result.Disk).Output()
		if err != nil || string(out) != "the workload\n" {
			t.Errorf("the disk's workload: %v\n%s", err, out)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("something out of the wrapped image was executed")
	}
}
